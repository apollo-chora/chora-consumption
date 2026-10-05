// learning_path_handler_test — EXT scope LearningPath endpoints.
//
//	POST /learning-paths
//	POST /learning-paths/{id}/enrollments
//	GET  /learning-paths/{id}/progress?gcid=X
//
// Distinct from the older /api/learning-paths under router.go (skeleton).
package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// extServer mounts the EXT-scope routes (LearningPath + AtomAttempt + KG).
// Returns the server pointer + handler so tests can probe published events.
func extServer(t *testing.T) (*ExtServer, http.Handler) {
	t.Helper()
	s := NewExtServer(nil) // nil graph → KG endpoints test it separately
	return s, http.Handler(s.Routes())
}

func extReq(t *testing.T, h http.Handler, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const (
	tTenant = "01970000-0000-7000-8000-000000000001"
	tGCID   = "01970000-0000-7000-9000-000000000001"
	tAtom1  = "01970000-0000-7000-a000-000000000001"
	tAtom2  = "01970000-0000-7000-a000-000000000002"
)

func extHeaders() map[string]string {
	return map[string]string{
		"X-Tenant-Id": tTenant,
		"gcid":        tGCID,
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
}

// TestExt_CreatePath creates a LearningPath via POST.
func TestExt_CreatePath(t *testing.T) {
	_, h := extServer(t)
	body := map[string]any{
		"title":    "Algebra Foundations",
		"atom_ids": []string{tAtom1, tAtom2},
	}
	w := extReq(t, h, http.MethodPost, "/learning-paths", body, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["path_id"] == nil || resp["path_id"] == "" {
		t.Errorf("path_id missing: %v", resp)
	}
	if resp["title"] != "Algebra Foundations" {
		t.Errorf("title = %v", resp["title"])
	}
}

// TestExt_CreatePath_RequiresTenant rejects missing X-Tenant-Id.
func TestExt_CreatePath_RequiresTenant(t *testing.T) {
	_, h := extServer(t)
	body := map[string]any{"title": "X", "atom_ids": []string{tAtom1}}
	w := extReq(t, h, http.MethodPost, "/learning-paths", body, map[string]string{
		"gcid":        tGCID,
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 400 or 401", w.Code)
	}
}

// TestExt_EnrollLearner creates an enrollment.
func TestExt_EnrollLearner(t *testing.T) {
	s, h := extServer(t)
	// First create a path.
	body := map[string]any{"title": "T", "atom_ids": []string{tAtom1, tAtom2}}
	w := extReq(t, h, http.MethodPost, "/learning-paths", body, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("path create failed: %d", w.Code)
	}
	var pathResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pathResp)
	pathID := pathResp["path_id"].(string)

	// Enroll.
	w2 := extReq(t, h, http.MethodPost, "/learning-paths/"+pathID+"/enrollments",
		map[string]any{}, extHeaders())
	if w2.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, want 201; body=%s", w2.Code, w2.Body.String())
	}
	var er map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &er)
	if er["enrollment_id"] == nil {
		t.Errorf("enrollment_id missing: %v", er)
	}

	// Event was published.
	pub := s.Publisher.(*events.InMemoryPublisher)
	found := false
	for _, ev := range pub.Events() {
		if ev.Topic == events.TopicLearningPathEnrollmentCreated {
			found = true
		}
	}
	if !found {
		t.Errorf("expected enrollment.created event")
	}
}

// TestExt_GetProgress returns 0% for fresh enrollment.
func TestExt_GetProgress(t *testing.T) {
	_, h := extServer(t)
	body := map[string]any{"title": "T", "atom_ids": []string{tAtom1, tAtom2}}
	w := extReq(t, h, http.MethodPost, "/learning-paths", body, extHeaders())
	var pr map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pr)
	pathID := pr["path_id"].(string)

	extReq(t, h, http.MethodPost, "/learning-paths/"+pathID+"/enrollments",
		map[string]any{}, extHeaders())

	w3 := extReq(t, h, http.MethodGet,
		"/learning-paths/"+pathID+"/progress?gcid="+tGCID, nil, extHeaders())
	if w3.Code != http.StatusOK {
		t.Fatalf("progress status = %d; body=%s", w3.Code, w3.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &resp)
	if resp["percent_complete"] == nil {
		t.Errorf("percent_complete missing: %v", resp)
	}
}

// TestExt_GetProgress_NotFound returns 404 for unknown path.
func TestExt_GetProgress_NotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths/missing/progress?gcid="+tGCID, nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestExt_GetProgress_RequiresTenant (CHO-1624 RLS-ctx sweep): the progress
// read calls Paths.Get, whose pg repo runs rls.ApplySession(ctx) (reads the
// tenant from the CONTEXT). The gateway stamps X-Tenant-Id for the JWT-gated
// learning-paths routes, so getProgress MUST require + wrap it — without it the
// path read errors ErrNoTenantContext and progress 404s on every prod call.
// The X-Tenant-Id check runs BEFORE the path lookup, so a missing header is a
// 400 even for an unknown path (pre-fix: proceeded to the inmem lookup → 404).
func TestExt_GetProgress_RequiresTenant(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths/anything/progress?gcid="+tGCID, nil, map[string]string{
		"gcid":        tGCID,
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (X-Tenant-Id required for RLS)", w.Code)
	}
}

// TestExt_CreatePath_ValidatesAtomList rejects empty atom_ids per
// LearningPath aggregate invariant.
func TestExt_CreatePath_ValidatesAtomList(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/learning-paths",
		map[string]any{"title": "X", "atom_ids": []string{}},
		extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
