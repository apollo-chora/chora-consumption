// dose_growth_edges_test.go — W6 (Epic-1b): the weakness-drill slot reads the
// learner's Growth Edges (preferring cached drill atoms, then topic match),
// with the raw topic_accuracy pool as the legacy fallback.
package companion

import (
	"testing"
	"time"
)

func geSeeds() []AtomSeed {
	return []AtomSeed{
		{AtomID: "atom-frac-1", Topic: "Fractions", Title: "Fractions I"},
		{AtomID: "atom-frac-2", Topic: "Fractions", Title: "Fractions II"},
		{AtomID: "atom-mult-1", Topic: "Multiplication Tables", Title: "Times Tables"},
		{AtomID: "atom-hist-1", Topic: "History", Title: "WW2"},
		{AtomID: "atom-geo-1", Topic: "Geography", Title: "Capitals"},
	}
}

func geInput(edges []GrowthEdgeInput) DailyDoseInput {
	return DailyDoseInput{
		Seeds:       geSeeds(),
		States:      map[string]SM2State{},
		GrowthEdges: edges,
		Now:         time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
}

func weaknessEntries(d DailyDose) []DailyDoseEntry {
	out := []DailyDoseEntry{}
	for _, e := range d.Entries {
		if e.DoseReason == DoseReasonWeakness {
			out = append(out, e)
		}
	}
	return out
}

func TestDose_GrowthEdge_PrefersCachedDrillAtoms(t *testing.T) {
	dose := ComposeDailyDose(geInput([]GrowthEdgeInput{
		{
			ConceptKey:         "fractions",
			Strength:           0.9,
			CachedDrillAtomIDs: []string{"atom-frac-2"}, // W4 cache → exact pick
		},
	}))
	weak := weaknessEntries(dose)
	if len(weak) == 0 || weak[0].AtomID != "atom-frac-2" {
		t.Fatalf("weakness picks = %+v, want cached drill atom-frac-2 first", weak)
	}
}

func TestDose_GrowthEdge_TopicSlugFallback(t *testing.T) {
	dose := ComposeDailyDose(geInput([]GrowthEdgeInput{
		{ConceptKey: "multiplication-tables", Strength: 0.8}, // no cache → slug match on Topic
	}))
	weak := weaknessEntries(dose)
	if len(weak) != 1 || weak[0].AtomID != "atom-mult-1" {
		t.Fatalf("weakness picks = %+v, want atom-mult-1 via topic slug", weak)
	}
}

func TestDose_GrowthEdge_TagMatchAndStrengthOrder(t *testing.T) {
	dose := ComposeDailyDose(geInput([]GrowthEdgeInput{
		{ConceptKey: "ww2-chronology", Strength: 0.5, Tags: []string{"History"}},
		{ConceptKey: "fractions", Strength: 0.9},
	}))
	weak := weaknessEntries(dose)
	if len(weak) != 2 {
		t.Fatalf("weakness picks = %+v, want 2", weak)
	}
	// Strongest (shakiest) edge drills first.
	if weak[0].Topic != "Fractions" {
		t.Fatalf("first pick = %+v, want the 0.9 fractions edge first", weak[0])
	}
	if weak[1].AtomID != "atom-hist-1" {
		t.Fatalf("second pick = %+v, want tag-matched atom-hist-1", weak[1])
	}
}

func TestDose_GrowthEdge_CachedAtomOutsideUniverseFallsBack(t *testing.T) {
	dose := ComposeDailyDose(geInput([]GrowthEdgeInput{
		{
			ConceptKey:         "fractions",
			Strength:           0.9,
			CachedDrillAtomIDs: []string{"atom-not-in-universe"},
		},
	}))
	weak := weaknessEntries(dose)
	if len(weak) == 0 || (weak[0].AtomID != "atom-frac-1" && weak[0].AtomID != "atom-frac-2") {
		t.Fatalf("weakness picks = %+v, want topic fallback within the seed universe", weak)
	}
}

func TestDose_GrowthEdge_RespectsSlotCap(t *testing.T) {
	dose := ComposeDailyDose(geInput([]GrowthEdgeInput{
		{ConceptKey: "fractions", Strength: 0.9, CachedDrillAtomIDs: []string{"atom-frac-1", "atom-frac-2"}},
		{ConceptKey: "multiplication-tables", Strength: 0.8},
		{ConceptKey: "geography", Strength: 0.7},
	}))
	if got := len(weaknessEntries(dose)); got != DoseWeaknessCount {
		t.Fatalf("weakness picks = %d, want capped at %d", got, DoseWeaknessCount)
	}
}

func TestDose_NoGrowthEdges_LegacyAccuracyPathUnchanged(t *testing.T) {
	in := geInput(nil)
	in.TopicAccuracy = map[string]float64{"Fractions": 0.2}
	dose := ComposeDailyDose(in)
	weak := weaknessEntries(dose)
	if len(weak) == 0 || weak[0].Topic != "Fractions" {
		t.Fatalf("legacy accuracy path broken: %+v", weak)
	}
}
