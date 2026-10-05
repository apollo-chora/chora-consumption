package conceptgraph

import "testing"

// ADR-247 D3: WalkAncestors gives a focal node its place in the goal hierarchy,
// walking the hierarchy parent chain (target->source) from the focal node up to
// the root, collecting ancestor titles nearest-first (excluding the focal node
// and the root) plus the goal (root concept) title.
func node(id, title string) *ConceptNode { return &ConceptNode{ConceptID: id, Title: title} }
func hedge(parent, child string) *Edge {
	return &Edge{SourceConceptID: parent, TargetConceptID: child, Class: EdgeClassHierarchy}
}

func TestWalkAncestors_ChainNearestFirstExcludesFocalAndRoot(t *testing.T) {
	// root -> a -> b -> focal
	nodes := []*ConceptNode{node("root", "Calculus"), node("a", "Integration"), node("b", "Definite Integrals"), node("focal", "u-substitution")}
	edges := []*Edge{hedge("root", "a"), hedge("a", "b"), hedge("b", "focal")}
	got := WalkAncestors("focal", "root", nodes, edges)
	if got.GoalTitle != "Calculus" {
		t.Errorf("GoalTitle = %q; want the root concept title Calculus", got.GoalTitle)
	}
	want := []string{"Definite Integrals", "Integration"} // nearest parent first, root excluded
	if len(got.Ancestors) != len(want) {
		t.Fatalf("Ancestors = %v; want %v", got.Ancestors, want)
	}
	for i := range want {
		if got.Ancestors[i] != want[i] {
			t.Errorf("Ancestors[%d] = %q; want %q", i, got.Ancestors[i], want[i])
		}
	}
}

func TestWalkAncestors_DirectChildOfRootHasNoAncestors(t *testing.T) {
	nodes := []*ConceptNode{node("root", "Calculus"), node("focal", "Limits")}
	edges := []*Edge{hedge("root", "focal")}
	got := WalkAncestors("focal", "root", nodes, edges)
	if got.GoalTitle != "Calculus" {
		t.Errorf("GoalTitle = %q; want Calculus", got.GoalTitle)
	}
	if len(got.Ancestors) != 0 {
		t.Errorf("Ancestors = %v; want empty (root is excluded, not an ancestor)", got.Ancestors)
	}
}

func TestWalkAncestors_IgnoresLateralAndDeletedEdges(t *testing.T) {
	nodes := []*ConceptNode{node("root", "Calculus"), node("a", "Integration"), node("focal", "u-substitution"), node("side", "Trigonometry")}
	lateral := &Edge{SourceConceptID: "side", TargetConceptID: "focal", Class: EdgeClassLateral}
	del := hedge("a", "focal")
	del.DeletedAt = &cNow
	// The only LIVE hierarchy parent chain is root -> a, and a -> focal is deleted,
	// so focal has no live parent: no ancestors, goal title still resolves.
	edges := []*Edge{hedge("root", "a"), lateral, del}
	got := WalkAncestors("focal", "root", nodes, edges)
	if len(got.Ancestors) != 0 {
		t.Errorf("Ancestors = %v; want empty (lateral + deleted edges ignored)", got.Ancestors)
	}
	if got.GoalTitle != "Calculus" {
		t.Errorf("GoalTitle = %q; want Calculus", got.GoalTitle)
	}
}

func TestWalkAncestors_CycleGuardTerminates(t *testing.T) {
	// Malformed: focal -> x -> focal (a cycle among non-root nodes).
	nodes := []*ConceptNode{node("root", "Calculus"), node("focal", "A"), node("x", "B")}
	edges := []*Edge{hedge("focal", "x"), hedge("x", "focal")}
	got := WalkAncestors("focal", "root", nodes, edges) // must terminate, not hang
	if len(got.Ancestors) > 2 {
		t.Errorf("cycle guard failed: Ancestors = %v", got.Ancestors)
	}
}

func TestWalkAncestors_MissingRootNodeYieldsEmptyGoalTitle(t *testing.T) {
	nodes := []*ConceptNode{node("a", "Integration"), node("focal", "u-substitution")}
	edges := []*Edge{hedge("a", "focal")}
	got := WalkAncestors("focal", "root", nodes, edges)
	if got.GoalTitle != "" {
		t.Errorf("GoalTitle = %q; want empty when the root node is absent", got.GoalTitle)
	}
	if len(got.Ancestors) != 1 || got.Ancestors[0] != "Integration" {
		t.Errorf("Ancestors = %v; want [Integration]", got.Ancestors)
	}
}
