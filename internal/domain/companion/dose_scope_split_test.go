// dose_scope_split_test.go - ADR-242 D2 (owner-ruled 2026-08-17): the composed
// dose carries a THREE-WAY served-card split (from this goal / on its topics /
// broader), computed by the composer so the adapter never re-derives affinity.
package companion

import "testing"

func splitScope() *GoalScopeInput {
	return &GoalScopeInput{
		GoalID:       "goal-1",
		AtomIDs:      []string{"atom-bound-1", "atom-bound-2"},
		ConceptSlugs: []string{"Software Design"},
	}
}

func TestDoseScope_ThreeWaySplitOverServedCards(t *testing.T) {
	entries := []DailyDoseEntry{
		{AtomID: "atom-bound-1", Topic: "anything"},
		{AtomID: "atom-topic-1", Topic: "software-design"},
		{AtomID: "atom-rest-1", Topic: "baking"},
		{AtomID: "atom-bound-2", Topic: ""},
		{AtomID: "atom-rest-2", Topic: ""},
	}
	s := splitScope().scopeOf(entries)
	if s == nil {
		t.Fatal("a requested goal must yield a scope split")
	}
	if s.GoalID != "goal-1" {
		t.Errorf("GoalID = %q", s.GoalID)
	}
	if s.FromGoal != 2 || s.OnTopics != 1 || s.Broader != 2 {
		t.Fatalf("split = %d/%d/%d, want 2/1/2", s.FromGoal, s.OnTopics, s.Broader)
	}
	if got := s.FromGoal + s.OnTopics + s.Broader; got != len(entries) {
		t.Fatalf("split must sum to the served count: %d != %d", got, len(entries))
	}
}

func TestDoseScope_NilScopeYieldsNil(t *testing.T) {
	var g *GoalScopeInput
	if s := g.scopeOf([]DailyDoseEntry{{AtomID: "a"}}); s != nil {
		t.Fatalf("no goal requested must mean no scope block (D5), got %+v", s)
	}
}

func TestDoseScope_EmptyMaterialGoalReportsZeros(t *testing.T) {
	g := &GoalScopeInput{GoalID: "goal-empty"}
	s := g.scopeOf([]DailyDoseEntry{{AtomID: "a", Topic: "x"}, {AtomID: "b", Topic: "y"}})
	if s == nil {
		t.Fatal("an empty-material goal still discloses (0 from this goal is the ADR's required statement)")
	}
	if s.FromGoal != 0 || s.OnTopics != 0 || s.Broader != 2 {
		t.Fatalf("split = %d/%d/%d, want 0/0/2", s.FromGoal, s.OnTopics, s.Broader)
	}
}

func TestComposeDailyDose_CarriesScopeOnlyWhenGoalRequested(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-bound-1", Topic: "software-design", Title: "t1"},
		{AtomID: "atom-rest-1", Topic: "baking", Title: "t2"},
		{AtomID: "atom-rest-2", Topic: "gardening", Title: "t3"},
	}
	unscoped := ComposeDailyDose(DailyDoseInput{Seeds: seeds})
	if unscoped.Scope != nil {
		t.Fatalf("unscoped dose must carry no scope block (D5), got %+v", unscoped.Scope)
	}
	scoped := ComposeDailyDose(DailyDoseInput{
		Seeds:     seeds,
		GoalScope: &GoalScopeInput{GoalID: "goal-1", AtomIDs: []string{"atom-bound-1"}},
	})
	if scoped.Scope == nil {
		t.Fatal("goal-scoped dose must carry the D2 scope block")
	}
	if scoped.Scope.FromGoal < 1 {
		t.Fatalf("the bound atom is servable and must be counted from-goal: %+v", scoped.Scope)
	}
	if sum := scoped.Scope.FromGoal + scoped.Scope.OnTopics + scoped.Scope.Broader; sum != len(scoped.Entries) {
		t.Fatalf("split must sum to served cards: %d != %d", sum, len(scoped.Entries))
	}
}

func TestComposeDailyDose_FocusedPathCarriesScopeToo(t *testing.T) {
	seeds := []AtomSeed{
		{AtomID: "atom-bound-1", Topic: "software-design", Title: "t1"},
		{AtomID: "atom-focus-1", Topic: "off-goal", Title: "t2"},
	}
	out := ComposeDailyDose(DailyDoseInput{
		Seeds:       seeds,
		FocusEdgeID: "edge-1",
		GoalScope:   &GoalScopeInput{GoalID: "goal-1", AtomIDs: []string{"atom-bound-1"}},
	})
	if out.Scope == nil {
		t.Fatal("the focused-drill return path must carry the scope block too (ADR-242 D6 collision disclosure)")
	}
	if sum := out.Scope.FromGoal + out.Scope.OnTopics + out.Scope.Broader; sum != len(out.Entries) {
		t.Fatalf("split must sum to served cards: %d != %d", sum, len(out.Entries))
	}
}
