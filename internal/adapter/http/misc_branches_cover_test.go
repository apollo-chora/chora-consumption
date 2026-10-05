// misc_branches_cover_test.go — small reachable branch tests across several
// handlers (bad-JSON legs, query-param caps, method guards, success-detail
// reads, progress accumulation).
package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

// atom_attempt submitAnswer bad-JSON (existing session, malformed body).
func TestExt_SubmitAnswer_BadJSON(t *testing.T) {
	s := NewExtServer(nil)
	sess, _ := atom_attempt.Start(tTenant, tGCID, tAtom1, atom_attempt.SystemClock)
	s.Sessions.Save(context.Background(), sess)
	h := s.Routes()
	req := httptest.NewRequest(http.MethodPatch, "/sessions/"+sess.SessionID+"/answer", bytes.NewBufferString("{bad"))
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("submit bad JSON = %d; want 400", w.Code)
	}
}

// parseLimit caps values above 200 at 200 (upper-bound clamp).
func TestExt_ParseLimit_CapsAt200(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?limit=500", nil)
	if got := parseLimit(r, 50); got != 200 {
		t.Errorf("parseLimit(500) = %d; want 200 (upper clamp)", got)
	}
	// Exactly 200 passes through.
	r2 := httptest.NewRequest(http.MethodGet, "/x?limit=200", nil)
	if got := parseLimit(r2, 50); got != 200 {
		t.Errorf("parseLimit(200) = %d; want 200", got)
	}
}

// router GET /api/learning-paths/{id} success-detail read.
func TestRouter_GetLearningPathByID_Success(t *testing.T) {
	srv := NewServer()
	h := srv.Routes()
	// Create a path.
	cw := doReqOn(t, h, http.MethodPost, "/api/learning-paths",
		map[string]any{"name": "P", "atom_ids": []string{testAtom}},
		map[string]string{"X-Tenant-Id": testTenant, "gcid": testGCID})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create = %d", cw.Code)
	}
	var pr map[string]any
	_ = decodeJSON(cw, &pr)
	pid, _ := pr["path_id"].(string)

	gw := doReqOn(t, h, http.MethodGet, "/api/learning-paths/"+pid, nil, nil)
	if gw.Code != http.StatusOK {
		t.Errorf("get path by id = %d; want 200; body=%s", gw.Code, gw.Body.String())
	}
}

// doReqOn issues against an already-built handler (so state persists across
// the create+get pair).
func doReqOn(t *testing.T, h http.Handler, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, encodeJSONBody(t, body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
