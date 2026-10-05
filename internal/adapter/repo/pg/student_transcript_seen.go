// student_transcript_seen.go: the unseen-result flag on the learner's
// transcript (UX refactor Phase B, package B6 item 2).
//
// Two halves, deliberately kept apart from the sibling file's shared helpers.
//
//  1. scanTranscriptEntryRowWithSeen is a SEPARATE scan from
//     scanTranscriptEntryRow. The learner's own list carries seen_at; the
//     cross-learner gradebook read does NOT. Whether a learner has opened their
//     own result is a fact about their reading, not about the assessment, and
//     widening the shared helper would have handed every instructor a
//     per-student read-receipt nobody asked for. A test pins the absence.
//
//  2. MarkSeen is a guarded one-way UPDATE. RLS scopes the tenant; the explicit
//     gcid predicate scopes the learner, and on a WRITE that predicate is the
//     only thing standing between one learner and another learner's row. The
//     `AND seen_at IS NULL` guard makes the write idempotent and keeps the
//     first-seen stamp immovable, matching the domain's MarkSeen.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// markTranscriptEntrySeenSQL stamps the first-seen moment for ONE entry
// belonging to ONE learner.
//
// The seen_at IS NULL guard is what makes this one-way and idempotent: a
// second open affects zero rows, which the caller treats as success rather
// than as a failure.
const markTranscriptEntrySeenSQL = `
UPDATE student_transcript_entries
   SET seen_at = $4, updated_at = now()
 WHERE tenant_id = $1 AND gcid = $2 AND entry_id = $3 AND seen_at IS NULL`

// countUnseenTranscriptEntriesSQL counts the learner's unopened entries across
// their WHOLE transcript.
//
// No LIMIT, deliberately: this is the number the home badges, and counting a
// page of it is the defect this replaces. There is no deleted_at predicate
// because the table has no such column: 0079 states the projection is
// append/upsert-only with no learner-facing delete path, so soft-delete does
// not apply here and inventing the predicate would be a 42703.
//
// Served by the 0118 partial index (tenant_id, gcid, occurred_at DESC) WHERE
// seen_at IS NULL. MEASURED, not assumed: at 40k rows on PostgreSQL 18 the
// plan is an Index Only Scan on that index, so the count never touches the
// heap and reads an index that shrinks as results are opened. At trivial row
// counts the planner picks a different index, which is both correct and
// irrelevant.
const countUnseenTranscriptEntriesSQL = `
SELECT COUNT(*) FROM student_transcript_entries
 WHERE tenant_id = $1 AND gcid = $2 AND seen_at IS NULL`

// CountUnseenByGCID counts the learner's unopened results.
//
// RLS scopes the tenant; the explicit gcid predicate scopes the learner, the
// same pairing the list and the mark-seen write use. An error is returned
// rather than a zero, because zero renders as "you are all caught up", which
// is the one answer a failed read must never give.
func (r *TranscriptRepo) CountUnseenByGCID(ctx context.Context, tenantID, gcid string) (int, error) {
	if tenantID == "" || gcid == "" {
		return 0, errors.New("pg: CountUnseenByGCID requires tenant_id and gcid")
	}
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, countUnseenTranscriptEntriesSQL, tenantID, gcid).Scan(&n); err != nil {
			return fmt.Errorf("pg: count unseen transcript entries: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// scanTranscriptEntryRowWithSeen scans the learner-scoped projection, which
// carries seen_at as its final column.
func scanTranscriptEntryRowWithSeen(rows Rows) (*st.TranscriptEntry, error) {
	var (
		e            st.TranscriptEntry
		kind         string
		deliveryType *string
		courseID     *string
		seenAt       *time.Time
	)
	if err := rows.Scan(
		&e.EntryID, &e.TenantID, &e.GCID, &kind, &e.SourceRef, &e.Title, &deliveryType,
		&e.ScoreEarned, &e.ScorePossible, &e.ScorePercent, &e.Passed,
		&courseID, &e.OccurredAt, &e.IdempotencyKey, &e.CreatedAt, &e.UpdatedAt, &seenAt,
	); err != nil {
		return nil, err
	}
	e.Kind = st.Kind(kind)
	if deliveryType != nil {
		e.DeliveryType = st.DeliveryType(*deliveryType)
	}
	if courseID != nil {
		e.CourseID = *courseID
	}
	// NULL seen_at is the meaningful value: never opened.
	e.SeenAt = seenAt
	return &e, nil
}

// MarkSeen records that the learner has opened one of their own results.
//
// Already-seen is SUCCESS, not an error: zero rows affected means the second
// tap on a result the learner had already read, and reporting that as a
// failure would show an error for doing nothing wrong.
func (r *TranscriptRepo) MarkSeen(ctx context.Context, tenantID, gcid, entryID string, at time.Time) error {
	if tenantID == "" || gcid == "" || entryID == "" {
		return errors.New("pg: MarkSeen requires tenant_id, gcid and entry_id")
	}
	// Mirrors the domain guard so a caller bug cannot reach the database and
	// stamp the year 1 zero time, which would sort before every real entry.
	if at.IsZero() {
		return errors.New("pg: MarkSeen requires a non-zero timestamp")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markTranscriptEntrySeenSQL, tenantID, gcid, entryID, at.UTC()); err != nil {
			return fmt.Errorf("pg: mark transcript entry seen: %w", err)
		}
		return nil
	})
}
