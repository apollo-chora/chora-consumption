// student_transcript.go — Postgres adapter for the StudentTranscript
// read-model (student_transcript.Repository), W6 Slice 1. Schema mirrors
// migrations/0079_student_transcript.up.sql.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the
// domain query.
//
// Idempotency: Upsert is ON CONFLICT (tenant_id, idempotency_key) DO UPDATE —
// a true Pub/Sub redelivery AND a legitimate re-grade of the same submission
// (same deterministic natural-key idempotency_key, fresh event_id) both
// converge on the SAME row.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// defaultTranscriptListLimit bounds GET /v1/me/transcript when the caller
// supplies no (or a non-positive) limit.
const defaultTranscriptListLimit = 50

// TranscriptRepo is the Postgres-backed StudentTranscript projection repo.
type TranscriptRepo struct {
	tx TxRunner
}

// NewTranscriptRepo constructs the repo around a TxRunner.
func NewTranscriptRepo(tx TxRunner) *TranscriptRepo {
	return &TranscriptRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ st.Repository = (*TranscriptRepo)(nil)

// Upsert idempotently writes a TranscriptEntry (ON CONFLICT on the
// (tenant_id, idempotency_key) natural key). A nil entry is a no-op (never
// opens a transaction) — mirrors MapClusterRepo.Save's nil-guard.
func (r *TranscriptRepo) Upsert(ctx context.Context, e *st.TranscriptEntry) error {
	if e == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertTranscriptEntrySQL,
			e.EntryID, e.TenantID, e.GCID, string(e.Kind), e.SourceRef, e.Title,
			// CHO-2224: "" binds as NULL, never ''. NULL reads as "not
			// attributable" (a freestanding assessment has no Offering); an empty
			// string would be a distinct, meaningless value that breaks IS NULL.
			nullableText(string(e.DeliveryType)),
			e.ScoreEarned, e.ScorePossible, e.ScorePercent, e.Passed,
			nullableText(e.CourseID), e.OccurredAt, e.IdempotencyKey, e.CreatedAt, e.UpdatedAt,
		)
		return err
	})
}

// ListByGCID returns a learner's own entries, newest-occurred first, capped
// at limit (defaulting to defaultTranscriptListLimit when limit <= 0).
func (r *TranscriptRepo) ListByGCID(ctx context.Context, tenantID, gcid string, limit int) ([]*st.TranscriptEntry, error) {
	if limit <= 0 {
		limit = defaultTranscriptListLimit
	}
	out := make([]*st.TranscriptEntry, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listTranscriptByGCIDSQL, tenantID, gcid, limit)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier returns nil — treat as empty
		}
		defer rows.Close()
		for rows.Next() {
			e, serr := scanTranscriptEntryRowWithSeen(rows)
			if serr != nil {
				return serr
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// ListByAssessmentIDs returns the tenant's assessment-kind entries whose
// source_ref is in assessmentIDs, across every learner (the R+ per-offering
// gradebook join). An empty/nil assessmentIDs short-circuits BEFORE opening a
// transaction (no round-trip for a query that can only return zero rows).
// Tenant-scoped only; the HTTP layer is responsible for the
// instructor/admin role gate before calling this.
func (r *TranscriptRepo) ListByAssessmentIDs(ctx context.Context, tenantID string, assessmentIDs []string) ([]*st.TranscriptEntry, error) {
	out := make([]*st.TranscriptEntry, 0)
	if len(assessmentIDs) == 0 {
		return out, nil
	}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listTranscriptByAssessmentIDsSQL, tenantID, assessmentIDs)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			e, serr := scanTranscriptEntryRow(rows)
			if serr != nil {
				return serr
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// scanTranscriptEntryRow scans one row shaped like the SELECT list shared by
// listTranscriptByGCIDSQL / listTranscriptByAssessmentIDsSQL.
func scanTranscriptEntryRow(rows Rows) (*st.TranscriptEntry, error) {
	var (
		e            st.TranscriptEntry
		kind         string
		deliveryType *string
		courseID     *string
	)
	if err := rows.Scan(
		&e.EntryID, &e.TenantID, &e.GCID, &kind, &e.SourceRef, &e.Title, &deliveryType,
		&e.ScoreEarned, &e.ScorePossible, &e.ScorePercent, &e.Passed,
		&courseID, &e.OccurredAt, &e.IdempotencyKey, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return nil, err
	}
	e.Kind = st.Kind(kind)
	// NULL delivery_type = not attributable, which is the zero value here.
	if deliveryType != nil {
		e.DeliveryType = st.DeliveryType(*deliveryType)
	}
	if courseID != nil {
		e.CourseID = *courseID
	}
	return &e, nil
}

// --- SQL templates (review-via-test) ---

const (
	upsertTranscriptEntrySQL = `
INSERT INTO student_transcript_entries
       (entry_id, tenant_id, gcid, kind, source_ref, title, delivery_type,
        score_earned, score_possible, score_percent, passed,
        course_id, occurred_at, idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (tenant_id, idempotency_key) DO UPDATE SET
       title          = EXCLUDED.title,
       delivery_type  = EXCLUDED.delivery_type,
       score_earned   = EXCLUDED.score_earned,
       score_possible = EXCLUDED.score_possible,
       score_percent  = EXCLUDED.score_percent,
       passed         = EXCLUDED.passed,
       course_id      = EXCLUDED.course_id,
       occurred_at    = EXCLUDED.occurred_at,
       updated_at     = EXCLUDED.updated_at`

	listTranscriptByGCIDSQL = `
SELECT entry_id, tenant_id, gcid, kind, source_ref, title, delivery_type,
       score_earned, score_possible, score_percent, passed,
       course_id, occurred_at, idempotency_key, created_at, updated_at, seen_at
  FROM student_transcript_entries
 WHERE tenant_id = $1 AND gcid = $2
 ORDER BY occurred_at DESC
 LIMIT $3`

	listTranscriptByAssessmentIDsSQL = `
SELECT entry_id, tenant_id, gcid, kind, source_ref, title, delivery_type,
       score_earned, score_possible, score_percent, passed,
       course_id, occurred_at, idempotency_key, created_at, updated_at
  FROM student_transcript_entries
 WHERE tenant_id = $1 AND kind = 'assessment' AND source_ref = ANY($2)
 ORDER BY occurred_at DESC`
)
