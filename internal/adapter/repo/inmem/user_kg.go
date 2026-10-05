// user_kg.go — in-memory adapter implementations of the per-user
// Knowledge Graph ports declared at
//
//	services/chora-consumption/internal/domain/user_knowledge_graph/ports.go
//
// These are MVP/test adapters. Production (M12+) replaces them with
// PostgreSQL via pgx + RLS — the libs/chora-go-common/rls helper is
// the canonical entry point for the SET LOCAL chora.tenant_id +
// chora.user_gcid pair (per multi-tenant-rls skill). Per ADR-143 §2 the
// per-user RLS policies are declared in migrations 0002 + 0003.
//
// All repos are thread-safe via per-store mutex. Soft-deleted entities
// are retained in-memory but excluded from default reads (per
// ddd-enforcement #6).
package inmem

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------- MapClusterRepo ----------

// MapClusterRepo is the in-memory adapter for
// userknowledgegraph.MapClusterRepo.
type MapClusterRepo struct {
	mu    sync.RWMutex
	store map[string]*userknowledgegraph.MapCluster
}

// NewMapClusterRepo constructs an empty repo.
func NewMapClusterRepo() *MapClusterRepo {
	return &MapClusterRepo{store: make(map[string]*userknowledgegraph.MapCluster)}
}

// Save persists a cluster (insert or update). The OCC version field on
// the cluster mirrors the production semantics; the in-memory adapter
// does NOT enforce concurrent-modification (production pgx will).
func (r *MapClusterRepo) Save(ctx context.Context, c *userknowledgegraph.MapCluster) error {
	if c == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Defensive copy of the struct (callers retain a pointer; mutation
	// after Save shouldn't affect the stored row).
	cp := *c
	r.store[c.ClusterID] = &cp
	return nil
}

// Load fetches a cluster by ID. Returns ErrNotFound when absent or
// soft-deleted.
func (r *MapClusterRepo) Load(ctx context.Context, id string) (*userknowledgegraph.MapCluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.store[id]
	if !ok || c.DeletedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

// ListActiveByUser returns ACTIVE clusters for the (tenant, user).
func (r *MapClusterRepo) ListActiveByUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.MapCluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.MapCluster, 0)
	for _, c := range r.store {
		if c.DeletedAt != nil {
			continue
		}
		if c.TenantID != tenantID || c.UserGCID != userGCID {
			continue
		}
		if c.Status != userknowledgegraph.ClusterStatusActive {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// CountActiveByUser is an O(n) helper — production pgx uses a SELECT
// COUNT(*) WHERE status='active'.
func (r *MapClusterRepo) CountActiveByUser(ctx context.Context, tenantID, userGCID string) (int, error) {
	list, err := r.ListActiveByUser(ctx, tenantID, userGCID)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// FindBySurvivor returns clusters that have been merged INTO the
// surviving cluster. Used for the H+ "merged from" breadcrumb.
func (r *MapClusterRepo) FindBySurvivor(ctx context.Context, tenantID, survivingClusterID string) ([]*userknowledgegraph.MapCluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.MapCluster, 0)
	for _, c := range r.store {
		if c.DeletedAt != nil {
			continue
		}
		if c.TenantID != tenantID {
			continue
		}
		if c.MergedIntoClusterID != survivingClusterID {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	return out, nil
}

// ListAllByUser returns every non-deleted cluster (any status) for
// (tenant, user). Used by `GET /v1/me/knowledge-graph/clusters?status=*`.
func (r *MapClusterRepo) ListAllByUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.MapCluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.MapCluster, 0)
	for _, c := range r.store {
		if c.DeletedAt != nil {
			continue
		}
		if c.TenantID != tenantID || c.UserGCID != userGCID {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ---------- ExplorationRepo ----------

// ExplorationRepo is the in-memory adapter for
// userknowledgegraph.ExplorationRepo.
type ExplorationRepo struct {
	mu    sync.RWMutex
	store map[string]*userknowledgegraph.Exploration // exploration_id → exp
	trail map[string][]*userknowledgegraph.TrailHop  // exploration_id → []hop (ordered)
}

// NewExplorationRepo constructs an empty repo.
func NewExplorationRepo() *ExplorationRepo {
	return &ExplorationRepo{
		store: make(map[string]*userknowledgegraph.Exploration),
		trail: make(map[string][]*userknowledgegraph.TrailHop),
	}
}

// Save persists an exploration (insert or update).
func (r *ExplorationRepo) Save(ctx context.Context, e *userknowledgegraph.Exploration) error {
	if e == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	r.store[e.ExplorationID] = &cp
	return nil
}

// Load returns an exploration or ErrNotFound.
func (r *ExplorationRepo) Load(ctx context.Context, id string) (*userknowledgegraph.Exploration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.store[id]
	if !ok || e.DeletedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	cp := *e
	return &cp, nil
}

// ListByCluster returns the explorations for a cluster.
func (r *ExplorationRepo) ListByCluster(ctx context.Context, clusterID string) ([]*userknowledgegraph.Exploration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.Exploration, 0)
	for _, e := range r.store {
		if e.DeletedAt != nil {
			continue
		}
		if e.ClusterID != clusterID {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// AppendTrailHop atomically updates the exploration AND appends the hop
// in the same critical section. Production pgx wraps both in a single
// transaction; the trigger `enforce_kg_trail_append_only` rejects any
// UPDATE/DELETE on kg_exploration_trail.
func (r *ExplorationRepo) AppendTrailHop(ctx context.Context, e *userknowledgegraph.Exploration, hop *userknowledgegraph.TrailHop) error {
	if e == nil || hop == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	r.store[e.ExplorationID] = &cp
	hopCp := *hop
	r.trail[e.ExplorationID] = append(r.trail[e.ExplorationID], &hopCp)
	return nil
}

// LoadTrail returns the hops in chronological (step_index ASC) order.
func (r *ExplorationRepo) LoadTrail(ctx context.Context, explorationID string) ([]*userknowledgegraph.TrailHop, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hops := r.trail[explorationID]
	out := make([]*userknowledgegraph.TrailHop, 0, len(hops))
	for _, h := range hops {
		cp := *h
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepIndex < out[j].StepIndex })
	return out, nil
}

// LatestStepIndex returns the highest step_index for an exploration, or
// -1 when no hops exist.
func (r *ExplorationRepo) LatestStepIndex(ctx context.Context, explorationID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hops := r.trail[explorationID]
	if len(hops) == 0 {
		return -1, nil
	}
	max := -1
	for _, h := range hops {
		if h.StepIndex > max {
			max = h.StepIndex
		}
	}
	return max, nil
}

// ---------- HexagonRepo ----------

// HexagonRepo is the in-memory adapter for userknowledgegraph.HexagonRepo.
//
// Key shape: (exploration_id, focal_atom_id) → *HexagonNode.
type HexagonRepo struct {
	mu    sync.RWMutex
	store map[string]*userknowledgegraph.HexagonNode // composite key
}

// NewHexagonRepo constructs an empty repo.
func NewHexagonRepo() *HexagonRepo {
	return &HexagonRepo{store: make(map[string]*userknowledgegraph.HexagonNode)}
}

func hexKey(explorationID, focalAtomID string) string {
	return explorationID + "|" + focalAtomID
}

// Upsert inserts or updates the hexagon for (exploration, focal).
func (r *HexagonRepo) Upsert(ctx context.Context, h *userknowledgegraph.HexagonNode) error {
	if h == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *h
	r.store[hexKey(h.ExplorationID, h.FocalAtomID)] = &cp
	return nil
}

// FindFresh returns a fresh hexagon (invalidated_at IS NULL). Returns
// ErrNotFound on cache miss OR when the row is invalidated.
func (r *HexagonRepo) FindFresh(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.store[hexKey(explorationID, focalAtomID)]
	if !ok || h.DeletedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	if h.InvalidatedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	cp := *h
	return &cp, nil
}

// Find returns a hexagon regardless of invalidation state.
func (r *HexagonRepo) Find(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.store[hexKey(explorationID, focalAtomID)]
	if !ok || h.DeletedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	cp := *h
	return &cp, nil
}

// FindAllFocalsByUser returns the distinct focal_atom_ids that are
// focal in any HexagonNode of the (tenant, user) — across ALL active
// clusters EXCEPT excludeClusterID. Backed by idx_hex_user_focals in
// production. This is the JUNCTION DETECTION key.
func (r *HexagonRepo) FindAllFocalsByUser(ctx context.Context, tenantID, userGCID, excludeClusterID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	for _, h := range r.store {
		if h.DeletedAt != nil {
			continue
		}
		if h.TenantID != tenantID || h.UserGCID != userGCID {
			continue
		}
		if h.ClusterID == excludeClusterID {
			continue
		}
		if h.InvalidatedAt != nil {
			// Invalidated rows still count for junction detection — they
			// represent the user's existing focal map even before fog
			// regen. Production may differ; for v1 we follow ADR-143
			// §6's intent: "user's existing focal atom_ids across all
			// other active clusters".
		}
		seen[h.FocalAtomID] = true
	}
	out := make([]string, 0, len(seen))
	for atom := range seen {
		out = append(out, atom)
	}
	sort.Strings(out)
	return out, nil
}

// MarkInvalidatedByFocal marks all hexagons whose focal_atom_id matches
// OR whose neighbors_json contains the atom — used by atom_published /
// atom_revision_updated subscribers.
func (r *HexagonRepo) MarkInvalidatedByFocal(ctx context.Context, atomID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, h := range r.store {
		if h.DeletedAt != nil {
			continue
		}
		matches := h.FocalAtomID == atomID
		if !matches {
			for _, n := range h.Neighbors {
				if n.AtomID == atomID {
					matches = true
					break
				}
			}
		}
		if !matches {
			continue
		}
		if err := h.Invalidate(reason); err == nil {
			count++
		}
	}
	return count, nil
}

// MarkInvalidatedByUser marks all hexagons for one user.
func (r *HexagonRepo) MarkInvalidatedByUser(ctx context.Context, tenantID, userGCID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, h := range r.store {
		if h.DeletedAt != nil {
			continue
		}
		if h.TenantID != tenantID || h.UserGCID != userGCID {
			continue
		}
		if err := h.Invalidate(reason); err == nil {
			count++
		}
	}
	return count, nil
}

// HasFocalInCluster reports whether any non-deleted hexagon row exists
// for the (cluster_id, focal_atom_id) tuple. Used by the junction
// detector to attribute focals to clusters in the in-memory adapter.
// Production pgx uses an index probe via idx_hex_user_focals + cluster_id.
func (r *HexagonRepo) HasFocalInCluster(_ context.Context, clusterID, focalAtomID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, h := range r.store {
		if h.DeletedAt != nil {
			continue
		}
		if h.ClusterID == clusterID && h.FocalAtomID == focalAtomID {
			return true, nil
		}
	}
	return false, nil
}

// FindInvalidatedOlderThan returns hexagons whose invalidated_at is older
// than the supplied cutoff (RFC3339 string for the in-memory adapter;
// production accepts time.Time). The simple in-memory implementation
// parses the cutoff string and compares.
func (r *HexagonRepo) FindInvalidatedOlderThan(ctx context.Context, cutoff string) ([]*userknowledgegraph.HexagonNode, error) {
	cutoffT, err := time.Parse(time.RFC3339Nano, cutoff)
	if err != nil {
		// Try plain RFC3339.
		cutoffT, err = time.Parse(time.RFC3339, cutoff)
		if err != nil {
			return nil, err
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.HexagonNode, 0)
	for _, h := range r.store {
		if h.DeletedAt != nil || h.InvalidatedAt == nil {
			continue
		}
		if h.InvalidatedAt.Before(cutoffT) {
			cp := *h
			out = append(out, &cp)
		}
	}
	return out, nil
}

// ---------- AtomSemanticEdgesRepo ----------

// AtomSemanticEdgesRepo is the in-memory adapter for the relocated
// authoring-time atom_semantic_edges table.
type AtomSemanticEdgesRepo struct {
	mu    sync.RWMutex
	store map[string]*userknowledgegraph.AtomSemanticEdge // edge_id → edge
}

// NewAtomSemanticEdgesRepo constructs an empty repo.
func NewAtomSemanticEdgesRepo() *AtomSemanticEdgesRepo {
	return &AtomSemanticEdgesRepo{store: make(map[string]*userknowledgegraph.AtomSemanticEdge)}
}

// Save persists an edge.
func (r *AtomSemanticEdgesRepo) Save(ctx context.Context, e *userknowledgegraph.AtomSemanticEdge) error {
	if e == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	r.store[e.EdgeID] = &cp
	return nil
}

// ListBySource returns the outbound edges for an atom in one tenant.
func (r *AtomSemanticEdgesRepo) ListBySource(ctx context.Context, tenantID, sourceAtomID string) ([]*userknowledgegraph.AtomSemanticEdge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.AtomSemanticEdge, 0)
	for _, e := range r.store {
		if e.DeletedAt != nil {
			continue
		}
		if e.TenantID != tenantID {
			continue
		}
		if e.SourceAtomID != sourceAtomID {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

// ListByTarget returns the inbound edges for an atom in one tenant.
func (r *AtomSemanticEdgesRepo) ListByTarget(ctx context.Context, tenantID, targetAtomID string) ([]*userknowledgegraph.AtomSemanticEdge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.AtomSemanticEdge, 0)
	for _, e := range r.store {
		if e.DeletedAt != nil {
			continue
		}
		if e.TenantID != tenantID {
			continue
		}
		if e.TargetAtomID != targetAtomID {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return out, nil
}

// ---------- JunctionRepo ----------

// JunctionRepo is the in-memory adapter for the persistent Junction
// records.
type JunctionRepo struct {
	mu    sync.RWMutex
	store map[string]*userknowledgegraph.Junction // junction_id → junction
}

// NewJunctionRepo constructs an empty repo.
func NewJunctionRepo() *JunctionRepo {
	return &JunctionRepo{store: make(map[string]*userknowledgegraph.Junction)}
}

// Save persists a junction.
func (r *JunctionRepo) Save(ctx context.Context, j *userknowledgegraph.Junction) error {
	if j == nil {
		return userknowledgegraph.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *j
	// Defensive copy of the slice
	if j.OverlapAtomIDs != nil {
		cp.OverlapAtomIDs = append([]string(nil), j.OverlapAtomIDs...)
	}
	r.store[j.JunctionID] = &cp
	return nil
}

// Load returns a junction by ID.
func (r *JunctionRepo) Load(ctx context.Context, id string) (*userknowledgegraph.Junction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.store[id]
	if !ok || j.DeletedAt != nil {
		return nil, userknowledgegraph.ErrNotFound
	}
	cp := *j
	cp.OverlapAtomIDs = append([]string(nil), j.OverlapAtomIDs...)
	return &cp, nil
}

// ListPendingForUser returns the PENDING junctions for one user.
func (r *JunctionRepo) ListPendingForUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.Junction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*userknowledgegraph.Junction, 0)
	for _, j := range r.store {
		if j.DeletedAt != nil {
			continue
		}
		if j.TenantID != tenantID || j.UserGCID != userGCID {
			continue
		}
		if j.Status != userknowledgegraph.JunctionStatusPending {
			continue
		}
		cp := *j
		cp.OverlapAtomIDs = append([]string(nil), j.OverlapAtomIDs...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.Before(out[j].DetectedAt) })
	return out, nil
}

// FindPendingByPair returns the pending junction for the (tenant, user,
// clusterA, clusterB) tuple — pair order does NOT matter.
func (r *JunctionRepo) FindPendingByPair(ctx context.Context, tenantID, userGCID, clusterAID, clusterBID string) (*userknowledgegraph.Junction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, j := range r.store {
		if j.DeletedAt != nil {
			continue
		}
		if j.TenantID != tenantID || j.UserGCID != userGCID {
			continue
		}
		if j.Status != userknowledgegraph.JunctionStatusPending {
			continue
		}
		if (j.ClusterAID == clusterAID && j.ClusterBID == clusterBID) ||
			(j.ClusterAID == clusterBID && j.ClusterBID == clusterAID) {
			cp := *j
			cp.OverlapAtomIDs = append([]string(nil), j.OverlapAtomIDs...)
			return &cp, nil
		}
	}
	return nil, nil
}
