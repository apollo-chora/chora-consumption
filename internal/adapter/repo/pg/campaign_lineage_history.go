// campaign_lineage_history.go — WS-C6 (CHO-2085, ADR-227 D14): the XP
// re-award refusal read. Walks concept_node_lineage UPWARD from a concept
// (to → from, transitively — a survivor's ancestors are its absorbed nodes,
// a split child's are its parent chain) and folds the TOMBSTONED
// campaign_node_progress rows of {self ∪ ancestors} into (max rungs ever
// cleared, ever won). All prior ladder state lives in tombstones by the
// merge/split applier's construction: merge tombstones BOTH live rows before
// inserting the AND row, split tombstones the parent's. A node that never
// merged/split reads (0, false) — one indexed round-trip.
//
// Same-DB read (chora_consumption), RLS-scoped like every campaign repo;
// per-learner scoping is the explicit predicate on top (defence-in-depth).
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
)

// CampaignLineageHistoryRepo satisfies subscribers.CampaignLineageHistory.
type CampaignLineageHistoryRepo struct {
	tx TxRunner
}

// NewCampaignLineageHistoryRepo constructs the guard read around a TxRunner.
func NewCampaignLineageHistoryRepo(tx TxRunner) *CampaignLineageHistoryRepo {
	return &CampaignLineageHistoryRepo{tx: tx}
}

// PriorLadderState returns the lineage-historical high-water mark for one
// concept: the max rungs_cleared and whether any row ever won, across the
// concept's own tombstoned ladder rows and every lineage ancestor's rows.
func (r *CampaignLineageHistoryRepo) PriorLadderState(ctx context.Context, tenantID, learnerGCID, conceptID string) (int, bool, error) {
	var maxRungs int
	var everWon bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, priorLadderStateSQL, tenantID, learnerGCID, conceptID)
		if row == nil {
			return nil // stub Querier — no history
		}
		return row.Scan(&maxRungs, &everWon)
	})
	if err != nil {
		return 0, false, err
	}
	return maxRungs, everWon, nil
}

// priorLadderStateSQL — recursive to→from walk over live lineage rows, then
// an aggregate over TOMBSTONED ladder rows of self ∪ ancestors. UNION (not
// UNION ALL) de-duplicates and terminates on any cyclic lineage data.
const priorLadderStateSQL = `
WITH RECURSIVE lineage_up AS (
    SELECT l.from_concept_id
      FROM concept_node_lineage l
     WHERE l.tenant_id = $1 AND l.learner_gcid = $2
       AND l.to_concept_id = $3 AND l.deleted_at IS NULL
    UNION
    SELECT l.from_concept_id
      FROM concept_node_lineage l
      JOIN lineage_up u ON l.to_concept_id = u.from_concept_id
     WHERE l.tenant_id = $1 AND l.learner_gcid = $2 AND l.deleted_at IS NULL
)
SELECT COALESCE(MAX(p.rungs_cleared), 0)::int,
       COALESCE(BOOL_OR(p.won_at IS NOT NULL), FALSE)
  FROM campaign_node_progress p
 WHERE p.tenant_id = $1 AND p.learner_gcid = $2
   AND p.deleted_at IS NOT NULL
   AND (p.concept_id = $3 OR p.concept_id IN (SELECT from_concept_id FROM lineage_up))`
