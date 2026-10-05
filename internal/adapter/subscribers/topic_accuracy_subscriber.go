// topic_accuracy_subscriber.go — consumes
// `chora.consumption.atom_session.completed.v1` events and updates the
// per-(tenant, gcid, topic) MCQ rolling-accuracy projection.
//
// This subscriber owns the WRITE path for the
// `inmem.TopicAccuracyRepo` / production `topic_accuracy` table that
// the Companion daily-dose composer's WEAKNESS slot reads from. Per the
// S6.4 brief (carry-forward from A-Companion S5.1):
//
//	"TopicAccuracy + ActivePathTopics repos depend on subscriber wiring
//	 from S4 atom_session.completed (MCQ accuracy) + S5.2 KG agent's
//	 LearningPath bootstrap (topics). For Phyllis MVP demo they can be
//	 hot-seeded; production wiring is the S5.2 KG agent's or S6 Course
//	 Application work — A-Companion leaves the read path ready but the
//	 write path is owned upstream."
//
// Idempotency model:
//
//   - Envelope.event_id idempotency: at-least-once Pub/Sub delivery
//     produces duplicates. The shared idempotencyTracker drops them.
//   - Domain idempotency: the canonical key is (gcid, atom_session_id).
//     A republished event with a fresh event_id but the same session
//     MUST NOT double-count. Tracked separately via sessionTracker.
//
// Eligibility:
//
//   - Only MCQ atoms with a non-empty CorrectOptionID contribute (the
//     IsMCQ gradability check). Non-MCQ atoms (text / video) are silently
//     skipped — accuracy is not a meaningful signal for non-graded content.
//   - When the atom_index projection has not yet hydrated for a given
//     atom_id (atom_index.ErrNotFound — out-of-order Pub/Sub delivery per
//     the S1.3 reorder window), the handler drops the attempt without
//     erroring. The event is acked so it does not retry forever; the
//     AtomCreated subscriber hydrates the projection later, but Pub/Sub
//     never re-emits the completed event.
//   - A TRANSIENT atom_index lookup failure (anything other than
//     ErrNotFound) NACKs unmarked (CHO-2130) — ack-dropping it consumed a
//     countable attempt.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// AtomSessionCompletedPayload mirrors the Protobuf-encoded body of
// `chora.consumption.atom_session.completed.v1`. The full schema lives
// at chora-contracts/proto/events/consumption/atom_session.proto; we
// mirror only the fields the projection needs.
type AtomSessionCompletedPayload struct {
	SessionID   string
	AtomID      string
	LearnerGCID string
	TenantID    string
	IsCorrect   bool
	// ReviewDue is true when this completion is the answer to a spaced-
	// repetition review prompt (per ADR-149 §EXP economy ebbinghaus_review
	// derived path). Set by the upstream publisher when SM-2 marks the
	// atom as due. Optional — defaults to false.
	ReviewDue   bool
	Traceparent string
	Tracestate  string
	OccurredAt  TimeAlias
}

// TimeAlias decouples this file from a hard `time.Time` import while
// still letting test code inject `time.Now().UTC()` values. We alias
// inside the package via topic_accuracy_subscriber_aliases.go to keep
// the public payload type ergonomic.
type TimeAlias = timeAlias

// TopicAccuracySubscriber projects MCQ accuracy from completed atom
// sessions into the TopicAccuracyRepo.
//
// Two dedup layers (both backed by idempotent.Store per W1.8):
//   - envIDs: envelope.event_id (at-least-once Pub/Sub redelivery)
//   - sessions: domain key (tenant|gcid|session_id) for republished events
//     with a fresh event_id but the same session
//
// CHO-2167: `accuracy` is the DOMAIN PORT, not the in-memory adapter. It was
// previously the concrete *inmem.TopicAccuracyRepo, which is what kept the
// durable pg adapter out of the dose composer's read path.
type TopicAccuracySubscriber struct {
	accuracy topic_accuracy.Repository
	atoms    atom_index.Repo
	envIDs   *idempotencyTracker
	sessions *idempotencyTracker
}

// NewTopicAccuracySubscriber constructs the subscriber over the port.
func NewTopicAccuracySubscriber(accuracy topic_accuracy.Repository, atoms atom_index.Repo) *TopicAccuracySubscriber {
	return &TopicAccuracySubscriber{
		accuracy: accuracy,
		atoms:    atoms,
		envIDs:   newIdempotencyTracker(),
		sessions: newIdempotencyTracker(),
	}
}

// Handle records an MCQ attempt against the topic_accuracy projection.
// Returns nil on success or deliberate no-op (duplicate / non-MCQ /
// unclassified / not-yet-hydrated atom). Returns an error (Pub/Sub NACK) on
// an incomplete envelope or a TRANSIENT atom_index lookup failure.
//
// Process-then-mark (CHO-2130, pattern per CHO-2107): both dedup layers peek
// via seen() at entry and are mark()ed only after the attempt landed (or the
// drop is deliberate) — the previous claim-first markSeen burned both keys
// before the eligibility read, so any failure after the claim swallowed the
// redelivery and the countable attempt was lost.
//
// Transient-vs-notfound (CHO-2130 honest NACK): atom_index.ErrNotFound is
// LEGIT out-of-order hydration (atom.created not yet projected per the S1.3
// reorder window) — ack, because Pub/Sub never re-emits the completed event.
// Any OTHER Get error is transient infra — NACK unmarked so the redelivery
// re-counts instead of ack-dropping the attempt.
func (s *TopicAccuracySubscriber) Handle(ctx context.Context, env events.Envelope, p AtomSessionCompletedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.envIDs.seen(env.EventID) {
		return nil // envelope-level dedup: at-least-once retry
	}

	// Domain idempotency: the canonical key per the S6.4 brief is
	// (gcid, atom_session_id). Re-emitted events with a fresh event_id
	// must NOT double-count.
	sessionKey := sessionDedupKey(p.TenantID, p.LearnerGCID, p.SessionID)
	if sessionKey != "" && s.sessions.seen(sessionKey) {
		s.envIDs.mark(env.EventID)
		return nil // republished session — already counted
	}

	// Eligibility: only MCQ atoms feed accuracy.
	atom, err := s.atoms.Get(ctx, p.AtomID)
	if err != nil && !errors.Is(err, atom_index.ErrNotFound) {
		return fmt.Errorf("topic_accuracy subscriber: atom lookup %q: %w", p.AtomID, err)
	}
	if atom == nil {
		s.envIDs.mark(env.EventID)
		return nil // out-of-order hydration — deliberate drop, processing concluded
	}
	if !atom.IsMCQ() {
		s.envIDs.mark(env.EventID)
		return nil // text/video atoms are not gradable
	}
	topic := atom.PrimaryTopic()
	if strings.TrimSpace(topic) == "" {
		s.envIDs.mark(env.EventID)
		return nil // unclassified atom — no topic to score against
	}

	// Fail-loud (CHO-2167): the write is fallible now that the durable adapter
	// is bindable. A dropped error here would ACK the event and silently lose a
	// countable attempt — and, because the marks below would never run, would
	// also poison the redelivery. NACK unmarked instead.
	if err := s.accuracy.RecordAttempt(ctx, p.TenantID, p.LearnerGCID, topic, p.IsCorrect); err != nil {
		return fmt.Errorf("topic_accuracy subscriber: record attempt (topic=%q): %w", topic, err)
	}
	s.envIDs.mark(env.EventID)
	if sessionKey != "" {
		s.sessions.mark(sessionKey)
	}
	return nil
}

// sessionDedupKey builds the durable (tenant, gcid, session_id) domain dedup
// key, or "" when sessionID is blank (no dedup possible — acceptable for raw
// fixtures). The "session:" prefix scopes the key to avoid collision with
// envID: records in the same idempotency_keys table.
func sessionDedupKey(tenantID, gcid, sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	return "session:" + tenantID + "|" + gcid + "|" + sessionID
}
