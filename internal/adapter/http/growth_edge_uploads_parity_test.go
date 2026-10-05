// growth_edge_uploads_parity_test.go — RED tests for upload-door parity (Wave C,
// ADR-205 D2/D5 / CHO-1973): the POST .../uploads producer must also parse the
// FE's structured_clues + requested_outputs JSON-string multipart parts and carry
// them into the published weakness_doc.uploaded.v1 payload (proto fields 10 + 11).
// Reuses ueServer/fakeUploadRepo/fakeBlobUploader/ueReq from the sibling test.
package http_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// multipartUploadExtra builds a multipart body with the file + upload_kind +
// any extra string fields (structured_clues, requested_outputs, ...).
func multipartUploadExtra(t *testing.T, kind, mime string, extra map[string]string) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="test.pdf"`)
	h.Set("Content-Type", mime)
	pw, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pw.Write([]byte("%PDF-1.4 fake marked test"))
	_ = mw.WriteField("upload_kind", kind)
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func TestUploadGrowthEdgeDoc_PublishesStructuredCluesAndRequestedOutputs(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})

	sc := `{"subject":"physical-geography","weak_topic_keys":["riverine-flood-causes"],"self_confidence":3,"context_kind":"PAST_PAPER","note":"struggled with causation"}`
	ro := `{"focused_dose":true,"practice_test":true}`
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"structured_clues":  sc,
		"requested_outputs": ro,
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	evs := srv.Publisher.(*events.InMemoryPublisher).Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events want 1", len(evs))
	}
	// Marshal+decode the published payload so a json.RawMessage value renders as
	// a nested object for assertion.
	raw, err := json.Marshal(evs[0].Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	gotSC, ok := decoded["structured_clues"].(map[string]any)
	if !ok {
		t.Fatalf("structured_clues missing/!object in payload: %v", decoded["structured_clues"])
	}
	if gotSC["subject"] != "physical-geography" || gotSC["self_confidence"].(float64) != 3 {
		t.Errorf("structured_clues = %v", gotSC)
	}
	if keys, ok := gotSC["weak_topic_keys"].([]any); !ok || len(keys) != 1 || keys[0] != "riverine-flood-causes" {
		t.Errorf("weak_topic_keys = %v", gotSC["weak_topic_keys"])
	}
	gotRO, ok := decoded["requested_outputs"].(map[string]any)
	if !ok {
		t.Fatalf("requested_outputs missing/!object in payload: %v", decoded["requested_outputs"])
	}
	if gotRO["focused_dose"] != true || gotRO["practice_test"] != true {
		t.Errorf("requested_outputs = %v", gotRO)
	}
}

func TestUploadGrowthEdgeDoc_OmitsCluesWhenAbsent(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "notes", "text/plain", nil) // no clues fields
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d", w.Code)
	}
	payload := srv.Publisher.(*events.InMemoryPublisher).Events()[0].Payload
	if _, present := payload["structured_clues"]; present {
		t.Error("structured_clues must be omitted when the FE sends none")
	}
	if _, present := payload["requested_outputs"]; present {
		t.Error("requested_outputs must be omitted when the FE sends none")
	}
}

func TestUploadGrowthEdgeDoc_MalformedStructuredCluesRejected(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"structured_clues": "{not valid json", // fail loud, never forward garbage
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (malformed structured_clues)", w.Code)
	}
}

func TestUploadGrowthEdgeDoc_MalformedRequestedOutputsRejected(t *testing.T) {
	srv := ueServer(&fakeUploadRepo{}, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"requested_outputs": "not-json",
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (malformed requested_outputs)", w.Code)
	}
}
