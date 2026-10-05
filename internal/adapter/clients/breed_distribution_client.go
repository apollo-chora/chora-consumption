// breed_distribution_client.go — REST adapter that satisfies the
// growth.BreedDistributionProvider port by calling chora-tenancy's
// GET /api/companion-eggs/{sku}/odds endpoint.
//
// Per ddd-enforcement HARD RULE: chora-consumption MUST NOT query the
// chora_tenancy database directly. This client is the sanctioned
// cross-domain read path until the chora-tenancy gRPC service surfaces
// a typed PreviewEggOdds RPC (PROD-C scope).
//
// Per feedback_no_inline_config: base URL + cache TTL come from env vars
// resolved at cmd/server boot:
//
//	CHORA_TENANCY_BASE_URL              base URL for chora-tenancy
//	CHORA_TENANCY_EGG_ODDS_CACHE_TTL    cache TTL (Go duration, default 5m)
//
// Per ai-runtime-guardrails / IMDA D2 transparency: the breed_distribution
// is the IMDA-required odds disclosure surface; cache TTL of 5m is the
// upper bound a learner could see stale odds before a tenant admin
// re-publishes a SKU. Cache is on by default; tests construct without
// caching for determinism.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// DefaultBreedDistributionCacheTTL — 5 minutes (per task instructions). The
// CHORA_TENANCY_EGG_ODDS_CACHE_TTL env var overrides at boot.
const DefaultBreedDistributionCacheTTL = 5 * time.Minute

// BreedDistributionClient is a REST adapter that calls chora-tenancy's
// GET /api/companion-eggs/{sku}/odds.
//
// Implements growth.BreedDistributionProvider.
type BreedDistributionClient struct {
	baseURL string
	hc      *http.Client
	ttl     time.Duration

	mu    sync.RWMutex
	cache map[string]breedCacheEntry // key = "tenantID|sku"
}

type breedCacheEntry struct {
	dist     []growth.BreedWeight
	loadedAt time.Time
}

// BreedDistributionClientOptions configures the client.
type BreedDistributionClientOptions struct {
	BaseURL    string
	Timeout    time.Duration
	CacheTTL   time.Duration // <=0 disables caching
	HTTPClient *http.Client  // optional override (tests)
}

// NewBreedDistributionClient builds a client. Production wires base URL +
// timeout from env vars at cmd/server boot; tests construct a custom
// http.Client to stub the upstream.
func NewBreedDistributionClient(opts BreedDistributionClientOptions) *BreedDistributionClient {
	hc := opts.HTTPClient
	if hc == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		hc = &http.Client{Timeout: timeout}
	}
	ttl := opts.CacheTTL
	if ttl < 0 {
		ttl = 0
	}
	return &BreedDistributionClient{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		hc:      hc,
		ttl:     ttl,
		cache:   make(map[string]breedCacheEntry),
	}
}

// Compile-time guarantee.
var _ growth.BreedDistributionProvider = (*BreedDistributionClient)(nil)

// Lookup returns the breed_distribution for an egg SKU. Returns
// growth.ErrSkuNotFound on a 404 from chora-tenancy.
func (c *BreedDistributionClient) Lookup(tenantID, eggSku string) ([]growth.BreedWeight, error) {
	if strings.TrimSpace(eggSku) == "" {
		return nil, fmt.Errorf("%w: egg_sku required", growth.ErrInvalidArguments)
	}
	if c.baseURL == "" {
		return nil, errors.New("clients: breed distribution base URL not configured")
	}
	cacheKey := tenantID + "|" + eggSku

	// Cache lookup.
	if c.ttl > 0 {
		c.mu.RLock()
		entry, ok := c.cache[cacheKey]
		c.mu.RUnlock()
		if ok && time.Since(entry.loadedAt) < c.ttl {
			out := make([]growth.BreedWeight, len(entry.dist))
			copy(out, entry.dist)
			return out, nil
		}
	}

	// Fetch upstream.
	dist, err := c.fetch(tenantID, eggSku)
	if err != nil {
		return nil, err
	}
	// Validate sum-to-100 before caching + returning.
	if err := growth.ValidateBreedDistribution(dist); err != nil {
		return nil, fmt.Errorf("clients: upstream returned invalid distribution: %w", err)
	}

	if c.ttl > 0 {
		c.mu.Lock()
		c.cache[cacheKey] = breedCacheEntry{
			dist:     append([]growth.BreedWeight(nil), dist...),
			loadedAt: time.Now(),
		}
		c.mu.Unlock()
	}
	return dist, nil
}

// Invalidate drops cached entries (or a single (tenant, sku) when both are
// supplied). Used by subscribers when chora-tenancy publishes a catalog
// update event.
func (c *BreedDistributionClient) Invalidate(tenantID, eggSku string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tenantID == "" && eggSku == "" {
		c.cache = make(map[string]breedCacheEntry)
		return
	}
	delete(c.cache, tenantID+"|"+eggSku)
}

// fetch performs the upstream REST call.
func (c *BreedDistributionClient) fetch(tenantID, eggSku string) ([]growth.BreedWeight, error) {
	url := fmt.Sprintf("%s/api/companion-eggs/%s/odds", c.baseURL, eggSku)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("clients: build request: %w", err)
	}
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clients: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		// continue
	case http.StatusNotFound:
		return nil, growth.ErrSkuNotFound
	default:
		return nil, fmt.Errorf("clients: GET %s: status %d body %s", url, resp.StatusCode, string(body))
	}
	return parseOddsResponse(body)
}

// oddsWire mirrors chora-tenancy's response shape:
//
//	{
//	    "sku": "...",
//	    "odds": [{"species": "owl", "probability": 35.0, "rarity": "common"}, ...],
//	    "total_weight": 100.0,
//	    "distribution_updated_at": "...",
//	    "chora_imda_dimension": "transparency"
//	}
type oddsWire struct {
	SKU                   string    `json:"sku"`
	Odds                  []oddWire `json:"odds"`
	TotalWeight           float64   `json:"total_weight"`
	DistributionUpdatedAt string    `json:"distribution_updated_at"`
}

type oddWire struct {
	Species     string  `json:"species"`
	Probability float64 `json:"probability"`
	Rarity      string  `json:"rarity"`
}

// parseOddsResponse decodes the upstream JSON and converts to []BreedWeight.
func parseOddsResponse(body []byte) ([]growth.BreedWeight, error) {
	var w oddsWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("clients: decode odds response: %w", err)
	}
	if len(w.Odds) == 0 {
		return nil, fmt.Errorf("clients: empty odds response")
	}
	out := make([]growth.BreedWeight, 0, len(w.Odds))
	for _, o := range w.Odds {
		out = append(out, growth.BreedWeight{
			Species:     o.Species,
			Probability: o.Probability,
			Rarity:      o.Rarity,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Static (no-network) provider — fallback when chora-tenancy is unreachable
// at boot. Yields deterministic distributions per SKU; tests + local dev.
// ---------------------------------------------------------------------------

// StaticBreedDistributionProvider serves a hardcoded breed_distribution map
// keyed by egg SKU. Useful for local dev when chora-tenancy is not
// reachable; production must wire BreedDistributionClient against the live
// tenancy URL.
//
// NOTE: this is NOT a substitute for the canonical chora-tenancy catalog —
// it exists as a dev-only fallback so chora-consumption boots cleanly when
// the cross-service base URL is not yet configured.
type StaticBreedDistributionProvider struct {
	dists map[string][]growth.BreedWeight
}

// NewStaticBreedDistributionProvider seeds with the canonical ADR-149
// "egg.standard.v1" distribution if the supplied map is empty.
func NewStaticBreedDistributionProvider(dists map[string][]growth.BreedWeight) *StaticBreedDistributionProvider {
	if len(dists) == 0 {
		// 5 hero species only (CHO-2032) — a fallback that offered the retired
		// gacha breeds would fail growth.ValidateBreedDistribution now.
		dists = map[string][]growth.BreedWeight{
			"egg.standard.v1": {
				{Species: "owl", Probability: 25, Rarity: "common"},
				{Species: "fox", Probability: 25, Rarity: "common"},
				{Species: "penguin", Probability: 25, Rarity: "common"},
				{Species: "dragon", Probability: 15, Rarity: "uncommon"},
				{Species: "phoenix", Probability: 10, Rarity: "legendary"},
			},
		}
	}
	return &StaticBreedDistributionProvider{dists: dists}
}

// Lookup returns the static distribution for a SKU. Returns
// growth.ErrSkuNotFound on miss.
func (p *StaticBreedDistributionProvider) Lookup(_, eggSku string) ([]growth.BreedWeight, error) {
	d, ok := p.dists[eggSku]
	if !ok {
		return nil, growth.ErrSkuNotFound
	}
	out := make([]growth.BreedWeight, len(d))
	copy(out, d)
	return out, nil
}

// Compile-time guarantee.
var _ growth.BreedDistributionProvider = (*StaticBreedDistributionProvider)(nil)
