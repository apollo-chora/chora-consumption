package atom_index

import "testing"

// Servable is the ADR-243 D4 servability predicate. It had three copies before
// this test existed (doseAtomUniverse, the goal-scoped dose tranche, and the
// concept-attachment candidate source), each spelling out
// `!a.Playable() || !a.IsAnswerable()` by hand. ADR-243 D4 says Source A REUSES
// the predicate the dose applies, which is only true if there is one of it: a
// fourth caller that forgot the IsAnswerable half would offer the learner an
// un-gradable dead end, and the map node it produced could never light up.
func TestServable_PublishedAndAnswerable(t *testing.T) {
	cases := []struct {
		name string
		a    *AtomIndex
		want bool
	}{
		{
			"published gradable mcq",
			&AtomIndex{AtomType: "mcq", CorrectOptionID: "opt-c", Status: StatusPublished},
			true,
		},
		{
			"published open-ended",
			&AtomIndex{AtomType: "essay", Status: StatusPublished},
			true,
		},
		{
			// Playable but NOT answerable: an MCQ with no answer key cannot be
			// graded, so it produces no signal.
			"published mcq with no answer key",
			&AtomIndex{AtomType: "mcq", Status: StatusPublished},
			false,
		},
		{
			// Answerable but NOT playable: still a draft.
			"draft gradable mcq",
			&AtomIndex{AtomType: "mcq", CorrectOptionID: "opt-c", Status: StatusDraft},
			false,
		},
		{
			"archived open-ended",
			&AtomIndex{AtomType: "essay", Status: StatusArchived},
			false,
		},
		{
			"published but typeless",
			&AtomIndex{Status: StatusPublished},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Servable(); got != c.want {
				t.Errorf("Servable() = %v; want %v", got, c.want)
			}
		})
	}
}

func TestServable_SoftDeletedIsNeverServable(t *testing.T) {
	a := &AtomIndex{AtomType: "mcq", CorrectOptionID: "opt-c", Status: StatusPublished}
	if !a.Servable() {
		t.Fatalf("precondition: want a servable atom before the soft delete")
	}
	a.SoftDelete(a.PublishedAt)
	if a.Servable() {
		t.Errorf("Servable() = true after SoftDelete; a soft-deleted atom is never served")
	}
}

// A nil projection is NOT servable rather than a panic. Every caller reaches
// this predicate straight off a repository read, where (nil, nil) is the
// documented "unprojected" answer, so a nil receiver is a reachable input and
// the honest reading of it is "nothing to serve".
func TestServable_NilIsNotServable(t *testing.T) {
	var a *AtomIndex
	if a.Servable() {
		t.Errorf("Servable() = true on a nil projection; want false")
	}
}
