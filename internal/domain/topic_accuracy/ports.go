// Package topic_accuracy owns the persistence port for the `topic_accuracy`
// projection — per-(tenant, gcid, topic_tag) rolling MCQ counters, which feed
// the daily dose's WEAKNESS slot.
//
// Why this port exists (CHO-2167, closing CHO-1471):
//
// Like active_path_topics, this projection had a durable pg adapter that the
// composition root could not bind, because Server/ExtServer held the CONCRETE
// *inmem.TopicAccuracyRepo. The consequence here was worse than volatility, it
// was a SPLIT-BRAIN: DerivedWeaknessProjector (a different consumer, wired in
// cmd/server/derived_weakness_wiring.go) DOES hold the pg adapter and has been
// writing real rows to chora_consumption.topic_accuracy in production — while
// the daily-dose composer read a per-pod in-memory map that started empty and
// was fed by a subscriber nothing ever dispatched. Two stores, one projection,
// and the read side was on the wrong one.
//
// The domain owns this port; adapters depend inward on it, never the reverse.
//
// Interface segregation: this is the surface the dose composer + the
// TopicAccuracySubscriber need. DerivedWeaknessProjector needs a strict
// superset (it also drives the DB-level (gcid, session_id) dedup inside its
// fold transaction), so subscribers.AccuracyRecorder embeds this port and adds
// MarkSessionSeen. Only the pg adapter satisfies that larger role — which is
// correct: durable dedup is meaningless against a heap map.
package topic_accuracy

import "context"

// Repository persists the rolling MCQ accuracy projection. Satisfied by the pg
// adapter (production) and the in-memory adapter (dev/test fallback).
type Repository interface {
	// RecordAttempt atomically increments attempts (+1) and, when isCorrect,
	// correct (+1) for one (learner, topic).
	RecordAttempt(ctx context.Context, tenantID, gcid, topicTag string, isCorrect bool) error

	// GetByLearner returns map[topic_tag]accuracy with accuracy ∈ [0, 1],
	// skipping topics with zero attempts. Always returns a non-nil map on
	// success.
	GetByLearner(ctx context.Context, tenantID, gcid string) (map[string]float64, error)
}
