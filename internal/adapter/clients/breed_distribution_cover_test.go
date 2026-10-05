// breed_distribution_cover_test.go — supplemental coverage for the REST
// + gRPC breed-distribution clients: TTL-clamp constructor branches, the
// REST default-status error path, gRPC default-DialOpts + dial-error
// branches, and Close / Invalidate nil-safety.
package clients_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// TestNewBreedDistributionClient_NegativeTTLClampedToZero — a negative TTL
// is clamped to 0 (caching disabled), so repeated lookups always hit the
// upstream.
func TestNewBreedDistributionClient_NegativeTTLClampedToZero(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sku":"egg.standard.v1","odds":[
			{"species":"owl","probability":60,"rarity":"common"},
			{"species":"fox","probability":40,"rarity":"common"}],"total_weight":100}`))
	}))
	defer srv.Close()

	c := clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
		BaseURL:    srv.URL,
		CacheTTL:   -1 * time.Minute, // clamped to 0
		HTTPClient: srv.Client(),
	})
	for i := 0; i < 2; i++ {
		if _, err := c.Lookup("t", "egg.standard.v1"); err != nil {
			t.Fatalf("Lookup #%d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d; want 2 (caching disabled by negative TTL clamp)", calls)
	}
}

// TestBreedDistributionClient_Fetch_Non200NonNotFoundError — a 5xx (neither
// 200 nor 404) surfaces as a generic error carrying the status + body.
func TestBreedDistributionClient_Fetch_Non200NonNotFoundError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("tenancy degraded"))
	}))
	defer srv.Close()

	c := clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	_, err := c.Lookup("t", "egg.standard.v1")
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if errors.Is(err, growth.ErrSkuNotFound) {
		t.Error("503 must NOT map to ErrSkuNotFound")
	}
}

// TestBreedDistributionClient_Lookup_NotFoundMapsToSentinel — a 404
// surfaces as growth.ErrSkuNotFound.
func TestBreedDistributionClient_Lookup_NotFoundMapsToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	_, err := c.Lookup("t", "egg.missing.v1")
	if !errors.Is(err, growth.ErrSkuNotFound) {
		t.Errorf("err = %v; want growth.ErrSkuNotFound", err)
	}
}

// TestBreedDistributionClient_Fetch_TransportErrorWrapped — when
// chora-tenancy is unreachable the upstream GET error surfaces wrapped
// (the dev-only static fallback is the caller's concern, not this client's).
func TestBreedDistributionClient_Fetch_TransportErrorWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	c := clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
		BaseURL:    deadURL,
		HTTPClient: &http.Client{Timeout: 500 * time.Millisecond},
	})
	_, err := c.Lookup("t", "egg.standard.v1")
	if err == nil {
		t.Fatal("expected transport error against dead upstream")
	}
	if errors.Is(err, growth.ErrSkuNotFound) {
		t.Error("transport error must NOT map to ErrSkuNotFound")
	}
}

// TestBreedDistributionClient_DefaultTimeoutWhenZero — constructing without
// an explicit timeout or HTTPClient uses the default 5s client (it must not
// be nil and must be usable).
func TestBreedDistributionClient_DefaultTimeoutWhenZero(t *testing.T) {
	c := clients.NewBreedDistributionClient(clients.BreedDistributionClientOptions{
		BaseURL: "https://example.test",
	})
	// Empty sku still validates without a network call.
	if _, err := c.Lookup("t", ""); !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want growth.ErrInvalidArguments", err)
	}
}

// --- gRPC client constructor + nil-safety ------------------------------------

// TestNewBreedDistributionGRPCClient_DefaultsDialOptsAndTimeout — when no
// DialOpts and a zero/negative CallTimeout + TTL are supplied, the
// constructor fills the insecure-default DialOpts, the default call
// timeout, and clamps the negative TTL — and still returns a usable client.
func TestNewBreedDistributionGRPCClient_DefaultsDialOptsAndTimeout(t *testing.T) {
	c, err := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target:      "localhost:0", // grpc.Dial is lazy; no connection attempted at construction
		CacheTTL:    -1 * time.Minute,
		CallTimeout: 0,
		// DialOpts intentionally empty → exercises the insecure-default branch.
	})
	if err != nil {
		t.Fatalf("NewBreedDistributionGRPCClient: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
	defer c.Close()

	// Empty sku is rejected without touching the wire.
	if _, err := c.Lookup("t", ""); !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want growth.ErrInvalidArguments", err)
	}
}

// TestBreedDistributionGRPCClient_Close_Idempotent — Close on a nil client
// and a double-Close both return nil without panicking.
func TestBreedDistributionGRPCClient_Close_Idempotent(t *testing.T) {
	var nilClient *clients.BreedDistributionGRPCClient
	if err := nilClient.Close(); err != nil {
		t.Errorf("nil-receiver Close err = %v; want nil", err)
	}

	c, err := clients.NewBreedDistributionGRPCClient(clients.BreedDistributionGRPCClientOptions{
		Target: "localhost:0",
	})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("first Close err = %v; want nil", err)
	}
	// Second Close: conn already nil → returns nil.
	if err := c.Close(); err != nil {
		t.Errorf("second Close err = %v; want nil (idempotent)", err)
	}
}

// TestBreedDistributionGRPCClient_Invalidate_NilSafe — Invalidate on a nil
// client is a no-op (no panic). The non-nil full-clear + single-key drop
// paths are already covered by the main suite; here we pin the nil guard.
func TestBreedDistributionGRPCClient_Invalidate_NilSafe(t *testing.T) {
	var nilClient *clients.BreedDistributionGRPCClient
	nilClient.Invalidate("t", "sku") // must not panic
}

// TestBreedDistributionGRPCClient_Lookup_NilClientGuard — Lookup on a
// zero-value client (no underlying gRPC client) fails loud rather than
// dereferencing a nil pointer.
func TestBreedDistributionGRPCClient_Lookup_NilClientGuard(t *testing.T) {
	c := &clients.BreedDistributionGRPCClient{}
	_, err := c.Lookup("t", "egg.standard.v1")
	if err == nil {
		t.Fatal("expected error on uninitialised gRPC client")
	}
}
