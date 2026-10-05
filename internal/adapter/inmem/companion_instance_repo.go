// companion_instance_repo.go — in-memory adapter for the multi-Companion
// (1:N) repository port (`companion.InstanceRepository`).
//
// Used by tests + MVP wiring before production lands on
// repo/pg.CompanionInstanceRepo at M14.1. Thread-safe via per-store mutex.
//
// Soft-deleted Instances are retained but excluded from
//   - Get → returns ErrInstanceNotFound
//   - ListByOwner → filtered out
//   - Create cap-count → soft-deleted rows do not count
//
// per ddd-enforcement.md #5 + #6 (soft delete + default-read filter).
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// CompanionInstanceRepo is the in-memory adapter for
// companion.InstanceRepository. Keyed by CompanionID (UUIDv7); secondary
// index byOwner accelerates ListByOwner + cap-count queries.
type CompanionInstanceRepo struct {
	mu      sync.RWMutex
	byID    map[string]*companion.Instance
	byOwner map[string]map[string]struct{} // key=tenant|gcid → set of CompanionID
}

// NewCompanionInstanceRepo constructs an empty repo.
func NewCompanionInstanceRepo() *CompanionInstanceRepo {
	return &CompanionInstanceRepo{
		byID:    make(map[string]*companion.Instance),
		byOwner: make(map[string]map[string]struct{}),
	}
}

func ownerKey(tenantID, gcid string) string { return tenantID + "|" + gcid }

// Create inserts a new Instance after atomically checking the owner's
// active-Instance count against `maxPerUser`. Returns
// companion.ErrRosterCapReached when the cap is exceeded.
//
// Cap check + insert happen under the same lock to avoid TOCTOU on
// concurrent Create calls for the same owner.
func (r *CompanionInstanceRepo) Create(_ context.Context, inst *companion.Instance, maxPerUser int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := ownerKey(inst.TenantID, inst.OwnerGCID)
	active := r.activeCountLocked(k)
	if maxPerUser > 0 && active >= maxPerUser {
		return companion.ErrRosterCapReached
	}

	r.byID[inst.CompanionID] = inst
	if r.byOwner[k] == nil {
		r.byOwner[k] = map[string]struct{}{}
	}
	r.byOwner[k][inst.CompanionID] = struct{}{}
	return nil
}

// Get returns the Instance by CompanionID, excluding soft-deleted (returns
// companion.ErrInstanceNotFound in that case).
func (r *CompanionInstanceRepo) Get(_ context.Context, companionID string) (*companion.Instance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst, ok := r.byID[companionID]
	if !ok || inst.IsDeleted() {
		return nil, companion.ErrInstanceNotFound
	}
	return inst, nil
}

// ListByOwner returns all non-deleted Instances for (tenantID, ownerGCID).
// Order is unspecified — callers sort by created_at when display order
// matters (the MVP HTTP handlers do so).
func (r *CompanionInstanceRepo) ListByOwner(_ context.Context, tenantID, ownerGCID string) ([]*companion.Instance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k := ownerKey(tenantID, ownerGCID)
	ids := r.byOwner[k]
	out := make([]*companion.Instance, 0, len(ids))
	for id := range ids {
		inst := r.byID[id]
		if inst == nil || inst.IsDeleted() {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// ListRosterByOwner returns RosterEntry projections for each non-deleted
// Instance owned by (tenantID, ownerGCID). Per debt #44 / A24 / B5 (2026-
// 05-16) the FE consumes this single call instead of fetching
// /v1/me/companions + per-Companion /growth (N+1 elimination).
//
// The in-memory adapter does NOT carry the ADR-149 growth-axis schema (the
// pg adapter does — additive 0032 ALTER TABLE on the same row). For tests
// + local dev wiring without a Postgres pool, this returns zero-value
// GrowthSnapshot + CosmeticSnapshot per entry. The handler renders the
// pre-hatch projection in that case (matches what FE already does in
// `wireToSummary` when ConfiguredRules JSONB lacks growth fields).
func (r *CompanionInstanceRepo) ListRosterByOwner(_ context.Context, tenantID, ownerGCID string) ([]*companion.RosterEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k := ownerKey(tenantID, ownerGCID)
	ids := r.byOwner[k]
	out := make([]*companion.RosterEntry, 0, len(ids))
	for id := range ids {
		inst := r.byID[id]
		if inst == nil || inst.IsDeleted() {
			continue
		}
		out = append(out, &companion.RosterEntry{
			Instance: inst,
			Growth: &companion.GrowthSnapshot{
				Stage:          0,
				StageName:      companion.StageName(0),
				ExpToNextStage: companion.ExpToNextStage(0),
			},
			Cosmetic: &companion.CosmeticSnapshot{},
		})
	}
	return out, nil
}

// Update upserts an existing Instance. If the CompanionID is unknown the
// adapter inserts it — production pg adapter rejects unknown IDs in M14.1
// (Postgres `WHERE companion_id = $1` clause yields 0 affected rows).
//
// MVP behaviour is forgiving so handler tests can `_ = r.Create(...)` then
// `_ = r.Update(...)` without dancing through soft-delete recovery.
func (r *CompanionInstanceRepo) Update(_ context.Context, inst *companion.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[inst.CompanionID]; !ok {
		// Index the new owner-key on first sight.
		k := ownerKey(inst.TenantID, inst.OwnerGCID)
		if r.byOwner[k] == nil {
			r.byOwner[k] = map[string]struct{}{}
		}
		r.byOwner[k][inst.CompanionID] = struct{}{}
	}
	r.byID[inst.CompanionID] = inst
	return nil
}

// SoftDelete marks the Instance deleted_at without removing it. Idempotent
// — re-deleting a soft-deleted row is a no-op (returns nil).
func (r *CompanionInstanceRepo) SoftDelete(_ context.Context, companionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.byID[companionID]
	if !ok {
		// MVP behaviour: silently nil — matches the "delete an unknown row
		// is success" Postgres convention. Production may switch to
		// companion.ErrInstanceNotFound at M14.1.
		return nil
	}
	if inst.IsDeleted() {
		return nil
	}
	now := time.Now().UTC()
	inst.DeletedAt = &now
	inst.UpdatedAt = now
	return nil
}

// activeCountLocked counts non-deleted Instances for an owner key. The
// caller MUST hold the write lock.
func (r *CompanionInstanceRepo) activeCountLocked(k string) int {
	ids := r.byOwner[k]
	count := 0
	for id := range ids {
		if inst, ok := r.byID[id]; ok && !inst.IsDeleted() {
			count++
		}
	}
	return count
}

// Compile-time guarantee that *CompanionInstanceRepo satisfies the port.
var _ companion.InstanceRepository = (*CompanionInstanceRepo)(nil)
