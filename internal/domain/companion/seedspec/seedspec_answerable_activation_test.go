// seedspec_answerable_activation_test.go — CHO-2016 P2 wave B (the "answerable
// pipe"): pins the answerable activation SET + its stage gating + its
// seedspec↔catalogue↔cost_map consistency. The set is the release contract for
// migration 0075; the flip itself is HELD pending the ADR-174 answerable eval
// gate (owner-driven), exactly as the P1/P2-sight waves gated their flips.
package seedspec

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestP2AnswerableActivationTwo_ShapeAndGating asserts the wave is EXACTLY
// quiz_me + socratic_drill, each an active Scholar Skill at its spec stage
// (quiz_me st3, socratic_drill st4).
func TestP2AnswerableActivationTwo_ShapeAndGating(t *testing.T) {
	byKey := CatalogueByKey()
	wantStage := map[string]int{"quiz_me": 3, "socratic_drill": 4}

	if len(P2AnswerableActivationTwo) != len(wantStage) {
		t.Fatalf("P2AnswerableActivationTwo has %d keys, want %d", len(P2AnswerableActivationTwo), len(wantStage))
	}
	seen := map[string]bool{}
	for _, key := range P2AnswerableActivationTwo {
		if seen[key] {
			t.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		e, ok := byKey[key]
		if !ok {
			t.Fatalf("P2AnswerableActivationTwo key %q absent from the catalogue", key)
		}
		if e.SkillKind != companion.SkillKindActive {
			t.Errorf("%q kind = %q, want active", key, e.SkillKind)
		}
		if e.MinGrowthStage != wantStage[key] {
			t.Errorf("%q min_growth_stage = %d, want %d", key, e.MinGrowthStage, wantStage[key])
		}
	}
}

// TestP2AnswerableActivationTwo_IsLaunchRemainderSubset ties the wave to the
// canonical launch set: it must be a SUBSET of P2ActivationFive and never
// re-activate a P1 Skill. kg_explore stays out (its suggestion-inbox write
// builder has not landed).
func TestP2AnswerableActivationTwo_IsLaunchRemainderSubset(t *testing.T) {
	inFive := map[string]bool{}
	for _, k := range P2ActivationFive {
		inFive[k] = true
	}
	for _, k := range P2AnswerableActivationTwo {
		if !inFive[k] {
			t.Errorf("%q is not in P2ActivationFive — activation waves must subset the launch set", k)
		}
	}
	// kg_explore is deliberately NOT in the answerable wave.
	for _, k := range P2AnswerableActivationTwo {
		if k == "kg_explore" {
			t.Errorf("kg_explore must NOT be in the answerable wave (its inbox-write builder is unbuilt)")
		}
	}
	// Disjoint from the sight wave (each wave releases a distinct subset).
	inSight := map[string]bool{}
	for _, k := range P2SightActivationTwo {
		inSight[k] = true
	}
	for _, k := range P2AnswerableActivationTwo {
		if inSight[k] {
			t.Errorf("%q overlaps P2SightActivationTwo (double activation)", k)
		}
	}
}

// TestP2AnswerableActivationTwo_CostMapConsistency is the seedspec↔catalogue↔
// cost_map drift guard: every answerable-wave Skill's catalogue PriceKey must
// resolve to a canonical cost (else the live invoke runner fail-loud 500s at the
// LookupCost pre-flight). Locks the spec-§7 prices too (quiz_me 0, socratic 15).
func TestP2AnswerableActivationTwo_CostMapConsistency(t *testing.T) {
	byKey := CatalogueByKey()
	wantCost := map[string]int64{"quiz_me": 0, "socratic_drill": 15}
	for _, key := range P2AnswerableActivationTwo {
		e := byKey[key]
		if e.PriceKey == "" {
			t.Fatalf("%q has an empty PriceKey — an active Skill must be metered", key)
		}
		got, err := companion.LookupCost(e.PriceKey)
		if err != nil {
			t.Fatalf("%q PriceKey %q not in the canonical cost map: %v", key, e.PriceKey, err)
		}
		if got != wantCost[key] {
			t.Errorf("%q cost = %d, want %d (spec §7)", key, got, wantCost[key])
		}
	}
}
