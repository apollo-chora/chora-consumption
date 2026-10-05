// growth_edge_uploads_handler_test.go — RED tests for the W8b-2 upload PRODUCER
// (POST .../uploads) + poll (GET .../uploads/{id}).
package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/storage"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- fakes ---

type fakeBlobUploader struct {
	called bool
	req    storage.UploadReq
	uri    string
	err    error
}

func (f *fakeBlobUploader) Upload(_ context.Context, req storage.UploadReq) (string, error) {
	f.called = true
	_, _ = io.Copy(io.Discard, req.Body) // drain like the real streamer
	f.req = req
	if f.err != nil {
		return "", f.err
	}
	if f.uri == "" {
		f.uri = "gs://chora-weakness-uploads/tenants/t/weakness-uploads/u/source"
	}
	return f.uri, nil
}

type fakeUploadRepo struct {
	inserted  []wu.Upload
	getResult *wu.Upload
	getErr    error
	insertErr error
}

func (f *fakeUploadRepo) Insert(_ context.Context, u wu.Upload) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, u)
	return nil
}
func (f *fakeUploadRepo) Get(_ context.Context, _, _ string) (*wu.Upload, error) {
	return f.getResult, f.getErr
}
func (f *fakeUploadRepo) MarkCompleted(context.Context, string, string, []string, time.Time) error {
	return nil
}
func (f *fakeUploadRepo) MarkFailed(context.Context, string, string, string, time.Time) error {
	return nil
}

func ueServer(repo wu.Repository, up storage.BlobUploader) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.WeaknessUploads = repo
	ext.WeaknessBlobs = up
	return ext
}

// multipartUpload builds a multipart body. mime="" omits the file part's
// Content-Type; kind="" omits the upload_kind field; noFile omits the file part.
func multipartUpload(t *testing.T, kind, mime string, noFile bool) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if !noFile {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="test.pdf"`)
		if mime != "" {
			h.Set("Content-Type", mime)
		}
		pw, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = pw.Write([]byte("%PDF-1.4 fake marked test"))
	}
	if kind != "" {
		_ = mw.WriteField("upload_kind", kind)
	}
	_ = mw.WriteField("context_hint", "Sec 3 geography")
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func ueReq(method, path string, body []byte, ct string, withHeaders bool) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if withHeaders {
		r.Header.Set("X-Tenant-Id", geTenant)
		r.Header.Set("gcid", geGCID)
		r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	return r
}

func TestUploadGrowthEdgeDoc_HappyPath(t *testing.T) {
	repo := &fakeUploadRepo{}
	up := &fakeBlobUploader{}
	srv := ueServer(repo, up)
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "QUEUED" || resp["upload_id"] == "" {
		t.Errorf("resp = %v", resp)
	}
	if !up.called || up.req.MIME != "application/pdf" || up.req.TenantID != geTenant {
		t.Errorf("uploader req = %+v", up.req)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("Insert calls = %d want 1", len(repo.inserted))
	}
	job := repo.inserted[0]
	if job.Status != wu.StatusQueued || job.UploadKind != "marked_test" || job.SourceBlobURI != up.uri {
		t.Errorf("job = %+v", job)
	}
	// Published uploaded.v1 with traceparent + payload.
	pub := srv.Publisher.(*events.InMemoryPublisher)
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events want 1", len(evs))
	}
	if evs[0].Topic != "chora.consumption.weakness_doc.uploaded.v1" {
		t.Errorf("topic = %q", evs[0].Topic)
	}
	if evs[0].Envelope.Traceparent == "" {
		t.Error("uploaded.v1 envelope MUST carry a traceparent (analyzed.v1 propagates it)")
	}
	if evs[0].Payload["source_blob_uri"] != up.uri || evs[0].Payload["upload_kind"] != "marked_test" {
		t.Errorf("payload = %v", evs[0].Payload)
	}
}

func TestUploadGrowthEdgeDoc_CarriesGoalID(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	const goalID = "0190aaaa-bbbb-7ccc-8ddd-000000000009"
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"goal_id": goalID,
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// The goal scope lands on the persisted job (ADR-238 M-D1: handler SETs it).
	if len(repo.inserted) != 1 || repo.inserted[0].GoalID != goalID {
		t.Errorf("job goal_id = %q; want %q", repo.inserted[0].GoalID, goalID)
	}
	// ...and rides the weakness_doc.uploaded.v1 payload for tracing/consistency.
	payload := srv.Publisher.(*events.InMemoryPublisher).Events()[0].Payload
	if payload["goal_id"] != goalID {
		t.Errorf("payload goal_id = %v; want %q", payload["goal_id"], goalID)
	}
}

func TestUploadGrowthEdgeDoc_OmitsGoalIDWhenAbsent(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "notes", "text/plain", nil) // no goal_id
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d", w.Code)
	}
	if repo.inserted[0].GoalID != "" {
		t.Errorf("job goal_id = %q; want empty for a non-goal upload", repo.inserted[0].GoalID)
	}
	if _, present := srv.Publisher.(*events.InMemoryPublisher).Events()[0].Payload["goal_id"]; present {
		t.Error("goal_id must be omitted from the payload when the FE sends none")
	}
}

func TestUploadGrowthEdgeDoc_GeneratesTraceparentWhenAbsent(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "notes", "text/plain", false)
	r := ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true)
	r.Header.Del("traceparent") // no inbound trace

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d", w.Code)
	}
	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	if len(evs) != 1 || evs[0].Envelope.Traceparent == "" {
		t.Fatal("a fresh traceparent must be generated when the request lacks one")
	}
}

func TestUploadGrowthEdgeDoc_MissingFile(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "marked_test", "", true) // noFile
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_InvalidKind(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "bogus", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_UnsupportedMIME(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "marked_test", "application/zip", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d want 415", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_BlobUploadError(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{err: io.ErrClosedPipe})
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_NilWiring503(t *testing.T) {
	srv := httpadapter.NewExtServer(nil) // no WeaknessUploads/WeaknessBlobs
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_MissingContext(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, false)) // no headers
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_MethodNotAllowed(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("PUT", "/v1/me/growth-edges/uploads", nil, "", true))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_AcceptsMIMEWithCharset(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "notes", "text/markdown; charset=utf-8", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (charset param must be stripped)", w.Code)
	}
	if repo.inserted[0].SourceMIME != "text/markdown" {
		t.Errorf("mime=%q want text/markdown", repo.inserted[0].SourceMIME)
	}
}

func TestUploadGrowthEdgeDoc_BadMultipart(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	// multipart Content-Type but a non-multipart body → ParseMultipartForm errors.
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", []byte("not multipart"), "multipart/form-data; boundary=xyz", true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_InsertError(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{insertErr: io.ErrClosedPipe}, &fakeBlobUploader{})
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
}

func TestGetUploadJob_ErrorPaths(t *testing.T) {
	t.Run("method not allowed", func(t *testing.T) {
		srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, ueReq("PUT", "/v1/me/growth-edges/uploads/up-1", nil, "", true))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", w.Code)
		}
	})
	t.Run("missing context", func(t *testing.T) {
		srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", false))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", w.Code)
		}
	})
	t.Run("missing path id", func(t *testing.T) {
		srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/", nil, "", true))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", w.Code)
		}
	})
	t.Run("get error 500", func(t *testing.T) {
		srv := ueServer(&fakeUploadRepo{getErr: io.ErrClosedPipe}, &fakeBlobUploader{})
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", true))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", w.Code)
		}
	})
}

type failPublisher struct{}

func (failPublisher) Publish(string, events.Envelope, map[string]any) error { return io.ErrClosedPipe }

func TestUploadGrowthEdgeDoc_PublishError(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	srv.Publisher = failPublisher{}
	body, ct := multipartUpload(t, "marked_test", "application/pdf", false)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 (publish failure)", w.Code)
	}
}

func TestGetUploadJob_OK(t *testing.T) {
	now := time.Now().UTC()
	job := &wu.Upload{UploadID: "up-1", TenantID: geTenant, LearnerGCID: geGCID, Status: wu.StatusCompleted, UpsertedEdgeIDs: []string{"e1", "e2"}, CreatedAt: now}
	srv := ueServer(&fakeUploadRepo{getResult: job}, &fakeBlobUploader{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", true))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["status"] != "COMPLETED" {
		t.Errorf("status=%v", got["status"])
	}
	if ids, ok := got["upserted_growth_edge_ids"].([]any); !ok || len(ids) != 2 {
		t.Errorf("ids=%v", got["upserted_growth_edge_ids"])
	}
}

func TestGetUploadJob_NotFound(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{getResult: nil}, &fakeBlobUploader{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/missing", nil, "", true))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestGetUploadJob_NilRepo503(t *testing.T) {
	srv := httpadapter.NewExtServer(nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("GET", "/v1/me/growth-edges/uploads/up-1", nil, "", true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}
