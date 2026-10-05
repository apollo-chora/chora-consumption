// growth.go — Postgres adapter for the ADR-149 Companion Growth axis
// repository port (`growth.Repository`).
//
// Schema mirrors migrations/0032_familiar_growth.sql (objects renamed live by 0110_companion_rename):
//
//   - companion_instances (ALTERed with growth axis columns)
//   - companion_growth_events (append-only EXP ledger, UNIQUE(companion_id,
//     source, idempotency_key) for idempotency)
//   - companion_growth_daily_counters (per-(companion, source, day) counter)
//
// Per multi-tenant-rls SKILL: every read/write runs rls.ApplySession to
// set chora.tenant_id + chora.user_gcid session GUCs inside the transaction
// (defence-in-depth complementing per-database domain isolation).
//
// Atomic-tx semantics:
//
//   - AwardExpTx — one transaction:
//     1. SELECT FOR UPDATE the companion row
//     2. UPSERT the (companion, source, day) counter (clamped per ADR-149
//     §EXP economy daily-cap table)
//     3. INSERT the growth_event with ON CONFLICT DO NOTHING + RETURNING
//     (idempotent on (companion_id, source, idempotency_key))
//     4. UPDATE companion_instances.growth_exp + growth_stage +
//     last_stage_up_at + effective_llm_tier_cached
//     5. Run the domain PostAwardInTx hook with the ambient Querier
//     (CHO-2039: the species-Path grant mint joins THIS transaction; a
//     hook error rolls back steps 1-4 with it)
//
//   - CommitHatch — one transaction:
//     1. SELECT FOR UPDATE the row, verify growth_stage=0 + hatched_at IS NULL
//     2. UPDATE the row to Stage 1 + species + shiny + resonant_atom_id
//
//   - ProvisionEgg — one transaction:
//     1. INSERT companion_instances with ON CONFLICT (egg_purchase_id)
//     DO NOTHING (idempotent on duplicate Stripe webhooks)
//     2. SELECT the row (whether newly inserted or pre-existing)
//
// Per feedback_no_inline_config: connection strings + topic names + cache
// TTLs come from env vars resolved at cmd/server boot — this package only
// accepts a TxRunner (production: pgxpool-backed PgxTxRunner; tests: stub).
package pg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ---------------------------------------------------------------------------
// GrowthRepo
// ---------------------------------------------------------------------------

// GrowthRepo is the Postgres-backed implementation of growth.Repository.
type GrowthRepo struct {
	tx TxRunner
}

// NewGrowthRepo constructs the repo around a TxRunner. Production wires a
// PgxTxRunner backed by a pgxpool.Pool; tests inject a stubTxRunner.
func NewGrowthRepo(tx TxRunner) *GrowthRepo {
	return &GrowthRepo{tx: tx}
}

// Compile-time guarantee that *GrowthRepo satisfies the port.
var _ growth.Repository = (*GrowthRepo)(nil)

// ---------------------------------------------------------------------------
// GetGrowthRow
// ---------------------------------------------------------------------------

// GetGrowthRow returns the growth-axis snapshot for a companion.
func (r *GrowthRepo) GetGrowthRow(ctx context.Context, tenantID, companionID string) (*growth.CompanionGrowthRow, error) {
	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, selectGrowthRowSQL, companionID)
		if row == nil {
			return nil
		}
		got, err := scanGrowthRow(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return growth.ErrCompanionNotFound
			}
			return err
		}
		if got.TenantID != tenantID {
			return growth.ErrCompanionNotFound
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, growth.ErrCompanionNotFound
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// AwardExpTx — atomic EXP ledger UPSERT.
// ---------------------------------------------------------------------------

// AwardExpTx applies an EXP increment atomically.
func (r *GrowthRepo) AwardExpTx(ctx context.Context, in growth.AwardExpTxInput) (*growth.AwardExpTxOutput, error) {
	if strings.TrimSpace(in.TenantID) == "" ||
		strings.TrimSpace(in.CompanionID) == "" ||
		strings.TrimSpace(in.IdempotencyKey) == "" ||
		strings.TrimSpace(in.Source) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/source/idempotency required", growth.ErrInvalidArguments)
	}
	if in.RequestedDelta < 0 {
		return nil, fmt.Errorf("%w: requested_delta must be >= 0", growth.ErrInvalidArguments)
	}
	var out *growth.AwardExpTxOutput
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}

		// 1. SELECT FOR UPDATE the companion row.
		row, err := selectGrowthRowForUpdate(ctx, q, in.CompanionID)
		if err != nil {
			return err
		}
		if row == nil {
			return growth.ErrCompanionNotFound
		}
		if row.TenantID != in.TenantID {
			return growth.ErrCompanionNotFound
		}

		// 2. Daily-cap UPSERT — compute clamped delta against today's tally.
		day := in.Now.UTC().Format("2006-01-02")
		var alreadyAwardedToday int
		counterRow := q.QueryRow(ctx, selectGrowthDailyCounterSQL,
			in.TenantID, in.CompanionID, in.Source, day)
		if counterRow != nil {
			if scanErr := counterRow.Scan(&alreadyAwardedToday); scanErr != nil &&
				!errors.Is(scanErr, ErrNoRows) {
				return fmt.Errorf("pg: select daily counter: %w", scanErr)
			}
		}
		clamped, capHit := growth.ClampDeltaWithCap(in.DailyCap, alreadyAwardedToday, in.RequestedDelta)

		// 3. INSERT growth_event with ON CONFLICT DO NOTHING for idempotency.
		// We compute the post-award running total client-side to keep the
		// SQL append-only (no SELECT-after-INSERT race).
		expTotalAfter := row.GrowthExp + clamped
		prevStage := row.GrowthStage
		awardOutcome := growth.StateAfterAward(prevStage, row.GrowthExp, clamped)
		stageUp := awardOutcome.StageUp
		newStage := awardOutcome.NewStage

		insTag, insErr := q.Exec(ctx, insertGrowthEventSQL,
			in.TenantID,
			in.CompanionID,
			in.OwnerGCID,
			in.Source,
			in.RequestedDelta,
			clamped,
			expTotalAfter,
			capHit,
			stageUp,
			false, // triggered_revelation — Stage-3 path, handled separately
			in.IdempotencyKey,
			nullStr(in.SourceEventID),
			nullStr(in.SourceTopic),
			nullStr(in.SourceSessionID),
			nullInt(in.SourceTurnSeq),
			in.Now,
		)
		if insErr != nil {
			return fmt.Errorf("pg: insert growth_event: %w", insErr)
		}

		// 4. Idempotency is decided by whether the INSERT actually wrote a
		//    row. `ON CONFLICT (companion_id, source, idempotency_key) DO
		//    NOTHING` yields RowsAffected==0 only on a genuine replay.
		//
		//    We must NOT compare awarded_at here: PostgreSQL timestamptz
		//    truncates to microseconds, so a freshly-inserted row reads back
		//    with a different instant than in.Now (nanosecond precision) and
		//    would be misclassified as a replay — silently skipping the
		//    counter UPSERT + companion_instances UPDATE below, which dropped
		//    EVERY EXP award (growth_exp never advanced even though the
		//    ledger row committed). RowsAffected is the authoritative signal.
		existing := q.QueryRow(ctx, selectExistingGrowthEventSQL,
			in.CompanionID, in.Source, in.IdempotencyKey)
		existingRow, dupErr := scanGrowthEventRow(existing)
		if dupErr != nil && !errors.Is(dupErr, ErrNoRows) {
			return fmt.Errorf("pg: select existing growth_event: %w", dupErr)
		}
		if insTag.RowsAffected == 0 {
			// Replay path — the (companion, source, idempotency_key) tuple
			// already existed; return the snapshot unchanged. The
			// in-transaction hook still runs (CHO-2039 repair lane: it
			// heal-mints Path grants for rows whose grants lag their
			// stage, atomically with this replay transaction).
			out = &growth.AwardExpTxOutput{
				Row:           row,
				GrowthEvent:   existingRow,
				Duplicate:     true,
				ClampedDelta:  0,
				DailyCapHit:   existingRow != nil && existingRow.DailyCapHit,
				PreviousStage: row.GrowthStage,
				NewStage:      row.GrowthStage,
			}
			return runPostAwardInTx(ctx, q, in.PostAwardInTx, out)
		}

		// 5. UPSERT counter (idempotent on a duplicate — uses excluded value).
		if _, err := q.Exec(ctx, upsertGrowthDailyCounterSQL,
			in.TenantID, in.CompanionID, in.Source, day, clamped, in.Now,
		); err != nil {
			return fmt.Errorf("pg: upsert daily counter: %w", err)
		}

		// 6. UPDATE companion_instances cache columns.
		manaTier := in.ManaTier
		if manaTier == "" {
			manaTier = "basic"
		}
		llmTier := growth.LLMTierForStageAndMana(newStage, manaTier)
		var stageUpAt any
		if stageUp {
			stageUpAt = in.Now
		}
		if _, err := q.Exec(ctx, updateGrowthExpAndStageSQL,
			expTotalAfter,
			newStage,
			llmTier,
			stageUpAt,
			in.CompanionID,
		); err != nil {
			return fmt.Errorf("pg: update companion growth: %w", err)
		}

		// Refresh row for return.
		row.GrowthExp = expTotalAfter
		row.GrowthStage = newStage
		row.EffectiveLLMTierCached = llmTier
		if stageUp {
			now := in.Now
			row.LastStageUpAt = &now
		}

		out = &growth.AwardExpTxOutput{
			Row:                row,
			GrowthEvent:        existingRow,
			Duplicate:          false,
			ClampedDelta:       clamped,
			DailyCapHit:        capHit,
			TriggeredStageUp:   stageUp,
			PreviousStage:      prevStage,
			NewStage:           newStage,
			NewlyUnlockedTools: growth.NewlyUnlockedTools(prevStage, newStage),
			NewMemoryMode:      growth.MemoryModeForStage(newStage),
			EffectiveLLMTier:   llmTier,
		}
		// 7. Domain in-transaction hook (CHO-2039 / CR §8 R6-3): the
		//    species-Path grant mint rides here so it commits or rolls
		//    back WITH the award — a hook error aborts every write above.
		return runPostAwardInTx(ctx, q, in.PostAwardInTx, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// runPostAwardInTx invokes the domain-supplied in-transaction hook
// (nil-safe) with an ambient-transaction ctx, so same-package repos called
// by the hook — e.g. CompanionLoadoutRepo grant mints, same
// chora_consumption DB — JOIN this transaction (tx_ambient.go +
// PgxTxRunner.RunInTx). A hook error propagates to RunInTx, which rolls the
// whole transaction back.
func runPostAwardInTx(ctx context.Context, q Querier, hook func(context.Context, *growth.AwardExpTxOutput) error, out *growth.AwardExpTxOutput) error {
	if hook == nil {
		return nil
	}
	return hook(WithAmbientQuerier(ctx, q), out)
}

// ---------------------------------------------------------------------------
// CommitHatch — Stage 0 → Stage 1 atomic transition.
// ---------------------------------------------------------------------------

// CommitHatch advances a REVEALED Stage-0 row to Stage 1 with the supplied
// identity bundle. Commit-only since CHO-2229 — the breed roll was persisted
// by CommitReveal and is deliberately NOT writable here: the species the
// learner named against is the species they get.
func (r *GrowthRepo) CommitHatch(ctx context.Context, in growth.HatchTxInput) (*growth.CompanionGrowthRow, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" ||
		strings.TrimSpace(in.OwnerGCID) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner required", growth.ErrInvalidArguments)
	}
	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row, err := selectGrowthRowForUpdate(ctx, q, in.CompanionID)
		if err != nil {
			return err
		}
		if row == nil || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
			return growth.ErrCompanionNotFound
		}
		if row.GrowthStage != 0 || row.HatchedAt != nil {
			return growth.ErrAlreadyHatched
		}
		if row.RevealedAt == nil {
			return growth.ErrNotRevealed
		}
		llmTier := growth.LLMTierForStageAndMana(1, "basic")
		if _, err := q.Exec(ctx, commitHatchSQL,
			in.ResonantAtomID,
			in.DisplayName,
			in.Tone,
			in.LearnerPersona,
			in.Now,
			llmTier,
			in.CompanionID,
		); err != nil {
			return fmt.Errorf("pg: commit hatch: %w", err)
		}
		// Refresh in-memory row (the roll fields already hold the persisted
		// reveal from the FOR UPDATE read).
		row.GrowthStage = 1
		row.ResonantAtom = in.ResonantAtomID
		row.DisplayName = in.DisplayName
		row.Tone = in.Tone
		row.LearnerPersona = in.LearnerPersona
		now := in.Now
		row.HatchedAt = &now
		row.LastStageUpAt = &now
		row.EffectiveLLMTierCached = llmTier
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// CommitReveal — persist the breed roll ahead of the hatch commit (CHO-2229).
// ---------------------------------------------------------------------------

// CommitReveal persists the rolled breed on a stirring Stage-0 pod. The
// UPDATE is guarded by revealed_at IS NULL and the row is read FOR UPDATE,
// so a concurrent second reveal serialises behind the winner, sees its
// revealed_at, and comes back Duplicate=true with the WINNER'S roll — the
// caller must adopt it and must not publish again.
func (r *GrowthRepo) CommitReveal(ctx context.Context, in growth.RevealTxInput) (*growth.RevealTxOutput, error) {
	if strings.TrimSpace(in.TenantID) == "" || strings.TrimSpace(in.CompanionID) == "" ||
		strings.TrimSpace(in.OwnerGCID) == "" || strings.TrimSpace(in.Species) == "" {
		return nil, fmt.Errorf("%w: tenant/companion/owner/species required", growth.ErrInvalidArguments)
	}
	var out *growth.RevealTxOutput
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row, err := selectGrowthRowForUpdate(ctx, q, in.CompanionID)
		if err != nil {
			return err
		}
		if row == nil || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
			return growth.ErrCompanionNotFound
		}
		if row.GrowthStage != 0 || row.HatchedAt != nil {
			return growth.ErrAlreadyHatched
		}
		if row.RevealedAt != nil {
			out = &growth.RevealTxOutput{Row: row, Duplicate: true}
			return nil
		}
		if _, err := q.Exec(ctx, commitRevealSQL,
			in.Species,
			in.ShinyVariant,
			in.SpeciesRarity,
			in.RolledProbability,
			in.Now,
			in.CompanionID,
		); err != nil {
			return fmt.Errorf("pg: commit reveal: %w", err)
		}
		row.Species = in.Species
		row.ShinyVariant = in.ShinyVariant
		row.SpeciesRarity = in.SpeciesRarity
		row.RolledProb = in.RolledProbability
		now := in.Now
		row.RevealedAt = &now
		out = &growth.RevealTxOutput{Row: row}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// AddKgNeighbor
// ---------------------------------------------------------------------------

// AddKgNeighbor appends an atom_id to visible_kg_neighbors. Idempotent on
// (companion_id, atom_id) — second append returns growth.ErrNeighborAlreadyVisible.

// ---------------------------------------------------------------------------
// CommitBornHatched
// ---------------------------------------------------------------------------

// CommitBornHatched advances a freshly-minted stage-0 sovereign row to stage
// 1 (born hatched, CHO-2013 P1): stage 1, 2 slots (ADR-218 D2 stage+1),
// hatched_at stamped, species overridden only when the learner picked one.
func (r *GrowthRepo) CommitBornHatched(ctx context.Context, in growth.BornHatchedTxInput) (*growth.CompanionGrowthRow, error) {
	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row, err := selectGrowthRowForUpdate(ctx, q, in.CompanionID)
		if err != nil {
			return err
		}
		if row == nil || row.TenantID != in.TenantID {
			return growth.ErrCompanionNotFound
		}
		if row.GrowthStage != 0 || row.HatchedAt != nil {
			return growth.ErrAlreadyHatched
		}
		tag, err := q.Exec(ctx, commitBornHatchedSQL, in.Species, in.Now, in.CompanionID)
		if err != nil {
			return fmt.Errorf("pg: commit born-hatched: %w", err)
		}
		if tag.RowsAffected == 0 {
			return growth.ErrAlreadyHatched
		}
		row.GrowthStage = 1
		if in.Species != "" {
			row.Species = in.Species
		}
		hatched := in.Now
		row.HatchedAt = &hatched
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SetResonantConcept
// ---------------------------------------------------------------------------

// SetResonantConcept persists (or clears, nil) the awakening
// resonant-concept pick (R3-1, CHO-2013 P1). Same tx + RLS discipline as
// AddKgNeighbor: session GUCs land before the row read, tenant re-checked
// inside the transaction.
func (r *GrowthRepo) SetResonantConcept(ctx context.Context, tenantID, companionID string, conceptID *string) (*growth.CompanionGrowthRow, error) {
	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row, err := selectGrowthRowForUpdate(ctx, q, companionID)
		if err != nil {
			return err
		}
		if row == nil || row.TenantID != tenantID {
			return growth.ErrCompanionNotFound
		}
		if _, err := q.Exec(ctx, setResonantConceptSQL, conceptID, companionID); err != nil {
			return fmt.Errorf("pg: set resonant concept: %w", err)
		}
		if conceptID == nil {
			row.ResonantConceptID = ""
		} else {
			row.ResonantConceptID = *conceptID
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// MarkAhaMoment
// ---------------------------------------------------------------------------

// MarkAhaMoment sets aha_moment_consumed=true + aha_moment_active_until +
// preview tier. Single-shot.
func (r *GrowthRepo) MarkAhaMoment(ctx context.Context, in growth.AhaMomentInput) (*growth.CompanionGrowthRow, error) {
	if strings.TrimSpace(in.PreviewLLMTier) == "" {
		return nil, fmt.Errorf("%w: preview_llm_tier required", growth.ErrInvalidArguments)
	}
	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row, err := selectGrowthRowForUpdate(ctx, q, in.CompanionID)
		if err != nil {
			return err
		}
		if row == nil || row.TenantID != in.TenantID || row.OwnerGCID != in.OwnerGCID {
			return growth.ErrCompanionNotFound
		}
		if row.AhaMomentConsumed {
			return growth.ErrAhaMomentConsumed
		}
		if _, err := q.Exec(ctx, markAhaMomentSQL,
			in.WindowExpiresAt,
			in.PreviewLLMTier,
			in.CompanionID,
		); err != nil {
			return fmt.Errorf("pg: mark aha moment: %w", err)
		}
		row.AhaMomentConsumed = true
		exp := in.WindowExpiresAt
		row.AhaMomentActiveUntil = &exp
		row.AhaMomentPreviewLLMTier = in.PreviewLLMTier
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// CountCompanionsOfSpecies
// ---------------------------------------------------------------------------

// CountCompanionsOfSpecies counts the caller's non-deleted Companions of the
// given species (excluding the supplied companionID).
func (r *GrowthRepo) CountCompanionsOfSpecies(ctx context.Context, tenantID, ownerGCID, species, excludeCompanionID string) (int, error) {
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, countCompanionsOfSpeciesSQL,
			tenantID, ownerGCID, species, excludeCompanionID)
		if row == nil {
			return nil
		}
		if scanErr := row.Scan(&n); scanErr != nil && !errors.Is(scanErr, ErrNoRows) {
			return fmt.Errorf("pg: count companions: %w", scanErr)
		}
		return nil
	})
	return n, err
}

// OwnerSpeciesSet returns the DISTINCT non-empty species of the caller's
// ACTIVE (deleted_at IS NULL) Companions, excluding excludeCompanionID. An empty
// map means every species is still unseen. Owner ruling 2026-08-07: this is the
// set ExcludeOwnedSpecies subtracts from the SKU distribution so a ceremony
// never repeats a companion type while an unseen one remains. RLS-scoped
// (SET LOCAL tenant inside the tx).
func (r *GrowthRepo) OwnerSpeciesSet(ctx context.Context, tenantID, ownerGCID, excludeCompanionID string) (map[string]bool, error) {
	owned := make(map[string]bool)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, ownerSpeciesSetSQL, tenantID, ownerGCID, excludeCompanionID)
		if err != nil {
			return fmt.Errorf("pg: owner species set: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var species string
			if scanErr := rows.Scan(&species); scanErr != nil {
				return fmt.Errorf("pg: scan owner species: %w", scanErr)
			}
			if species != "" {
				owned[species] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return owned, nil
}

// ---------------------------------------------------------------------------
// LastAwardAt
// ---------------------------------------------------------------------------

// LastAwardAt returns the awarded_at of the most recent growth-ledger award
// for (tenant, companion, source), or the zero time when none exists. The
// WS-C5 campaign XP subscriber consults it for the goal_sealed ~1/week
// spacing guard (ADR-227 D10 — exp_source_def carries daily caps only).
// RLS-scoped (SET LOCAL tenant). Satisfies subscribers.GrowthAwardHistory.
func (r *GrowthRepo) LastAwardAt(ctx context.Context, tenantID, companionID, source string) (time.Time, error) {
	var last *time.Time
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, selectLastGrowthAwardAtSQL, tenantID, companionID, source)
		if row == nil {
			return nil
		}
		if scanErr := row.Scan(&last); scanErr != nil && !errors.Is(scanErr, ErrNoRows) {
			return fmt.Errorf("pg: last growth award: %w", scanErr)
		}
		return nil
	})
	if err != nil || last == nil {
		return time.Time{}, err
	}
	return *last, nil
}

// ---------------------------------------------------------------------------
// ListGrowthEvents
// ---------------------------------------------------------------------------

// ListGrowthEvents returns paginated ledger rows. pageToken format:
// `<unix_nano>:<growth_event_id>` for stable cursor pagination.
//
// E2E-BE-FAM-GROWTH §2 fix (2026-05-16): when the cursor is empty
// (first page), the cursor params are sent as typed nil — NOT as zero
// time.Time + empty string. The query casts `$4::uuid` eagerly, and
// PostgreSQL rejects the empty string with SQLSTATE 22P02 even though
// the `$3::timestamptz IS NULL` branch would short-circuit at row eval.
// pgx serializes Go nil as SQL NULL, which the `::uuid` cast accepts.
func (r *GrowthRepo) ListGrowthEvents(ctx context.Context, in growth.ListGrowthEventsInput) (*growth.ListGrowthEventsOutput, error) {
	if in.PageSize <= 0 {
		in.PageSize = 50
	}
	if in.PageSize > 200 {
		in.PageSize = 200
	}
	cursorTs, cursorID := decodePageToken(in.PageToken)
	// Typed-nil bind so pgx sends SQL NULL when the cursor is empty.
	var cursorTsArg any = cursorTs
	var cursorIDArg any = cursorID
	if cursorTs.IsZero() {
		cursorTsArg = nil
	}
	if cursorID == "" {
		cursorIDArg = nil
	}

	var events []*growth.GrowthEventRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listGrowthEventsSQL,
			in.TenantID, in.CompanionID, cursorTsArg, cursorIDArg, in.PageSize+1,
		)
		if err != nil {
			return fmt.Errorf("pg: list growth events: %w", err)
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			ev, scanErr := scanGrowthEventRows(rows)
			if scanErr != nil {
				return fmt.Errorf("pg: scan growth event: %w", scanErr)
			}
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := &growth.ListGrowthEventsOutput{Events: events}
	if len(events) > in.PageSize {
		next := events[in.PageSize-1]
		out.Events = events[:in.PageSize]
		out.NextPageToken = encodePageToken(next.AwardedAt, next.GrowthEventID)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// ProvisionEgg — Stage 0 INSERT, idempotent on egg_purchase_id.
// ---------------------------------------------------------------------------

// ProvisionEgg inserts a Stage-0 companion_instances row in response to
// chora.tenancy.companion_egg.payment_succeeded.v1.
func (r *GrowthRepo) ProvisionEgg(ctx context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error) {
	if strings.TrimSpace(in.TenantID) == "" ||
		strings.TrimSpace(in.OwnerGCID) == "" ||
		strings.TrimSpace(in.EggSku) == "" ||
		strings.TrimSpace(in.EggPurchaseID) == "" {
		return nil, fmt.Errorf("%w: tenant/owner/sku/purchase_id required", growth.ErrInvalidArguments)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	soft := in.SoftExpiryAt
	hard := in.HardExpiryAt
	source := in.EggSource
	if source == "" {
		source = "purchase"
	}
	// Default the legacy "name" + "specialization" columns (from 0006
	// migration) so the row satisfies NOT NULL constraints. UI computes
	// the breed-aware nickname at display time.
	defaultName := "Egg"
	defaultSpec := "general"

	var out *growth.CompanionGrowthRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// 1. Idempotency check on egg_purchase_id.
		lookup := q.QueryRow(ctx, provisionEggLookupByPurchaseSQL, in.EggPurchaseID)
		if lookup != nil {
			existing, lerr := scanGrowthRow(lookup)
			if lerr == nil && existing != nil {
				out = existing
				return nil
			}
			if lerr != nil && !errors.Is(lerr, ErrNoRows) {
				return fmt.Errorf("pg: lookup egg purchase: %w", lerr)
			}
		}

		// 2. INSERT. ON CONFLICT (egg_purchase_id) DO NOTHING guards against
		//    races where two pods process the same Stripe webhook.
		_, err := q.Exec(ctx, provisionEggInsertSQL,
			in.TenantID,
			in.OwnerGCID,
			defaultName,
			defaultSpec,
			in.EggSku,
			in.EggPurchaseID,
			source,
			now,
			soft,
			hard,
		)
		if err != nil {
			return fmt.Errorf("pg: provision egg: %w", err)
		}

		// 3. SELECT the row (newly inserted OR race-winner).
		row := q.QueryRow(ctx, provisionEggLookupByPurchaseSQL, in.EggPurchaseID)
		if row == nil {
			return fmt.Errorf("pg: provision egg: missing row after insert")
		}
		got, serr := scanGrowthRow(row)
		if serr != nil {
			return fmt.Errorf("pg: provision egg select: %w", serr)
		}
		out = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("pg: provision egg: nil row")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// selectGrowthRowForUpdate runs the SELECT FOR UPDATE inside an open TX.
// Returns nil + nil when no row matches (caller maps to ErrCompanionNotFound).
func selectGrowthRowForUpdate(ctx context.Context, q Querier, companionID string) (*growth.CompanionGrowthRow, error) {
	row := q.QueryRow(ctx, selectGrowthRowForUpdateSQL, companionID)
	if row == nil {
		return nil, nil
	}
	got, err := scanGrowthRow(row)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return got, nil
}

// scanGrowthRow scans a single companion_instances row in the
// selectGrowthRow*SQL column order.
func scanGrowthRow(row Row) (*growth.CompanionGrowthRow, error) {
	var (
		out              growth.CompanionGrowthRow
		species          *string
		shinyVariant     *bool
		speciesRarity    *string
		rolledProb       *float64
		resonantAtom     *string
		resonantConcept  *string
		revealedAt       *time.Time
		hatchedAt        *time.Time
		ahaActiveUntil   *time.Time
		ahaPreviewTier   *string
		eggSku           *string
		eggPurchaseID    *string
		eggPurchasedAt   *time.Time
		eggSoftExpiry    *time.Time
		eggHardExpiry    *time.Time
		eggSource        *string
		effLLMTierCached *string
		lastStageUpAt    *time.Time
		displayName      *string
		tone             *string
		learnerPersona   *string
		specialization   *string
	)
	if err := row.Scan(
		&out.CompanionID,
		&out.TenantID,
		&out.OwnerGCID,
		&out.GrowthStage,
		&species,
		&shinyVariant,
		&speciesRarity,
		&rolledProb,
		&out.GrowthExp,
		&resonantAtom,
		&resonantConcept,
		&revealedAt,
		&hatchedAt,
		&out.AhaMomentConsumed,
		&ahaActiveUntil,
		&ahaPreviewTier,
		&eggSku,
		&eggPurchaseID,
		&eggPurchasedAt,
		&eggSoftExpiry,
		&eggHardExpiry,
		&eggSource,
		&effLLMTierCached,
		&lastStageUpAt,
		&displayName,
		&tone,
		&learnerPersona,
		&specialization,
	); err != nil {
		return nil, err
	}
	if species != nil {
		out.Species = *species
	}
	if shinyVariant != nil {
		out.ShinyVariant = *shinyVariant
	}
	if speciesRarity != nil {
		out.SpeciesRarity = *speciesRarity
	}
	if rolledProb != nil {
		out.RolledProb = *rolledProb
	}
	if resonantAtom != nil {
		out.ResonantAtom = *resonantAtom
	}
	if resonantConcept != nil {
		out.ResonantConceptID = *resonantConcept
	}
	out.RevealedAt = revealedAt
	out.HatchedAt = hatchedAt
	out.AhaMomentActiveUntil = ahaActiveUntil
	if ahaPreviewTier != nil {
		out.AhaMomentPreviewLLMTier = *ahaPreviewTier
	}
	if eggSku != nil {
		out.EggSku = *eggSku
	}
	if eggPurchaseID != nil {
		out.EggPurchaseID = *eggPurchaseID
	}
	out.EggPurchasedAt = eggPurchasedAt
	out.EggSoftExpiry = eggSoftExpiry
	out.EggHardExpiry = eggHardExpiry
	if eggSource != nil {
		out.EggSource = *eggSource
	}
	if effLLMTierCached != nil {
		out.EffectiveLLMTierCached = *effLLMTierCached
	}
	out.LastStageUpAt = lastStageUpAt
	if displayName != nil {
		out.DisplayName = *displayName
	}
	if tone != nil {
		out.Tone = *tone
	}
	if learnerPersona != nil {
		out.LearnerPersona = *learnerPersona
	}
	if specialization != nil {
		out.Specialization = *specialization
	}
	return &out, nil
}

// scanGrowthEventRow scans an individual companion_growth_events row from a
// QueryRow path.
func scanGrowthEventRow(row Row) (*growth.GrowthEventRow, error) {
	if row == nil {
		return nil, nil
	}
	var (
		ev              growth.GrowthEventRow
		sourceEventID   *string
		sourceTopic     *string
		sourceSessionID *string
		sourceTurnSeq   *int
	)
	if err := row.Scan(
		&ev.GrowthEventID,
		&ev.TenantID,
		&ev.CompanionID,
		&ev.OwnerGCID,
		&ev.Source,
		&ev.RequestedDelta,
		&ev.AwardedDelta,
		&ev.ExpTotalAfter,
		&ev.DailyCapHit,
		&ev.TriggeredStageUp,
		&ev.TriggeredRevelation,
		&ev.IdempotencyKey,
		&sourceEventID,
		&sourceTopic,
		&sourceSessionID,
		&sourceTurnSeq,
		&ev.AwardedAt,
	); err != nil {
		return nil, err
	}
	if sourceEventID != nil {
		ev.SourceEventID = *sourceEventID
	}
	if sourceTopic != nil {
		ev.SourceTopic = *sourceTopic
	}
	if sourceSessionID != nil {
		ev.SourceSessionID = *sourceSessionID
	}
	if sourceTurnSeq != nil {
		ev.SourceTurnSeq = *sourceTurnSeq
	}
	return &ev, nil
}

// scanGrowthEventRows scans one row out of a multi-row Rows iterator.
func scanGrowthEventRows(rows Rows) (*growth.GrowthEventRow, error) {
	var (
		ev              growth.GrowthEventRow
		sourceEventID   *string
		sourceTopic     *string
		sourceSessionID *string
		sourceTurnSeq   *int
	)
	if err := rows.Scan(
		&ev.GrowthEventID,
		&ev.TenantID,
		&ev.CompanionID,
		&ev.OwnerGCID,
		&ev.Source,
		&ev.RequestedDelta,
		&ev.AwardedDelta,
		&ev.ExpTotalAfter,
		&ev.DailyCapHit,
		&ev.TriggeredStageUp,
		&ev.TriggeredRevelation,
		&ev.IdempotencyKey,
		&sourceEventID,
		&sourceTopic,
		&sourceSessionID,
		&sourceTurnSeq,
		&ev.AwardedAt,
	); err != nil {
		return nil, err
	}
	if sourceEventID != nil {
		ev.SourceEventID = *sourceEventID
	}
	if sourceTopic != nil {
		ev.SourceTopic = *sourceTopic
	}
	if sourceSessionID != nil {
		ev.SourceSessionID = *sourceSessionID
	}
	if sourceTurnSeq != nil {
		ev.SourceTurnSeq = *sourceTurnSeq
	}
	return &ev, nil
}

// nullStr returns interface{} of nil for empty strings (so pgx encodes
// NULL) or the string itself otherwise.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt returns interface{} of nil for zero ints (so pgx encodes NULL).
func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

// encodePageToken formats a (timestamp, event_id) pair as a stable token.
func encodePageToken(ts time.Time, eventID string) string {
	return strconv.FormatInt(ts.UnixNano(), 10) + ":" + eventID
}

// decodePageToken parses a token emitted by encodePageToken. Returns zero
// values when the token is empty or malformed (treated as "start from
// beginning").
func decodePageToken(token string) (time.Time, string) {
	if token == "" {
		return time.Time{}, ""
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return time.Time{}, ""
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, ""
	}
	return time.Unix(0, nanos).UTC(), parts[1]
}

// ---------------------------------------------------------------------------
// SQL templates — reviewed before pgx codegen / live integration runs.
// ---------------------------------------------------------------------------

const selectGrowthRowSQL = `
	SELECT companion_id, tenant_id, owner_gcid,
	       growth_stage, species, shiny_variant, species_rarity,
	       rolled_probability, growth_exp,
	       resonant_atom_id, resonant_concept_id, revealed_at, hatched_at,
	       aha_moment_consumed, aha_moment_active_until,
	       aha_moment_preview_llm_tier,
	       egg_sku, egg_purchase_id, egg_purchased_at,
	       egg_soft_expiry_at, egg_hard_expiry_at, egg_source,
	       effective_llm_tier_cached, last_stage_up_at,
	       name, NULL::text AS tone, NULL::text AS learner_persona,
	       specialization
	  FROM companion_instances
	 WHERE companion_id = $1
	   AND deleted_at IS NULL`

const selectGrowthRowForUpdateSQL = `
	SELECT companion_id, tenant_id, owner_gcid,
	       growth_stage, species, shiny_variant, species_rarity,
	       rolled_probability, growth_exp,
	       resonant_atom_id, resonant_concept_id, revealed_at, hatched_at,
	       aha_moment_consumed, aha_moment_active_until,
	       aha_moment_preview_llm_tier,
	       egg_sku, egg_purchase_id, egg_purchased_at,
	       egg_soft_expiry_at, egg_hard_expiry_at, egg_source,
	       effective_llm_tier_cached, last_stage_up_at,
	       name, NULL::text AS tone, NULL::text AS learner_persona,
	       specialization
	  FROM companion_instances
	 WHERE companion_id = $1
	   AND deleted_at IS NULL
	 FOR UPDATE`

const selectGrowthDailyCounterSQL = `
	SELECT exp_awarded
	  FROM companion_growth_daily_counters
	 WHERE tenant_id   = $1
	   AND companion_id = $2
	   AND source      = $3
	   AND day_bucket  = $4::date`

const upsertGrowthDailyCounterSQL = `
	INSERT INTO companion_growth_daily_counters
	    (tenant_id, companion_id, source, day_bucket,
	     exp_awarded, event_count, last_awarded_at)
	VALUES ($1, $2, $3, $4::date, $5, 1, $6)
	ON CONFLICT (tenant_id, companion_id, source, day_bucket) DO UPDATE
	SET exp_awarded     = companion_growth_daily_counters.exp_awarded + EXCLUDED.exp_awarded,
	    event_count     = companion_growth_daily_counters.event_count + 1,
	    last_awarded_at = EXCLUDED.last_awarded_at`

const insertGrowthEventSQL = `
	INSERT INTO companion_growth_events
	    (tenant_id, companion_id, owner_gcid, source,
	     requested_delta, awarded_delta, exp_total_after,
	     daily_cap_hit, triggered_stage_up, triggered_revelation,
	     idempotency_key, source_event_id, source_topic,
	     source_session_id, source_turn_seq, awarded_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	ON CONFLICT (companion_id, source, idempotency_key) DO NOTHING`

const selectExistingGrowthEventSQL = `
	SELECT growth_event_id, tenant_id, companion_id, owner_gcid, source,
	       requested_delta, awarded_delta, exp_total_after,
	       daily_cap_hit, triggered_stage_up, triggered_revelation,
	       idempotency_key, source_event_id, source_topic,
	       source_session_id, source_turn_seq, awarded_at
	  FROM companion_growth_events
	 WHERE companion_id     = $1
	   AND source          = $2
	   AND idempotency_key = $3
	 LIMIT 1`

// WS-C5 (CHO-2084) — the seal-spacing guard read: max() over zero rows
// yields one NULL row, scanned as a nil *time.Time (= "never awarded").
const selectLastGrowthAwardAtSQL = `
	SELECT max(awarded_at)
	  FROM companion_growth_events
	 WHERE tenant_id   = $1
	   AND companion_id = $2
	   AND source      = $3`

// CHO-2012 (ADR-218 D2/D8): both stage-writing statements keep the legacy
// skill_slots_unlocked + evolution_tier columns in sync with the stage
// (slots = stage+1 clamped to 7; tier = the L6 derived band) so roster
// projections stay truthful without re-deriving in every reader.
// $2 is referenced in three contexts (smallint assignment, LEAST arithmetic,
// CASE comparison) — every reference casts to int so server-side parse
// analysis deduces ONE type; a bare mix deduces smallint vs integer and
// PREPARE dies with 42P08 (prepare_smoke_integration_test.go pins this).
const updateGrowthExpAndStageSQL = `
	UPDATE companion_instances
	   SET growth_exp                 = $1,
	       growth_stage               = ($2::int)::smallint,
	       skill_slots_unlocked       = LEAST($2::int + 1, 7),
	       evolution_tier             = CASE
	                                      WHEN $2::int >= 6 THEN 'sage'
	                                      WHEN $2::int >= 4 THEN 'master'
	                                      WHEN $2::int >= 2 THEN 'adept'
	                                      ELSE 'apprentice'
	                                    END,
	       effective_llm_tier_cached  = $3,
	       last_stage_up_at           = COALESCE($4, last_stage_up_at),
	       updated_at                 = now()
	 WHERE companion_id = $5
	   AND deleted_at IS NULL`

// commitHatchSQL is COMMIT-ONLY since CHO-2229: the roll columns (species /
// shiny_variant / species_rarity / rolled_probability) were persisted by
// commitRevealSQL and are deliberately absent — a hatch can never overwrite
// the roll the learner named against. The revealed_at guard mirrors the
// domain gate under the row lock.
const commitHatchSQL = `
	UPDATE companion_instances
	   SET growth_stage              = 1,
	       skill_slots_unlocked      = 2,
	       resonant_atom_id          = NULLIF($1, '')::uuid,
	       name                      = $2,
	       configured_rules          = configured_rules
	                                    || jsonb_build_object('tone', $3::text,
	                                                          'learner_persona', $4::text),
	       hatched_at                = $5,
	       last_stage_up_at          = $5,
	       effective_llm_tier_cached = $6,
	       updated_at                = now()
	 WHERE companion_id = $7
	   AND growth_stage = 0
	   AND hatched_at IS NULL
	   AND revealed_at IS NOT NULL
	   AND deleted_at IS NULL`

// commitRevealSQL persists the CHO-2229 breed roll. The revealed_at IS NULL
// guard makes the roll write-once at the SQL layer too: a racing second
// reveal matches 0 rows and the caller returns the winner's persisted roll.
const commitRevealSQL = `
	UPDATE companion_instances
	   SET species            = $1,
	       shiny_variant      = $2,
	       species_rarity     = $3,
	       rolled_probability = $4,
	       revealed_at        = $5,
	       updated_at         = now()
	 WHERE companion_id = $6
	   AND growth_stage = 0
	   AND hatched_at IS NULL
	   AND revealed_at IS NULL
	   AND deleted_at IS NULL`

const commitBornHatchedSQL = `
	UPDATE companion_instances
	   SET growth_stage         = 1,
	       skill_slots_unlocked = 2,
	       species              = COALESCE(NULLIF($1, ''), species),
	       hatched_at           = $2,
	       updated_at           = now()
	 WHERE companion_id = $3
	   AND growth_stage = 0
	   AND hatched_at IS NULL
	   AND deleted_at IS NULL`

const setResonantConceptSQL = `
	UPDATE companion_instances
	   SET resonant_concept_id = $1,
	       updated_at          = now()
	 WHERE companion_id = $2
	   AND deleted_at IS NULL`

const markAhaMomentSQL = `
	UPDATE companion_instances
	   SET aha_moment_consumed         = TRUE,
	       aha_moment_active_until     = $1,
	       aha_moment_preview_llm_tier = $2,
	       updated_at                  = now()
	 WHERE companion_id = $3
	   AND deleted_at IS NULL`

const countCompanionsOfSpeciesSQL = `
	SELECT count(*)
	  FROM companion_instances
	 WHERE tenant_id  = $1
	   AND owner_gcid = $2
	   AND species    = $3
	   AND companion_id != $4
	   AND deleted_at IS NULL`

// ownerSpeciesSetSQL selects the DISTINCT species the caller already owns
// (owner ruling 2026-08-07, the no-repeat inversion). NULLIF keeps an
// unrevealed pod's blank species out of the set: a pod is a mystery artifact
// (CHO-2227) and must not narrow the odds of a ceremony it has not walked.
// No ORDER BY: a SET has no first element, which is the point of the inversion.
const ownerSpeciesSetSQL = `
	SELECT DISTINCT species
	  FROM companion_instances
	 WHERE tenant_id   = $1
	   AND owner_gcid  = $2
	   AND companion_id != $3
	   AND deleted_at IS NULL
	   AND NULLIF(species, '') IS NOT NULL`

const listGrowthEventsSQL = `
	SELECT growth_event_id, tenant_id, companion_id, owner_gcid, source,
	       requested_delta, awarded_delta, exp_total_after,
	       daily_cap_hit, triggered_stage_up, triggered_revelation,
	       idempotency_key, source_event_id, source_topic,
	       source_session_id, source_turn_seq, awarded_at
	  FROM companion_growth_events
	 WHERE tenant_id   = $1
	   AND companion_id = $2
	   AND ($3::timestamptz IS NULL
	        OR awarded_at < $3::timestamptz
	        OR (awarded_at = $3::timestamptz AND growth_event_id > $4::uuid))
	 ORDER BY awarded_at DESC, growth_event_id ASC
	 LIMIT $5`

// provisionEggInsertSQL is the Stage-0 Companion INSERT. The idempotency guard
// is uq_companion_instances_egg_purchase_id (migration 0033), a PARTIAL unique
// index `... (egg_purchase_id) WHERE egg_purchase_id IS NOT NULL` (NULL rows —
// legacy/trial Companions — are exempt). Postgres only selects a partial index
// as an ON CONFLICT arbiter when the conflict target repeats that predicate, so
// the `WHERE egg_purchase_id IS NOT NULL` below is LOAD-BEARING: without it the
// INSERT fails with SQLSTATE 42P10 ("no unique or exclusion constraint matching
// the ON CONFLICT specification") and no purchased egg ever provisions.
//
// CHO-2227: `species` is named EXPLICITLY as NULL and must stay that way. A pod
// is a mystery artifact — 0032:36-38 "lootbox-rolled at HatchEgg, NOT at
// purchase. NULL while in Stage 0 (Egg)". Omitting the column does NOT leave it
// empty: 0046 gave it a random-hero column DEFAULT, so an omitted column mints
// a hidden species nobody rolled and nobody sees. That decoy is then read by
// ownerSpeciesSetSQL, and under the 2026-08-07 no-repeat ruling it EXCLUDES an
// unseen breed from the learner's own odds (and 409s their explicit pick of it)
// on the strength of a pod they have never opened.
//
// insertCompanionInstanceSQL (companion_instance.go) names the column too, so no
// INSERT in service code relies on the DEFAULT any more. Migration 0107 drops
// it; until that is applied, any INSERT that forgets the column silently
// re-opens this bug class.
const provisionEggInsertSQL = `
	INSERT INTO companion_instances
	    (tenant_id, owner_gcid, name, specialization,
	     growth_stage, growth_exp, species,
	     egg_sku, egg_purchase_id, egg_source,
	     egg_purchased_at, egg_soft_expiry_at, egg_hard_expiry_at)
	VALUES ($1, $2, $3, $4, 0, 0, NULL, $5, $6, $7, $8, $9, $10)
	ON CONFLICT (egg_purchase_id) WHERE egg_purchase_id IS NOT NULL DO NOTHING`

const provisionEggLookupByPurchaseSQL = `
	SELECT companion_id, tenant_id, owner_gcid,
	       growth_stage, species, shiny_variant, species_rarity,
	       rolled_probability, growth_exp,
	       resonant_atom_id, resonant_concept_id, revealed_at, hatched_at,
	       aha_moment_consumed, aha_moment_active_until,
	       aha_moment_preview_llm_tier,
	       egg_sku, egg_purchase_id, egg_purchased_at,
	       egg_soft_expiry_at, egg_hard_expiry_at, egg_source,
	       effective_llm_tier_cached, last_stage_up_at,
	       name, NULL::text AS tone, NULL::text AS learner_persona,
	       specialization
	  FROM companion_instances
	 WHERE egg_purchase_id = $1
	   AND deleted_at IS NULL
	 LIMIT 1`
