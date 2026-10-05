// seedspec_scout_activation_test.go — kg_explore P2 wave C (the "suggestion-inbox
// scout"): pins the scout activation SET + its stage gating + its
// seedspec↔catalogue↔cost_map consistency. The set is the release contract for
// migration 0085; the flip itself is HELD pending the ADR-174 §8 scout eval gate
// (owner-driven), exactly as the P1 / P2-sight / P2-answerable waves gated their
// flips.
package seedspec

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestP2ScoutActivationOne_ShapeAndGating asserts the wave is EXACTLY kg_explore,
// an active Weaver Skill at its spec stage (st4), with its suggestion-inbox sink +
// generative policy.
func TestP2ScoutActivationOne_ShapeAndGating(t *testing.T) {
	if len(P2ScoutActivationOne) != 1 || P2ScoutActivationOne[0] != "kg_explore" {
		t.Fatalf("P2ScoutActivationOne = %v, want exactly [kg_explore]", P2ScoutActivationOne)
	}
	e, ok := CatalogueByKey()["kg_explore"]
	if !ok {
		t.Fatalf("kg_explore absent from the catalogue")
	}
	if e.SkillKind != companion.SkillKindActive {
		t.Errorf("kg_explore kind = %q, want active", e.SkillKind)
	}
	if e.MinGrowthStage != 4 {
		t.Errorf("kg_explore min_growth_stage = %d, want 4 (spec §2.3)", e.MinGrowthStage)
	}
	if e.Family != companion.FamilyWeaver {
		t.Errorf("kg_explore family = %q, want weaver", e.Family)
	}
	if e.OutputSink != companion.SinkSuggestionInbox {
		t.Errorf("kg_explore output_sink = %q, want suggestion_inbox", e.OutputSink)
	}
	if e.PolicyClass != companion.PolicyGenerative {
		t.Errorf("kg_explore policy_class = %q, want generative", e.PolicyClass)
	}
}

// TestP2ScoutActivationOne_IsLaunchRemainderSubset ties the wave to the canonical
// launch set: it must be a SUBSET of P2ActivationFive, never re-activate a P1
// Skill, and be DISJOINT from the sight + answerable waves (each wave releases a
// distinct subset). kg_explore is the LAST of the P2 remainder — with it, the three
// P2 waves cover all of P2ActivationFive.
func TestP2ScoutActivationOne_IsLaunchRemainderSubset(t *testing.T) {
	inFive := map[string]bool{}
	for _, k := range P2ActivationFive {
		inFive[k] = true
	}
	for _, k := range P2ScoutActivationOne {
		if !inFive[k] {
			t.Errorf("%q is not in P2ActivationFive — activation waves must subset the launch set", k)
		}
	}
	inSight := map[string]bool{}
	for _, k := range P2SightActivationTwo {
		inSight[k] = true
	}
	inAnswerable := map[string]bool{}
	for _, k := range P2AnswerableActivationTwo {
		inAnswerable[k] = true
	}
	for _, k := range P2ScoutActivationOne {
		if inSight[k] {
			t.Errorf("%q overlaps P2SightActivationTwo (double activation)", k)
		}
		if inAnswerable[k] {
			t.Errorf("%q overlaps P2AnswerableActivationTwo (double activation)", k)
		}
	}
	// The three P2 waves together must cover EXACTLY P2ActivationFive (no launch
	// remainder left dark, no smuggled extra).
	union := map[string]bool{}
	for _, set := range [][]string{P2SightActivationTwo, P2AnswerableActivationTwo, P2ScoutActivationOne} {
		for _, k := range set {
			union[k] = true
		}
	}
	if len(union) != len(P2ActivationFive) {
		t.Errorf("the three P2 waves cover %d keys, want %d (= P2ActivationFive)", len(union), len(P2ActivationFive))
	}
	for _, k := range P2ActivationFive {
		if !union[k] {
			t.Errorf("P2ActivationFive key %q is not released by any P2 wave", k)
		}
	}
}

// TestP2ScoutActivationOne_CostMapConsistency is the seedspec↔catalogue↔cost_map
// drift guard: kg_explore's catalogue PriceKey must resolve to its canonical cost
// (else the live invoke runner fail-loud 500s at the LookupCost pre-flight). Locks
// the spec-§7 price (kg_explore 20).
func TestP2ScoutActivationOne_CostMapConsistency(t *testing.T) {
	e := CatalogueByKey()["kg_explore"]
	if e.PriceKey == "" {
		t.Fatalf("kg_explore has an empty PriceKey — an active Skill must be metered")
	}
	got, err := companion.LookupCost(e.PriceKey)
	if err != nil {
		t.Fatalf("kg_explore PriceKey %q not in the canonical cost map: %v", e.PriceKey, err)
	}
	if got != 20 {
		t.Errorf("kg_explore cost = %d, want 20 (spec §7)", got)
	}
}
