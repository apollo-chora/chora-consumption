package atomrefresh_test

import (
	"testing"

	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
)

func cand(learner, goal, focal, title, theme string) atomrefresh.Candidate {
	return atomrefresh.Candidate{
		LearnerGCID:    learner,
		GoalID:         goal,
		FocalConceptID: focal,
		FocalTitle:     title,
		Theme:          theme,
	}
}

func TestRankCandidates_DropsZeroScoreAndOrdersByAffinity(t *testing.T) {
	atom := atomrefresh.AtomSignal{
		Title: "Refactoring legacy code with unit tests",
		Tags:  []string{"refactoring", "testing"},
	}
	in := []atomrefresh.Candidate{
		cand("l1", "g1", "f-baking", "Sourdough Baking", "Cooking"),
		cand("l1", "g1", "f-refactor", "Refactoring Patterns", "Software Design"),
		cand("l2", "g2", "f-testing", "Unit Testing", "Software Design"),
	}
	got := atomrefresh.RankCandidates(atom, in)
	if len(got) != 2 {
		t.Fatalf("expected the off-topic candidate dropped, got %d results: %+v", len(got), got)
	}
	for _, g := range got {
		if g.FocalConceptID == "f-baking" {
			t.Fatalf("zero-affinity candidate must be dropped: %+v", got)
		}
	}
}

func TestRankCandidates_TitleOverlapOutranksThemeOnlyOverlap(t *testing.T) {
	atom := atomrefresh.AtomSignal{Title: "Dependency Injection", Tags: nil}
	in := []atomrefresh.Candidate{
		cand("l1", "g1", "f-theme-only", "Observer Pattern", "Dependency Injection"),
		cand("l1", "g1", "f-title-hit", "Dependency Injection Basics", "Architecture"),
	}
	got := atomrefresh.RankCandidates(atom, in)
	if len(got) != 2 {
		t.Fatalf("both candidates overlap, got %d: %+v", len(got), got)
	}
	if got[0].FocalConceptID != "f-title-hit" {
		t.Fatalf("focal-title overlap must outrank theme-only overlap, got order %+v", got)
	}
}

func TestRankCandidates_TagsCountTowardAffinity(t *testing.T) {
	atom := atomrefresh.AtomSignal{Title: "Untitled drill", Tags: []string{"polymorphism"}}
	in := []atomrefresh.Candidate{
		cand("l1", "g1", "f-poly", "Polymorphism", "Software Design"),
	}
	got := atomrefresh.RankCandidates(atom, in)
	if len(got) != 1 {
		t.Fatalf("tag overlap with the focal title must score, got %+v", got)
	}
}

func TestRankCandidates_ShortAndStopTokensDoNotScore(t *testing.T) {
	atom := atomrefresh.AtomSignal{Title: "The art of it", Tags: nil}
	in := []atomrefresh.Candidate{
		cand("l1", "g1", "f-x", "The state of it", "For the win"),
	}
	if got := atomrefresh.RankCandidates(atom, in); len(got) != 0 {
		t.Fatalf("stop/short tokens alone must not create affinity: %+v", got)
	}
}

func TestRankCandidates_DeterministicTieBreak(t *testing.T) {
	atom := atomrefresh.AtomSignal{Title: "Recursion", Tags: nil}
	in := []atomrefresh.Candidate{
		cand("l2", "g2", "f-b", "Recursion Drills", "Math"),
		cand("l1", "g1", "f-a", "Recursion Basics", "Math"),
	}
	first := atomrefresh.RankCandidates(atom, in)
	second := atomrefresh.RankCandidates(atom, []atomrefresh.Candidate{in[1], in[0]})
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected both ranked: %+v / %+v", first, second)
	}
	if first[0].FocalConceptID != second[0].FocalConceptID || first[1].FocalConceptID != second[1].FocalConceptID {
		t.Fatalf("tie-break must be input-order independent: %+v vs %+v", first, second)
	}
}
