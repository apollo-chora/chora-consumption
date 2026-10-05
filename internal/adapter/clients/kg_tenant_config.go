// kg_tenant_config.go — adapter for the Knowledge Graph tenant config
// reader (per ADR-143 §8 + ADR-142 precedent).
//
// Reads two per-tenant `config_overrides` keys from chora-tenancy:
//   - max_concurrent_kg_clusters_per_user (int 1..10, default 3)
//   - kg_fog_invalidation_grace_seconds   (int 60..3600, default 300)
//
// Per feedback_no_inline_config: the env-var defaults are read at
// service boot (cmd/server) and passed in. The Pub/Sub-cached
// projection from chora-tenancy lands later. Until it lands, this
// adapter exposes a STATIC reader that returns the env-default for
// every tenant — sufficient for Phyllis Phase II (3 clusters per user
// per ADR-143).
//
// Hexagonal port:
//
//	user_knowledge_graph.TenantConfigReader
package clients

import (
	"context"
	"errors"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// Default config dial bounds per ADR-143 §8.
const (
	KGDefaultMaxClustersPerUser    = 3
	KGDefaultFogInvalidationGraceS = 300
	kgMinClustersPerUser           = 1
	kgMaxClustersPerUser           = 10
	kgMinFogGraceS                 = 60
	kgMaxFogGraceS                 = 3600
)

// ErrKGConfigInvalidBound — config value outside the documented range.
var ErrKGConfigInvalidBound = errors.New("kg_tenant_config: value outside documented bound")

// StaticKGConfigReader returns a fixed config for every tenant. Used in
// dev / Phyllis Phase II until the Pub/Sub projection from
// chora-tenancy is wired.
//
// Implements userknowledgegraph.TenantConfigReader.
type StaticKGConfigReader struct {
	max      int
	graceSec int
}

// NewStaticKGConfigReader constructs a reader with the supplied dials.
// Both values must be within their documented bounds (per ADR-143 §8).
//
// Per feedback_no_inline_config: cmd/server is the ONLY layer that
// reads env vars. It passes resolved ints into this constructor.
func NewStaticKGConfigReader(maxClusters, fogGraceSeconds int) (*StaticKGConfigReader, error) {
	if maxClusters < kgMinClustersPerUser || maxClusters > kgMaxClustersPerUser {
		return nil, ErrKGConfigInvalidBound
	}
	if fogGraceSeconds < kgMinFogGraceS || fogGraceSeconds > kgMaxFogGraceS {
		return nil, ErrKGConfigInvalidBound
	}
	return &StaticKGConfigReader{max: maxClusters, graceSec: fogGraceSeconds}, nil
}

// ReadKnowledgeGraphConfig returns the static dial — same response for
// every tenant. The tenantID is accepted for the port shape but ignored.
func (r *StaticKGConfigReader) ReadKnowledgeGraphConfig(ctx context.Context, tenantID string) (userknowledgegraph.KnowledgeGraphConfig, error) {
	if strings.TrimSpace(tenantID) == "" {
		return userknowledgegraph.KnowledgeGraphConfig{}, errors.New("kg_tenant_config: tenant_id required")
	}
	return userknowledgegraph.KnowledgeGraphConfig{
		MaxConcurrentClustersPerUser: r.max,
		FogInvalidationGraceSeconds:  r.graceSec,
	}, nil
}

// PerTenantOverrideKGConfigReader is an in-process reader that allows
// per-tenant override of the defaults. Used by tests to verify the cap
// behaviour for tenants whose config differs from the platform default.
//
// Implements userknowledgegraph.TenantConfigReader.
type PerTenantOverrideKGConfigReader struct {
	defaults  userknowledgegraph.KnowledgeGraphConfig
	overrides map[string]userknowledgegraph.KnowledgeGraphConfig
}

// NewPerTenantOverrideKGConfigReader constructs a reader with platform
// defaults + a per-tenant override map.
func NewPerTenantOverrideKGConfigReader(defaultMax, defaultGraceS int) (*PerTenantOverrideKGConfigReader, error) {
	if defaultMax < kgMinClustersPerUser || defaultMax > kgMaxClustersPerUser {
		return nil, ErrKGConfigInvalidBound
	}
	if defaultGraceS < kgMinFogGraceS || defaultGraceS > kgMaxFogGraceS {
		return nil, ErrKGConfigInvalidBound
	}
	return &PerTenantOverrideKGConfigReader{
		defaults: userknowledgegraph.KnowledgeGraphConfig{
			MaxConcurrentClustersPerUser: defaultMax,
			FogInvalidationGraceSeconds:  defaultGraceS,
		},
		overrides: make(map[string]userknowledgegraph.KnowledgeGraphConfig),
	}, nil
}

// SetOverride records a tenant override; values are bounds-checked.
func (r *PerTenantOverrideKGConfigReader) SetOverride(tenantID string, cfg userknowledgegraph.KnowledgeGraphConfig) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return errors.New("kg_tenant_config: tenant_id required")
	}
	if cfg.MaxConcurrentClustersPerUser < kgMinClustersPerUser || cfg.MaxConcurrentClustersPerUser > kgMaxClustersPerUser {
		return ErrKGConfigInvalidBound
	}
	if cfg.FogInvalidationGraceSeconds < kgMinFogGraceS || cfg.FogInvalidationGraceSeconds > kgMaxFogGraceS {
		return ErrKGConfigInvalidBound
	}
	r.overrides[tenantID] = cfg
	return nil
}

// ReadKnowledgeGraphConfig returns the override or default.
func (r *PerTenantOverrideKGConfigReader) ReadKnowledgeGraphConfig(ctx context.Context, tenantID string) (userknowledgegraph.KnowledgeGraphConfig, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return userknowledgegraph.KnowledgeGraphConfig{}, errors.New("kg_tenant_config: tenant_id required")
	}
	if cfg, ok := r.overrides[tenantID]; ok {
		return cfg, nil
	}
	return r.defaults, nil
}
