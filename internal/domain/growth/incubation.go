// incubation.go — F-I1 (CHO-2088, ADR-228 incubation arc): a Stage-0 egg
// must accrue EXP up to a hatch threshold before it is "stirring" and may
// hatch. This file owns the pure incubation primitives: the domain-default
// threshold and the one-shot crossing predicate.
//
// Owner decision (F-I1.2): the crossing is DERIVED in-transaction from
// prevExp/newExp — there is NO persisted "stirred" flag and NO migration.
// Because a strict threshold boundary is crossed by exactly one award
// (prevExp < threshold <= newExp), the emit is naturally one-shot.
package growth

// DefaultHatchExpThreshold is the domain-default incubation EXP a Stage-0
// egg must accrue before it is stirring and may hatch. cmd/server resolves
// COMPANION_HATCH_EXP_THRESHOLD and validates 0 < t < 50 at boot; NewService
// falls back to this value when the config is unset so the hatch gate can
// never silently disable itself (fail-closed).
const DefaultHatchExpThreshold = 25

// CrossesHatchThreshold reports whether a single EXP award moved a Stage-0
// egg ACROSS the hatch threshold — prevExp was below it and newExp reached
// or passed it. Only a pre-hatch egg (prevStage == StageEgg) can stir, and
// only ONE award can satisfy prevExp < threshold <= newExp, so the crossing
// is one-shot without a persisted flag. A non-positive threshold disables
// stirring (never crosses).
func CrossesHatchThreshold(prevStage, prevExp, newExp, threshold int) bool {
	if threshold <= 0 || prevStage != StageEgg {
		return false
	}
	return prevExp < threshold && newExp >= threshold
}
