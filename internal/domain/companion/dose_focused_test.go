// dose_focused_test.go — Phase 2A: the focused-practice Daily Dose. An optional
// FocusEdgeID scopes the WHOLE dose budget to drilling ONE Growth Edge (cached
// drill atoms first, then topic/tag slug matches), bypassing the 40/30/30
// cascade. When FocusEdgeID matches no edge the composer falls through to a
// normal fresh dose (graceful fallback). When FocusEdgeID is empty the existing
// cascade is byte-for-byte unchanged.
//
// TDD strict (RED → GREEN → REFACTOR): these assert the focused path before it
// exists — they fail to compile (DailyDoseInput.FocusEdgeID undefined) until the
// field + pickFocusedEdgeSlots land.
package companion

import (
	"testing"
	"time"
)

func focusInput(seeds []AtomSeed, edges []GrowthEdgeInput, focusID string) DailyDoseInput {
	return DailyDoseInput{
		Seeds:       seeds,
		States:      map[string]SM2State{},
		GrowthEdges: edges,
		FocusEdgeID: focusID,
		Now:         time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
	}
}

// TestDose_Focused_CachedDrillAtomsThenFreshTopUp — an edge with 3 cached drill
// atoms present in the seed universe yields 3 weakness picks (all from that
// edge) + 2 fresh top-up = the 5-card budget. The concept_key matches no seed
// topic so the topic fallback adds nothing, leaving room for fresh.
func TestDose_Focused_CachedDrillAtomsThenFreshTopUp(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-a1", Topic: "Alpha", Title: "A1"},
		{AtomID: "atom-a2", Topic: "Alpha", Title: "A2"},
		{AtomID: "atom-a3", Topic: "Alpha", Title: "A3"},
		{AtomID: "atom-b1", Topic: "Beta", Title: "B1"},
		{AtomID: "atom-b2", Topic: "Beta", Title: "B2"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "gamma", // matches no seed topic → fallback adds nothing
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-a1", "atom-a2", "atom-a3"},
	}
	dose := ComposeDailyDose(focusInput(seeds, []GrowthEdgeInput{edge}, "edge-1"))

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
	weak := weaknessEntries(dose)
	if len(weak) != 3 {
		t.Fatalf("weakness picks = %d, want 3 (all cached drill atoms of the focused edge)", len(weak))
	}
	gotWeak := map[string]bool{}
	for _, e := range weak {
		gotWeak[e.AtomID] = true
	}
	for _, id := range []string{"atom-a1", "atom-a2", "atom-a3"} {
		if !gotWeak[id] {
			t.Errorf("cached drill atom %s missing from weakness picks", id)
		}
	}
	if fresh := countByReason(dose.Entries, DoseReasonFresh); fresh != 2 {
		t.Errorf("fresh top-up = %d, want 2 (budget filled to %d)", fresh, DoseTargetSize)
	}
}

// TestDose_Focused_CapsAtTargetSize — an edge with 6 cached drill atoms yields
// exactly DoseTargetSize weakness picks, capped at the global budget (the whole
// dose drills the one edge).
func TestDose_Focused_CapsAtTargetSize(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-a1", Topic: "Alpha", Title: "A1"},
		{AtomID: "atom-a2", Topic: "Alpha", Title: "A2"},
		{AtomID: "atom-a3", Topic: "Alpha", Title: "A3"},
		{AtomID: "atom-a4", Topic: "Alpha", Title: "A4"},
		{AtomID: "atom-a5", Topic: "Alpha", Title: "A5"},
		{AtomID: "atom-a6", Topic: "Alpha", Title: "A6"},
		{AtomID: "atom-b1", Topic: "Beta", Title: "B1"},
	}
	edge := GrowthEdgeInput{
		EdgeID:     "edge-1",
		ConceptKey: "gamma",
		Strength:   0.9,
		CachedDrillAtomIDs: []string{
			"atom-a1", "atom-a2", "atom-a3", "atom-a4", "atom-a5", "atom-a6",
		},
	}
	dose := ComposeDailyDose(focusInput(seeds, []GrowthEdgeInput{edge}, "edge-1"))

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
	if w := len(weaknessEntries(dose)); w != DoseTargetSize {
		t.Fatalf("weakness picks = %d, want %d (capped at budget)", w, DoseTargetSize)
	}
}

// TestDose_Focused_NoTopicSlugFallback — when the edge's cached atoms are NOT in
// the seed universe, the focused weakness slot serves NOTHING (no topic/tag slug
// fallback). CHO-1895 alignment: a topic-matched atom that is not a cached drill
// atom can never recover the edge via RecoverByDrillAtomID, so it must never be
// served as the focused weakness drill. The topic-matching seeds appear only as
// fresh top-up; the 5-card budget still holds.
func TestDose_Focused_NoTopicSlugFallback(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"},
		{AtomID: "atom-frac-3", Topic: "Fractions", Title: "Fractions III"},
		{AtomID: "atom-hist-1", Topic: "History", Title: "WW2"},
		{AtomID: "atom-geo-1", Topic: "Geography", Title: "Capitals"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "fractions", // would slug-match the Fractions seeds — must NOT drill them
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-not-in-universe"},
	}
	dose := ComposeDailyDose(focusInput(seeds, []GrowthEdgeInput{edge}, "edge-1"))

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
	if w := len(weaknessEntries(dose)); w != 0 {
		t.Fatalf("weakness picks = %d, want 0 (no slug fallback — cache fully missed)", w)
	}
	if fresh := countByReason(dose.Entries, DoseReasonFresh); fresh != DoseTargetSize {
		t.Errorf("fresh picks = %d, want %d (topic-matching seeds are fresh, not weakness)", fresh, DoseTargetSize)
	}
}

// TestDose_Focused_UnknownEdgeFallsThroughToFreshDose — FocusEdgeID set but no
// matching edge in GrowthEdges falls through to a normal fresh dose: 5 cards, no
// weakness picks, no panic.
func TestDose_Focused_UnknownEdgeFallsThroughToFreshDose(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-a1", Topic: "Alpha", Title: "A1"},
		{AtomID: "atom-a2", Topic: "Alpha", Title: "A2"},
		{AtomID: "atom-a3", Topic: "Alpha", Title: "A3"},
		{AtomID: "atom-a4", Topic: "Alpha", Title: "A4"},
		{AtomID: "atom-a5", Topic: "Alpha", Title: "A5"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "fractions",
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-a1"},
	}
	dose := ComposeDailyDose(focusInput(seeds, []GrowthEdgeInput{edge}, "edge-DOES-NOT-EXIST"))

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("graceful-fallback dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
	if w := len(weaknessEntries(dose)); w != 0 {
		t.Errorf("weakness picks = %d, want 0 — an unknown focus edge must NOT drill", w)
	}
	if fresh := countByReason(dose.Entries, DoseReasonFresh); fresh != DoseTargetSize {
		t.Errorf("fresh picks = %d, want %d (normal fresh dose)", fresh, DoseTargetSize)
	}
}

// TestDose_Focused_NilEdgesFallsThrough — FocusEdgeID set but GrowthEdges nil
// (the handler's fail-soft path when the edge read returns nothing) yields a
// normal fresh dose with no panic.
func TestDose_Focused_NilEdgesFallsThrough(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-a1", Topic: "Alpha", Title: "A1"},
		{AtomID: "atom-a2", Topic: "Alpha", Title: "A2"},
	}
	dose := ComposeDailyDose(focusInput(seeds, nil, "edge-1"))

	if len(dose.Entries) != 2 {
		t.Fatalf("dose size = %d, want 2 (fresh top-up over the 2 seeds)", len(dose.Entries))
	}
	if w := len(weaknessEntries(dose)); w != 0 {
		t.Errorf("weakness picks = %d, want 0 (nil edges → no drill)", w)
	}
}

// TestDose_Focused_EmptyFocusEdgeID_UsesNormalCascade — with FocusEdgeID empty,
// the normal cascade runs: pickGrowthEdgeSlots drills ONE atom per edge across
// frontiers (capped at DoseWeaknessCount), NOT the whole budget from one edge.
// Regression guard that the focused path is bypassed when growth_edge_id is
// absent.
func TestDose_Focused_EmptyFocusEdgeID_UsesNormalCascade(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-a1", Topic: "Alpha", Title: "A1"},
		{AtomID: "atom-a2", Topic: "Alpha", Title: "A2"},
		{AtomID: "atom-b1", Topic: "Beta", Title: "B1"},
		{AtomID: "atom-b2", Topic: "Beta", Title: "B2"},
		{AtomID: "atom-c1", Topic: "Gamma", Title: "C1"},
	}
	edges := []GrowthEdgeInput{
		{EdgeID: "e1", ConceptKey: "alpha", Strength: 0.9, CachedDrillAtomIDs: []string{"atom-a1", "atom-a2"}},
		{EdgeID: "e2", ConceptKey: "beta", Strength: 0.8, CachedDrillAtomIDs: []string{"atom-b1", "atom-b2"}},
	}
	dose := ComposeDailyDose(focusInput(seeds, edges, "")) // FocusEdgeID empty → normal cascade

	weak := weaknessEntries(dose)
	if len(weak) != DoseWeaknessCount {
		t.Fatalf("normal-cascade weakness = %d, want %d (one atom per edge)", len(weak), DoseWeaknessCount)
	}
	topics := map[string]bool{}
	for _, e := range weak {
		topics[e.Topic] = true
	}
	if !topics["Alpha"] || !topics["Beta"] {
		t.Errorf("normal cascade should drill one atom per edge across frontiers, got %+v", weak)
	}
}

// TestDose_Focused_Deterministic — the focused path is deterministic: identical
// input yields byte-identical entries (same atoms, same order, same reasons).
func TestDose_Focused_Deterministic(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"},
		{AtomID: "atom-frac-3", Topic: "Fractions", Title: "Fractions III"},
		{AtomID: "atom-hist-1", Topic: "History", Title: "WW2"},
	}
	edge := GrowthEdgeInput{
		EdgeID:             "edge-1",
		ConceptKey:         "fractions",
		Strength:           0.9,
		CachedDrillAtomIDs: []string{"atom-frac-2"},
	}
	in := focusInput(seeds, []GrowthEdgeInput{edge}, "edge-1")
	d1 := ComposeDailyDose(in)
	d2 := ComposeDailyDose(in)
	if len(d1.Entries) != len(d2.Entries) {
		t.Fatalf("non-deterministic size: %d vs %d", len(d1.Entries), len(d2.Entries))
	}
	for i := range d1.Entries {
		if d1.Entries[i].AtomID != d2.Entries[i].AtomID || d1.Entries[i].DoseReason != d2.Entries[i].DoseReason {
			t.Errorf("entry[%d] = %+v vs %+v (non-deterministic)", i, d1.Entries[i], d2.Entries[i])
		}
	}
}
