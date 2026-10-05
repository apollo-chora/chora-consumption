// atom_refresh_ledger.go: Postgres adapter for the ADR-244 D5 atom-refresh
// proposal ledger (atomrefresh.Ledger). Schema:
// migrations/0108_atom_refresh_ledger.up.sql.
//
// Per multi-tenant-rls every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the
// query; per-learner scoping is an explicit learner_gcid predicate as
// defence-in-depth.
//
// Claim implements the redelivery-safe claim-then-mark protocol exactly as
// campaign_reveal.go does: INSERT ... ON CONFLICT (tenant, learner, atom,
// focal) DO NOTHING RETURNING mints the row and reports claimed=true, or,
// when a live row already holds the pairing, RETURNING is empty, so it
// SELECTs the existing live row and reports claimed=false. MarkPublished
// stamps request_id + published_at on the live row; RowsAffected==0 means no
// live claim, which is fail-loud (atomrefresh.ErrRefreshClaimNotFound)
// because Claim always precedes MarkPublished. CountClaimedSince backs the
// per-learner per-day cost cap.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
)

// AtomRefreshLedgerRepo is the Postgres-backed atomrefresh.Ledger.
type AtomRefreshLedgerRepo struct {
	tx TxRunner
}

// NewAtomRefreshLedgerRepo constructs the repo around a TxRunner.
func NewAtomRefreshLedgerRepo(tx TxRunner) *AtomRefreshLedgerRepo {
	return &AtomRefreshLedgerRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ atomrefresh.Ledger = (*AtomRefreshLedgerRepo)(nil)

// Claim reserves the proposal on the live (tenant, learner, atom, focal)
// identity. claimed=true + the fresh row on a genuine insert; claimed=false +
// the existing live row when one already holds the pairing (the redelivery /
// backfill-re-emit signal). A conflict with no findable live row is a broken
// invariant and fails loud.
func (r *AtomRefreshLedgerRepo) Claim(ctx context.Context, claim atomrefresh.RefreshClaim) (bool, *atomrefresh.RefreshClaim, error) {
	var (
		claimed  bool
		existing *atomrefresh.RefreshClaim
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if row := q.QueryRow(ctx, claimAtomRefreshSQL,
			claim.ID, claim.TenantID, claim.LearnerGCID, claim.AtomID, claim.FocalConceptID,
			claim.TriggerEventID, claim.RequestID, claim.PublishedAt, claim.CreatedAt,
		); row != nil {
			inserted, err := scanAtomRefreshClaim(row)
			if err == nil {
				claimed = true
				existing = inserted
				return nil
			}
			if !errors.Is(err, ErrNoRows) && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// Conflict (or stub Querier): load the live row that already holds it.
		row2 := q.QueryRow(ctx, getAtomRefreshByIdentitySQL,
			claim.TenantID, claim.LearnerGCID, claim.AtomID, claim.FocalConceptID)
		if row2 == nil {
			return nil // stub Querier, nothing to load
		}
		found, err := scanAtomRefreshClaim(row2)
		if err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("pg: claim atom refresh conflicted but no live row for (tenant, learner, atom, focal)")
			}
			return err
		}
		claimed = false
		existing = found
		return nil
	})
	return claimed, existing, err
}

// MarkPublished stamps request_id + published_at on the live claim row.
func (r *AtomRefreshLedgerRepo) MarkPublished(ctx context.Context, tenantID, learnerGCID, atomID, focalConceptID, requestID string, at time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, markAtomRefreshPublishedSQL,
			tenantID, learnerGCID, atomID, focalConceptID, requestID, at)
		if err != nil {
			return fmt.Errorf("pg: mark atom refresh published: %w", err)
		}
		if tag.RowsAffected == 0 {
			return fmt.Errorf("%w: (tenant, learner, atom, focal)", atomrefresh.ErrRefreshClaimNotFound)
		}
		return nil
	})
}

// CountClaimedSince counts live claims for the learner created at or after
// `since` (the per-day cap read).
func (r *AtomRefreshLedgerRepo) CountClaimedSince(ctx context.Context, tenantID, learnerGCID string, since time.Time) (int, error) {
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, countAtomRefreshSinceSQL, tenantID, learnerGCID, since)
		if row == nil {
			return nil // stub Querier
		}
		return row.Scan(&n)
	})
	return n, err
}

// scanAtomRefreshClaim maps one row (the shared column order) into the domain
// aggregate. scanner is satisfied by both Row and Rows.
func scanAtomRefreshClaim(s interface{ Scan(dest ...any) error }) (*atomrefresh.RefreshClaim, error) {
	var c atomrefresh.RefreshClaim
	if err := s.Scan(
		&c.ID, &c.TenantID, &c.LearnerGCID, &c.AtomID, &c.FocalConceptID, &c.TriggerEventID,
		&c.RequestID, &c.PublishedAt, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// --- SQL templates (review-via-test; prepare-smoked in
// atom_refresh_prepare_smoke_integration_test.go, new-files-only like the
// dose_kg_pref + companion_ritual siblings) ---

const (
	claimAtomRefreshSQL = `
INSERT INTO atom_refresh_ledger
       (id, tenant_id, learner_gcid, atom_id, focal_concept_id, trigger_event_id,
        request_id, published_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
ON CONFLICT (tenant_id, learner_gcid, atom_id, focal_concept_id) WHERE deleted_at IS NULL
DO NOTHING
RETURNING id, tenant_id, learner_gcid, atom_id, focal_concept_id, trigger_event_id,
          request_id, published_at, created_at`

	getAtomRefreshByIdentitySQL = `
SELECT id, tenant_id, learner_gcid, atom_id, focal_concept_id, trigger_event_id,
       request_id, published_at, created_at
  FROM atom_refresh_ledger
 WHERE tenant_id = $1 AND learner_gcid = $2 AND atom_id = $3 AND focal_concept_id = $4
   AND deleted_at IS NULL`

	markAtomRefreshPublishedSQL = `
UPDATE atom_refresh_ledger
   SET request_id   = $5,
       published_at = $6,
       updated_at   = $6
 WHERE tenant_id = $1 AND learner_gcid = $2 AND atom_id = $3 AND focal_concept_id = $4
   AND deleted_at IS NULL`

	countAtomRefreshSinceSQL = `
SELECT count(*)
  FROM atom_refresh_ledger
 WHERE tenant_id = $1 AND learner_gcid = $2 AND created_at >= $3
   AND deleted_at IS NULL`
)
