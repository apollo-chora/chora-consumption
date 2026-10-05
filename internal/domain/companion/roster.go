// roster.go — `RosterEntry` value object composing `Instance` with the
// ADR-149 growth-axis snapshot + cosmetic projection.
//
// Background — debt #44 / A24 / B5 (2026-05-16):
//
//	FE's CompanionGrowthService.listMyCompanions previously called
//	GET /v1/me/companions then per-Companion fetched GET /v1/me/companions/{id}/
//	growth — classic N+1. Per FE ping in handoff §B5 (commit 4cff383e) +
//	user direction 2026-05-16 ("arch-clean, no debts, do it the correct
//	way"), the BE list endpoint now returns the enriched RosterEntry shape
//	in a single call.
//
// Hexagonal decoupling rationale:
//
//	The growth-axis columns live on `companion_instances` (per migration
//	0032_familiar_growth.sql ALTER TABLE) but ALSO surface through the
//	`growth.CompanionGrowthRow` projection used by the GrowthRepo. To avoid
//	a domain-to-domain import (companion → growth) we re-project the
//	growth-axis columns as lightweight value objects on this package. The
//	pg adapter populates both from a single JOIN-less SELECT (all columns
//	live on companion_instances).
//
// Per hexagonal SKILL §1 the domain owns the port; adapters depend on
// these types — never the reverse.
package companion

import "time"

// GrowthSnapshot is the read-side projection of the ADR-149 7-stage
// Angelic Dragon growth axis columns on companion_instances.
//
// Mirrors the column set added by migration 0032_familiar_growth.sql.
// Pointer fields denote nullability — Stage-0 (Egg) rows carry NULL for
// `Species` / `ResonantAtomID` / `HatchedAt` until commit-hatch fires.
//
// `ExpToNextStage` is computed at projection time from `growth.STAGE_EXP_
// THRESHOLDS` — clients reading the wire just consume the number, they
// don't recompute. This means the FE never has to round-trip the threshold
// table.
type GrowthSnapshot struct {
	Stage                int        // 0-6 per ADR-149 §"The 7 stages"
	StageName            string     // canonical breed-neutral name (egg/baby/fledgling/...)
	Exp                  int        // cumulative EXP at this Companion
	ExpToNextStage       int        // threshold to reach Stage+1 (0 when at Stage 6)
	CurrentBreed         string     // species — empty when at Stage 0 (pre-hatch)
	BreedRevealedAt      *time.Time // hatched_at — NULL when at Stage 0
	EffectiveLLMTier     string     // cached LLM tier per Mana × Growth matrix
	LastStageUpAt        *time.Time // last stage-up; NULL when never stage-upped
	ResonantAtomID       string     // immutable Resonant Atom anchor; empty pre-hatch
	AhaMomentConsumed    bool       // Stage-3 Source Revelation one-shot flag
	AhaMomentActiveUntil *time.Time // active window end; NULL outside window
}

// CosmeticSnapshot captures the visual-display projection — currently
// derived from the growth-axis columns on companion_instances. Per debt
// #44 there is no separate `companion_cosmetics` table; we derive from:
//
//   - `Shiny`         ← companion_instances.shiny_variant (BOOLEAN)
//   - `Rarity`        ← companion_instances.species_rarity (TEXT)
//   - `EquippedSkinID`← reserved for the future DigitalSkin grant table
//     (NULL today; FE renders the default skin)
//
// Per CLAUDE.md §13 Domain Vocabulary: DigitalSkin is the canonical name
// for the equipped-cosmetic concept (always earned, never purchased per
// `BP-01` Learner Ownership).
type CosmeticSnapshot struct {
	EquippedSkinID string // empty = render default skin
	Shiny          bool   // shiny variant flag
	Rarity         string // common | uncommon | rare | legendary | ""
}

// RosterEntry composes an `Instance` with optional `GrowthSnapshot` +
// `CosmeticSnapshot` projections. Returned by
// InstanceRepository.ListRosterByOwner per debt #44 / A24 / B5.
//
// `Growth` and `Cosmetic` are POINTERS so a Stage-0 (Egg) row that has
// never been hatched still serialises cleanly when no growth data has
// landed (the migration 0032 was additive — pre-0032 rows have default
// 0 growth_stage but the projection still reads). Nil = caller should
// fall back to legacy `Instance.ConfiguredRules` JSONB derivation.
type RosterEntry struct {
	Instance *Instance
	Growth   *GrowthSnapshot
	Cosmetic *CosmeticSnapshot
}

// StageName returns the canonical breed-neutral stage name per
// ADR-149 §"The 7 stages". Stage values outside [0,6] return "unknown" so
// callers can still render a recognisable label.
//
// Kept on the domain side (rather than the FE-side STAGE_NAMES map) so
// server-side rendering (e.g., notifications) stays consistent with what
// the wire surfaces.
func StageName(stage int) string {
	switch stage {
	case 0:
		return "egg"
	case 1:
		return "baby"
	case 2:
		return "fledgling"
	case 3:
		return "awakened"
	case 4:
		return "structural"
	case 5:
		return "teen"
	case 6:
		return "matured"
	default:
		return "unknown"
	}
}

// StageExpThresholds is the canonical EXP threshold table per ADR-149
// §"EXP economy" — mirror of the FE `STAGE_EXP_THRESHOLDS` map in
// `chora-web/src/app/core/companion/companion-growth.model.ts`.
//
// Threshold[stage] = cumulative EXP REQUIRED to enter `stage`. Therefore:
//   - Stage 0 (Egg) → 0 EXP entry
//   - Stage 1 (Baby) → 0 EXP entry (hatch event consumes the egg but
//     doesn't gate on EXP)
//   - Stage 2 (Fledgling) → 50 EXP entry
//   - Stage 6 (Matured) → 3000 EXP entry
//
// `ExpToNextStage` (rendered on the wire) is computed as
// `StageExpThresholds[stage+1]` for stage ∈ [0,5] and 0 at Stage 6.
var StageExpThresholds = [7]int{
	0,    // Stage 0 — Egg
	0,    // Stage 1 — Baby
	50,   // Stage 2 — Fledgling
	200,  // Stage 3 — Awakened (Aha-moment trigger)
	500,  // Stage 4 — Structural
	1200, // Stage 5 — Teen
	3000, // Stage 6 — Matured
}

// ExpToNextStage returns the EXP threshold to enter Stage+1. Returns 0
// when called at Stage 6 (Matured — no next stage). Returns 0 when stage
// is outside the canonical range [0,6] so callers don't have to bounds-
// check.
func ExpToNextStage(stage int) int {
	if stage < 0 || stage >= 6 {
		return 0
	}
	return StageExpThresholds[stage+1]
}

// ExpToNextStageOrHatch returns the EXP denominator a READ should surface for a
// row at `stage`, given the configured incubation gate.
//
// D3. A Stage-0 pod's "next stage" is the HATCH, which is an event gated on a
// configured EXP threshold rather than a point on the growth curve. That is why
// StageExpThresholds[1] is 0, and why ExpToNextStage(0) correctly returns 0:
// a non-zero entry there would let stage-up arithmetic auto-hatch past the
// ceremony. But a read that renders a warming bar needs the hatch gate as its
// denominator, which is what the single-companion growth read already surfaces.
//
// The roster read used the curve alone, so the two reads disagreed: the list
// said 0 while the per-companion read said 25, and the incubation card believed
// the list. A pod with 32 EXP rendered "Warming 32 / 0 EXP" behind a bar pinned
// at 0 percent and a hatch CTA that could never free. Both reads now answer
// from this one rule.
//
// The gate is a PARAMETER, never a constant: it is configurable through
// COMPANION_HATCH_EXP_THRESHOLD, so recomputing it here would reintroduce the
// same class of disagreement one layer down. A non-positive threshold means the
// caller has no configured gate, and the curve value stands rather than a
// fabricated one.
func ExpToNextStageOrHatch(stage, hatchThreshold int) int {
	if stage == 0 && hatchThreshold > 0 {
		return hatchThreshold
	}
	return ExpToNextStage(stage)
}
