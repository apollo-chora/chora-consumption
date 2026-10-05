// weakness_upload_pending.go: the server-side "pending diagnosis reviews" read
// model (UX refactor Phase B, package B6 item 1).
//
// Why this exists. The learner's most curiosity-first home card is "your
// diagnosis is waiting for you", and until now the FE remembered it in
// localStorage. `pending-review.store.ts` says so in its own header: one key,
// not a list, "the backend exposes no list pending reviews endpoint". The
// consequences are that the card dies on a device change, cannot survive a
// cleared browser, and can only ever remember ONE park even though the domain
// permits several.
//
// Nothing new is stored. An upload parked at the bounded HITL interrupt already
// carries Status == AWAITING_REVIEW and its review panel in review_payload
// (ADR-205 D4 / CHO-1973). This is purely a list over rows that already exist.
//
// RLS. Same posture as every other read in this file's sibling: RunInTx, then
// rls.ApplySession (SET LOCAL chora.tenant_id), then the query with an EXPLICIT
// learner_gcid predicate. Both halves are load-bearing and neither substitutes
// for the other: without ApplySession a NOBYPASSRLS role returns zero rows with
// no error, which is indistinguishable from "nothing is pending"; without the
// learner predicate the query would return every learner in the tenant.
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// Limits on the list. An unbounded read here is a denial of service on the
// learner's own home page, and a park count in the hundreds is already a
// symptom rather than something to render.
const (
	defaultPendingReviewLimit = 20
	maxPendingReviewLimit     = 100
)

// listAwaitingReviewSQL selects the parked jobs for one learner.
//
// Column list is deliberately identical to getWeaknessUploadSQL so the two
// reads scan the same shape and cannot drift apart. Ordering is newest first:
// the card shows the most recent park, and a stable order makes the limit
// meaningful rather than arbitrary.
//
// The status literal is inlined rather than parameterised on purpose. It is not
// caller input, it is the definition of this read model, and a caller able to
// pass a status would turn a pending-reviews list into a general upload search.
const listAwaitingReviewSQL = `
SELECT upload_id, learner_gcid, upload_kind, source_mime, source_blob_uri, status,
       upserted_edge_ids, edge_count, COALESCE(failure_reason, ''), created_at, analyzed_at,
       review_payload, COALESCE(goal_id::text, ''), COALESCE(entry_concept_id::text, '')
FROM weakness_doc_uploads
WHERE learner_gcid = $1 AND deleted_at IS NULL AND status = 'AWAITING_REVIEW'
ORDER BY created_at DESC
LIMIT $2`

// boundPendingReviewLimit clamps a caller-supplied limit. Zero or negative
// means "use the default" rather than "no limit", because an unset query
// parameter must never widen a read.
func boundPendingReviewLimit(limit int) int {
	if limit <= 0 {
		return defaultPendingReviewLimit
	}
	if limit > maxPendingReviewLimit {
		return maxPendingReviewLimit
	}
	return limit
}

// ListAwaitingReview returns the learner's uploads parked at the HITL review
// interrupt, newest first, bounded.
//
// Returns an EMPTY SLICE and no error when nothing is pending. The handler
// serialises the result straight to JSON, and a nil slice would render `null`
// where the contract says `[]`.
func (r *WeaknessUploadRepo) ListAwaitingReview(ctx context.Context, learnerGCID string, limit int) ([]wu.Upload, error) {
	out := []wu.Upload{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listAwaitingReviewSQL, learnerGCID, boundPendingReviewLimit(limit))
		if err != nil {
			return fmt.Errorf("pg: list awaiting-review uploads: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				u             wu.Upload
				status        string
				analyzedAt    *time.Time
				reviewPayload []byte
			)
			if err := rows.Scan(
				&u.UploadID, &u.LearnerGCID, &u.UploadKind, &u.SourceMIME, &u.SourceBlobURI,
				&status, &u.UpsertedEdgeIDs, &u.EdgeCount, &u.FailureReason, &u.CreatedAt, &analyzedAt,
				&reviewPayload, &u.GoalID, &u.EntryConceptID,
			); err != nil {
				return fmt.Errorf("pg: scan awaiting-review upload: %w", err)
			}
			u.Status = wu.Status(status)
			u.AnalyzedAt = analyzedAt
			if u.UpsertedEdgeIDs == nil {
				u.UpsertedEdgeIDs = []string{}
			}
			// Fail loud on a corrupt panel rather than serving a review card
			// with nothing in it and no way for the learner to tell why.
			if len(reviewPayload) > 0 {
				var panel wu.ReviewPanel
				if err := json.Unmarshal(reviewPayload, &panel); err != nil {
					return fmt.Errorf("pg: unmarshal review_payload for %s: %w", u.UploadID, err)
				}
				u.Review = &panel
			}
			out = append(out, u)
		}
		// rows.Err must be checked: an iteration that stops half way otherwise
		// reads as a short list, which is the quietest possible wrong answer.
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iterate awaiting-review uploads: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
