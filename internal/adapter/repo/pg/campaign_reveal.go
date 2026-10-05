// campaign_reveal.go — Postgres adapter for the free-on-win fog-reveal ledger
// (campaign.RevealLedger, ADR-227 D2; WS-C4 CHO-2083). Schema:
// migrations/0080_campaign_reveal_ledger.up.sql.
//
// Per multi-tenant-rls every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the
// query; per-learner scoping is an explicit learner_gcid predicate as
// defence-in-depth.
//
// Claim implements the redelivery-safe claim-then-mark protocol: INSERT ...
// ON CONFLICT (tenant, learner, goal, concept) DO NOTHING RETURNING mints the
// row and reports claimed=true, or — when a live row already holds the reveal
// — RETURNING is empty, so it SELECTs the existing live row and reports
// claimed=false. MarkPublished stamps request_id + published_at on the live
// row; RowsAffected==0 means no live claim, which is fail-loud
// (campaign.ErrRevealClaimNotFound) because Claim always precedes MarkPublished.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

// CampaignRevealLedgerRepo is the Postgres-backed campaign.RevealLedger.
type CampaignRevealLedgerRepo struct {
	tx TxRunner
}

// NewCampaignRevealLedgerRepo constructs the repo around a TxRunner.
func NewCampaignRevealLedgerRepo(tx TxRunner) *CampaignRevealLedgerRepo {
	return &CampaignRevealLedgerRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ campaign.RevealLedger = (*CampaignRevealLedgerRepo)(nil)

// Claim reserves the free reveal on the live (tenant, learner, goal, concept)
// identity. claimed=true + the fresh row on a genuine insert; claimed=false +
// the existing live row when one already holds the reveal (the redelivery
// signal). A conflict with no findable live row is a broken invariant and
// fails loud.
func (r *CampaignRevealLedgerRepo) Claim(ctx context.Context, claim campaign.RevealClaim) (bool, *campaign.RevealClaim, error) {
	var (
		claimed  bool
		existing *campaign.RevealClaim
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// Try to insert. RETURNING yields the fresh row; a live row on the
		// semantic identity makes ON CONFLICT DO NOTHING skip the insert and
		// RETURNING comes back empty.
		if row := q.QueryRow(ctx, claimRevealSQL,
			claim.ID, claim.TenantID, claim.LearnerGCID, claim.GoalID, claim.ConceptID,
			claim.NodeWonEventID, claim.RequestID, claim.PublishedAt, claim.CreatedAt,
		); row != nil {
			inserted, err := scanRevealClaim(row)
			if err == nil {
				claimed = true
				existing = inserted
				return nil
			}
			if !errors.Is(err, ErrNoRows) && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// Conflict (or stub Querier) — load the live row that already holds it.
		row2 := q.QueryRow(ctx, getRevealByIdentitySQL,
			claim.TenantID, claim.LearnerGCID, claim.GoalID, claim.ConceptID)
		if row2 == nil {
			return nil // stub Querier — nothing to load
		}
		found, err := scanRevealClaim(row2)
		if err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("pg: claim reveal conflicted but no live row for (tenant, learner, goal, concept)")
			}
			return err
		}
		claimed = false
		existing = found
		return nil
	})
	return claimed, existing, err
}

// MarkPublished stamps request_id + published_at on the live reveal row.
func (r *CampaignRevealLedgerRepo) MarkPublished(ctx context.Context, tenantID, learnerGCID, goalID, conceptID, requestID string, at time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, markRevealPublishedSQL,
			tenantID, learnerGCID, goalID, conceptID, requestID, at)
		if err != nil {
			return fmt.Errorf("pg: mark reveal published: %w", err)
		}
		if tag.RowsAffected == 0 {
			return fmt.Errorf("%w: (tenant, learner, goal, concept)", campaign.ErrRevealClaimNotFound)
		}
		return nil
	})
}

// scanRevealClaim maps one row (the shared column order) into the domain
// aggregate. scanner is satisfied by both Row and Rows.
func scanRevealClaim(s interface{ Scan(dest ...any) error }) (*campaign.RevealClaim, error) {
	var c campaign.RevealClaim
	if err := s.Scan(
		&c.ID, &c.TenantID, &c.LearnerGCID, &c.GoalID, &c.ConceptID, &c.NodeWonEventID,
		&c.RequestID, &c.PublishedAt, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// --- SQL templates (review-via-test; registered in prepareSmokeStatements) ---

const (
	claimRevealSQL = `
INSERT INTO campaign_reveal_ledger
       (id, tenant_id, learner_gcid, goal_id, concept_id, node_won_event_id,
        request_id, published_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
ON CONFLICT (tenant_id, learner_gcid, goal_id, concept_id) WHERE deleted_at IS NULL
DO NOTHING
RETURNING id, tenant_id, learner_gcid, goal_id, concept_id, node_won_event_id,
          request_id, published_at, created_at`

	getRevealByIdentitySQL = `
SELECT id, tenant_id, learner_gcid, goal_id, concept_id, node_won_event_id,
       request_id, published_at, created_at
  FROM campaign_reveal_ledger
 WHERE tenant_id = $1 AND learner_gcid = $2 AND goal_id = $3 AND concept_id = $4
   AND deleted_at IS NULL`

	markRevealPublishedSQL = `
UPDATE campaign_reveal_ledger
   SET request_id   = $5,
       published_at = $6,
       updated_at   = $6
 WHERE tenant_id = $1 AND learner_gcid = $2 AND goal_id = $3 AND concept_id = $4
   AND deleted_at IS NULL`
)
