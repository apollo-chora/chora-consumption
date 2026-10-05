// router_error_paths_cover_test.go — white-box tests for the legacy /api/*
// (Phyllis-MVP) router handlers' error + branch paths. Reuses doReq /
// newTestServer / testTenant / testGCID / testAtom from router_test.go.
package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// companionNewForTest constructs a bonded legacy Companion (router.go shape).
func companionNewForTest(tenantID, gcid string) (*companion.Companion, error) {
	return companion.New(tenantID, gcid, "Companion")
}

// ---------------------------------------------------------------------------
// /api/learning-paths
// ---------------------------------------------------------------------------

func TestRouter_LearningPaths_MethodNotAllowed(t *testing.T) {
	w := doReq(t, http.MethodDelete, "/api/learning-paths", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/learning-paths = %d; want 405", w.Code)
	}
}

func TestRouter_CreatePath_BadJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/learning-paths", bytes.NewBufferString("{nope"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d; want 400", w.Code)
	}
}

func TestRouter_CreatePath_InvalidPath(t *testing.T) {
	// empty atom_ids → learning_path.New rejects → 400 INVALID_PATH.
	w := doReq(t, http.MethodPost, "/api/learning-paths",
		map[string]any{"name": "P", "atom_ids": []string{}},
		map[string]string{"X-Tenant-Id": testTenant, "gcid": testGCID})
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid path = %d; want 400", w.Code)
	}
}

func TestRouter_ListPaths_OK(t *testing.T) {
	// Create one path then list — exercises the listPaths success branch +
	// toPathResp mapping loop.
	srv := NewServer()
	h := srv.Routes()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"name": "P1", "atom_ids": []string{testAtom}})
	cr := httptest.NewRequest(http.MethodPost, "/api/learning-paths", &buf)
	cr.Header.Set("X-Tenant-Id", testTenant)
	cr.Header.Set("gcid", testGCID)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusCreated {
		t.Fatalf("create = %d", cw.Code)
	}

	lr := httptest.NewRequest(http.MethodGet, "/api/learning-paths", nil)
	lr.Header.Set("X-Tenant-Id", testTenant)
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, lr)
	if lw.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", lw.Code, lw.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(lw.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %d; want 1", len(items))
	}
}

func TestRouter_LearningPathByID_MissingID(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/learning-paths/", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty id = %d; want 400 MISSING_ID", w.Code)
	}
}

func TestRouter_LearningPathByID_MethodNotAllowed(t *testing.T) {
	w := doReq(t, http.MethodDelete, "/api/learning-paths/some-id", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE by-id = %d; want 405", w.Code)
	}
}

// ---------------------------------------------------------------------------
// /api/sessions
// ---------------------------------------------------------------------------

func TestRouter_Sessions_MethodNotAllowed(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/sessions", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/sessions = %d; want 405", w.Code)
	}
}

func TestRouter_StartSession_MissingContext(t *testing.T) {
	w := doReq(t, http.MethodPost, "/api/sessions", map[string]any{"atom_id": testAtom}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("start w/o context = %d; want 400", w.Code)
	}
}

func TestRouter_StartSession_BadJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString("{"))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d; want 400", w.Code)
	}
}

func TestRouter_StartSession_InvalidSession(t *testing.T) {
	// empty atom_id → atom_attempt.Start rejects → 400 INVALID_SESSION.
	w := doReq(t, http.MethodPost, "/api/sessions", map[string]any{"atom_id": ""},
		map[string]string{"X-Tenant-Id": testTenant, "gcid": testGCID})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty atom_id = %d; want 400", w.Code)
	}
}

func TestRouter_SessionByID_MissingID(t *testing.T) {
	w := doReq(t, http.MethodPatch, "/api/sessions/", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty id = %d; want 400 MISSING_ID", w.Code)
	}
}

func TestRouter_SessionByID_UnknownSubroute(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/sessions/abc/bogus", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

func TestRouter_CompleteSession_MethodNotAllowed(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/sessions/abc/complete", nil, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET complete = %d; want 405", w.Code)
	}
}

func TestRouter_CompleteSession_NotFound(t *testing.T) {
	w := doReq(t, http.MethodPatch, "/api/sessions/missing/complete",
		map[string]any{"score": 0.9},
		map[string]string{"X-Tenant-Id": testTenant, "gcid": testGCID})
	if w.Code != http.StatusNotFound {
		t.Errorf("complete missing session = %d; want 404", w.Code)
	}
}

func TestRouter_CompleteSession_BadJSON(t *testing.T) {
	srv := NewServer()
	h := srv.Routes()
	// Start a session first.
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"atom_id": testAtom})
	sr := httptest.NewRequest(http.MethodPost, "/api/sessions", &buf)
	sr.Header.Set("X-Tenant-Id", testTenant)
	sr.Header.Set("gcid", testGCID)
	sw := httptest.NewRecorder()
	h.ServeHTTP(sw, sr)
	var start map[string]any
	_ = json.Unmarshal(sw.Body.Bytes(), &start)
	sid, _ := start["session_id"].(string)

	cr := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sid+"/complete", bytes.NewBufferString("{bad"))
	cr.Header.Set("X-Tenant-Id", testTenant)
	cr.Header.Set("gcid", testGCID)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusBadRequest {
		t.Errorf("bad JSON complete = %d; want 400", cw.Code)
	}
}

func TestRouter_CompleteSession_ScoreOutOfRange(t *testing.T) {
	srv := NewServer()
	h := srv.Routes()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"atom_id": testAtom})
	sr := httptest.NewRequest(http.MethodPost, "/api/sessions", &buf)
	sr.Header.Set("X-Tenant-Id", testTenant)
	sr.Header.Set("gcid", testGCID)
	sw := httptest.NewRecorder()
	h.ServeHTTP(sw, sr)
	var start map[string]any
	_ = json.Unmarshal(sw.Body.Bytes(), &start)
	sid, _ := start["session_id"].(string)

	var cbuf bytes.Buffer
	_ = json.NewEncoder(&cbuf).Encode(map[string]any{"score": 1.5}) // out of [0,1]
	cr := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sid+"/complete", &cbuf)
	cr.Header.Set("X-Tenant-Id", testTenant)
	cr.Header.Set("gcid", testGCID)
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, cr)
	if cw.Code != http.StatusBadRequest {
		t.Errorf("score>1 = %d; want 400 COMPLETE_FAILED", cw.Code)
	}
}

// ---------------------------------------------------------------------------
// /api/companions/{gcid}
// ---------------------------------------------------------------------------

func TestRouter_CompanionByGCID_MissingGCID(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/companions/", nil,
		map[string]string{"X-Tenant-Id": testTenant})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty gcid = %d; want 400 MISSING_GCID", w.Code)
	}
}

func TestRouter_CompanionByGCID_MissingTenant(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/companions/"+testGCID, nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing tenant = %d; want 400 MISSING_TENANT", w.Code)
	}
}

func TestRouter_CompanionByGCID_UnknownSubroute(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/companions/"+testGCID+"/bogus", nil,
		map[string]string{"X-Tenant-Id": testTenant})
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

func TestRouter_GetCompanion_MethodNotAllowed(t *testing.T) {
	// GET-only handler; DELETE → 405.
	w := doReq(t, http.MethodDelete, "/api/companions/"+testGCID, nil,
		map[string]string{"X-Tenant-Id": testTenant})
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE companion = %d; want 405", w.Code)
	}
}

func TestRouter_AwardXP_MethodNotAllowed(t *testing.T) {
	// xp endpoint is POST-only; GET → 405.
	w := doReq(t, http.MethodGet, "/api/companions/"+testGCID+"/xp", nil,
		map[string]string{"X-Tenant-Id": testTenant})
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET xp = %d; want 405", w.Code)
	}
}

func TestRouter_AwardXP_BadJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/companions/"+testGCID+"/xp", bytes.NewBufferString("{"))
	req.Header.Set("X-Tenant-Id", testTenant)
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON xp = %d; want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// helpers — requireContext branches
// ---------------------------------------------------------------------------

// awardXP on a gcid with NO existing companion auto-bonds a default Companion
// before applying the delta (covers the auto-bond branch in awardXP).
func TestRouter_AwardXP_AutoBondsWhenMissing(t *testing.T) {
	srv := NewServer()
	h := srv.Routes()
	// No prior GET → no companion bonded. POST /xp must auto-bond + apply.
	req := httptest.NewRequest(http.MethodPost, "/api/companions/"+testGCID+"/xp", encodeJSONBody(t, map[string]int{"delta": 10}))
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("awardXP auto-bond = %d; want 200; body=%s", w.Code, w.Body.String())
	}
}

// toCompanionResp normalizes a nil Traits slice to [] (not null).
func TestRouter_ToCompanionResp_NilTraits(t *testing.T) {
	srv := NewServer()
	// Auto-bond a companion (its Traits are nil at bond time).
	f, err := companionNewForTest(testTenant, testGCID)
	if err != nil {
		t.Fatalf("bond: %v", err)
	}
	f.Traits = nil
	resp := toCompanionResp(f)
	if resp.Traits == nil {
		t.Errorf("toCompanionResp Traits must be non-nil []")
	}
	_ = srv
}

func TestRouter_RequireContext_Branches(t *testing.T) {
	// missing tenant.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, _, err := requireContext(r); err == nil {
		t.Error("missing tenant should error")
	}
	// tenant present, gcid missing.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("X-Tenant-Id", testTenant)
	if _, _, err := requireContext(r2); err == nil {
		t.Error("missing gcid should error")
	}
	// both present.
	r3 := httptest.NewRequest(http.MethodGet, "/", nil)
	r3.Header.Set("X-Tenant-Id", testTenant)
	r3.Header.Set("gcid", testGCID)
	tn, g, err := requireContext(r3)
	if err != nil || tn != testTenant || g != testGCID {
		t.Errorf("requireContext = (%q,%q,%v); want (%q,%q,nil)", tn, g, err, testTenant, testGCID)
	}
}
