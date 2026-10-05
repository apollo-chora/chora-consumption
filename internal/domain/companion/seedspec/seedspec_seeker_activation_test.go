// seedspec_seeker_activation_test.go — P5 Far Sight (CHO-2017): the first
// Seeker activation wave (fact_check) pins its SET + stage/policy gating + its
// seedspec↔catalogue↔cost_map consistency. The set is the release contract for
// migration 0087; the flip itself is HELD pending the ADR-174 §8 external_egress
// eval gate (owner-driven), exactly as the P1 / P2 waves gated their flips.
package seedspec

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestP5SeekerActivationFactCheck_ShapeAndGating asserts the wave is EXACTLY
// fact_check, an active Seeker Skill at its spec stage (st5) with its chat sink +
// external_egress policy class (the tenant allow/deny + minors gate dimension).
func TestP5SeekerActivationFactCheck_ShapeAndGating(t *testing.T) {
	if len(P5SeekerActivationFactCheck) != 1 || P5SeekerActivationFactCheck[0] != "fact_check" {
		t.Fatalf("P5SeekerActivationFactCheck = %v, want exactly [fact_check]", P5SeekerActivationFactCheck)
	}
	e, ok := CatalogueByKey()["fact_check"]
	if !ok {
		t.Fatalf("fact_check absent from the catalogue")
	}
	if e.SkillKind != companion.SkillKindActive {
		t.Errorf("fact_check kind = %q, want active", e.SkillKind)
	}
	if e.MinGrowthStage != 5 {
		t.Errorf("fact_check min_growth_stage = %d, want 5 (spec §2.4)", e.MinGrowthStage)
	}
	if e.Family != companion.FamilySeeker {
		t.Errorf("fact_check family = %q, want seeker", e.Family)
	}
	if e.OutputSink != companion.SinkChat {
		t.Errorf("fact_check output_sink = %q, want chat", e.OutputSink)
	}
	if e.PolicyClass != companion.PolicyExternalEgress {
		t.Errorf("fact_check policy_class = %q, want external_egress", e.PolicyClass)
	}
}

// TestP5SeekerActivationFactCheck_DisjointFromP2 ties the wave to its phase:
// fact_check is a P5 Seeker, NOT part of the P2 launch remainder, and must not
// re-activate any P1/P2 Skill (each wave releases a distinct, gated subset).
func TestP5SeekerActivationFactCheck_DisjointFromP2(t *testing.T) {
	prior := map[string]bool{}
	for _, k := range P1ActivationThree {
		prior[k] = true
	}
	for _, k := range P2ActivationFive {
		prior[k] = true
	}
	for _, k := range P5SeekerActivationFactCheck {
		if prior[k] {
			t.Errorf("%q overlaps a P1/P2 wave — a P5 Seeker must be its own distinct activation", k)
		}
	}
	// The sibling Seekers stay dark this wave (their builders are unbuilt).
	for _, k := range P5SeekerActivationFactCheck {
		if k == "web_research" || k == "source_reader" {
			t.Errorf("%q must NOT ride the fact_check wave (its builder is unbuilt)", k)
		}
	}
}

// TestP5SeekerActivationFactCheck_CostMapConsistency is the seedspec↔catalogue↔
// cost_map drift guard: fact_check's catalogue PriceKey must resolve to a
// canonical cost (else the live invoke runner fail-loud 500s at the LookupCost
// pre-flight). Post-ADR-231 D6 the metering is re-homed from the fenced turn to
// the gateway grounded-search egress, but the PriceKey + price are unchanged —
// the runner pre-flights this exact cost and grounded.Query.ActionCode carries it
// to the egress. Locks the spec-§2.4 price (fact_check 40).
func TestP5SeekerActivationFactCheck_CostMapConsistency(t *testing.T) {
	byKey := CatalogueByKey()
	for _, key := range P5SeekerActivationFactCheck {
		e := byKey[key]
		if e.PriceKey == "" {
			t.Fatalf("%q has an empty PriceKey — an active Skill must be metered", key)
		}
		got, err := companion.LookupCost(e.PriceKey)
		if err != nil {
			t.Fatalf("%q PriceKey %q not in the canonical cost map: %v", key, e.PriceKey, err)
		}
		if got != 40 {
			t.Errorf("%q cost = %d, want 40 (spec §2.4)", key, got)
		}
	}
}
