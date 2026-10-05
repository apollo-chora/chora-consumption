package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	testTenant = "01970000-0000-7000-8000-000000000001"
	testGCID   = "01970000-0000-7000-9000-000000000001"
	testAtom   = "01970000-0000-7000-a000-000000000001"
)

func newTestServer() http.Handler {
	return NewServer().Routes()
}

func doReq(t *testing.T, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	return w
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestHealth_AliasParity asserts that /health returns the same status + body
// shape as /healthz. Cross-service convention is /health (most services); we
// keep /healthz live because k8s manifests already reference it. Debt sweep
// 2026-05-13: add /health as an alias so the convention is uniform.
func TestHealth_AliasParity(t *testing.T) {
	handler := newTestServer()

	healthzReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthzRes := httptest.NewRecorder()
	handler.ServeHTTP(healthzRes, healthzReq)
	if healthzRes.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", healthzRes.Code)
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRes := httptest.NewRecorder()
	handler.ServeHTTP(healthRes, healthReq)
	if healthRes.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200 (alias)", healthRes.Code)
	}

	// Same body shape (both contain "status" key with value "ok").
	var healthzBody, healthBody map[string]string
	if err := json.Unmarshal(healthzRes.Body.Bytes(), &healthzBody); err != nil {
		t.Fatalf("decode /healthz body: %v", err)
	}
	if err := json.Unmarshal(healthRes.Body.Bytes(), &healthBody); err != nil {
		t.Fatalf("decode /health body: %v", err)
	}
	if healthzBody["status"] != "ok" {
		t.Errorf("/healthz status field = %q, want ok", healthzBody["status"])
	}
	if healthBody["status"] != "ok" {
		t.Errorf("/health status field = %q, want ok", healthBody["status"])
	}
	if _, ok := healthzBody["time"]; !ok {
		t.Errorf("/healthz body missing time field")
	}
	if _, ok := healthBody["time"]; !ok {
		t.Errorf("/health body missing time field")
	}
}

// TestHealth_TrailingSlashAlias mirrors the /healthz/ trailing-slash route
// for Cloud Run GFE quirk parity.
func TestHealth_TrailingSlashAlias(t *testing.T) {
	handler := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("/health/ status = %d, want 200 (trailing-slash alias)", w.Code)
	}
}

func TestReadyz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	newTestServer().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestCreatePath_RequiresContext(t *testing.T) {
	w := doReq(t, http.MethodPost, "/api/learning-paths",
		map[string]any{"name": "P", "atom_ids": []string{testAtom}},
		nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing context)", w.Code)
	}
}

func TestCreatePath_HappyPath(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	body := map[string]any{
		"name":     "Intro to Chora",
		"atom_ids": []string{testAtom},
	}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/api/learning-paths", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["path_id"] == nil || resp["path_id"] == "" {
		t.Error("expected path_id in response")
	}
}

func TestGetPath_NotFound(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/learning-paths/nonexistent", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestListPaths_RequiresTenant(t *testing.T) {
	w := doReq(t, http.MethodGet, "/api/learning-paths", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing tenant)", w.Code)
	}
}

func TestStartSession_HappyPath(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	body := map[string]any{"atom_id": testAtom}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
}

func TestCompleteSession_FullFlow(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	// Start
	body := map[string]any{"atom_id": testAtom}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", testTenant)
	req.Header.Set("gcid", testGCID)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var startResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &startResp)
	sessionID := startResp["session_id"].(string)

	// Complete
	completeBody := map[string]any{"score": 0.9}
	var cbuf bytes.Buffer
	json.NewEncoder(&cbuf).Encode(completeBody)
	creq := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sessionID+"/complete", &cbuf)
	creq.Header.Set("Content-Type", "application/json")
	creq.Header.Set("X-Tenant-Id", testTenant)
	creq.Header.Set("gcid", testGCID)
	cw := httptest.NewRecorder()
	handler.ServeHTTP(cw, creq)
	if cw.Code != http.StatusOK {
		t.Errorf("complete status = %d, want 200; body=%s", cw.Code, cw.Body.String())
	}
}

func TestGetCompanion_AutoBond(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/companions/"+testGCID, nil)
	req.Header.Set("X-Tenant-Id", testTenant)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["level"] == nil {
		t.Error("expected level in response")
	}
}

func TestAwardXP_LevelsUp(t *testing.T) {
	srv := NewServer()
	handler := srv.Routes()

	// First call creates companion via auto-bond.
	greq := httptest.NewRequest(http.MethodGet, "/api/companions/"+testGCID, nil)
	greq.Header.Set("X-Tenant-Id", testTenant)
	gw := httptest.NewRecorder()
	handler.ServeHTTP(gw, greq)

	// Award 400 XP — should level up.
	body := map[string]any{"delta": 400}
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/api/companions/"+testGCID+"/xp", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", testTenant)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	leveled, _ := resp["leveled_up"].(bool)
	if !leveled {
		t.Errorf("expected leveled_up=true, got resp=%+v", resp)
	}
}
