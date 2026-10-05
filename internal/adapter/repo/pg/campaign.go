// campaign.go — Postgres adapter for the campaign ladder aggregate
// (campaign.ProgressRepository, ADR-227 D16; WS-C1 CHO-2080). Schema mirrors
// migrations/0077_familiar_campaign.up.sql + 0078_campaign_counter_and_seal.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET
// LOCAL chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE
// the query; per-learner scoping is an explicit learner_gcid predicate as
// defence-in-depth. Save is an UPSERT on the live-row unique index
// uq_campaign_node_progress_live (tenant, learner, concept WHERE deleted_at
// IS NULL) — ladder rows are created lazily by the first graded answer and
// mutated in place after.
//
// rung_cleared_at is TIMESTAMPTZ[] ↔ []time.Time (pgx native);
// last_advance_date is DATE ↔ *time.Time (midnight UTC — the domain
// compares calendar dates only).
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

// CampaignProgressRepo is the Postgres-backed campaign.ProgressRepository.
type CampaignProgressRepo struct {
	tx TxRunner
}

// NewCampaignProgressRepo constructs the repo around a TxRunner.
func NewCampaignProgressRepo(tx TxRunner) *CampaignProgressRepo {
	return &CampaignProgressRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ campaign.ProgressRepository = (*CampaignProgressRepo)(nil)

// Save upserts the ladder row on its live (tenant, learner, concept) identity.
func (r *CampaignProgressRepo) Save(ctx context.Context, p *campaign.NodeProgress) error {
	if p == nil {
		return nil
	}
	// A fresh ladder has cleared nothing; the column is NOT NULL, so a nil
	// slice must land as an empty array, never SQL NULL (23502 killed the
	// first-ever answer fold — caught by the accelerated C8 walk; the
	// retrieved_atom_ids idiom).
	clearedAt := p.RungClearedAt
	if clearedAt == nil {
		clearedAt = []time.Time{}
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertCampaignProgressSQL,
			p.ID, p.TenantID, p.LearnerGCID, p.ConceptID,
			p.RungsCleared, clearedAt, p.CurrentRungCorrect,
			p.WonAt, p.LastAdvanceDate, p.CreatedAt, p.UpdatedAt, p.DeletedAt,
		)
		return err
	})
}

// GetByConcept returns one live ladder row, or (nil, nil) when absent.
func (r *CampaignProgressRepo) GetByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string) (*campaign.NodeProgress, error) {
	var out *campaign.NodeProgress
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getCampaignProgressSQL, tenantID, learnerGCID, conceptID)
		if row == nil {
			return nil // stub Querier — not found
		}
		p, err := scanCampaignProgress(row)
		if err != nil {
			if err == ErrNoRows {
				return nil // not found — lazily created on first grading
			}
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ListByLearner returns the learner's live ladder rows.
func (r *CampaignProgressRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*campaign.NodeProgress, error) {
	out := make([]*campaign.NodeProgress, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listCampaignProgressSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanCampaignProgress(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// scanCampaignProgress maps one row (the shared SELECT column order) into the
// domain aggregate. scanner is satisfied by both Row and Rows.
func scanCampaignProgress(s interface{ Scan(dest ...any) error }) (*campaign.NodeProgress, error) {
	var (
		p         campaign.NodeProgress
		wonAt     *time.Time
		advDate   *time.Time
		deletedAt *time.Time
	)
	if err := s.Scan(
		&p.ID, &p.TenantID, &p.LearnerGCID, &p.ConceptID,
		&p.RungsCleared, &p.RungClearedAt, &p.CurrentRungCorrect,
		&wonAt, &advDate, &p.CreatedAt, &p.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	p.WonAt = wonAt
	p.LastAdvanceDate = advDate
	p.DeletedAt = deletedAt
	return &p, nil
}

// --- SQL templates (review-via-test; registered in prepareSmokeStatements) ---

const (
	upsertCampaignProgressSQL = `
INSERT INTO campaign_node_progress
       (id, tenant_id, learner_gcid, concept_id, rungs_cleared, rung_cleared_at,
        current_rung_correct, won_at, last_advance_date, created_at, updated_at, deleted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (tenant_id, learner_gcid, concept_id) WHERE deleted_at IS NULL
DO UPDATE SET
       rungs_cleared        = EXCLUDED.rungs_cleared,
       rung_cleared_at      = EXCLUDED.rung_cleared_at,
       current_rung_correct = EXCLUDED.current_rung_correct,
       won_at               = EXCLUDED.won_at,
       last_advance_date    = EXCLUDED.last_advance_date,
       updated_at           = EXCLUDED.updated_at,
       deleted_at           = EXCLUDED.deleted_at`

	getCampaignProgressSQL = `
SELECT id, tenant_id, learner_gcid, concept_id, rungs_cleared, rung_cleared_at,
       current_rung_correct, won_at, last_advance_date, created_at, updated_at, deleted_at
  FROM campaign_node_progress
 WHERE tenant_id = $1 AND learner_gcid = $2 AND concept_id = $3 AND deleted_at IS NULL`

	listCampaignProgressSQL = `
SELECT id, tenant_id, learner_gcid, concept_id, rungs_cleared, rung_cleared_at,
       current_rung_correct, won_at, last_advance_date, created_at, updated_at, deleted_at
  FROM campaign_node_progress
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`
)
