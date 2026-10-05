// dose_focused_alignment_test.go — drill-atom→grow alignment invariant
// (CHO-1895 follow-up): a FOCUSED Daily Dose (FocusEdgeID set) MUST fill its
// weakness slot ONLY from the edge's cached_drill_atom_ids, never from a broad
// topic/tag slug match. Reason: RecoverByDrillAtomID recovers an edge whose
// cached_drill_atom_ids contains the completed atom — a topic-matched atom that
// is NOT cached can never recover the edge via the drill path, so serving it as
// the focused weakness drill is a dead end (the upload→practice→grow loop never
// closes). When no cached drill atom is servable the slot fails soft (0 weakness;
// the budget tops up with fresh) — never a misaligned drill.
//
// TDD strict (RED → GREEN): these assert the aligned behaviour BEFORE the slug
// fallback is removed from pickFocusedEdgeSlots, so they fail (the current code
// serves topic-matched non-cached atoms as weakness).
package companion

import (
	"testing"
	"time"
)

func alignInput(seeds []AtomSeed, edges []GrowthEdgeInput, focusID string) DailyDoseInput {
	return DailyDoseInput{
		Seeds:       seeds,
		States:      map[string]SM2State{},
		GrowthEdges: edges,
		FocusEdgeID: focusID,
		Now:         time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	}
}

// TestDose_Focused_WeaknessSlotOnlyCachedDrillAtoms — the focused edge has ONE
// cached drill atom present in the seed universe, and its concept_key matches
// the topic of OTHER seeds. The aligned composer serves exactly that one cached
// atom as weakness; the topic-matching siblings are NOT weakness picks (they may
// appear as fresh top-up). Pre-fix the slug fallback would (wrongly) add the
// topic siblings as weakness.
func TestDose_Focused_WeaknessSlotOnlyCachedDrillAtoms(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"}, // cached drill
		{AtomID: "atom-frac-3", Topic: "Fractions", Title: "Fractions III"},
		{AtomID: "atom-hist-1", Topic: "History", Title: "WW2"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "fractions", // matches frac-1/3 topic — must NOT be drilled
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-frac-2"},
	}
	dose := ComposeDailyDose(alignInput(seeds, []GrowthEdgeInput{edge}, "edge-1"))

	weak := weaknessEntries(dose)
	if len(weak) != 1 {
		t.Fatalf("weakness picks = %d, want exactly 1 (only the cached drill atom)", len(weak))
	}
	if weak[0].AtomID != "atom-frac-2" {
		t.Fatalf("weakness pick = %q, want atom-frac-2 (the cached drill atom)", weak[0].AtomID)
	}
	// Every weakness pick MUST be one of the edge's cached drill atoms.
	cached := map[string]bool{"atom-frac-2": true}
	for _, e := range weak {
		if !cached[e.AtomID] {
			t.Errorf("weakness pick %q is NOT a cached drill atom — drill recovery can never fire", e.AtomID)
		}
	}
	// Budget still met via fresh top-up.
	if len(dose.Entries) != DoseTargetSize-0 && len(dose.Entries) != len(seeds) {
		t.Fatalf("dose size = %d, want %d (full budget when seeds permit)", len(dose.Entries), len(seeds))
	}
}

// TestDose_Focused_CacheMiss_NoWeakness_FailSoft — the focused edge's cached
// drill atoms are NOT in the seed universe, but its concept_key matches seed
// topics. The aligned composer serves ZERO weakness picks (fail soft) rather
// than a misaligned topic match; the budget fills with fresh. Pre-fix the slug
// fallback served 3 Fractions atoms as weakness — none of which could recover
// the edge.
func TestDose_Focused_CacheMiss_NoWeakness_FailSoft(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"},
		{AtomID: "atom-frac-3", Topic: "Fractions", Title: "Fractions III"},
		{AtomID: "atom-hist-1", Topic: "History", Title: "WW2"},
		{AtomID: "atom-geo-1", Topic: "Geography", Title: "Capitals"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "fractions",
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-not-in-universe"},
	}
	dose := ComposeDailyDose(alignInput(seeds, []GrowthEdgeInput{edge}, "edge-1"))

	if w := len(weaknessEntries(dose)); w != 0 {
		t.Fatalf("weakness picks = %d, want 0 (no servable cached drill atom → fail soft, no misaligned drill)", w)
	}
	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d (fresh top-up fills the budget)", len(dose.Entries), DoseTargetSize)
	}
	if fresh := countByReason(dose.Entries, DoseReasonFresh); fresh != DoseTargetSize {
		t.Errorf("fresh picks = %d, want %d (whole budget is fresh on a cache miss)", fresh, DoseTargetSize)
	}
}
