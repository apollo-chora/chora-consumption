// compose_test.go — the composite (EXT-first, legacy fallback) handler.
//
// The try-primary-into-a-recorder design means every fallback-owned hit
// (health probes, internal pubsub pushes) used to take a wasted pass
// through the EXT tree whose OTel middleware logged a spurious
// `status=404` line — the walk day-1 "404 pairs" red herring. Known
// fallback-owned namespaces now declare themselves at the call site and
// skip the primary entirely.
package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type countingHandler struct {
	calls int
	serve func(w http.ResponseWriter, r *http.Request)
}

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls++
	h.serve(w, r)
}

func extStyle404(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND"}}`))
}

func composeGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The no-prefix contract (primary wins / NOT_FOUND falls through /
// specific 404 surfaces) is pinned in zz_zero_cover_test.go.

func TestComposeHandlers_FallbackFirstPrefixesSkipPrimary(t *testing.T) {
	primary := &countingHandler{serve: extStyle404}
	fallback := &countingHandler{serve: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("legacy"))
	}}
	h := ComposeHandlers(primary, fallback, nil,
		"/healthz", "/health", "/api/internal/pubsub/", "/internal/pubsub/")

	for _, path := range []string{
		"/healthz",
		"/health",
		"/api/internal/pubsub/companion-growth-source-events",
		"/internal/pubsub/kg-retention",
	} {
		if w := composeGet(t, h, path); w.Code != http.StatusOK || w.Body.String() != "legacy" {
			t.Fatalf("%s must serve from the fallback: %d %q", path, w.Code, w.Body.String())
		}
	}
	if primary.calls != 0 {
		t.Fatalf("fallback-owned namespaces must never take the primary pass (no spurious ext 404 logs), got %d calls", primary.calls)
	}
	// A non-declared path still tries the primary first.
	if w := composeGet(t, h, "/v1/me/goals"); w.Code != http.StatusOK || w.Body.String() != "legacy" {
		t.Fatalf("undeclared path must keep the try-primary contract: %d %q", w.Code, w.Body.String())
	}
	if primary.calls != 1 {
		t.Fatalf("undeclared path must take exactly one primary pass, got %d", primary.calls)
	}
}

// CHO-2137: the companions family is fallback-owned EXCEPT two EXT exacts
// (/acquire + /bindings). primaryExact is checked BEFORE the fallbackFirst
// prefixes, so declaring the "/v1/me/companions" prefix kills the wasted EXT
// recorder pass (+ its spurious otel 404 line) on every roster/growth/ritual
// hit without breaking the EXT-owned routes nested inside the family.
func TestComposeHandlers_PrimaryExactWinsInsideFallbackFirstFamily(t *testing.T) {
	primary := &countingHandler{serve: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ext"))
	}}
	fallback := &countingHandler{serve: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("legacy"))
	}}
	h := ComposeHandlers(primary, fallback,
		[]string{"/v1/me/companions/acquire", "/v1/me/companions/bindings"},
		"/v1/me/companions")

	for _, path := range []string{"/v1/me/companions/acquire", "/v1/me/companions/bindings"} {
		if w := composeGet(t, h, path); w.Code != http.StatusOK || w.Body.String() != "ext" {
			t.Fatalf("%s must serve from the primary (EXT owns it): %d %q", path, w.Code, w.Body.String())
		}
	}
	if fallback.calls != 0 {
		t.Fatalf("primary-exact paths must never hit the fallback, got %d calls", fallback.calls)
	}

	primary.calls = 0
	for _, path := range []string{
		"/v1/me/companions",
		"/v1/me/companions/fam-1/growth",
		"/v1/me/companions/fam-1/rituals/rit-1/run",
	} {
		if w := composeGet(t, h, path); w.Code != http.StatusOK || w.Body.String() != "legacy" {
			t.Fatalf("%s must serve from the fallback: %d %q", path, w.Code, w.Body.String())
		}
	}
	if primary.calls != 0 {
		t.Fatalf("fallback-owned companions paths must never take the primary pass (no spurious ext 404 logs), got %d calls", primary.calls)
	}
}
