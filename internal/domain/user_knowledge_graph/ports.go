package userknowledgegraph

import (
	"context"
	"errors"
)

// =============================================================================
// Hexagonal architecture ports for the per-user Knowledge Graph aggregates.
//
// Per ADR-143 + hexagonal skill: domain code never imports infrastructure.
// The interfaces declared here are the ONLY way the domain talks to the
// outside world. Adapters live in chora-consumption/internal/adapter/.
//
// Canonical pattern reference:
//
//	services/chora-consumption/internal/domain/companion/mana_quoter.go
//
// Adapter implementations (NOT in this package) wire:
//
//   - Postgres pgx repos for MapClusterRepo / ExplorationRepo / HexagonRepo
//     (with RLS context: chora.tenant_id, chora.user_gcid, chora.role)
//   - gRPC client to chora-fog-orchestrator for FogOrchestratorClient
//   - gRPC client to chora-tenancy for TenantConfigReader (mirrors mana_quoter)
//   - Pub/Sub publisher with EventEnvelope outbox semantics for EventPublisher
//
// Idempotency is the caller's responsibility on every mutation port —
// the application service threads idempotency_key through to the adapter
// (which translates to (gcid, idempotency_key) DB unique constraints,
// gRPC retry-safe semantics, or Pub/Sub deduplication keys).
// =============================================================================

// Common port-level errors. Adapter layer maps these to gRPC / HTTP codes.
var (
	ErrNotFound        = errors.New("kg: not found")
	ErrConcurrentMod   = errors.New("kg: concurrent modification (OCC version mismatch)")
	ErrConflictState   = errors.New("kg: state conflict (target already merged or archived)")
	ErrTenantConfig    = errors.New("kg: tenant config unavailable")
	ErrFogOrchestrator = errors.New("kg: fog orchestrator unavailable")
	ErrPublishFailed   = errors.New("kg: event publish failed")
	ErrCapReached      = errors.New("kg: max_concurrent_kg_clusters_per_user reached")
)

// =============================================================================
// Repository ports — read + write Postgres persistence
// =============================================================================

// MapClusterRepo persists and queries MapCluster aggregates. RLS context
// (tenant_id + user_gcid) is set on the connection by the adapter before
// any query.
type MapClusterRepo interface {
	// Save persists a new cluster (INSERT) or updates an existing one
	// (UPDATE with OCC on Version). Returns ErrConcurrentMod when the
	// stored Version doesn't match the in-memory cluster's pre-mutation
	// Version.
	Save(ctx context.Context, cluster *MapCluster) error

	// Load fetches a cluster by ID. Returns ErrNotFound when absent or
	// when RLS hides it from the caller.
	Load(ctx context.Context, clusterID string) (*MapCluster, error)

	// ListActiveByUser returns all ACTIVE clusters for the (tenant, user).
	// Used for cap enforcement at create-time + the H+ admin "users at cap"
	// stat panel.
	ListActiveByUser(ctx context.Context, tenantID, userGCID string) ([]*MapCluster, error)

	// CountActiveByUser is an O(1) helper for cap pre-check that doesn't
	// fetch full rows.
	CountActiveByUser(ctx context.Context, tenantID, userGCID string) (int, error)

	// FindBySurvivor returns clusters merged INTO the given surviving
	// cluster (provenance traversal — useful for "merged from" UI breadcrumb).
	FindBySurvivor(ctx context.Context, tenantID, survivingClusterID string) ([]*MapCluster, error)

	// ListAllByUser returns ALL non-deleted clusters for the (tenant, user)
	// regardless of status — the canvas management panel renders active +
	// merged + archived with status chips.
	ListAllByUser(ctx context.Context, tenantID, userGCID string) ([]*MapCluster, error)
}

// ExplorationRepo persists and queries Exploration aggregates.
type ExplorationRepo interface {
	Save(ctx context.Context, exploration *Exploration) error
	Load(ctx context.Context, explorationID string) (*Exploration, error)

	// ListByCluster returns all explorations for a cluster (active + archived).
	ListByCluster(ctx context.Context, clusterID string) ([]*Exploration, error)

	// AppendTrailHop atomically appends a trail row in the same transaction
	// as the Exploration UPDATE (focal change + version bump).
	// Postgres trigger enforces append-only on kg_exploration_trail.
	AppendTrailHop(ctx context.Context, exploration *Exploration, hop *TrailHop) error

	// LoadTrail returns trail hops for an exploration in chronological
	// order (step_index ASC).
	LoadTrail(ctx context.Context, explorationID string) ([]*TrailHop, error)

	// LatestStepIndex returns the highest step_index for an exploration,
	// or -1 when no hops exist yet. Used by application service to compute
	// the next step_index.
	LatestStepIndex(ctx context.Context, explorationID string) (int, error)
}

// AtomSemanticEdgesRepo persists and queries the relocated authoring-time
// atom-to-atom edges (the renamed knowledge_graph_edges table from
// chora_creation, now living in chora_consumption per ADR-143 §3).
//
// Tenant-scoped reads — RLS sets `chora.tenant_id` only (no user_gcid
// scoping for this aggregate, since authoring edges are shared across
// tenant users).
type AtomSemanticEdgesRepo interface {
	Save(ctx context.Context, edge *AtomSemanticEdge) error

	// ListBySource returns the outbound authoring edges for an atom in
	// one tenant. Excludes soft-deleted rows.
	ListBySource(ctx context.Context, tenantID, sourceAtomID string) ([]*AtomSemanticEdge, error)

	// ListByTarget returns the inbound authoring edges for an atom in
	// one tenant. Excludes soft-deleted rows.
	ListByTarget(ctx context.Context, tenantID, targetAtomID string) ([]*AtomSemanticEdge, error)
}

// JunctionRepo persists and queries the persistent Junction records
// for user-consented merge accept/reject lifecycle. Per-user RLS
// scoping applies (RLS sets both chora.tenant_id and chora.user_gcid).
type JunctionRepo interface {
	Save(ctx context.Context, junction *Junction) error
	Load(ctx context.Context, junctionID string) (*Junction, error)

	// ListPendingForUser returns the PENDING junctions for one user.
	// Used by the H+ admin "users with pending joins" stat panel + the
	// learner UI's notification badge.
	ListPendingForUser(ctx context.Context, tenantID, userGCID string) ([]*Junction, error)

	// FindPendingByPair returns the pending Junction for a (tenant,
	// user, clusterA, clusterB) tuple regardless of pair ordering.
	// Returns (nil, nil) when no pending junction exists. Used by the
	// junction-detection upsert path to avoid duplicate detections.
	FindPendingByPair(ctx context.Context, tenantID, userGCID, clusterAID, clusterBID string) (*Junction, error)
}

// HexagonRepo persists and queries the fog cache.
type HexagonRepo interface {
	// Upsert inserts on first call, updates on subsequent calls for the
	// same (exploration_id, focal_atom_id). OCC on Version protects
	// against concurrent fog regen.
	Upsert(ctx context.Context, hexagon *HexagonNode) error

	// FindFresh returns a fresh hexagon (invalidated_at IS NULL) for the
	// given (exploration, focal). Returns ErrNotFound on cache miss OR
	// when the row exists but is invalidated.
	FindFresh(ctx context.Context, explorationID, focalAtomID string) (*HexagonNode, error)

	// Find returns a hexagon regardless of fresh/stale state, or ErrNotFound.
	// Used by force_regenerate flag path.
	Find(ctx context.Context, explorationID, focalAtomID string) (*HexagonNode, error)

	// FindAllFocalsByUser is the JUNCTION DETECTION key. Returns the
	// distinct focal_atom_ids that are focal in any HexagonNode of the
	// given (tenant, user) — across ALL active clusters EXCEPT
	// excludeClusterID (the cluster being explored).
	//
	// Backed by idx_hex_user_focals — sub-millisecond index probe.
	FindAllFocalsByUser(ctx context.Context, tenantID, userGCID, excludeClusterID string) ([]string, error)

	// MarkInvalidatedByFocal marks all hexagons whose focal_atom_id matches
	// OR whose neighbors_json contains the atom — used by atom_published /
	// atom_revision_updated subscribers.
	MarkInvalidatedByFocal(ctx context.Context, atomID string, reason FogInvalidationReason) (count int, err error)

	// MarkInvalidatedByUser marks all hexagons for a user — used by
	// user_retention_shifted subscriber.
	MarkInvalidatedByUser(ctx context.Context, tenantID, userGCID string, reason FogInvalidationReason) (count int, err error)

	// FindInvalidatedOlderThan returns hexagons whose invalidated_at is
	// older than the cutoff — used by the sweeper for forced regeneration.
	FindInvalidatedOlderThan(ctx context.Context, cutoff string) ([]*HexagonNode, error)

	// HasFocalInCluster reports whether atom focalAtomID is the focal of
	// any fresh hexagon in clusterID — the junction detector's overlap
	// probe against the OTHER cluster's explored surface.
	HasFocalInCluster(ctx context.Context, clusterID, focalAtomID string) (bool, error)
}

// =============================================================================
// External service clients (gRPC adapters)
// =============================================================================

// FogOrchestratorClient calls chora-fog-orchestrator's GenerateFog RPC.
// Adapter must thread W3C trace context (traceparent / tracestate) through
// gRPC metadata; SPIFFE identity for mTLS via Cloud Service Mesh.
type FogOrchestratorClient interface {
	// GenerateFog produces 6 ranked + LLM-confidence-calibrated neighbors
	// for the given focal. Synchronous (P50 < 2.5s, P99 < 6s SLO).
	// Idempotent on requestID. existingUserAtomIDs feeds the orchestrator's
	// detect_junctions node (caller populates from
	// HexagonRepo.FindAllFocalsByUser).
	GenerateFog(ctx context.Context, req FogRequest) (*FogResult, error)
}

// FogRequest mirrors the GenerateFogRequest proto shape.
type FogRequest struct {
	RequestID                   string // UUIDv7
	TenantID                    string
	UserGCID                    string
	ClusterID                   string
	ExplorationID               string
	FocalAtomID                 string
	DeliveryMode                string // "graph_discovery"
	ExistingUserAtomIDs         []string
	AdapterURI                  string // optional — tenant LoRA
	FogInvalidationGraceSeconds int32  // tenant config dial
}

// FogResult mirrors the GenerateFogResponse proto shape.
type FogResult struct {
	Neighbors       []HexagonNeighbor
	JunctionAtomIDs []string
	ModelID         string
	TotalCostMicros int64
	InputTokens     int64
	OutputTokens    int64
	FinalState      string // "SUCCESS" / "FAILED" / "ESCALATED"
	ErrorMessage    string
}

// TenantConfigReader reads per-tenant config_overrides keys via gRPC to
// chora-tenancy. Mirrors the precedent at
// services/chora-consumption/internal/domain/companion/mana_quoter.go.
type TenantConfigReader interface {
	// ReadKnowledgeGraphConfig fetches the two KG-specific config dials.
	// Returns env-default values when no per-tenant override exists.
	ReadKnowledgeGraphConfig(ctx context.Context, tenantID string) (KnowledgeGraphConfig, error)
}

// KnowledgeGraphConfig is the resolved tenant-config for KG.
type KnowledgeGraphConfig struct {
	MaxConcurrentClustersPerUser int // 1..10, default KG_DEFAULT_MAX_CLUSTERS_PER_USER
	FogInvalidationGraceSeconds  int // 60..3600, default KG_FOG_INVALIDATION_GRACE_SECONDS
}

// =============================================================================
// Event publisher (Pub/Sub outbox)
// =============================================================================

// EventPublisher publishes domain events with the mandatory EventEnvelope.
// Adapter implementation uses outbox pattern: events written to an
// outbox table in the SAME transaction as the aggregate state change,
// then a separate dispatcher picks them up and publishes to Pub/Sub.
//
// Topic taxonomy: chora.consumption.{aggregate}.{event_type}.v{N} per
// chora-contracts/CLAUDE.md §2.
type EventPublisher interface {
	// PublishMapClusterCreated → chora.consumption.kg_map_cluster.created.v1
	PublishMapClusterCreated(ctx context.Context, cluster *MapCluster) error

	// PublishMapClusterMerged → chora.consumption.kg_map_cluster.merged.v1
	// Carries surviving + merged cluster IDs + via_atom + new display_name.
	PublishMapClusterMerged(ctx context.Context, surviving, merged *MapCluster, viaAtomID string) error

	// PublishMapClusterArchived → chora.consumption.kg_map_cluster.archived.v1
	PublishMapClusterArchived(ctx context.Context, cluster *MapCluster) error

	// PublishExplorationCreated → chora.consumption.kg_exploration.created.v1
	PublishExplorationCreated(ctx context.Context, exploration *Exploration) error

	// PublishExplorationFocalChanged → chora.consumption.kg_exploration.focal_changed.v1
	PublishExplorationFocalChanged(ctx context.Context, exploration *Exploration, hop *TrailHop) error

	// PublishExplorationArchived → chora.consumption.kg_exploration.archived.v1
	PublishExplorationArchived(ctx context.Context, exploration *Exploration) error

	// PublishHexagonFogGenerated → chora.consumption.kg_hexagon_fog.generated.v1
	// Carries cost/token data for the AI Kernel cost ledger.
	PublishHexagonFogGenerated(ctx context.Context, hexagon *HexagonNode, fog *FogResult) error

	// PublishHexagonFogInvalidated → chora.consumption.kg_hexagon_fog.invalidated.v1
	PublishHexagonFogInvalidated(ctx context.Context, hexagon *HexagonNode) error

	// PublishJunctionDetected → chora.consumption.kg_junction.detected.v1
	// Caller passes both clusters (current + other) for the event payload.
	PublishJunctionDetected(ctx context.Context, current, other *MapCluster, viaAtomID, triggeringRunID string) error

	// PublishJunctionAccepted → chora.consumption.kg_junction.accepted.v1
	// Emitted on user-consented merge.
	PublishJunctionAccepted(ctx context.Context, junction *Junction) error

	// PublishJunctionRejected → chora.consumption.kg_junction.rejected.v1
	// Emitted on user decline.
	PublishJunctionRejected(ctx context.Context, junction *Junction) error
}
