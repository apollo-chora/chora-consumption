// ports.go — driven port for the weakness-upload job aggregate. Adapters live in
// internal/adapter/* and depend on this package (hexagonal dependency direction).
package weakness_upload

import (
	"context"
	"time"
)

// Repository persists the upload-job lifecycle. Every method is per-GCID +
// tenant-scoped (RLS applied in the adapter; tenant rides the ctx, learner_gcid
// is an explicit predicate). Cross-DB queries are forbidden — chora_consumption
// only.
type Repository interface {
	// Insert persists a fresh QUEUED job.
	Insert(ctx context.Context, u Upload) error
	// Get returns one job by id, scoped to the learner; nil + nil-error when absent.
	Get(ctx context.Context, learnerGCID, uploadID string) (*Upload, error)
	// MarkCompleted flips a QUEUED/ANALYZING job to COMPLETED with the upserted
	// Growth-Edge ids (idempotent — a re-delivered analyzed.v1 is a no-op).
	MarkCompleted(ctx context.Context, learnerGCID, uploadID string, edgeIDs []string, now time.Time) error
	// MarkFailed flips a non-terminal job to FAILED with a non-leaky reason.
	MarkFailed(ctx context.Context, learnerGCID, uploadID, reason string, now time.Time) error
}

// PendingReviewLister is the READ-MODEL port for the learner's diagnoses
// parked at the bounded HITL review interrupt (B6 item 1).
//
// Deliberately separate from Repository rather than a method on it. Repository
// is the write-side lifecycle port with one production implementation and
// several structural fakes across the http and subscriber tests; widening it
// would break every one of those for a read that none of them performs. The pg
// WeaknessUploadRepo satisfies BOTH interfaces structurally, which is the same
// shape the analysed and review-pending subscribers already rely on.
//
// Why the server has to own this list at all: before it existed the FE kept
// the parked review in localStorage (`pending-review.store.ts`), one key rather
// than a list, so the learner's most curiosity-first home card did not survive
// a device change or a cleared browser.
type PendingReviewLister interface {
	// ListAwaitingReview returns the learner's parked uploads, newest first,
	// bounded by limit (zero or negative means the adapter's default). Returns
	// an empty slice, never nil, when nothing is pending.
	ListAwaitingReview(ctx context.Context, learnerGCID string, limit int) ([]Upload, error)
}
