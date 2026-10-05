package learner_profile

import "context"

// ProjectionRepo persists the LearnerProfile read-model. Every method is
// tenant-scoped via the RLS session GUC (the pg adapter calls rls.ApplySession
// first, mirroring learner_weakness); cross-DB queries are forbidden — the
// projection is fed only by verified Pub/Sub events.
type ProjectionRepo interface {
	// UpsertFact idempotently writes a verified fact. Idempotent on BOTH the
	// natural key (tenant, learner, type, ref) and source_event_id, so a
	// redelivered event never double-writes and a re-occurrence updates in place.
	UpsertFact(ctx context.Context, f *Fact) error

	// AppendActivity appends one entry to the per-learner activity/decision log.
	// Idempotent on source_event_id (a redelivered event logs once).
	AppendActivity(ctx context.Context, e *ActivityEntry) error

	// ListFacts returns all live facts (deleted_at IS NULL) for a learner.
	ListFacts(ctx context.Context, tenantID, learnerGCID string) ([]*Fact, error)

	// RecentActivity returns the most recent activity entries, newest first,
	// bounded by limit.
	RecentActivity(ctx context.Context, tenantID, learnerGCID string, limit int) ([]*ActivityEntry, error)
}
