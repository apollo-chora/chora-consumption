// subtree_test.go — WS-A2 (My Knowledge unification, CHO-2005): the pure
// hierarchy-subtree traversal that scopes a map to its Goal.RootConceptID
// (ADR-214 D1 "the goal's scope = the sub-tree under that root"). Downward
// hierarchy walk (source=parent -> target=child); lateral edges NOT traversed;
// live-only; cycle-guarded.
package conceptgraph

import (
	"sort"
	"testing"
	"time"
)

// cn builds a live ConceptNode with the given id.
func cn(id string) ConceptNode {
	return ConceptNode{ConceptID: id, TenantID: "t", LearnerGCID: "g", Title: id}
}

// he builds a live hierarchy edge parent -> child (source=parent, target=child).
func he(parent, child string) Edge {
	return Edge{EdgeID: parent + "->" + child, TenantID: "t", LearnerGCID: "g",
		SourceConceptID: parent, TargetConceptID: child, Class: EdgeClassHierarchy}
}

// le builds a live lateral (relates-to) edge a -> b.
func le(a, b string) Edge {
	return Edge{EdgeID: a + "~" + b, TenantID: "t", LearnerGCID: "g",
		SourceConceptID: a, TargetConceptID: b, Class: EdgeClassLateral}
}

// keys returns the sorted membership of a set for stable comparison.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eqKeys(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	sort.Strings(want)
	g := keys(got)
	if len(g) != len(want) {
		t.Fatalf("subtree = %v; want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("subtree = %v; want %v", g, want)
		}
	}
}

func TestSubtree_LinearChain(t *testing.T) {
	concepts := []ConceptNode{cn("R"), cn("A"), cn("B"), cn("C")}
	edges := []Edge{he("R", "A"), he("A", "B"), he("B", "C")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A", "B", "C")
}

func TestSubtree_LateralNotTraversed(t *testing.T) {
	// R relates-to X (lateral). X must NOT enter the map (cross-tree junction).
	concepts := []ConceptNode{cn("R"), cn("X")}
	edges := []Edge{le("R", "X")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R")
}

func TestSubtree_Diamond_NoDuplicate(t *testing.T) {
	// R->A, R->B, A->C, B->C : C reachable two ways, counted once.
	concepts := []ConceptNode{cn("R"), cn("A"), cn("B"), cn("C")}
	edges := []Edge{he("R", "A"), he("R", "B"), he("A", "C"), he("B", "C")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A", "B", "C")
}

func TestSubtree_SoftDeletedChildAndDescendantsExcluded(t *testing.T) {
	// R->A->C ; C is soft-deleted -> C excluded (and any of its descendants).
	del := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	cC := cn("C")
	cC.DeletedAt = &del
	concepts := []ConceptNode{cn("R"), cn("A"), cC, cn("D")}
	edges := []Edge{he("R", "A"), he("A", "C"), he("C", "D")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A")
}

func TestSubtree_SoftDeletedHierarchyEdgeNotTraversed(t *testing.T) {
	del := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	e := he("R", "A")
	e.DeletedAt = &del
	concepts := []ConceptNode{cn("R"), cn("A")}
	eqKeys(t, SubtreeConceptIDs(concepts, []Edge{e}, "R"), "R")
}

func TestSubtree_SoftDeletedRootEmpty(t *testing.T) {
	del := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	cR := cn("R")
	cR.DeletedAt = &del
	concepts := []ConceptNode{cR, cn("A")}
	edges := []Edge{he("R", "A")}
	if got := SubtreeConceptIDs(concepts, edges, "R"); len(got) != 0 {
		t.Fatalf("deleted root subtree = %v; want empty", keys(got))
	}
}

func TestSubtree_UnknownAndBlankRootEmpty(t *testing.T) {
	concepts := []ConceptNode{cn("R"), cn("A")}
	edges := []Edge{he("R", "A")}
	if got := SubtreeConceptIDs(concepts, edges, "ZZ"); len(got) != 0 {
		t.Fatalf("unknown root subtree = %v; want empty", keys(got))
	}
	if got := SubtreeConceptIDs(concepts, edges, "  "); len(got) != 0 {
		t.Fatalf("blank root subtree = %v; want empty", keys(got))
	}
}

func TestSubtree_CycleGuarded(t *testing.T) {
	// Corrupt data: R->A and A->R. Must terminate and return the reachable set.
	concepts := []ConceptNode{cn("R"), cn("A")}
	edges := []Edge{he("R", "A"), he("A", "R")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A")
}

func TestSubtree_SeparateTreeExcluded(t *testing.T) {
	// Two disjoint hierarchies; rooting at R must not reach X's tree.
	concepts := []ConceptNode{cn("R"), cn("A"), cn("X"), cn("Y")}
	edges := []Edge{he("R", "A"), he("X", "Y")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A")
}

func TestSubtree_IsolatedRoot(t *testing.T) {
	concepts := []ConceptNode{cn("R")}
	eqKeys(t, SubtreeConceptIDs(concepts, nil, "R"), "R")
}

func TestSubtree_IntraTreeLateralDoesNotExpand(t *testing.T) {
	// A lateral edge fully inside the subtree changes nothing about membership.
	concepts := []ConceptNode{cn("R"), cn("A"), cn("B")}
	edges := []Edge{he("R", "A"), he("R", "B"), le("A", "B")}
	eqKeys(t, SubtreeConceptIDs(concepts, edges, "R"), "R", "A", "B")
}
