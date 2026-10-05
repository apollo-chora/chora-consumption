// learning_edges.go — Postgres adapter for the ceremony learning-edges applier
// (conceptgraph.LearningEdgeApplier, CHO-2038). A ticked learning-edge is a
// minted ConceptNode + its hierarchy Edge (root->concept); this persists the
// whole batch in ONE transaction so a partial failure rolls back cleanly (the
// learner sees all-or-nothing on the map). Reuses the WS-1 concept_nodes /
// concept_edges INSERT SQL, mirroring SuggestionRepo.ApplyAccept's one-tx
// contract (all intra-chora_consumption).
//
// Per multi-tenant-rls: rls.ApplySession runs inside the tx BEFORE any write.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// LearningEdgeRepo is the Postgres-backed conceptgraph.LearningEdgeApplier.
type LearningEdgeRepo struct {
	tx TxRunner
}

// NewLearningEdgeRepo constructs the repo around a TxRunner.
func NewLearningEdgeRepo(tx TxRunner) *LearningEdgeRepo {
	return &LearningEdgeRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ conceptgraph.LearningEdgeApplier = (*LearningEdgeRepo)(nil)

// ApplyLearningEdges persists every minted (ConceptNode + hierarchy Edge) pair
// in ONE transaction. Empty/nil slice is a no-op. Each node carries its
// remediate|explore intent (CHO-2038).
func (r *LearningEdgeRepo) ApplyLearningEdges(ctx context.Context, minted []conceptgraph.MintedLearningEdge) error {
	if len(minted) == 0 {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		for _, m := range minted {
			if m.Node != nil {
				if _, err := q.Exec(ctx, insertConceptNodeSQL, conceptNodeInsertArgs(m.Node)...); err != nil {
					return err
				}
			}
			if m.Edge != nil {
				if _, err := q.Exec(ctx, insertConceptEdgeSQL,
					m.Edge.EdgeID, m.Edge.TenantID, m.Edge.LearnerGCID, m.Edge.SourceConceptID, m.Edge.TargetConceptID,
					string(m.Edge.Class), string(m.Edge.Provenance), m.Edge.CreatedAt, m.Edge.UpdatedAt,
				); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
