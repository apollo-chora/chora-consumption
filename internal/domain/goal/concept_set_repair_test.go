// concept_set_repair_test.go — WS-C6 (CHO-2085, ADR-227 D14 + addendum #7)
// slug-axis repair of goals.concept_set after a concept merge/split. RED-first.
//
// Binding semantics pinned here:
//   - merge: the absorbed node's concept_key leaves the set, the survivor's
//     joins it; split: the parent's key leaves, the children's join.
//   - repairs normalise + de-duplicate exactly like the constructor
//     (normaliseConcepts vocabulary) and preserve first-seen order.
//   - a no-op repair reports changed=false and does NOT touch UpdatedAt (the
//     caller skips the goal UPDATE entirely).
//   - soft-deleted goals fail loud (ErrDeleted), mirroring every other mutator.
package goal

import (
	"errors"
	"testing"
	"time"
)

func repairTestGoal(t *testing.T, concepts []string) *Goal {
	t.Helper()
	g, err := NewGoal(NewGoalInput{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		LearnerGCID: "00000000-0000-7000-8000-000000001999",
		Kind:        KindCuriosity,
		ConceptSet:  concepts,
		Now:         time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewGoal: %v", err)
	}
	return g
}

func TestRepairConceptSet_RemoveAndAdd(t *testing.T) {
	g := repairTestGoal(t, []string{"algebra", "fractions", "geometry"})
	before := g.UpdatedAt
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	changed, err := g.RepairConceptSet([]string{"fractions"}, []string{"rational-numbers"}, now)
	if err != nil {
		t.Fatalf("RepairConceptSet: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := []string{"algebra", "geometry", "rational-numbers"}
	if len(g.ConceptSet) != len(want) {
		t.Fatalf("ConceptSet = %v, want %v", g.ConceptSet, want)
	}
	for i := range want {
		if g.ConceptSet[i] != want[i] {
			t.Fatalf("ConceptSet[%d] = %q, want %q (full: %v)", i, g.ConceptSet[i], want[i], g.ConceptSet)
		}
	}
	if !g.UpdatedAt.After(before) {
		t.Fatalf("UpdatedAt not bumped: %v", g.UpdatedAt)
	}
}

func TestRepairConceptSet_AddDedupesAndNormalises(t *testing.T) {
	g := repairTestGoal(t, []string{"algebra"})
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	changed, err := g.RepairConceptSet(nil, []string{"  algebra  ", "", "geometry", "geometry"}, now)
	if err != nil {
		t.Fatalf("RepairConceptSet: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true (geometry added)")
	}
	want := []string{"algebra", "geometry"}
	if len(g.ConceptSet) != 2 || g.ConceptSet[0] != want[0] || g.ConceptSet[1] != want[1] {
		t.Fatalf("ConceptSet = %v, want %v", g.ConceptSet, want)
	}
}

func TestRepairConceptSet_RemoveNormalisesInput(t *testing.T) {
	g := repairTestGoal(t, []string{"algebra", "fractions"})
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	changed, err := g.RepairConceptSet([]string{"  fractions  "}, nil, now)
	if err != nil {
		t.Fatalf("RepairConceptSet: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if len(g.ConceptSet) != 1 || g.ConceptSet[0] != "algebra" {
		t.Fatalf("ConceptSet = %v, want [algebra]", g.ConceptSet)
	}
}

func TestRepairConceptSet_NoOpLeavesUpdatedAt(t *testing.T) {
	g := repairTestGoal(t, []string{"algebra"})
	before := g.UpdatedAt
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	changed, err := g.RepairConceptSet([]string{"not-present"}, []string{"algebra"}, now)
	if err != nil {
		t.Fatalf("RepairConceptSet: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false, got true (set: %v)", g.ConceptSet)
	}
	if !g.UpdatedAt.Equal(before) {
		t.Fatalf("UpdatedAt moved on a no-op: %v -> %v", before, g.UpdatedAt)
	}
}

func TestRepairConceptSet_SoftDeletedFailsLoud(t *testing.T) {
	g := repairTestGoal(t, []string{"algebra"})
	g.SoftDelete(time.Date(2026, 7, 10, 8, 30, 0, 0, time.UTC))

	if _, err := g.RepairConceptSet([]string{"algebra"}, nil, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)); !errors.Is(err, ErrDeleted) {
		t.Fatalf("err = %v, want ErrDeleted", err)
	}
}
