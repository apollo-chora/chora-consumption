// learner_profile.go — Postgres adapter for the Global LearnerProfile read-model
// (learner_profile.ProjectionRepo). Schema mirrors
// migrations/0050_learner_profile.up.sql.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// query; per-learner scoping is an explicit learner_gcid predicate. The detail
// JSONB is marshalled to a string and cast `$N::jsonb` (mirrors learner_weakness
// descriptor); read back via `detail::text`.
//
// Idempotency:
//   - UpsertFact      ON CONFLICT (tenant, learner, fact_type, ref) DO UPDATE —
//     a redelivered event upserts in place; a re-occurrence updates the row.
//   - AppendActivity  ON CONFLICT (tenant, source_event_id) DO NOTHING — a
//     redelivered event logs exactly once (append-only log).
package pg

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// LearnerProfileRepo is the Postgres-backed projection repo.
type LearnerProfileRepo struct {
	tx TxRunner
}

// NewLearnerProfileRepo constructs the repo around a TxRunner.
func NewLearnerProfileRepo(tx TxRunner) *LearnerProfileRepo {
	return &LearnerProfileRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ lp.ProjectionRepo = (*LearnerProfileRepo)(nil)

// UpsertFact idempotently writes a verified fact (ON CONFLICT on the natural key).
func (r *LearnerProfileRepo) UpsertFact(ctx context.Context, f *lp.Fact) error {
	if f == nil {
		return nil
	}
	detail := marshalDetail(f.Detail)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertLearnerFactSQL,
			f.ID, f.TenantID, f.LearnerGCID, string(f.Type), f.RefID, detail,
			f.SourceEventID, f.OccurredAt, f.RecordedAt, f.UpdatedAt,
		)
		return err
	})
}

// AppendActivity appends one activity-log entry (ON CONFLICT source_event_id DO NOTHING).
func (r *LearnerProfileRepo) AppendActivity(ctx context.Context, e *lp.ActivityEntry) error {
	if e == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertLearnerActivitySQL,
			e.ID, e.TenantID, e.LearnerGCID, e.Kind, e.Summary, e.RefID,
			e.SourceEventID, e.OccurredAt, e.RecordedAt,
		)
		return err
	})
}

// ListFacts returns all live facts for a learner (deleted_at IS NULL), newest first.
func (r *LearnerProfileRepo) ListFacts(ctx context.Context, tenantID, learnerGCID string) ([]*lp.Fact, error) {
	out := make([]*lp.Fact, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listLearnerFactsSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier returns nil — treat as empty
		}
		defer rows.Close()
		for rows.Next() {
			var (
				f          lp.Fact
				factType   string
				detailJSON string
				deletedAt  *time.Time
			)
			if err := rows.Scan(
				&f.ID, &f.TenantID, &f.LearnerGCID, &factType, &f.RefID, &detailJSON,
				&f.SourceEventID, &f.OccurredAt, &f.RecordedAt, &f.UpdatedAt, &deletedAt,
			); err != nil {
				return err
			}
			f.Type = lp.FactType(factType)
			f.Detail = unmarshalDetail(detailJSON)
			f.DeletedAt = deletedAt
			out = append(out, &f)
		}
		return rows.Err()
	})
	return out, err
}

// RecentActivity returns the most recent activity entries (newest first), bounded
// by limit (defaulting to MaxRecentActivity when limit <= 0).
func (r *LearnerProfileRepo) RecentActivity(ctx context.Context, tenantID, learnerGCID string, limit int) ([]*lp.ActivityEntry, error) {
	if limit <= 0 {
		limit = lp.MaxRecentActivity
	}
	out := make([]*lp.ActivityEntry, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, recentLearnerActivitySQL, tenantID, learnerGCID, limit)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var e lp.ActivityEntry
			if err := rows.Scan(
				&e.ID, &e.TenantID, &e.LearnerGCID, &e.Kind, &e.Summary, &e.RefID,
				&e.SourceEventID, &e.OccurredAt, &e.RecordedAt,
			); err != nil {
				return err
			}
			out = append(out, &e)
		}
		return rows.Err()
	})
	return out, err
}

// --- marshalling helpers ---

func marshalDetail(d lp.Detail) string {
	b, err := json.Marshal(d)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalDetail(s string) lp.Detail {
	var d lp.Detail
	if strings.TrimSpace(s) == "" {
		return d
	}
	_ = json.Unmarshal([]byte(s), &d)
	return d
}

// --- SQL templates (review-via-test) ---

const (
	upsertLearnerFactSQL = `
INSERT INTO learner_profile_facts
       (id, tenant_id, learner_gcid, fact_type, ref_id, detail,
        source_event_id, occurred_at, recorded_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)
ON CONFLICT (tenant_id, learner_gcid, fact_type, ref_id) DO UPDATE SET
       detail          = EXCLUDED.detail,
       source_event_id = EXCLUDED.source_event_id,
       occurred_at     = EXCLUDED.occurred_at,
       updated_at      = EXCLUDED.updated_at,
       deleted_at      = NULL`

	insertLearnerActivitySQL = `
INSERT INTO learner_activity_log
       (id, tenant_id, learner_gcid, kind, summary, ref_id,
        source_event_id, occurred_at, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, source_event_id) DO NOTHING`

	listLearnerFactsSQL = `
SELECT id, tenant_id, learner_gcid, fact_type, ref_id, detail::text,
       source_event_id, occurred_at, recorded_at, updated_at, deleted_at
  FROM learner_profile_facts
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
 ORDER BY occurred_at DESC`

	recentLearnerActivitySQL = `
SELECT id, tenant_id, learner_gcid, kind, summary, ref_id,
       source_event_id, occurred_at, recorded_at
  FROM learner_activity_log
 WHERE tenant_id = $1 AND learner_gcid = $2
 ORDER BY occurred_at DESC
 LIMIT $3`
)
