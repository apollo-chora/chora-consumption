// dose_composer_v2_wiring_test.go — CHO-2045 / ADR-224 sub-phase C: the dose
// handler threads each Growth Edge's subject (Category) into the composer and,
// when Server.ComposerV2Enabled is set (env DOSE_COMPOSER_V2_ENABLED), runs the
// v2 subject/KG-balanced + seeded-sampled selection instead of the v1 strict
// global-shakiest fill. Dark by default: NewServer() leaves the flag false, so
// every existing dose test keeps the v1 path.
package http

import (
	"context"
	"testing"
	"time"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// doseEdgeCat builds a Growth Edge with an explicit subject (Category).
func doseEdgeCat(t *testing.T, label, category string, strength float64, cached ...string) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:     testTenant,
		LearnerGCID:  testGCID,
		ConceptLabel: label,
		Category:     category,
		Embedding:    []float32{0.1},
		Strength:     strength,
		Source:       lw.SourceExplicit,
		Now:          time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("edge: %v", err)
	}
	w.CachedDrillAtomIDs = cached
	return *w
}

func containsID(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// The edge loader must carry the subject (Category) onto the composer's carrier,
// so the v2 allocator can bucket by subject.
func TestDoseGrowthEdges_ThreadsCategory(t *testing.T) {
	srv := NewServer()
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{
		doseEdgeCat(t, "Fractions", "Math", 0.8, "atom-x"),
		doseEdgeCat(t, "Photosynthesis", "Science", 0.6, "atom-y"),
	}}
	edges := srv.doseGrowthEdges(context.Background(), testTenant, testGCID)
	if len(edges) != 2 {
		t.Fatalf("want 2 edges, got %d", len(edges))
	}
	cats := map[string]string{}
	for _, e := range edges {
		cats[e.ConceptLabel] = e.Category
	}
	if cats["Fractions"] != "Math" || cats["Photosynthesis"] != "Science" {
		t.Fatalf("Category not threaded onto GrowthEdgeInput: %+v", cats)
	}
}

// With the flag ON the dose spreads the weakness slots across subjects; with it
// OFF the v1 path takes the two globally-shakiest (both Math), so the milder
// Science atom appears ONLY under v2. This proves the flag flips the behaviour.
func TestDailyDose_ComposerV2Flag_SpreadsAcrossSubjects(t *testing.T) {
	// Strengths stay below CriticalWeaknessStrength (0.9) so the v2 spread runs
	// rather than the critical-edge urgency override (which would take both slots).
	mathA, mathB, sci := realDoseAtomIDs[0], realDoseAtomIDs[1], realDoseAtomIDs[2]
	edges := []lw.LearnerWeakness{
		doseEdgeCat(t, "Fractions", "Math", 0.80, mathA),
		doseEdgeCat(t, "Multiplication", "Math", 0.75, mathB),
		doseEdgeCat(t, "Photosynthesis", "Science", 0.55, sci),
	}

	// Flag OFF (v1): weakness = two shakiest = both Math; Science never surfaces.
	off := NewServer()
	seedRealAtomUniverse(t, off, realDoseAtomIDs)
	off.LearnerWeakness = &doseLWStub{items: edges}
	if weakOff := doseWeaknessAtomIDs(t, off); containsID(weakOff, sci) {
		t.Fatalf("v1 (flag off) must not spread to Science; got %v", weakOff)
	}

	// Flag ON (v2): the spread cap forces one slot to Science.
	on := NewServer()
	seedRealAtomUniverse(t, on, realDoseAtomIDs)
	on.LearnerWeakness = &doseLWStub{items: edges}
	on.ComposerV2Enabled = true
	if weakOn := doseWeaknessAtomIDs(t, on); !containsID(weakOn, sci) {
		t.Fatalf("v2 (flag on) must spread across subjects to include Science %s; got %v", sci, weakOn)
	}
}
