package topic_retention

import "context"

// Repository persists the per-(tenant_id, gcid, topic_id) Ebbinghaus retention
// aggregate. Every method is tenant-scoped via the RLS session GUC (the pg
// adapter calls rls.ApplySession first, mirroring goal / learner_profile);
// per-learner scoping is an explicit gcid predicate. Cross-DB queries are
// forbidden (ddd-enforcement #1) — the TopicScore lives wholly in
// chora_consumption.
//
// The in-memory adapter (dev / test) and the pg adapter (prod, durable) both
// satisfy this port; cmd/server swaps the in-memory default for the pg repo at
// boot via ExtServer.WireTopicRetentionPg so the score survives pod restarts
// and stays cross-tenant-safe.
type Repository interface {
	// Save upserts a TopicScore by its (tenant_id, gcid, topic_id) identity.
	Save(ctx context.Context, s *TopicScore) error

	// Get returns the live score for (tenant_id, gcid, topic_id), or
	// (nil, nil) when none matches (not an error — the caller seeds a fresh
	// score on a nil result). Soft-deleted rows read as not found.
	Get(ctx context.Context, tenantID, gcid, topicID string) (*TopicScore, error)

	// ListByLearner returns the learner's live scores, newest-reviewed first,
	// capped at limit. limit <= 0 means unbounded.
	ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*TopicScore, error)
}
