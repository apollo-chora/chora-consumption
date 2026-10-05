// breed_distribution_grpc_client.go — gRPC adapter that satisfies the
// growth.BreedDistributionProvider port by calling chora-tenancy's
// CompanionEgg.PreviewEggOdds RPC.
//
// Per ddd-enforcement HARD RULE: chora-consumption MUST NOT query the
// chora_tenancy database directly. This client is the typed-RPC successor
// to the REST BreedDistributionClient — faster wire format, first-class
// mesh mTLS via Cloud Service Mesh, no JSON round-trip cost.
//
// Per feedback_no_inline_config: target URL + cache TTL come from env vars
// resolved at cmd/server boot:
//
//	CHORA_TENANCY_GRPC_BASE_URL        gRPC target for chora-tenancy
//	                                   (e.g., "chora-tenancy:9090" via mesh DNS)
//	CHORA_TENANCY_EGG_ODDS_CACHE_TTL   cache TTL (Go duration, default 5m)
//
// Cache semantics match the REST client:
//   - TTL 5m default
//   - Sum-to-100 validation before caching
//   - Invalidate() supports per-(tenant, sku) drops + full clear
//
// REST fallback: growth_wiring.go in chora-consumption picks gRPC over
// REST when CHORA_TENANCY_GRPC_BASE_URL is set; otherwise falls through
// to the REST BreedDistributionClient. Static fallback still applies if
// neither base URL is configured.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	tenancyv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/tenancy/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// DefaultBreedDistributionGRPCCallTimeout is the per-call deadline applied
// when the caller does not override via options.
const DefaultBreedDistributionGRPCCallTimeout = 5 * time.Second

// BreedDistributionGRPCClient is a gRPC adapter that calls chora-tenancy's
// CompanionEgg.PreviewEggOdds RPC.
//
// Implements growth.BreedDistributionProvider.
//
// Construction always returns a *BreedDistributionGRPCClient. Close()
// MUST be called at service shutdown to drain the underlying *grpc.ClientConn.
type BreedDistributionGRPCClient struct {
	target      string
	ttl         time.Duration
	callTimeout time.Duration

	conn   *grpc.ClientConn
	client tenancyv1.CompanionEggClient

	mu    sync.RWMutex
	cache map[string]grpcCacheEntry // key = "tenantID|sku"
}

type grpcCacheEntry struct {
	dist     []growth.BreedWeight
	loadedAt time.Time
}

// BreedDistributionGRPCClientOptions configures the client.
type BreedDistributionGRPCClientOptions struct {
	// Target is the gRPC dial target. In production this is the mesh DNS
	// name "chora-tenancy:9090"; in dev it can be "localhost:9090". In
	// tests, "bufconn" + a custom DialOption.
	Target string

	// CacheTTL — <=0 disables caching.
	CacheTTL time.Duration

	// CallTimeout — per-RPC deadline. Defaults to 5s if zero.
	CallTimeout time.Duration

	// DialOpts overrides the default insecure credentials. Tests inject
	// bufconn + insecure credentials. Production wires
	// credentials.NewClientTLSFromCert or mesh-provided credentials.
	//
	// If empty, the client defaults to insecure.NewCredentials() —
	// suitable for in-cluster traffic where Cloud Service Mesh handles
	// mTLS at L4 (the adapter sees plain HTTP/2 between sidecars).
	DialOpts []grpc.DialOption
}

// NewBreedDistributionGRPCClient builds a client. Target must be non-empty.
//
// Per CLAUDE.md "Cloud Service Mesh + Workload Identity Federation" — the
// mesh handles mTLS transparently; the client adapter speaks plain gRPC
// to its sidecar. Production deployments do NOT need TLS credentials
// configured at this layer.
func NewBreedDistributionGRPCClient(opts BreedDistributionGRPCClientOptions) (*BreedDistributionGRPCClient, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, errors.New("clients: gRPC target required")
	}
	ttl := opts.CacheTTL
	if ttl < 0 {
		ttl = 0
	}
	timeout := opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultBreedDistributionGRPCCallTimeout
	}

	dialOpts := opts.DialOpts
	if len(dialOpts) == 0 {
		dialOpts = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}
	// Use Dial — bufconn requires the legacy API; grpc.NewClient doesn't
	// support custom dialers that bypass DNS resolution.
	//nolint:staticcheck // bufconn requires legacy Dial.
	conn, err := grpc.Dial(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("clients: grpc.Dial %s: %w", target, err)
	}
	c := &BreedDistributionGRPCClient{
		target:      target,
		ttl:         ttl,
		callTimeout: timeout,
		conn:        conn,
		client:      tenancyv1.NewCompanionEggClient(conn),
		cache:       make(map[string]grpcCacheEntry),
	}
	return c, nil
}

// Compile-time guarantee.
var _ growth.BreedDistributionProvider = (*BreedDistributionGRPCClient)(nil)

// Close drains the underlying *grpc.ClientConn. Idempotent.
func (c *BreedDistributionGRPCClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Lookup returns the breed_distribution for an egg SKU. Returns
// growth.ErrSkuNotFound on a NOT_FOUND gRPC status.
//
// Other gRPC errors (PERMISSION_DENIED, UNAVAILABLE, INTERNAL) surface
// as generic wrapped errors — callers should not treat them as
// SKU-not-found.
func (c *BreedDistributionGRPCClient) Lookup(tenantID, eggSku string) ([]growth.BreedWeight, error) {
	if strings.TrimSpace(eggSku) == "" {
		return nil, fmt.Errorf("%w: egg_sku required", growth.ErrInvalidArguments)
	}
	if c == nil || c.client == nil {
		return nil, errors.New("clients: gRPC client not initialised")
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
	ctx, cancel := context.WithTimeout(context.Background(), c.callTimeout)
	defer cancel()
	resp, err := c.client.PreviewEggOdds(ctx, &tenancyv1.PreviewEggOddsRequest{
		TenantId: tenantID,
		EggSku:   eggSku,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, growth.ErrSkuNotFound
		}
		return nil, fmt.Errorf("clients: PreviewEggOdds: %w", err)
	}

	// Translate proto → domain.
	dist := make([]growth.BreedWeight, 0, len(resp.GetOdds()))
	for _, o := range resp.GetOdds() {
		dist = append(dist, growth.BreedWeight{
			Species:     o.GetSpecies(),
			Probability: o.GetProbability(),
			Rarity:      o.GetRarity(),
		})
	}

	// Validate sum-to-100 before caching + returning.
	if err := growth.ValidateBreedDistribution(dist); err != nil {
		return nil, fmt.Errorf("clients: upstream returned invalid distribution: %w", err)
	}

	if c.ttl > 0 {
		c.mu.Lock()
		c.cache[cacheKey] = grpcCacheEntry{
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
func (c *BreedDistributionGRPCClient) Invalidate(tenantID, eggSku string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if tenantID == "" && eggSku == "" {
		c.cache = make(map[string]grpcCacheEntry)
		return
	}
	delete(c.cache, tenantID+"|"+eggSku)
}
