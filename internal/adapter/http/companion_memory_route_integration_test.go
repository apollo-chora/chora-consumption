// companion_memory_route_integration_test.go — proves the ADR-215 WS-1 memory
// READ route (CHO-1997) is REACHABLE end-to-end through the base Server's
// /v1/me/companions/{id}/memory dispatch (the CHO-1995 wire-up seam), and that a
// nil handler falls through rather than intercepting.
package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompanionMemoryRoute_ReachableViaInstanceByIDDispatch(t *testing.T) {
	srv := NewServer()
	hit := false
	srv.CompanionMemoryRead = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/"+fmCompanionID+"/memory", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	if !hit {
		t.Fatal("memory handler NOT reached via /v1/me/companions/{id}/memory dispatch")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
}

func TestCompanionMemoryRoute_NilHandler_DoesNotIntercept(t *testing.T) {
	srv := NewServer() // CompanionMemoryRead left nil
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/"+fmCompanionID+"/memory", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	// Must not panic and must not 200 from a (nil) memory handler — the {id}/memory
	// subpath falls through to the growth-route dispatch.
	srv.Routes().ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("nil memory handler should not serve 200; got %d", w.Code)
	}
}
