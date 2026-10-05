// concept_rerooting.go — Postgres applier for a conceptgraph.ReRootPlan
// (ADR-212 WS-2). Re-rooting is APPEND-ONLY (D8): the plan's superseded
// parent->child hierarchy edges are SOFT-DELETED (Update semantics — endpoints
// immutable, only deleted_at/updated_at change) and the flipped child->parent
// edges are CREATED, all inside ONE tx.RunInTx so the re-root commits atomically
// (history preserved, never overwritten). Reuses the WS-1 concept_edges SQL
// (updateConceptEdgeSQL / insertConceptEdgeSQL) + the same RLS + (id, tenant,
// learner) scoping contract as concept_edge.go.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// ConceptReRootRepo applies a conceptgraph.ReRootPlan atomically.
type ConceptReRootRepo struct {
	tx TxRunner
}

// NewConceptReRootRepo constructs the applier around a TxRunner.
func NewConceptReRootRepo(tx TxRunner) *ConceptReRootRepo {
	return &ConceptReRootRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ conceptgraph.ReRootApplier = (*ConceptReRootRepo)(nil)

// Apply persists the plan in one transaction: RLS session first, then soft-delete
// the superseded hierarchy edges, then insert the flipped ones. A no-op plan
// (target already the apex) short-circuits and touches nothing.
func (r *ConceptReRootRepo) Apply(ctx context.Context, plan conceptgraph.ReRootPlan) error {
	if plan.IsNoOp() {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// (a) supersede the old parent->child edges (deleted_at + updated_at were
		// stamped by the domain plan; endpoints are immutable so are not written).
		for i := range plan.ToSoftDelete {
			e := plan.ToSoftDelete[i]
			if _, err := q.Exec(ctx, updateConceptEdgeSQL,
				e.EdgeID, e.TenantID, e.LearnerGCID, string(e.Class), string(e.Provenance),
				e.UpdatedAt, e.DeletedAt,
			); err != nil {
				return err
			}
		}
		// (b) create the flipped child->parent edges (append-only, fresh rows).
		for i := range plan.ToCreate {
			e := plan.ToCreate[i]
			if _, err := q.Exec(ctx, insertConceptEdgeSQL,
				e.EdgeID, e.TenantID, e.LearnerGCID, e.SourceConceptID, e.TargetConceptID,
				string(e.Class), string(e.Provenance), e.CreatedAt, e.UpdatedAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}
