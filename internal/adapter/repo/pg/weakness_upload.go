// weakness_upload.go — pgvector-free pg adapter for the Growth-Edge upload-job
// aggregate (Epic-1b W8b). Mirrors learner_weakness.go: every read/write runs
// rls.ApplySession (SET LOCAL chora.tenant_id) first, then the user query with an
// explicit learner_gcid predicate. chora_consumption only — no cross-DB.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

const insertWeaknessUploadSQL = `
INSERT INTO weakness_doc_uploads
    (upload_id, tenant_id, learner_gcid, upload_kind, source_mime, source_blob_uri, status, created_at, upload_rights_consent_at, goal_id, entry_concept_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10,'')::uuid, NULLIF($11,'')::uuid)
ON CONFLICT (upload_id) DO NOTHING`

// markBlobShreddedSQL records the delete-on-analyse of the GCS ciphertext on the
// job row (WS-5 audit marker). Idempotent — re-setting blob_deleted_at is a
// no-op. The cryptographic shred guarantee lives in weakness_blob_dek_wrap; this
// is the per-job audit trail.
const markBlobShreddedSQL = `
UPDATE weakness_doc_uploads
SET blob_deleted_at = $3
WHERE upload_id = $1 AND learner_gcid = $2 AND blob_deleted_at IS NULL`

// markWeaknessUploadAwaitingReviewSQL parks a non-terminal job at the bounded
// HITL interrupt (ADR-205 D4 / CHO-1973), storing the review panel JSONB. The
// status IN ('QUEUED', 'ANALYZING') guard makes a re-delivered review_pending —
// or one racing a terminal flip — a no-op (idempotent; the inbox already dedups
// the common case, this guards the rare concurrent reprocess).
const markWeaknessUploadAwaitingReviewSQL = `
UPDATE weakness_doc_uploads
SET status = 'AWAITING_REVIEW', review_payload = $3
WHERE upload_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
  AND status IN ('QUEUED', 'ANALYZING')`

const getWeaknessUploadSQL = `
SELECT upload_id, learner_gcid, upload_kind, source_mime, source_blob_uri, status,
       upserted_edge_ids, edge_count, COALESCE(failure_reason, ''), created_at, analyzed_at,
       review_payload, COALESCE(goal_id::text, ''), COALESCE(entry_concept_id::text, '')
FROM weakness_doc_uploads
WHERE upload_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`

// status <> 'COMPLETED' makes a re-delivered analyzed.v1 a no-op (idempotent).
const markWeaknessUploadCompletedSQL = `
UPDATE weakness_doc_uploads
SET status = 'COMPLETED', upserted_edge_ids = $3, edge_count = $4, analyzed_at = $5
WHERE upload_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL AND status <> 'COMPLETED'`

const markWeaknessUploadFailedSQL = `
UPDATE weakness_doc_uploads
SET status = 'FAILED', failure_reason = $3, analyzed_at = $4
WHERE upload_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL AND status NOT IN ('COMPLETED', 'FAILED')`

// WeaknessUploadRepo is the weakness_upload.Repository backed by a TxRunner.
type WeaknessUploadRepo struct {
	tx TxRunner
}

// NewWeaknessUploadRepo constructs the repo around a TxRunner.
func NewWeaknessUploadRepo(tx TxRunner) *WeaknessUploadRepo {
	return &WeaknessUploadRepo{tx: tx}
}

var _ wu.Repository = (*WeaknessUploadRepo)(nil)

func (r *WeaknessUploadRepo) Insert(ctx context.Context, u wu.Upload) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertWeaknessUploadSQL,
			u.UploadID, u.TenantID, u.LearnerGCID, u.UploadKind,
			u.SourceMIME, u.SourceBlobURI, string(wu.StatusQueued), u.CreatedAt,
			u.UploadRightsConsentAt, u.GoalID, u.EntryConceptID,
		); err != nil {
			return fmt.Errorf("pg: insert weakness_doc_upload: %w", err)
		}
		return nil
	})
}

// MarkBlobShredded records the delete-on-analyse of the GCS ciphertext on the
// originating job row (WS-5 audit). Idempotent + tenant/learner-scoped via RLS.
func (r *WeaknessUploadRepo) MarkBlobShredded(ctx context.Context, learnerGCID, uploadID string, now time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markBlobShreddedSQL, uploadID, learnerGCID, now.UTC()); err != nil {
			return fmt.Errorf("pg: mark weakness_doc_upload blob shredded: %w", err)
		}
		return nil
	})
}

// MarkAwaitingReview parks a non-terminal job at the bounded HITL interrupt
// (ADR-205 D4 / CHO-1973): flips QUEUED/ANALYZING → AWAITING_REVIEW and stores
// the review panel as review_payload JSONB. Idempotent + tenant/learner-scoped
// via RLS. The panel is marshalled here (JSON serialization is an adapter
// concern); the orchestrator-owned proposed_edge_id rides through OPAQUELY. This
// method is consumed via a structural ReviewParker interface, NOT the Repository
// port (mirrors MarkBlobShredded).
func (r *WeaknessUploadRepo) MarkAwaitingReview(ctx context.Context, learnerGCID, uploadID string, panel *wu.ReviewPanel, now time.Time) error {
	raw, err := json.Marshal(panel)
	if err != nil {
		return fmt.Errorf("pg: marshal review panel: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markWeaknessUploadAwaitingReviewSQL, uploadID, learnerGCID, raw); err != nil {
			return fmt.Errorf("pg: mark weakness_doc_upload awaiting review: %w", err)
		}
		return nil
	})
}

func (r *WeaknessUploadRepo) Get(ctx context.Context, learnerGCID, uploadID string) (*wu.Upload, error) {
	var out *wu.Upload
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var (
			u             wu.Upload
			status        string
			analyzedAt    *time.Time
			reviewPayload []byte
		)
		err := q.QueryRow(ctx, getWeaknessUploadSQL, uploadID, learnerGCID).Scan(
			&u.UploadID, &u.LearnerGCID, &u.UploadKind, &u.SourceMIME, &u.SourceBlobURI,
			&status, &u.UpsertedEdgeIDs, &u.EdgeCount, &u.FailureReason, &u.CreatedAt, &analyzedAt,
			&reviewPayload, &u.GoalID, &u.EntryConceptID,
		)
		if errors.Is(err, ErrNoRows) {
			return nil // absent → out stays nil
		}
		if err != nil {
			return fmt.Errorf("pg: get weakness_doc_upload: %w", err)
		}
		u.Status = wu.Status(status)
		u.AnalyzedAt = analyzedAt
		if u.UpsertedEdgeIDs == nil {
			u.UpsertedEdgeIDs = []string{}
		}
		// Hydrate the bounded HITL panel when present (AWAITING_REVIEW rows);
		// NULL JSONB scans into a nil []byte ⇒ Review stays nil. Fail-loud on a
		// corrupt payload rather than silently serving an empty panel.
		if len(reviewPayload) > 0 {
			var panel wu.ReviewPanel
			if err := json.Unmarshal(reviewPayload, &panel); err != nil {
				return fmt.Errorf("pg: unmarshal review_payload for %s: %w", uploadID, err)
			}
			u.Review = &panel
		}
		out = &u
		return nil
	})
	return out, err
}

// ScopeForUpload returns the ADR-238 scoping the upload was taken under: the
// goal (map) that BOUNDS concept resolution (M-D2/D1) and the entry concept that
// only BIASES ranking inside it (D2). A zero Scope means no goal scope OR
// the upload does not exist. It reuses Get so the RLS + tenant/learner scoping is
// identical and both values come from one row read. Structural (satisfies the
// subscriber's UploadScopeLookup port - NOT the Repository port).
func (r *WeaknessUploadRepo) ScopeForUpload(ctx context.Context, learnerGCID, uploadID string) (wu.Scope, error) {
	u, err := r.Get(ctx, learnerGCID, uploadID)
	if err != nil {
		return wu.Scope{}, err
	}
	if u == nil {
		return wu.Scope{}, nil
	}
	return wu.Scope{GoalID: u.GoalID, EntryConceptID: u.EntryConceptID}, nil
}

func (r *WeaknessUploadRepo) MarkCompleted(ctx context.Context, learnerGCID, uploadID string, edgeIDs []string, now time.Time) error {
	if edgeIDs == nil {
		edgeIDs = []string{}
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markWeaknessUploadCompletedSQL,
			uploadID, learnerGCID, edgeIDs, len(edgeIDs), now,
		); err != nil {
			return fmt.Errorf("pg: mark weakness_doc_upload completed: %w", err)
		}
		return nil
	})
}

func (r *WeaknessUploadRepo) MarkFailed(ctx context.Context, learnerGCID, uploadID, reason string, now time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, markWeaknessUploadFailedSQL,
			uploadID, learnerGCID, reason, now,
		); err != nil {
			return fmt.Errorf("pg: mark weakness_doc_upload failed: %w", err)
		}
		return nil
	})
}
