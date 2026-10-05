// breed_distribution_client_test.go — covers the REST client + the in-memory
// fallback provider for growth.BreedDistributionProvider.
package clients

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// helper: build a fake chora-tenancy that responds with a sum-to-100
// distribution.
func newFakeTenancy(status int, body any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/odds") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func validBody() map[string]any {
	return map[string]any{
		"sku": "egg.standard.v1",
		"odds": []map[string]any{
			{"species": "owl", "probability": 25.0, "rarity": "common"},
			{"species": "fox", "probability": 25.0, "rarity": "common"},
			{"species": "penguin", "probability": 20.0, "rarity": "common"},
			{"species": "dragon", "probability": 22.5, "rarity": "uncommon"},
			{"species": "phoenix", "probability": 7.5, "rarity": "legendary"},
		},
		"total_weight":            100.0,
		"distribution_updated_at": "2026-05-13T00:00:00Z",
	}
}

func TestBreedDistributionClient_Lookup_HappyPath(t *testing.T) {
	srv := newFakeTenancy(http.StatusOK, validBody())
	defer srv.Close()

	c := NewBreedDistributionClient(BreedDistributionClientOptions{
		BaseURL: srv.URL, CacheTTL: 0,
	})
	dist, err := c.Lookup("tenant-1", "egg.standard.v1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(dist) != 5 {
		t.Errorf("len(dist) = %d, want 5", len(dist))
	}
	if err := growth.ValidateBreedDistribution(dist); err != nil {
		t.Errorf("returned dist invalid: %v", err)
	}
}

func TestBreedDistributionClient_Lookup_404MapsToSkuNotFound(t *testing.T) {
	srv := newFakeTenancy(http.StatusNotFound, map[string]any{"error": "not found"})
	defer srv.Close()

	c := NewBreedDistributionClient(BreedDistributionClientOptions{BaseURL: srv.URL})
	_, err := c.Lookup("tenant-1", "egg.missing")
	if !errors.Is(err, growth.ErrSkuNotFound) {
		t.Errorf("err = %v; want ErrSkuNotFound", err)
	}
}

func TestBreedDistributionClient_Lookup_RejectsInvalidDistribution(t *testing.T) {
	// Sum != 100 → ValidateBreedDistribution fails.
	bad := map[string]any{
		"sku": "egg.invalid",
		"odds": []map[string]any{
			{"species": "owl", "probability": 50.0, "rarity": "common"},
			{"species": "fox", "probability": 30.0, "rarity": "common"},
		},
	}
	srv := newFakeTenancy(http.StatusOK, bad)
	defer srv.Close()
	c := NewBreedDistributionClient(BreedDistributionClientOptions{BaseURL: srv.URL})
	_, err := c.Lookup("tenant-1", "egg.invalid")
	if err == nil {
		t.Errorf("expected error from invalid sum-to-100")
	}
}

func TestBreedDistributionClient_Lookup_EmptySkuRejected(t *testing.T) {
	c := NewBreedDistributionClient(BreedDistributionClientOptions{BaseURL: "http://stub"})
	_, err := c.Lookup("tenant-1", "")
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Errorf("err = %v; want ErrInvalidArguments", err)
	}
}

func TestBreedDistributionClient_Lookup_NoBaseURL(t *testing.T) {
	c := NewBreedDistributionClient(BreedDistributionClientOptions{})
	_, err := c.Lookup("tenant-1", "egg.standard.v1")
	if err == nil {
		t.Error("expected error when base URL empty")
	}
}

func TestBreedDistributionClient_Lookup_CacheHitAvoidsSecondRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(validBody())
	}))
	defer srv.Close()

	c := NewBreedDistributionClient(BreedDistributionClientOptions{
		BaseURL: srv.URL, CacheTTL: 5 * time.Minute,
	})
	if _, err := c.Lookup("tenant-1", "egg.standard.v1"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if _, err := c.Lookup("tenant-1", "egg.standard.v1"); err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (cache miss expected)", hits)
	}

	// Invalidate forces refetch.
	c.Invalidate("tenant-1", "egg.standard.v1")
	if _, err := c.Lookup("tenant-1", "egg.standard.v1"); err != nil {
		t.Fatalf("third lookup: %v", err)
	}
	if hits != 2 {
		t.Errorf("hits = %d after invalidate, want 2", hits)
	}
}

func TestBreedDistributionClient_Lookup_InvalidateAllClearsCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(validBody())
	}))
	defer srv.Close()
	c := NewBreedDistributionClient(BreedDistributionClientOptions{
		BaseURL: srv.URL, CacheTTL: 5 * time.Minute,
	})
	_, _ = c.Lookup("t-1", "egg.standard.v1")
	c.Invalidate("", "")
	_, _ = c.Lookup("t-1", "egg.standard.v1")
	if hits != 2 {
		t.Errorf("hits = %d, want 2 after global invalidate", hits)
	}
}

func TestBreedDistributionClient_Lookup_Returns5xxAsError(t *testing.T) {
	srv := newFakeTenancy(http.StatusInternalServerError, map[string]any{"error": "boom"})
	defer srv.Close()
	c := NewBreedDistributionClient(BreedDistributionClientOptions{BaseURL: srv.URL})
	_, err := c.Lookup("tenant-1", "egg.standard.v1")
	if err == nil {
		t.Errorf("expected error on 5xx")
	}
}

func TestStaticBreedDistributionProvider_DefaultEgg(t *testing.T) {
	p := NewStaticBreedDistributionProvider(nil)
	dist, err := p.Lookup("any-tenant", "egg.standard.v1")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := growth.ValidateBreedDistribution(dist); err != nil {
		t.Errorf("default static dist invalid: %v", err)
	}
}

func TestStaticBreedDistributionProvider_UnknownSku(t *testing.T) {
	p := NewStaticBreedDistributionProvider(nil)
	_, err := p.Lookup("any-tenant", "egg.unknown.v1")
	if !errors.Is(err, growth.ErrSkuNotFound) {
		t.Errorf("err = %v; want ErrSkuNotFound", err)
	}
}

func TestStaticBreedDistributionProvider_CustomDists(t *testing.T) {
	p := NewStaticBreedDistributionProvider(map[string][]growth.BreedWeight{
		"egg.x": {
			{Species: "owl", Probability: 100, Rarity: "common"},
		},
	})
	dist, err := p.Lookup("t", "egg.x")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(dist) != 1 {
		t.Errorf("len(dist) = %d, want 1", len(dist))
	}
}

func TestBreedDistributionClient_ParseOddsResponse_EmptyArrayRejected(t *testing.T) {
	body := []byte(`{"sku":"egg.x","odds":[]}`)
	_, err := parseOddsResponse(body)
	if err == nil {
		t.Errorf("expected error on empty odds")
	}
}

func TestBreedDistributionClient_ParseOddsResponse_BadJSON(t *testing.T) {
	body := []byte(`not-json`)
	_, err := parseOddsResponse(body)
	if err == nil {
		t.Errorf("expected JSON decode error")
	}
}
