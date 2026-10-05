// seedspec_web_research_activation_test.go — P5 Far Sight (CHO-2017): the SECOND
// Seeker activation wave (web_research "Far Sight") pins its SET + stage/policy
// gating + its seedspec↔catalogue↔cost_map consistency. The set is the release
// contract for migration 0088; the flip itself is HELD pending the ADR-174 §8
// external_egress eval gate (owner-driven), exactly as the P1 / P2 / fact_check
// waves gated their flips.
//
// Owner ruling (2026-07-10): web_research has EXACTLY ONE output sink =
// memory_note. The spec §2.4 second sink (map suggestion inbox via kg.suggest)
// was a doc error and is DEFERRED — this test binds the single-sink invariant.
package seedspec

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestP5SeekerActivationWebResearch_ShapeAndGating asserts the wave is EXACTLY
// web_research, an active Seeker Skill at its spec stage (st5) with its
// memory_note sink (the ONE sink — owner ruling) + external_egress policy class.
func TestP5SeekerActivationWebResearch_ShapeAndGating(t *testing.T) {
	if len(P5SeekerActivationWebResearch) != 1 || P5SeekerActivationWebResearch[0] != "web_research" {
		t.Fatalf("P5SeekerActivationWebResearch = %v, want exactly [web_research]", P5SeekerActivationWebResearch)
	}
	e, ok := CatalogueByKey()["web_research"]
	if !ok {
		t.Fatalf("web_research absent from the catalogue")
	}
	if e.SkillKind != companion.SkillKindActive {
		t.Errorf("web_research kind = %q, want active", e.SkillKind)
	}
	if e.MinGrowthStage != 5 {
		t.Errorf("web_research min_growth_stage = %d, want 5 (spec §2.4)", e.MinGrowthStage)
	}
	if e.Family != companion.FamilySeeker {
		t.Errorf("web_research family = %q, want seeker", e.Family)
	}
	// Owner ruling: EXACTLY ONE sink = memory_note (NOT chat, NOT suggestion_inbox).
	if e.OutputSink != companion.SinkMemoryNote {
		t.Errorf("web_research output_sink = %q, want memory_note (single-sink owner ruling)", e.OutputSink)
	}
	if e.PolicyClass != companion.PolicyExternalEgress {
		t.Errorf("web_research policy_class = %q, want external_egress", e.PolicyClass)
	}
}

// TestP5SeekerActivationWebResearch_DisjointFromPriorWaves ties the wave to its
// phase: web_research is a P5 Seeker, distinct from every P1/P2 wave AND from the
// fact_check wave (each wave releases a distinct, gated subset).
func TestP5SeekerActivationWebResearch_DisjointFromPriorWaves(t *testing.T) {
	prior := map[string]bool{}
	for _, k := range P1ActivationThree {
		prior[k] = true
	}
	for _, k := range P2ActivationFive {
		prior[k] = true
	}
	for _, k := range P5SeekerActivationFactCheck {
		prior[k] = true
	}
	for _, k := range P5SeekerActivationWebResearch {
		if prior[k] {
			t.Errorf("%q overlaps a prior wave — a P5 web_research release must be its own distinct activation", k)
		}
	}
	// The remaining sibling Seeker (source_reader) stays dark this wave.
	for _, k := range P5SeekerActivationWebResearch {
		if k == "source_reader" {
			t.Errorf("%q must NOT ride the web_research wave (its builder is unbuilt)", k)
		}
	}
}

// TestP5SeekerActivationWebResearch_CostMapConsistency is the seedspec↔catalogue↔
// cost_map drift guard: web_research's catalogue PriceKey must resolve to a
// canonical cost (else the live invoke runner fail-loud 500s at the LookupCost
// pre-flight). Post-ADR-231 D6 the metering is re-homed from the fenced turn to
// the gateway grounded-search egress, but the PriceKey + price are unchanged —
// grounded.Query.ActionCode carries it to the egress. Locks the spec-§2.4 price
// (web_research 80).
func TestP5SeekerActivationWebResearch_CostMapConsistency(t *testing.T) {
	byKey := CatalogueByKey()
	for _, key := range P5SeekerActivationWebResearch {
		e := byKey[key]
		if e.PriceKey == "" {
			t.Fatalf("%q has an empty PriceKey — an active Skill must be metered", key)
		}
		got, err := companion.LookupCost(e.PriceKey)
		if err != nil {
			t.Fatalf("%q PriceKey %q not in the canonical cost map: %v", key, e.PriceKey, err)
		}
		if got != 80 {
			t.Errorf("%q cost = %d, want 80 (spec §2.4)", key, got)
		}
	}
}
