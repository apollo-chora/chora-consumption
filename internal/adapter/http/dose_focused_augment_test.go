// dose_focused_augment_test.go — CHO-1895 FE-demonstrability: the focused-practice
// dose (?growth_edge_id={id}) must SERVE the edge's gradable cached drill atoms so
// completing one recovers the edge via RecoverByDrillAtomID. Two adapter-level
// invariants the handler must satisfy:
//
//  1. SERVABILITY — a cached drill atom need NOT be on the learner's enrolled
//     LearningPaths to be drilled: the handler augments the dose universe with the
//     edge's resolved cached atoms (a focused-practice drill is the practice
//     material, enrolment-independent).
//  2. GRADABILITY — only gradable+answerable cached atoms (atom_index IsMCQ AND
//     topic-tagged) are served. A non-gradable cached atom (e.g. cached before the
//     drillcache gradability invariant) is dropped so the focused weakness slot is
//     never a dead end.
//
// TDD strict (RED → GREEN): before the handler resolve+filter+augment lands, an
// unenrolled cached atom is not in the universe so the composer serves 0 weakness.
package http

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// projectAtom saves one atom_index projection (gradable when correctOptionID != "").
func projectAtom(t *testing.T, srv *Server, atomID, topic, correctOptionID string) {
	t.Helper()
	a := &atom_index.AtomIndex{
		AtomID:          atomID,
		TenantID:        testTenant,
		Title:           "Drill " + atomID,
		AtomType:        "mcq",
		CorrectOptionID: correctOptionID, // "" ⇒ NOT gradable (IsMCQ false)
		AnswerCount:     4,
		TopicTags:       []string{topic},
		PublishedAt:     time.Now().UTC(),
	}
	if err := srv.AtomIndex.Save(context.Background(), a); err != nil {
		t.Fatalf("project atom %s: %v", atomID, err)
	}
}

func focusedWeaknessAtomIDs(t *testing.T, srv *Server, edgeID string) []string {
	t.Helper()
	entries := doseEntries(t, srv, "/companion/daily-dose?growth_edge_id="+edgeID)
	out := []string{}
	for _, e := range entries {
		if e.Reason == "weakness" {
			out = append(out, e.AtomID)
		}
	}
	return out
}

// TestDailyDose_Focused_AugmentsAndFiltersGradableCachedAtoms — the focused edge
// caches one GRADABLE and one NON-gradable atom, NEITHER enrolled. The handler
// augments the universe with the gradable one and serves it as weakness; the
// non-gradable one is dropped. Pre-fix neither is servable (unenrolled) so the
// slot is empty — RED.
func TestDailyDose_Focused_AugmentsAndFiltersGradableCachedAtoms(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs) // enrolled universe, topic product-ownership

	gradable := "019e0000-0000-7000-c000-000000000001"
	nonGradable := "019e0000-0000-7000-c000-000000000002"
	projectAtom(t, srv, gradable, "binary-search", "opt-b") // gradable (has answer key)
	projectAtom(t, srv, nonGradable, "binary-search", "")   // NOT gradable (no answer key)

	edge := doseEdge(t, "Binary Search", 0.9, gradable, nonGradable) // both cached, both unenrolled
	srv.LearnerWeakness = &doseLWStub{getResult: &edge}

	weak := focusedWeaknessAtomIDs(t, srv, edge.ID)
	if len(weak) != 1 {
		t.Fatalf("focused weakness picks = %v, want exactly 1 (the gradable cached atom, served via augmentation)", weak)
	}
	if weak[0] != gradable {
		t.Fatalf("focused weakness pick = %q, want %q (gradable cached drill atom)", weak[0], gradable)
	}
}

// TestDailyDose_Focused_AllCachedNonGradable_FailSoft — when every cached drill
// atom is non-gradable the focused slot serves 0 weakness (fail soft) and the
// dose still returns a full fresh budget (never a dead-end drill).
func TestDailyDose_Focused_AllCachedNonGradable_FailSoft(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)

	bad1 := "019e0000-0000-7000-c000-000000000011"
	bad2 := "019e0000-0000-7000-c000-000000000012"
	projectAtom(t, srv, bad1, "binary-search", "") // non-gradable
	projectAtom(t, srv, bad2, "binary-search", "") // non-gradable

	edge := doseEdge(t, "Binary Search", 0.9, bad1, bad2)
	srv.LearnerWeakness = &doseLWStub{getResult: &edge}

	entries := doseEntries(t, srv, "/companion/daily-dose?growth_edge_id="+edge.ID)
	weak, fresh := 0, 0
	for _, e := range entries {
		switch e.Reason {
		case "weakness":
			weak++
		case "fresh":
			fresh++
		}
	}
	if weak != 0 {
		t.Fatalf("weakness picks = %d, want 0 (no gradable cached atom → fail soft)", weak)
	}
	if fresh == 0 {
		t.Fatalf("dose has no fresh picks; want the budget topped up with fresh on a non-gradable cache")
	}
}
