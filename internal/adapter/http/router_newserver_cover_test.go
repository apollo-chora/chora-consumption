// router_newserver_cover_test.go — white-box tests driving NewServer's
// env-conditional wiring branches via the package-level `getenv` hook.
//
// Per feedback_no_inline_config the constructor is the ONLY env-aware layer;
// these helpers (NewExamPrepClient / NewManaClient / GKE-companion client) do
// NOT dial on construction, so the branches are exercisable without network.
// The agentengine ADC path is NOT exercised here (it needs ADC credentials) —
// that is a genuine ceiling recorded in ceiling_reason.
package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// withGetenv swaps the package getenv hook for the duration of fn.
func withGetenv(t *testing.T, m map[string]string) {
	t.Helper()
	prev := getenv
	getenv = func(k string) string { return m[k] }
	t.Cleanup(func() { getenv = prev })
}

func TestNewServer_WiresManaQuoter_WhenManaURLSet(t *testing.T) {
	// gRPC mesh target (host:port, no scheme) — grpc.NewClient is lazy so this
	// constructs without a live server.
	withGetenv(t, map[string]string{
		"CHORA_IDENTITY_MANA_GRPC_URL": "mana.invalid:9090",
	})
	srv := NewServer()
	if srv.ManaQuoter == nil {
		t.Errorf("ManaQuoter should be wired when the mana URL is set")
	}
}

func TestNewServer_NoEnv_LeavesOptionalWiringNil(t *testing.T) {
	withGetenv(t, map[string]string{}) // all empty
	srv := NewServer()
	if srv.ExamPrepCoach != nil || srv.ManaQuoter != nil || srv.CompanionEngine != nil {
		t.Errorf("optional wiring should be nil with no env set")
	}
	// The default server must still serve health.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("healthz = %d; want 200", w.Code)
	}
}
