// proofing_ticked_edges_test.go — CHO-2040: the ticked-edge read adapter.
//
// Pins the R8-7 behaviour: with key-at-mint (migration 0074) each TargetEdge
// carries the ConceptNode's minted concept_key, so the runner's EDGES_LACK_KEYS
// gate opens — plus the subtree + intent + soft-delete filtering semantics.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

type fakeNodeLister struct {
	nodes []*conceptgraph.ConceptNode
	err   error
}

func (f *fakeNodeLister) ListByLearner(_ context.Context, _, _ string) ([]*conceptgraph.ConceptNode, error) {
	return f.nodes, f.err
}

type fakeEdgeLister struct {
	edges []*conceptgraph.Edge
	err   error
}

func (f *fakeEdgeLister) ListByLearner(_ context.Context, _, _ string) ([]*conceptgraph.Edge, error) {
	return f.edges, f.err
}

const (
	tickRoot    = "01970000-aaaa-7000-8000-000000000101"
	tickChildA  = "01970000-aaaa-7000-8000-000000000102"
	tickChildB  = "01970000-aaaa-7000-8000-000000000103"
	tickOutside = "01970000-aaaa-7000-8000-000000000104"
)

func tickedFixture() (*fakeNodeLister, *fakeEdgeLister) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	gone := now.Add(time.Hour)
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: tickRoot, Title: "Quadratics", Intent: conceptgraph.IntentUnspecified, CreatedAt: now, UpdatedAt: now},
		{ConceptID: tickChildA, Title: "Roots", ConceptKey: "roots", Intent: conceptgraph.IntentRemediate, CreatedAt: now, UpdatedAt: now},
		{ConceptID: tickChildB, Title: "Completing the square", ConceptKey: "completing-the-square", Intent: conceptgraph.IntentExplore, CreatedAt: now, UpdatedAt: now},
		// Intent-tagged but OUTSIDE the root subtree — must not leak in.
		{ConceptID: tickOutside, Title: "Trigonometry", Intent: conceptgraph.IntentRemediate, CreatedAt: now, UpdatedAt: now},
	}
	// Soft-deleted intent-tagged child — invisible.
	deleted := &conceptgraph.ConceptNode{ConceptID: "01970000-aaaa-7000-8000-000000000105", Title: "Ghost", Intent: conceptgraph.IntentRemediate, CreatedAt: now, UpdatedAt: now, DeletedAt: &gone}
	nodes = append(nodes, deleted)
	edges := []*conceptgraph.Edge{
		{EdgeID: "01970000-bbbb-7000-8000-000000000201", SourceConceptID: tickRoot, TargetConceptID: tickChildA, Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "01970000-bbbb-7000-8000-000000000202", SourceConceptID: tickRoot, TargetConceptID: tickChildB, Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "01970000-bbbb-7000-8000-000000000203", SourceConceptID: tickRoot, TargetConceptID: deleted.ConceptID, Class: conceptgraph.EdgeClassHierarchy},
		// Lateral link to the outside concept — laterals never grow the subtree.
		{EdgeID: "01970000-bbbb-7000-8000-000000000204", SourceConceptID: tickChildA, TargetConceptID: tickOutside, Class: conceptgraph.EdgeClassLateral},
	}
	return &fakeNodeLister{nodes: nodes}, &fakeEdgeLister{edges: edges}
}

func TestTickedEdges_SubtreeIntentAndSoftDeleteFilters(t *testing.T) {
	nodes, edges := tickedFixture()
	r := NewProofingTickedEdgeReader(nodes, edges)
	got, err := r.ListTicked(context.Background(), "t", "g", tickRoot)
	if err != nil {
		t.Fatalf("ListTicked: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("edges = %d, want 2 (subtree ∩ intent-tagged ∩ live): %+v", len(got), got)
	}
	// Deterministic title order.
	if got[0].Title != "Completing the square" || got[1].Title != "Roots" {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].Intent != "explore" || got[1].Intent != "remediate" {
		t.Fatalf("intents wrong: %+v", got)
	}
	// Key-at-mint (migration 0074): the adapter maps ConceptNode.ConceptKey —
	// the minted slug, never forged from titles at read time. Non-empty keys
	// open the runner's EDGES_LACK_KEYS gate.
	if got[0].Key != "completing-the-square" || got[1].Key != "roots" {
		t.Fatalf("keys must be the minted concept_key (title-sorted): %+v", got)
	}
}

func TestTickedEdges_EmptyRootIsEmptyNotError(t *testing.T) {
	nodes, edges := tickedFixture()
	r := NewProofingTickedEdgeReader(nodes, edges)
	got, err := r.ListTicked(context.Background(), "t", "g", "  ")
	if err != nil || len(got) != 0 {
		t.Fatalf("blank root must yield empty (rootless goal), got %v (%v)", got, err)
	}
}

func TestTickedEdges_ListerErrorsSurface(t *testing.T) {
	nodes, edges := tickedFixture()
	nodes.err = errors.New("pg down")
	r := NewProofingTickedEdgeReader(nodes, edges)
	if _, err := r.ListTicked(context.Background(), "t", "g", tickRoot); err == nil {
		t.Fatalf("node lister error must surface")
	}
	nodes.err = nil
	edges.err = errors.New("pg down")
	if _, err := r.ListTicked(context.Background(), "t", "g", tickRoot); err == nil {
		t.Fatalf("edge lister error must surface")
	}
}
