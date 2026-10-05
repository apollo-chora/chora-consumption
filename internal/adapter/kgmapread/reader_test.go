package kgmapread

// reader_test.go — CHO-2014 (map_sight / kg.read_map): the ring-scoped concept
// map assembler over the learner's per-user concept_graph. Tested against fake
// concept ports (no pg): BFS ring distances, dangling-edge fail-soft, learner-
// safe title-only output, and error propagation.

import (
	"context"
	"errors"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

type fakeEdges struct {
	byConcept map[string][]*conceptgraph.Edge
	err       error
}

func (f fakeEdges) ListByConcept(_ context.Context, _, _, conceptID string) ([]*conceptgraph.Edge, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byConcept[conceptID], nil
}

type fakeNodes struct {
	titles map[string]string // conceptID → title; absent ⇒ (nil, nil) live-miss
	err    error
}

func (f fakeNodes) GetByID(_ context.Context, _, _, conceptID string) (*conceptgraph.ConceptNode, error) {
	if f.err != nil {
		return nil, f.err
	}
	title, ok := f.titles[conceptID]
	if !ok {
		return nil, nil
	}
	return &conceptgraph.ConceptNode{ConceptID: conceptID, Title: title}, nil
}

// edgesFrom builds a ListByConcept index from a directed adjacency spec such
// that querying EITHER endpoint returns the edge (mirrors the repo's "source
// OR target" contract).
func edgesFrom(adj map[string][]string) map[string][]*conceptgraph.Edge {
	out := map[string][]*conceptgraph.Edge{}
	for src, tgts := range adj {
		for _, tgt := range tgts {
			e := &conceptgraph.Edge{SourceConceptID: src, TargetConceptID: tgt}
			out[src] = append(out[src], e)
			out[tgt] = append(out[tgt], e)
		}
	}
	return out
}

func titleAt(nodes []MapNode, ring int) map[string]bool {
	set := map[string]bool{}
	for _, n := range nodes {
		if n.Ring == ring {
			set[n.Title] = true
		}
	}
	return set
}

func TestReader_OneRingReturnsCentreAndAdjacent(t *testing.T) {
	r := NewReader(
		fakeNodes{titles: map[string]string{"c": "Centre", "a": "Alpha", "b": "Beta", "z": "Zeta"}},
		fakeEdges{byConcept: edgesFrom(map[string][]string{"c": {"a", "b"}, "a": {"z"}})},
	)
	got, err := r.ReadRingMap(context.Background(), "t", "g", "c", 1)
	if err != nil {
		t.Fatalf("ReadRingMap: %v", err)
	}
	if r0 := titleAt(got, 0); len(r0) != 1 || !r0["Centre"] {
		t.Fatalf("ring0 = %v want {Centre}", r0)
	}
	r1 := titleAt(got, 1)
	if len(r1) != 2 || !r1["Alpha"] || !r1["Beta"] {
		t.Fatalf("ring1 = %v want {Alpha,Beta}", r1)
	}
	// Zeta is two hops out — must NOT appear at 1 ring.
	for _, n := range got {
		if n.Title == "Zeta" {
			t.Fatal("Zeta (2-hop) leaked into a 1-ring scan")
		}
		if n.IsCentre != (n.Ring == 0) {
			t.Fatalf("IsCentre/ring mismatch: %+v", n)
		}
	}
}

func TestReader_TwoRingsReachTwoHop(t *testing.T) {
	r := NewReader(
		fakeNodes{titles: map[string]string{"c": "Centre", "a": "Alpha", "z": "Zeta"}},
		fakeEdges{byConcept: edgesFrom(map[string][]string{"c": {"a"}, "a": {"z"}})},
	)
	got, err := r.ReadRingMap(context.Background(), "t", "g", "c", 2)
	if err != nil {
		t.Fatalf("ReadRingMap: %v", err)
	}
	if r2 := titleAt(got, 2); len(r2) != 1 || !r2["Zeta"] {
		t.Fatalf("ring2 = %v want {Zeta}", r2)
	}
}

func TestReader_DanglingEdgeIsFailSoft(t *testing.T) {
	// "b" is adjacent by edge but has no ConceptNode (pruned) → skipped, no error.
	r := NewReader(
		fakeNodes{titles: map[string]string{"c": "Centre", "a": "Alpha"}},
		fakeEdges{byConcept: edgesFrom(map[string][]string{"c": {"a", "b"}})},
	)
	got, err := r.ReadRingMap(context.Background(), "t", "g", "c", 1)
	if err != nil {
		t.Fatalf("ReadRingMap: %v", err)
	}
	for _, n := range got {
		if n.Title == "" {
			t.Fatalf("empty-title node leaked (dangling edge not skipped): %+v", got)
		}
	}
	if len(got) != 2 { // Centre + Alpha (b dropped)
		t.Fatalf("dangling edge not skipped: %+v", got)
	}
}

func TestReader_EdgeErrorPropagates(t *testing.T) {
	r := NewReader(
		fakeNodes{titles: map[string]string{"c": "Centre"}},
		fakeEdges{err: errors.New("edge repo down")},
	)
	if _, err := r.ReadRingMap(context.Background(), "t", "g", "c", 1); err == nil {
		t.Fatal("edge repo error must propagate")
	}
}

func TestReader_NodeErrorPropagates(t *testing.T) {
	r := NewReader(
		fakeNodes{err: errors.New("node repo down")},
		fakeEdges{byConcept: edgesFrom(map[string][]string{"c": {"a"}})},
	)
	if _, err := r.ReadRingMap(context.Background(), "t", "g", "c", 1); err == nil {
		t.Fatal("node repo error must propagate")
	}
}
