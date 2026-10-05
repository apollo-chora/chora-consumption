// growth_edge_uploads_ws5_test.go — WS-5 (ADR-205 D8 / CHO-1957): the upload
// door's source_material consent gate + per-blob envelope encrypt-on-upload.
package http_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

const ws5PlainBody = "%PDF-1.4 a learner's marked physics paper — momentum all wrong"

// fakeKEKClient wraps the DEK as sha256(aad)||dek (AAD-enforcing), mirroring the
// domain test fake. Only WrapDEK is exercised on the upload path.
type fakeKEKClient struct{ failWrap bool }

func (f fakeKEKClient) WrapDEK(_ context.Context, dek, aad []byte) ([]byte, string, error) {
	if f.failWrap {
		return nil, "", errors.New("kek wrap boom")
	}
	h := sha256.Sum256(aad)
	return append(append([]byte(nil), h[:]...), dek...), "fake-kek/v1", nil
}
func (f fakeKEKClient) UnwrapDEK(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	h := sha256.Sum256(aad)
	if len(wrapped) < sha256.Size || !bytes.Equal(wrapped[:sha256.Size], h[:]) {
		return nil, errors.New("aad mismatch")
	}
	return wrapped[sha256.Size:], nil
}

// capturingUploader records the uploaded bytes so a test can assert ciphertext.
type capturingUploader struct {
	body []byte
	req  storage.UploadReq
	err  error
}

func (c *capturingUploader) Upload(_ context.Context, req storage.UploadReq) (string, error) {
	c.body, _ = io.ReadAll(req.Body)
	c.req = req
	if c.err != nil {
		return "", c.err
	}
	return "gs://chora-weakness-uploads/tenants/t/weakness-uploads/u/source", nil
}

func ws5Multipart(t *testing.T, kind, mime, consent string) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="src.pdf"`)
	h.Set("Content-Type", mime)
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write([]byte(ws5PlainBody))
	_ = mw.WriteField("upload_kind", kind)
	if consent != "" {
		_ = mw.WriteField("upload_rights_consent", consent)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func ws5Post(t *testing.T, ext *httpadapter.ExtServer, kind, consent string) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := ws5Multipart(t, kind, "application/pdf", consent)
	w := httptest.NewRecorder()
	ext.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	return w
}

// ---- source_material consent gate ----

func TestUpload_SourceMaterialDisabledByDefault(t *testing.T) {
	ext := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	// no SourceMaterial gate wired (nil) → source_material rejected (DARK)
	w := ws5Post(t, ext, wu.KindSourceMaterial, "true")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s; want 403", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("SOURCE_MATERIAL_DISABLED")) {
		t.Fatalf("want SOURCE_MATERIAL_DISABLED; got %s", w.Body.String())
	}
}

func TestUpload_SourceMaterialEnabledRequiresConsent(t *testing.T) {
	repo := &fakeUploadRepo{}
	ext := ueServer(repo, &fakeBlobUploader{})
	ext.SourceMaterial = wu.NewSourceMaterialGate(true)
	w := ws5Post(t, ext, wu.KindSourceMaterial, "") // no consent
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte("UPLOAD_RIGHTS_CONSENT_REQUIRED")) {
		t.Fatalf("want 403 UPLOAD_RIGHTS_CONSENT_REQUIRED; got %d %s", w.Code, w.Body.String())
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("a consent-less source_material upload must NOT insert a job")
	}
}

func TestUpload_SourceMaterialWithConsentAccepted(t *testing.T) {
	repo := &fakeUploadRepo{}
	ext := ueServer(repo, &fakeBlobUploader{})
	ext.SourceMaterial = wu.NewSourceMaterialGate(true)
	w := ws5Post(t, ext, wu.KindSourceMaterial, "true")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s; want 202", w.Code, w.Body.String())
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("expected 1 inserted job; got %d", len(repo.inserted))
	}
	if repo.inserted[0].UploadRightsConsentAt == nil {
		t.Fatalf("source_material job must record the upload-rights consent timestamp")
	}
}

func TestUpload_NonSourceMaterialUnaffectedByGate(t *testing.T) {
	repo := &fakeUploadRepo{}
	ext := ueServer(repo, &fakeBlobUploader{})
	ext.SourceMaterial = wu.NewSourceMaterialGate(true) // enabled, but marked_test isn't gated
	w := ws5Post(t, ext, wu.KindMarkedTest, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("marked_test must be unaffected by the source_material gate; got %d %s", w.Code, w.Body.String())
	}
	if repo.inserted[0].UploadRightsConsentAt != nil {
		t.Fatalf("marked_test must NOT record a consent timestamp")
	}
}

// ---- envelope encrypt-on-upload ----

func ws5EnvelopeServer(repo wu.Repository, up storage.BlobUploader, kek wb.KEKClient, store wb.WrappedDEKStore) *httpadapter.ExtServer {
	ext := ueServer(repo, up)
	ext.BlobEnvelope = wb.NewEnvelope(kek)
	ext.BlobDEKs = store
	return ext
}

func TestUpload_EnvelopeEncryptsBlobAndPersistsDEK(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &capturingUploader{}
	store := wb.NewInMemoryWrappedDEKStore()
	ext := ws5EnvelopeServer(repo, up, fakeKEKClient{}, store)

	w := ws5Post(t, ext, wu.KindMarkedTest, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s; want 202", w.Code, w.Body.String())
	}
	// What landed in GCS must be ciphertext, NOT the plaintext.
	if bytes.Contains(up.body, []byte(ws5PlainBody)) {
		t.Fatalf("uploaded body leaks plaintext (envelope did not encrypt)")
	}
	if len(up.body) == 0 {
		t.Fatalf("no ciphertext uploaded")
	}
	// A wrapped DEK must be persisted for the upload (so it can be crypto-shredded).
	if len(repo.inserted) != 1 {
		t.Fatalf("expected 1 job; got %d", len(repo.inserted))
	}
	uploadID := repo.inserted[0].UploadID
	rec, err := store.Get(context.Background(), uploadID)
	if err != nil || rec == nil {
		t.Fatalf("wrapped DEK not persisted for upload %q (err=%v)", uploadID, err)
	}
	if len(rec.Wrapped) == 0 || rec.KEKVersion == "" || rec.Deleted {
		t.Fatalf("persisted wrapped DEK looks wrong: %+v", rec)
	}
}

func TestUpload_EnvelopeSealFailureFailsLoud(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &capturingUploader{}
	store := wb.NewInMemoryWrappedDEKStore()
	ext := ws5EnvelopeServer(repo, up, fakeKEKClient{failWrap: true}, store)

	w := ws5Post(t, ext, wu.KindMarkedTest, "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a KEK wrap failure must fail loud (500); got %d %s", w.Code, w.Body.String())
	}
	if up.body != nil {
		t.Fatalf("on seal failure NOTHING must be uploaded (never store plaintext); uploaded %d bytes", len(up.body))
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("on seal failure no job must be inserted")
	}
}
