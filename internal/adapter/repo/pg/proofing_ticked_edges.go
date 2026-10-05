// proofing_ticked_edges.go — CHO-2040: READ-ONLY adapter satisfying
// proofingtest.TickedEdgeReader over the EXISTING concept-graph read repos.
//
// "Ticked learning edges" = the LIVE intent-tagged (remediate|explore)
// ConceptNodes inside the goal root's hierarchy subtree — exactly what the
// CHO-2038 ceremony minted (migration 0066 concept_nodes.intent; ADR-214 D1
// subtree scope via conceptgraph.SubtreeConceptIDs). This adapter composes
// ConceptNodeRepo.ListByLearner + EdgeRepo.ListByLearner and runs the pure
// subtree engine in memory — it NEVER writes the concept graph (the KG
// session owns every write surface).
//
// R8-7 KEY: ConceptNode now carries a stable normalised concept_key set at
// mint (CHO-2038 key-at-mint; domain concept_node.go + migration 0074, sharing
// the learner_weakness concept_key vocabulary). This adapter maps that column
// into TargetEdge.Key, so the qgen `target_growth_edges` seam gets real KEYS
// and the runner's 422 EDGES_LACK_KEYS gate opens with NO handler change — the
// resolution the earlier gap comment anticipated. The key is NEVER forged from
// titles at read time (stable across renames); it is minted once from the
// title and persisted.
package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// conceptNodeLister / conceptEdgeLister are the narrow read slices this
// adapter needs (interface segregation; the pg repos satisfy them).
type conceptNodeLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.ConceptNode, error)
}

type conceptEdgeLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Edge, error)
}

// ProofingTickedEdgeReader implements proofingtest.TickedEdgeReader.
type ProofingTickedEdgeReader struct {
	nodes conceptNodeLister
	edges conceptEdgeLister
}

// NewProofingTickedEdgeReader composes the two concept-graph read repos.
func NewProofingTickedEdgeReader(nodes conceptNodeLister, edges conceptEdgeLister) *ProofingTickedEdgeReader {
	return &ProofingTickedEdgeReader{nodes: nodes, edges: edges}
}

// Compile-time check.
var _ pt.TickedEdgeReader = (*ProofingTickedEdgeReader)(nil)

// ListTicked returns the goal's ticked learning edges, title-ordered
// (deterministic wire order for the prompt + field 19). An empty/blank root
// returns an empty slice (a rootless goal has no subtree — the caller renders
// 422 NO_TICKED_EDGES).
func (r *ProofingTickedEdgeReader) ListTicked(ctx context.Context, tenantID, learnerGCID, rootConceptID string) ([]pt.TargetEdge, error) {
	if r == nil || r.nodes == nil || r.edges == nil {
		return nil, fmt.Errorf("proofing_ticked_edges: reader not wired")
	}
	root := strings.TrimSpace(rootConceptID)
	if root == "" {
		return []pt.TargetEdge{}, nil
	}
	nodePtrs, err := r.nodes.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return nil, fmt.Errorf("proofing_ticked_edges: list concepts: %w", err)
	}
	edgePtrs, err := r.edges.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return nil, fmt.Errorf("proofing_ticked_edges: list edges: %w", err)
	}

	concepts := make([]conceptgraph.ConceptNode, 0, len(nodePtrs))
	for _, n := range nodePtrs {
		if n != nil {
			concepts = append(concepts, *n)
		}
	}
	graphEdges := make([]conceptgraph.Edge, 0, len(edgePtrs))
	for _, e := range edgePtrs {
		if e != nil {
			graphEdges = append(graphEdges, *e)
		}
	}

	subtree := conceptgraph.SubtreeConceptIDs(concepts, graphEdges, root)
	out := make([]pt.TargetEdge, 0, 8)
	for i := range concepts {
		c := &concepts[i]
		if c.DeletedAt != nil || !subtree[c.ConceptID] || !c.Intent.Labelled() {
			continue
		}
		out = append(out, pt.TargetEdge{
			ConceptID: c.ConceptID,
			// Key-at-mint (CHO-2038, migration 0074): the ConceptNode's stable
			// normalised concept_key. Opens the R8-7 EDGES_LACK_KEYS gate with no
			// handler change — exactly as this package's comment prescribed.
			Key:    c.ConceptKey,
			Title:  c.Title,
			Intent: string(c.Intent),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ConceptID < out[j].ConceptID
	})
	return out, nil
}
