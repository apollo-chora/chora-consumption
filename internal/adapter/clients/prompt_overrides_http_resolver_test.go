package clients

// ADR-197 P3 (CHO-2368): the HTTP adapter behind PromptOverridesResolver.
// Calls the orchestrator's resolve read route
// (GET /v1/prompt-registry/agents/{agent}/resolve?tenant_id=), caches the
// verdict per tenant for a short TTL (session builds are frequent; the
// registry changes rarely), and surfaces transport/shape failures as errors
// for the engine client's fail-open WARN.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromptRegistryHTTPResolver_ParsesRouteResponse(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/prompt-registry/agents/companion_chat/resolve" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("tenant_id"); got != "tenant-x" {
			t.Errorf("tenant_id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent_id":"companion_chat","tenant_id":"tenant-x","version":"1.1.0","source":"platform_override","segments":{"role_frame":"CRAFT"}}`))
	}))
	defer srv.Close()

	r := NewPromptRegistryHTTPResolver(srv.URL, "companion_chat", 60*time.Second, srv.Client())
	res, err := r.ResolvePromptOverrides(context.Background(), "tenant-x")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Version != "1.1.0" || res.Source != "platform_override" {
		t.Errorf("resolution = %+v", res)
	}
	if res.OverridesJSON != `{"role_frame":"CRAFT"}` {
		t.Errorf("overrides json = %q", res.OverridesJSON)
	}
}

func TestPromptRegistryHTTPResolver_EmbeddedHasNoOverridesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent_id":"companion","version":"v1","source":"embedded","segments":{}}`))
	}))
	defer srv.Close()

	r := NewPromptRegistryHTTPResolver(srv.URL, "companion_chat", 60*time.Second, srv.Client())
	res, err := r.ResolvePromptOverrides(context.Background(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.OverridesJSON != "" {
		t.Errorf("embedded must carry no overrides json, got %q", res.OverridesJSON)
	}
	if res.Version != "v1" || res.Source != "embedded" {
		t.Errorf("resolution = %+v", res)
	}
}

func TestPromptRegistryHTTPResolver_CachesWithinTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agent_id":"companion","version":"1.1.0","source":"platform_override","segments":{"role_frame":"X"}}`))
	}))
	defer srv.Close()

	r := NewPromptRegistryHTTPResolver(srv.URL, "companion", time.Hour, srv.Client())
	for range 3 {
		if _, err := r.ResolvePromptOverrides(context.Background(), "tenant-x"); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("expected 1 upstream hit within TTL, got %d", hits.Load())
	}
	// A different tenant is a different cache entry.
	if _, err := r.ResolvePromptOverrides(context.Background(), "tenant-y"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("expected a fresh upstream hit for a new tenant, got %d", hits.Load())
	}
}

func TestPromptRegistryHTTPResolver_Non200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	r := NewPromptRegistryHTTPResolver(srv.URL, "companion", time.Minute, srv.Client())
	if _, err := r.ResolvePromptOverrides(context.Background(), "t"); err == nil {
		t.Fatal("non-200 must surface an error (engine client fail-opens on it)")
	}
}

func TestPromptRegistryHTTPResolver_MalformedBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	r := NewPromptRegistryHTTPResolver(srv.URL, "companion", time.Minute, srv.Client())
	if _, err := r.ResolvePromptOverrides(context.Background(), "t"); err == nil {
		t.Fatal("malformed body must surface an error")
	}
}
