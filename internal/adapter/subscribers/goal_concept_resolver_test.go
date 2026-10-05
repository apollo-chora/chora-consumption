// goal_concept_resolver_test.go — ADR-238 M-D2. Tests the resolver adapter that
// turns an upload's goal into the on-map ConceptCandidates the matcher ranks:
// GetByID(goal) → SubtreeConceptIDs(root) → embed each in-subtree node Title once.
// Driven by stub goal/node/edge/embedder collaborators (no live DB).
package subscribers

import (
	"context"
	"errors"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// --- stubs ---

type stubGoalReader struct {
	g                             *goal.Goal
	err                           error
	gotTenant, gotGCID, gotGoalID string
	calls                         int
}

func (s *stubGoalReader) GetByID(_ context.Context, tenantID, gcid, goalID string) (*goal.Goal, error) {
	s.calls++
	s.gotTenant, s.gotGCID, s.gotGoalID = tenantID, gcid, goalID
	return s.g, s.err
}

type stubNodeLister struct {
	nodes []*conceptgraph.ConceptNode
	err   error
}

func (s *stubNodeLister) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.nodes, s.err
}

type stubEdgeLister struct {
	edges []*conceptgraph.Edge
	err   error
}

func (s *stubEdgeLister) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.edges, s.err
}

type stubEmbedderByText struct {
	byText map[string][]float32
	def    []float32
	calls  []string
	err    error
}

func (e *stubEmbedderByText) Embed(_ context.Context, text, _ string) ([]float32, error) {
	e.calls = append(e.calls, text)
	if e.err != nil {
		return nil, e.err
	}
	if v, ok := e.byText[text]; ok {
		return v, nil
	}
	return e.def, nil
}

func gcrRootPtr(s string) *string { return &s }

// rootedGoal + a small 3-node/1-edge map: root -> child (hierarchy), off is
// disconnected. Sub-tree under root = {root, child}; off is excluded.
func mapFixture() (*goal.Goal, *stubNodeLister, *stubEdgeLister) {
	g := &goal.Goal{RootConceptID: gcrRootPtr("root")}
	nodes := &stubNodeLister{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: "root", Title: "Root Concept", ConceptKey: "root-concept"},
		{ConceptID: "child", Title: "Child Concept", ConceptKey: "child-concept"},
		{ConceptID: "off", Title: "Off Tree", ConceptKey: "off-tree"},
	}}
	edges := &stubEdgeLister{edges: []*conceptgraph.Edge{
		{EdgeID: "e1", SourceConceptID: "root", TargetConceptID: "child", Class: conceptgraph.EdgeClassHierarchy},
	}}
	return g, nodes, edges
}

func TestGoalConceptResolver_BuildsSubtreeCandidates(t *testing.T) {
	g, nodes, edges := mapFixture()
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"Root Concept":  {1, 0, 0},
		"Child Concept": {0, 1, 0},
	}, def: []float32{9, 9, 9}}
	goals := &stubGoalReader{g: g}

	got, err := candidatesOf(NewGoalConceptResolver(goals, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", ""))
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates; want 2 (root+child, off excluded): %+v", len(got), got)
	}
	// Order follows the node lister; root first.
	if got[0].ConceptID != "root" || got[0].ConceptKey != "root-concept" {
		t.Errorf("candidate[0] = %+v; want root/root-concept", got[0])
	}
	if len(got[0].Embedding) != 3 || got[0].Embedding[0] != 1 {
		t.Errorf("candidate[0] embedding not the Title embedding: %v", got[0].Embedding)
	}
	if got[1].ConceptID != "child" || got[1].ConceptKey != "child-concept" {
		t.Errorf("candidate[1] = %+v; want child/child-concept", got[1])
	}
	// off-tree node was NOT embedded (only in-subtree titles embed).
	for _, c := range emb.calls {
		if c == "Off Tree" {
			t.Errorf("off-tree node must not be embedded; calls=%v", emb.calls)
		}
	}
	if len(emb.calls) != 2 {
		t.Errorf("embedder called %d times; want 2 (once per in-subtree node): %v", len(emb.calls), emb.calls)
	}
	// tenant/gcid/goal threaded into the goal read.
	if goals.gotTenant != "tnt-1" || goals.gotGCID != "gcid-1" || goals.gotGoalID != "goal-1" {
		t.Errorf("goal read args = %q/%q/%q", goals.gotTenant, goals.gotGCID, goals.gotGoalID)
	}
}

func TestGoalConceptResolver_UnrootedGoalNoCandidates(t *testing.T) {
	_, nodes, edges := mapFixture()
	emb := &stubEmbedderByText{def: []float32{1}}
	goals := &stubGoalReader{g: &goal.Goal{RootConceptID: nil}} // no anchor

	got, err := candidatesOf(NewGoalConceptResolver(goals, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", ""))
	if err != nil {
		t.Fatalf("unrooted goal must not error: %v", err)
	}
	if got != nil {
		t.Errorf("unrooted goal must yield nil candidates; got %+v", got)
	}
	if len(emb.calls) != 0 {
		t.Errorf("unrooted goal must not embed anything; calls=%v", emb.calls)
	}
}

func TestGoalConceptResolver_BlankRootNoCandidates(t *testing.T) {
	_, nodes, edges := mapFixture()
	goals := &stubGoalReader{g: &goal.Goal{RootConceptID: gcrRootPtr("   ")}}
	got, err := candidatesOf(NewGoalConceptResolver(goals, nodes, edges, &stubEmbedderByText{def: []float32{1}}).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", ""))
	if err != nil || got != nil {
		t.Fatalf("blank-root goal = (%+v,%v); want (nil,nil)", got, err)
	}
}

func TestGoalConceptResolver_GoalNotFoundNoCandidates(t *testing.T) {
	_, nodes, edges := mapFixture()
	goals := &stubGoalReader{g: nil} // GetByID → (nil,nil) = absent
	got, err := candidatesOf(NewGoalConceptResolver(goals, nodes, edges, &stubEmbedderByText{def: []float32{1}}).
		Resolve(context.Background(), "tnt-1", "gcid-1", "missing", ""))
	if err != nil || got != nil {
		t.Fatalf("absent goal = (%+v,%v); want (nil,nil)", got, err)
	}
}

func TestGoalConceptResolver_SkipsEmptyTitleNode(t *testing.T) {
	g := &goal.Goal{RootConceptID: gcrRootPtr("root")}
	nodes := &stubNodeLister{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: "root", Title: "Root Concept", ConceptKey: "root-concept"},
		{ConceptID: "child", Title: "   ", ConceptKey: "blank"}, // in-subtree but untitled
	}}
	edges := &stubEdgeLister{edges: []*conceptgraph.Edge{
		{EdgeID: "e1", SourceConceptID: "root", TargetConceptID: "child", Class: conceptgraph.EdgeClassHierarchy},
	}}
	emb := &stubEmbedderByText{byText: map[string][]float32{"Root Concept": {1, 0, 0}}, def: []float32{2}}

	got, err := candidatesOf(NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", ""))
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(got) != 1 || got[0].ConceptID != "root" {
		t.Errorf("blank-title node must be skipped; got %+v", got)
	}
	if len(emb.calls) != 1 {
		t.Errorf("only the titled node embeds; calls=%v", emb.calls)
	}
}

func TestGoalConceptResolver_GoalReadErrorPropagates(t *testing.T) {
	_, nodes, edges := mapFixture()
	goals := &stubGoalReader{err: errors.New("goal boom")}
	if _, err := candidatesOf(NewGoalConceptResolver(goals, nodes, edges, &stubEmbedderByText{def: []float32{1}}).
		Resolve(context.Background(), "t", "g", "goal-1", "")); err == nil {
		t.Fatal("goal read error must propagate")
	}
}

func TestGoalConceptResolver_NodeListErrorPropagates(t *testing.T) {
	g, _, edges := mapFixture()
	nodes := &stubNodeLister{err: errors.New("nodes boom")}
	if _, err := candidatesOf(NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, &stubEmbedderByText{def: []float32{1}}).
		Resolve(context.Background(), "t", "g", "goal-1", "")); err == nil {
		t.Fatal("node list error must propagate")
	}
}

func TestGoalConceptResolver_EdgeListErrorPropagates(t *testing.T) {
	g, nodes, _ := mapFixture()
	edges := &stubEdgeLister{err: errors.New("edges boom")}
	if _, err := candidatesOf(NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, &stubEmbedderByText{def: []float32{1}}).
		Resolve(context.Background(), "t", "g", "goal-1", "")); err == nil {
		t.Fatal("edge list error must propagate")
	}
}

func TestGoalConceptResolver_EmbedErrorPropagates(t *testing.T) {
	g, nodes, edges := mapFixture()
	emb := &stubEmbedderByText{err: errors.New("vertex down")}
	if _, err := candidatesOf(NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, emb).
		Resolve(context.Background(), "t", "g", "goal-1", "")); err == nil {
		t.Fatal("embed error must propagate")
	}
}

// Compile-time: the concrete resolver satisfies the port the subscriber holds.
var _ GoalConceptResolver = NewGoalConceptResolver(nil, nil, nil, nil)

// candidatesOf keeps the candidate-focused tests above on their original shape
// after Resolve widened to GoalScope; the ROOT it now also returns has its own
// test below.
func candidatesOf(sc GoalScope, err error) ([]ConceptCandidate, error) {
	return sc.Candidates, err
}

func TestGoalConceptResolver_Resolve_ReturnsTheGoalRoot(t *testing.T) {
	// ADR-238 D4 anchors an unmatched edge's suggestion at the goal ROOT, since a
	// whole-map suggestion is dropped by the goal fence on every read. So the
	// root has to come back from the same resolve, not a second goal load.
	g, nodes, edges := mapFixture()
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	sc, err := NewGoalConceptResolver(&stubGoalReader{g: g}, nodes, edges, emb).
		Resolve(context.Background(), "tnt-1", "gcid-1", "goal-1", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sc.RootConceptID != *g.RootConceptID {
		t.Fatalf("RootConceptID = %q; want %q", sc.RootConceptID, *g.RootConceptID)
	}
}
