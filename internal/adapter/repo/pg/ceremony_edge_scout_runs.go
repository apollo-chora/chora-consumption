// ceremony_edge_scout_runs.go — Postgres adapter for the R8-1 first-run
// ledger (edgescout.RunStore; CHO-2040). Schema mirrors
// migrations/0067_ceremony_edge_scout_runs.up.sql.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the
// domain SQL; per-learner scoping is an explicit learner_gcid predicate on top
// of the tenant RLS policy.
//
// Idempotency: RecordRun is INSERT ... ON CONFLICT (tenant_id, goal_id,
// learner_gcid) DO NOTHING — the concurrent first-run double-tap burns the
// freebie exactly once and both writers see success (both genuinely ran a
// free turn; the ledger's job is that the NEXT run is paid).
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
)

const ceremonyEdgeScoutHasRunSQL = `SELECT EXISTS (
    SELECT 1 FROM ceremony_edge_scout_runs
     WHERE tenant_id = $1 AND goal_id = $2 AND learner_gcid = $3
)`

const ceremonyEdgeScoutRecordRunSQL = `INSERT INTO ceremony_edge_scout_runs
    (id, tenant_id, goal_id, learner_gcid, first_run_at)
    VALUES ($1, $2, $3, $4, $5)
    ON CONFLICT (tenant_id, goal_id, learner_gcid) DO NOTHING`

// CeremonyEdgeScoutRunRepo is the pg-backed first-run ledger.
type CeremonyEdgeScoutRunRepo struct {
	tx TxRunner
}

// NewCeremonyEdgeScoutRunRepo constructs the repo around a TxRunner.
func NewCeremonyEdgeScoutRunRepo(tx TxRunner) *CeremonyEdgeScoutRunRepo {
	return &CeremonyEdgeScoutRunRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ edgescout.RunStore = (*CeremonyEdgeScoutRunRepo)(nil)

// HasRun satisfies edgescout.RunStore.
func (r *CeremonyEdgeScoutRunRepo) HasRun(ctx context.Context, tenantID, goalID, learnerGCID string) (bool, error) {
	var has bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, ceremonyEdgeScoutHasRunSQL, tenantID, goalID, learnerGCID)
		if row == nil {
			return nil // stub Querier returns nil — treat as no row (false)
		}
		if err := row.Scan(&has); err != nil {
			return fmt.Errorf("pg: ceremony_edge_scout_runs exists probe: %w", err)
		}
		return nil
	})
	return has, err
}

// RecordRun satisfies edgescout.RunStore.
func (r *CeremonyEdgeScoutRunRepo) RecordRun(ctx context.Context, tenantID, goalID, learnerGCID string, at time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, ceremonyEdgeScoutRecordRunSQL,
			domain.NewUUIDv7(), tenantID, goalID, learnerGCID, at.UTC(),
		); err != nil {
			return fmt.Errorf("pg: record ceremony_edge_scout_run: %w", err)
		}
		return nil
	})
}
