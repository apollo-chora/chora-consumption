// Package kgmapread assembles the ring-scoped concept map that backs the
// kg.read_map agent tool (map_sight, CHO-2014).
//
// It reuses knowledge_graph_read.RingScan (the distance-preserving BFS) over the
// learner's OWN per-user concept_graph adjacency, seeded at the Companion's
// resonant concept, out to a stage-gated ring reach. Output is LEARNER-SAFE:
// concept TITLES + ring distance only — never the concept UUIDs, edge ids, or
// atom refs (no-UUID-leak discipline; the Companion narrates by name, not id).
//
// Hexagonal: depends only on narrow read ports (satisfied by the pg concept
// repos) + the domain BFS; it holds no infrastructure itself.
package kgmapread

import (
	"context"
	"fmt"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	kgr "github.com/apollo-chora/chora-consumption/internal/domain/knowledge_graph_read"
)

// ConceptTitleReader resolves one learner-scoped ConceptNode → its title.
// Satisfied by the pg ConceptNodeRepo (GetByID returns (nil, nil) on a miss).
type ConceptTitleReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, conceptID string) (*conceptgraph.ConceptNode, error)
}

// EdgeNeighborReader lists a concept's hex-face edges (source OR target).
// Satisfied by the pg EdgeRepo (ListByConcept).
type EdgeNeighborReader interface {
	ListByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string) ([]*conceptgraph.Edge, error)
}

// MapNode is one concept on the ring-scoped map — learner-SAFE by construction
// (title + ring distance only; no identifiers).
type MapNode struct {
	Title    string
	Ring     int // 0 = resonant centre, 1 = adjacent, 2 = two-hop
	IsCentre bool
}

// Reader composes the concept-title + edge-neighbor ports with the domain BFS.
type Reader struct {
	nodes ConceptTitleReader
	edges EdgeNeighborReader
}

// NewReader constructs the ring-map reader.
func NewReader(nodes ConceptTitleReader, edges EdgeNeighborReader) *Reader {
	return &Reader{nodes: nodes, edges: edges}
}

// graphFunc adapts a closure to the knowledge_graph_read.Graph port (so the
// per-request ctx rides the closure rather than a struct field).
type graphFunc func(node string) ([]string, error)

func (f graphFunc) Neighbors(node string) ([]string, error) { return f(node) }

// ReadRingMap returns the learner's concepts within `rings` rings of the centre
// concept, each with its ring distance. A dangling edge to a pruned concept is
// skipped (fail-soft on one stale ref — the rest of the map still renders).
func (r *Reader) ReadRingMap(ctx context.Context, tenantID, learnerGCID, centreConceptID string, rings int) ([]MapNode, error) {
	adjacency := graphFunc(func(conceptID string) ([]string, error) {
		edges, err := r.edges.ListByConcept(ctx, tenantID, learnerGCID, conceptID)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(edges))
		for _, e := range edges {
			if e == nil {
				continue
			}
			switch conceptID {
			case e.SourceConceptID:
				out = append(out, e.TargetConceptID)
			case e.TargetConceptID:
				out = append(out, e.SourceConceptID)
			}
		}
		return out, nil
	})

	ringNodes, err := kgr.RingScan(adjacency, centreConceptID, rings)
	if err != nil {
		return nil, err
	}

	out := make([]MapNode, 0, len(ringNodes))
	for _, rn := range ringNodes {
		node, err := r.nodes.GetByID(ctx, tenantID, learnerGCID, rn.ID)
		if err != nil {
			return nil, fmt.Errorf("kgmapread: resolve concept title: %w", err)
		}
		if node == nil {
			// Dangling edge → a pruned/absent concept: skip it (fail-soft).
			continue
		}
		out = append(out, MapNode{Title: node.Title, Ring: rn.Ring, IsCentre: rn.Ring == 0})
	}
	return out, nil
}
