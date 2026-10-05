// growth.go — in-memory adapter for the ADR-149 Companion Growth axis
// repository port. Used in tests + local dev. Production wiring uses pg.
//
// Per hexagonal SKILL: this adapter consumes the growth.Repository port
// declared in domain/growth/state.go.
package inmem

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// GrowthRepo is the in-memory implementation of growth.Repository.
type GrowthRepo struct {
	mu          sync.Mutex
	rows        map[string]*growth.CompanionGrowthRow // companion_id → row
	events      map[string][]*growth.GrowthEventRow   // companion_id → events
	idemHits    map[string]*growth.GrowthEventRow     // (companion_id|source|key) → event (for replay)
	dailyCounts map[string]int                        // (companion_id|source|YYYY-MM-DD) → exp_awarded today
	pidIndex    map[string]string                     // egg_purchase_id → companion_id (idempotency)
	seqEvt      int
}

// NewGrowthRepo constructs an empty repo.
func NewGrowthRepo() *GrowthRepo {
	return &GrowthRepo{
		rows:        make(map[string]*growth.CompanionGrowthRow),
		events:      make(map[string][]*growth.GrowthEventRow),
		idemHits:    make(map[string]*growth.GrowthEventRow),
		dailyCounts: make(map[string]int),
		pidIndex:    make(map[string]string),
	}
}

// SeedRow inserts a row directly. Used by tests.
func (r *GrowthRepo) SeedRow(row *growth.CompanionGrowthRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *row
	r.rows[row.CompanionID] = &clone
}

// GetGrowthRow returns the row for a companion.
func (r *GrowthRepo) GetGrowthRow(_ context.Context, tenantID, companionID string) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[companionID]
	if !ok || row.TenantID != tenantID {
		return nil, growth.ErrCompanionNotFound
	}
	clone := *row
	return &clone, nil
}

// AwardExpTx applies the EXP increment atomically. Transaction semantics
// mirror the pg adapter (CHO-2039): every write is STAGED on clones, the
// in-transaction PostAwardInTx hook runs, and only a nil hook error commits
// the staged state — a hook failure "rolls back" the whole award. The hook
// runs while this repo's mutex is held; hooks must not call back into this
// GrowthRepo (other in-memory repos are fine — separate mutexes).
func (r *GrowthRepo) AwardExpTx(ctx context.Context, in growth.AwardExpTxInput) (*growth.AwardExpTxOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID {
		return nil, growth.ErrCompanionNotFound
	}
	idemKey := in.CompanionID + "|" + in.Source + "|" + in.IdempotencyKey
	if existing, dup := r.idemHits[idemKey]; dup {
		clone := *row
		dupOut := &growth.AwardExpTxOutput{
			Row:           &clone,
			GrowthEvent:   existing,
			Duplicate:     true,
			ClampedDelta:  0,
			DailyCapHit:   false,
			PreviousStage: clone.GrowthStage,
			NewStage:      clone.GrowthStage,
		}
		// Replay path writes nothing here; the hook (repair-lane mint)
		// still runs and its error aborts, pg-parity.
		if in.PostAwardInTx != nil {
			if err := in.PostAwardInTx(ctx, dupOut); err != nil {
				return nil, err
			}
		}
		return dupOut, nil
	}

	dayBucket := in.Now.UTC().Format("2006-01-02")
	counterKey := in.CompanionID + "|" + in.Source + "|" + dayBucket
	already := r.dailyCounts[counterKey]
	clamped, capHit := growth.ClampDeltaWithCap(in.DailyCap, already, in.RequestedDelta)

	staged := *row
	prevStage := staged.GrowthStage
	out := growth.StateAfterAward(prevStage, staged.GrowthExp, clamped)
	staged.GrowthExp = out.NewExp
	staged.GrowthStage = out.NewStage
	if out.StageUp {
		now := in.Now
		staged.LastStageUpAt = &now
	}
	llmTier := growth.LLMTierForStageAndMana(staged.GrowthStage, in.ManaTier)
	staged.EffectiveLLMTierCached = llmTier

	// Eager sequence bump mirrors real DB sequences (IDs skip on rollback).
	r.seqEvt++
	ev := &growth.GrowthEventRow{
		GrowthEventID:    fmt.Sprintf("evt-%d", r.seqEvt),
		TenantID:         in.TenantID,
		CompanionID:      in.CompanionID,
		OwnerGCID:        in.OwnerGCID,
		Source:           in.Source,
		RequestedDelta:   in.RequestedDelta,
		AwardedDelta:     clamped,
		ExpTotalAfter:    staged.GrowthExp,
		DailyCapHit:      capHit,
		TriggeredStageUp: out.StageUp,
		IdempotencyKey:   in.IdempotencyKey,
		SourceEventID:    in.SourceEventID,
		SourceTopic:      in.SourceTopic,
		SourceSessionID:  in.SourceSessionID,
		SourceTurnSeq:    in.SourceTurnSeq,
		AwardedAt:        in.Now,
	}

	clone := staged
	txOut := &growth.AwardExpTxOutput{
		Row:                &clone,
		GrowthEvent:        ev,
		Duplicate:          false,
		ClampedDelta:       clamped,
		DailyCapHit:        capHit,
		TriggeredStageUp:   out.StageUp,
		PreviousStage:      prevStage,
		NewStage:           staged.GrowthStage,
		NewlyUnlockedTools: growth.NewlyUnlockedTools(prevStage, staged.GrowthStage),
		NewMemoryMode:      growth.MemoryModeForStage(staged.GrowthStage),
		EffectiveLLMTier:   llmTier,
	}
	if in.PostAwardInTx != nil {
		if err := in.PostAwardInTx(ctx, txOut); err != nil {
			return nil, err // nothing committed — the award rolls back
		}
	}

	// COMMIT the staged transaction.
	*row = staged
	r.dailyCounts[counterKey] = already + clamped
	r.events[in.CompanionID] = append(r.events[in.CompanionID], ev)
	r.idemHits[idemKey] = ev
	return txOut, nil
}

// CommitReveal persists the breed roll on a stirring Stage-0 pod (CHO-2229).
// Write-once: a second commit returns the original roll with Duplicate=true
// and never overwrites it — mirrors the pg revealed_at IS NULL guard.
func (r *GrowthRepo) CommitReveal(_ context.Context, in growth.RevealTxInput) (*growth.RevealTxOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	if row.RevealedAt != nil {
		clone := *row
		return &growth.RevealTxOutput{Row: &clone, Duplicate: true}, nil
	}
	row.Species = in.Species
	row.ShinyVariant = in.ShinyVariant
	row.SpeciesRarity = in.SpeciesRarity
	row.RolledProb = in.RolledProbability
	now := in.Now
	row.RevealedAt = &now
	clone := *row
	return &growth.RevealTxOutput{Row: &clone}, nil
}

// CommitHatch advances Stage 0 → 1. Commit-only since CHO-2229: the roll is
// already on the row (CommitReveal) and is never written here.
func (r *GrowthRepo) CommitHatch(_ context.Context, in growth.HatchTxInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	if row.RevealedAt == nil {
		return nil, growth.ErrNotRevealed
	}
	row.GrowthStage = 1
	row.ResonantAtom = in.ResonantAtomID
	row.DisplayName = in.DisplayName
	row.Tone = in.Tone
	row.LearnerPersona = in.LearnerPersona
	now := in.Now
	row.HatchedAt = &now
	row.LastStageUpAt = &now
	clone := *row
	return &clone, nil
}

// CommitBornHatched advances a stage-0 sovereign row to stage 1 (born
// hatched, CHO-2013 P1) with the optional species pick.
func (r *GrowthRepo) CommitBornHatched(_ context.Context, in growth.BornHatchedTxInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID {
		return nil, growth.ErrCompanionNotFound
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		return nil, growth.ErrAlreadyHatched
	}
	row.GrowthStage = 1
	if in.Species != "" {
		row.Species = in.Species
	}
	hatched := in.Now
	row.HatchedAt = &hatched
	clone := *row
	return &clone, nil
}

// SetResonantConcept persists (or clears, nil) the awakening
// resonant-concept pick (R3-1, CHO-2013 P1).
func (r *GrowthRepo) SetResonantConcept(_ context.Context, tenantID, companionID string, conceptID *string) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[companionID]
	if !ok || row.TenantID != tenantID {
		return nil, growth.ErrCompanionNotFound
	}
	if conceptID == nil {
		row.ResonantConceptID = ""
	} else {
		row.ResonantConceptID = *conceptID
	}
	clone := *row
	return &clone, nil
}

// MarkAhaMoment sets aha_moment_consumed=true with window/preview tier.
func (r *GrowthRepo) MarkAhaMoment(_ context.Context, in growth.AhaMomentInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[in.CompanionID]
	if !ok || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
		return nil, growth.ErrCompanionNotFound
	}
	if row.AhaMomentConsumed {
		return nil, growth.ErrAhaMomentConsumed
	}
	row.AhaMomentConsumed = true
	expires := in.WindowExpiresAt
	row.AhaMomentActiveUntil = &expires
	row.AhaMomentPreviewLLMTier = in.PreviewLLMTier
	clone := *row
	return &clone, nil
}

// CountCompanionsOfSpecies counts the caller's non-deleted Companions of the
// given species, excluding excludeCompanionID.
func (r *GrowthRepo) CountCompanionsOfSpecies(_ context.Context, tenantID, ownerGCID, species, excludeCompanionID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int
	for _, row := range r.rows {
		if row.TenantID == tenantID && row.OwnerGCID == ownerGCID && row.Species == species && row.CompanionID != excludeCompanionID {
			n++
		}
	}
	return n, nil
}

// OwnerSpeciesSet returns the DISTINCT non-empty species of the caller's
// active Companions, excluding excludeCompanionID (owner ruling 2026-08-07, the
// no-repeat inversion). Mirrors ownerSpeciesSetSQL: a blank species (an
// unrevealed pod) contributes nothing, and the map needs no ordering because a
// set has no first element. The inmem store never retains soft-deleted rows, so
// the pg `deleted_at IS NULL` predicate has no counterpart here.
func (r *GrowthRepo) OwnerSpeciesSet(_ context.Context, tenantID, ownerGCID, excludeCompanionID string) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	owned := make(map[string]bool)
	for _, row := range r.rows {
		if row.TenantID != tenantID || row.OwnerGCID != ownerGCID || row.CompanionID == excludeCompanionID {
			continue
		}
		species := strings.TrimSpace(row.Species)
		if species == "" {
			continue
		}
		owned[species] = true
	}
	return owned, nil
}

// ListGrowthEvents returns paginated ledger entries. Pagination is a no-op
// in the in-memory implementation (returns all in one page).
func (r *GrowthRepo) ListGrowthEvents(_ context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events[in.CompanionID]
	out := make([]*growth.GrowthEventRow, 0, len(events))
	for _, ev := range events {
		if ev.TenantID == in.TenantID {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AwardedAt.Before(out[j].AwardedAt)
	})
	return &growth.ListGrowthEventsOutput{Events: out, NextPageToken: ""}, nil
}

// ProvisionEgg provisions a Stage-0 row. Idempotent on egg_purchase_id.
func (r *GrowthRepo) ProvisionEgg(_ context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingID, ok := r.pidIndex[in.EggPurchaseID]; ok {
		if existing, ok := r.rows[existingID]; ok {
			clone := *existing
			return &clone, nil
		}
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	soft := in.SoftExpiryAt
	hard := in.HardExpiryAt
	row := &growth.CompanionGrowthRow{
		CompanionID:    "fam-" + in.EggPurchaseID,
		TenantID:       in.TenantID,
		OwnerGCID:      in.OwnerGCID,
		GrowthStage:    0,
		EggSku:         in.EggSku,
		EggPurchaseID:  in.EggPurchaseID,
		EggSource:      in.EggSource,
		EggPurchasedAt: &now,
		EggSoftExpiry:  &soft,
		EggHardExpiry:  &hard,
	}
	r.rows[row.CompanionID] = row
	r.pidIndex[in.EggPurchaseID] = row.CompanionID
	clone := *row
	return &clone, nil
}

// Compile-time guarantee that *GrowthRepo satisfies the port.
var _ growth.Repository = (*GrowthRepo)(nil)
