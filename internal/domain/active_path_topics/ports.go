// Package active_path_topics owns the persistence port for the
// `active_path_topics` projection — the topic-set covered by a learner's
// active LearningPath(s), which feeds the daily dose's CURIOSITY slot.
//
// Why this port exists (CHO-2167, closing CHO-1471):
//
// The projection shipped with BOTH a pg adapter (durable, RLS-bound,
// migration 0004) and an in-memory adapter — but the composition root held the
// CONCRETE *inmem.ActivePathTopicsRepo type on Server/ExtServer, so the pg
// adapter was unreachable and never bound in production. Hexagonal direction
// was inverted: the application layer depended on an adapter instead of on a
// port the adapters satisfy. The result was a projection that looked durable
// (it has a table, it has migrations, it has an adapter) but lived only in the
// pod's heap — and, because its subscriber was never dispatched either, was in
// fact never written at all (0 rows in chora_consumption, verified 2026-07-14).
//
// The domain owns this port; adapters depend inward on it, never the reverse.
//
// All methods take a context: the pg adapter establishes the per-request RLS
// session from it (rls.ApplySession reads tenant_id + gcid via tracing), and
// the RLS policies on active_path_topics are live for the app role
// (chora_consumption_app_rw is NOT the table owner and has rolbypassrls=f), so
// a missing tenant context means every write is rejected and every read returns
// zero rows. All methods return an error: a projection write that fails MUST
// surface (NACK) rather than silently drop the learner's topics.
package active_path_topics

import "context"

// Repository persists the per-(tenant, gcid, learning_path_id, topic_tag)
// projection. Satisfied by the pg adapter (production) and the in-memory
// adapter (dev/test fallback).
type Repository interface {
	// RecordPathTopics upserts one row per (learner, path, topic). Empty
	// topics is a no-op. Idempotent: re-recording the same topics for the
	// same path is a no-op (ON CONFLICT DO NOTHING on the PK), which is what
	// makes at-least-once Pub/Sub redelivery safe.
	//
	// learningPathID is part of the primary key and is NOT NULL — the same
	// topic legitimately appears on several of a learner's paths, and
	// GetTopics deduplicates back to a topic-set on read.
	RecordPathTopics(ctx context.Context, tenantID, gcid, learningPathID string, topics []string) error

	// GetTopics returns the deduplicated topic-set across the learner's
	// active paths. Always returns a non-nil map on success.
	GetTopics(ctx context.Context, tenantID, gcid string) (map[string]bool, error)
}
