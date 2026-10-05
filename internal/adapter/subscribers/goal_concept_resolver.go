// goal_concept_resolver.go — ADR-238 M-D2 (goal-scoped Diagnose ingest resolver).
// Turns the upload's Goal into the on-map ConceptCandidates the matcher ranks.
//
// A learner Goal IS a map (ADR-214 §1): it is anchored to a root ConceptNode and
// its scope is the sub-tree under that root (SubtreeConceptIDs). So the candidate
// set for resolving an analysed Growth Edge is exactly the titled ConceptNodes in
// that sub-tree — each embedded once (its Title) for cosine comparison against the
// edge's own embedding.
//
// Composition-only adapter: it holds narrow reader ports (goal / node / edge /
// embedder) so it is driven by stubs in test and by the pg repos + the Vertex
// embedder in cmd/server. An unrooted / absent goal is NOT an error — it simply
// yields no candidates (the ingest path then persists every edge goal-level).
package subscribers

import (
	"context"
	"strings"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// GoalScope is a goal's resolved matching context: the ROOT concept (the map
// itself, ADR-214) plus the on-map candidates an edge may bind to. The root is
// returned alongside the candidates because ADR-238 D4 anchors an unmatched
// edge's concept suggestion there: a whole-map (empty focal) suggestion is in NO
// goal subtree, so the goal fence in concept_suggestion_handler drops it on
// every goal-scoped read, which would leave D4's surfacing invisible.
type GoalScope struct {
	RootConceptID string
	Candidates    []ConceptCandidate
}

// GoalConceptResolver yields the on-map concept candidates for an upload's goal.
// The WeaknessAnalyzedSubscriber holds this port; the concrete impl below is
// wired in cmd/server. Returns (nil, nil) — NOT an error — when the goal is
// absent or unrooted (no map to scope to).
//
// entryConceptID is ADR-238 D2's SOFT hint: the concept whose drawer the learner
// opened Diagnose from. It marks candidates inside that concept's sub-tree as
// Preferred so the matcher can break a near-tie toward them. It NEVER narrows
// the candidate set - an entry concept that is empty, unknown, soft-deleted, or
// outside the goal simply leaves every candidate unbiased.
type GoalConceptResolver interface {
	Resolve(ctx context.Context, tenantID, learnerGCID, goalID, entryConceptID string) (GoalScope, error)
}

// --- narrow reader ports (satisfied by the pg repos + the Vertex embedder) ---

// goalByIDReader reads one Goal for the learner. *pg.GoalRepo satisfies it.
type goalByIDReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, goalID string) (*goal.Goal, error)
}

// conceptNodeLister lists the learner's live ConceptNodes. *pg.ConceptNodeRepo
// satisfies it.
type conceptNodeLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.ConceptNode, error)
}

// conceptEdgeLister lists the learner's live Edges. *pg.EdgeRepo satisfies it.
type conceptEdgeLister interface {
	ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Edge, error)
}

// goalConceptResolver is the composing GoalConceptResolver.
type goalConceptResolver struct {
	goals    goalByIDReader
	nodes    conceptNodeLister
	edges    conceptEdgeLister
	embedder lw.Embedder
}

// NewGoalConceptResolver composes the resolver from a goal reader, the concept
// node + edge listers, and the same embedder the explicit/derived paths use.
func NewGoalConceptResolver(goals goalByIDReader, nodes conceptNodeLister, edges conceptEdgeLister, embedder lw.Embedder) *goalConceptResolver {
	return &goalConceptResolver{goals: goals, nodes: nodes, edges: edges, embedder: embedder}
}

// Compile-time check: the concrete resolver satisfies the port.
var _ GoalConceptResolver = (*goalConceptResolver)(nil)

// Candidates resolves the goal to its sub-tree's titled ConceptNodes, each
// embedded once. An unrooted / absent goal → (nil, nil). Any read/embed error is
// surfaced (the subscriber treats a non-nil error as a soft-fail: it logs and
// persists the edges goal-level rather than NACKing).
func (r *goalConceptResolver) Resolve(ctx context.Context, tenantID, learnerGCID, goalID, entryConceptID string) (GoalScope, error) {
	g, err := r.goals.GetByID(ctx, tenantID, learnerGCID, goalID)
	if err != nil {
		return GoalScope{}, err
	}
	if g == nil || g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		return GoalScope{}, nil // unrooted / absent goal → no map to scope to (not an error)
	}

	nodePtrs, err := r.nodes.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return GoalScope{}, err
	}
	edgePtrs, err := r.edges.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return GoalScope{}, err
	}

	// SubtreeConceptIDs takes VALUES — deref the repo's pointer slices.
	nodes := make([]conceptgraph.ConceptNode, 0, len(nodePtrs))
	for _, n := range nodePtrs {
		if n != nil {
			nodes = append(nodes, *n)
		}
	}
	edges := make([]conceptgraph.Edge, 0, len(edgePtrs))
	for _, e := range edgePtrs {
		if e != nil {
			edges = append(edges, *e)
		}
	}

	inSubtree := conceptgraph.SubtreeConceptIDs(nodes, edges, *g.RootConceptID)

	// ADR-238 D2 - the ENTRY concept's own sub-tree, used ONLY to mark candidates
	// Preferred (a tie-break for the matcher). Computed over the same node/edge
	// snapshot, so an entry concept that is blank, unknown, soft-deleted, or on
	// another map yields an empty set and every candidate stays unbiased. It is
	// intersected with inSubtree by construction: the loop below only ever visits
	// candidates already inside the GOAL, so a hint cannot widen the candidate set
	// any more than it can narrow it.
	entrySubtree := conceptgraph.SubtreeConceptIDs(nodes, edges, strings.TrimSpace(entryConceptID))

	candidates := make([]ConceptCandidate, 0, len(nodes))
	for _, n := range nodes {
		if !inSubtree[n.ConceptID] {
			continue
		}
		title := strings.TrimSpace(n.Title)
		if title == "" {
			continue // an untitled node has nothing to embed/compare against
		}
		emb, err := r.embedder.Embed(ctx, title, tenantID)
		if err != nil {
			return GoalScope{}, err
		}
		candidates = append(candidates, ConceptCandidate{
			ConceptID:  n.ConceptID,
			ConceptKey: n.ConceptKey,
			Embedding:  emb,
			Preferred:  entrySubtree[n.ConceptID],
		})
	}
	return GoalScope{RootConceptID: *g.RootConceptID, Candidates: candidates}, nil
}
