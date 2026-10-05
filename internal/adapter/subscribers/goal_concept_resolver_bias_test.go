// goal_concept_resolver_bias_test.go - ADR-238 D2: the concept drawer is a SOFT
// entry. These tests pin the resolver half of that decision: which candidates
// get marked Preferred, and - more importantly - that marking one NEVER changes
// which candidates exist. The matcher half is in concept_match_test.go.
package subscribers

import (
	"context"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// deepMapFixture is a 4-node map: root -> branch -> leaf, plus a sibling under
// root. Sub-tree under "branch" = {branch, leaf}, which is what an entry hint on
// "branch" must mark.
//
//	root
//	├── branch
//	│   └── leaf
//	└── sibling
func deepMapFixture() (*goal.Goal, *stubNodeLister, *stubEdgeLister) {
	g := &goal.Goal{RootConceptID: gcrRootPtr("root")}
	nodes := &stubNodeLister{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: "root", Title: "Scrum", ConceptKey: "scrum"},
		{ConceptID: "branch", Title: "Scrum Artifacts", ConceptKey: "scrum-artifacts"},
		{ConceptID: "leaf", Title: "Sprint Backlog", ConceptKey: "sprint-backlog"},
		{ConceptID: "sibling", Title: "Scrum Roles", ConceptKey: "scrum-roles"},
	}}
	edges := &stubEdgeLister{edges: []*conceptgraph.Edge{
		{EdgeID: "e1", SourceConceptID: "root", TargetConceptID: "branch", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e2", SourceConceptID: "branch", TargetConceptID: "leaf", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e3", SourceConceptID: "root", TargetConceptID: "sibling", Class: conceptgraph.EdgeClassHierarchy},
	}}
	return g, nodes, edges
}

// preferredIDs returns the ids the resolver marked Preferred, for assertion.
func preferredIDs(cands []ConceptCandidate) map[string]bool {
	out := map[string]bool{}
	for _, c := range cands {
		if c.Preferred {
			out[c.ConceptID] = true
		}
	}
	return out
}

func resolveWithHint(t *testing.T, hint string) []ConceptCandidate {
	t.Helper()
	g, nodes, edges := deepMapFixture()
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	scope, err := NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", hint)
	if err != nil {
		t.Fatalf("Resolve(hint=%q): %v", hint, err)
	}
	return scope.Candidates
}

func TestGoalConceptResolver_HintMarksItsOwnSubtree(t *testing.T) {
	// The hint concept AND its descendants are Preferred; its parent + siblings
	// are not. SubtreeConceptIDs includes the root of the walk, so "branch"
	// itself must be marked - a hint that did not prefer the very node the
	// learner opened would be a strange kind of emphasis.
	got := preferredIDs(resolveWithHint(t, "branch"))
	want := map[string]bool{"branch": true, "leaf": true}
	if len(got) != len(want) {
		t.Fatalf("preferred = %v; want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("concept %q not marked Preferred; want the entry sub-tree marked", id)
		}
	}
}

func TestGoalConceptResolver_HintNeverChangesTheCandidateSet(t *testing.T) {
	// THE load-bearing resolver guard. D2 is emphasis, not scope: the same four
	// concepts must be offered whether or not a hint was given. If a hint ever
	// shrinks this set it has become the hard filter the ADR rejects.
	none := resolveWithHint(t, "")
	hinted := resolveWithHint(t, "branch")
	if len(none) != len(hinted) {
		t.Fatalf("candidate count with hint = %d, without = %d; a hint must not change the candidate set", len(hinted), len(none))
	}
	for i := range none {
		if none[i].ConceptID != hinted[i].ConceptID {
			t.Errorf("candidate %d = %q with hint, %q without; the set and its order must be identical", i, hinted[i].ConceptID, none[i].ConceptID)
		}
	}
}

func TestGoalConceptResolver_NoHintPrefersNothing(t *testing.T) {
	// A goal-level "Diagnose my map" upload carries no hint, so nothing is
	// preferred and ranking stays unbiased across the whole map.
	if got := preferredIDs(resolveWithHint(t, "")); len(got) != 0 {
		t.Fatalf("preferred with no hint = %v; want none", got)
	}
}

func TestGoalConceptResolver_UnknownHintPrefersNothing(t *testing.T) {
	// A hint naming a concept that is not on this map (deleted between upload and
	// ingest, or another learner's node) is inert - never an error, never a drop.
	if got := preferredIDs(resolveWithHint(t, "does-not-exist")); len(got) != 0 {
		t.Fatalf("preferred with an unknown hint = %v; want none (inert, not an error)", got)
	}
}

func TestGoalConceptResolver_HintOutsideGoalPrefersNothingButKeepsCandidates(t *testing.T) {
	// A hint pointing outside the goal's sub-tree cannot smuggle an off-goal
	// concept into the candidate set: the goal still BOUNDS, the hint only BIASES.
	g, nodes, edges := deepMapFixture()
	nodes.nodes = append(nodes.nodes, &conceptgraph.ConceptNode{
		ConceptID: "faraway", Title: "Unrelated Subject", ConceptKey: "unrelated-subject",
	})
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	scope, err := NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", "faraway")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, c := range scope.Candidates {
		if c.ConceptID == "faraway" {
			t.Fatal("an off-goal hint pulled its own concept into the candidate set; the goal must still bound resolution")
		}
		if c.Preferred {
			t.Errorf("concept %q marked Preferred from an off-goal hint", c.ConceptID)
		}
	}
	if len(scope.Candidates) != 4 {
		t.Fatalf("got %d candidates; want the goal's 4 regardless of the stray hint", len(scope.Candidates))
	}
}

func TestGoalConceptResolver_HintOnRootPrefersWholeMap(t *testing.T) {
	// Entering from the goal's own root concept prefers everything, which is the
	// same as preferring nothing - a degenerate but harmless case worth pinning
	// so nobody "optimises" it into a filter.
	got := preferredIDs(resolveWithHint(t, "root"))
	if len(got) != 4 {
		t.Fatalf("preferred from the root hint = %v; want all 4 concepts", got)
	}
}
