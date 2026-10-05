package student_transcript

import (
	"context"
	"time"
)

// Repository persists the per-learner StudentTranscript read-model. Every
// method is tenant-scoped via the RLS session GUC (the pg adapter calls
// rls.ApplySession first, mirroring learner_profile / topic_retention);
// per-learner scoping (ListByGCID) is an explicit gcid predicate. Cross-DB
// queries are forbidden (ddd-enforcement #1) — the projection is fed only by
// verified Pub/Sub events fanned out from the LearnerProfile push endpoints.
type Repository interface {
	// Upsert idempotently writes a TranscriptEntry keyed on
	// (tenant_id, idempotency_key). A redelivered event (or a legitimate
	// re-grade carrying the same deterministic natural-key idempotency_key)
	// updates the row in place rather than duplicating it.
	Upsert(ctx context.Context, e *TranscriptEntry) error

	// ListByGCID returns a learner's own entries, newest-occurred first,
	// capped at limit. limit <= 0 means the adapter applies its own default.
	ListByGCID(ctx context.Context, tenantID, gcid string, limit int) ([]*TranscriptEntry, error)

	// CountUnseenByGCID counts the learner's UNOPENED entries across their
	// whole transcript, not across a page of it.
	//
	// It is on the required Repository rather than in a narrow optional port
	// (the shape SeenMarker uses) precisely because it must not be skippable:
	// counting the returned page instead is what B6 shipped, and it silently
	// undercounts the one learner the badge exists for, the one with a backlog
	// longer than the page. A method the compiler demands cannot be left
	// unwired by a deployment.
	CountUnseenByGCID(ctx context.Context, tenantID, gcid string) (int, error)

	// ListByAssessmentIDs returns the tenant's assessment-kind entries whose
	// source_ref is in assessmentIDs, across ALL learners (the R+ per-offering
	// gradebook join). Tenant-scoped only — no gcid predicate; callers MUST
	// role-gate this at the HTTP layer (instructor/admin) before invoking it.
	ListByAssessmentIDs(ctx context.Context, tenantID string, assessmentIDs []string) ([]*TranscriptEntry, error)
}

// SeenMarker is the WRITE port for the unseen-result flag (B6 item 2).
//
// Deliberately separate from Repository, for the same reason PendingReviewLister
// is separate from the weakness-upload Repository: three fakes across the http
// tests implement Repository, and widening it would break all of them for a
// write none of them performs. The pg TranscriptRepo satisfies both.
//
// Scoping is not optional here. RLS scopes the TENANT; the explicit gcid
// argument scopes the LEARNER, and on a write that argument is the only thing
// standing between one learner and another learner's row.
type SeenMarker interface {
	// MarkSeen stamps the first-seen moment on one entry belonging to one
	// learner. Idempotent and one-way: a second call on an already-opened
	// entry is SUCCESS and leaves the original stamp untouched.
	MarkSeen(ctx context.Context, tenantID, gcid, entryID string, at time.Time) error
}
