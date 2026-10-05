// active_path_topics_subscriber.go — consumes
// `chora.consumption.learning_path.bootstrapped.v1` and
// `chora.consumption.learning_path.advanced.v1` events and unions the
// LearningPath's covered topics into the per-(tenant, gcid)
// active_path_topics projection.
//
// This subscriber owns the WRITE path for the
// `inmem.ActivePathTopicsRepo` / production `active_path_topics` table
// that the Companion daily-dose composer's CURIOSITY slot reads. Per the
// S6.4 brief (carry-forward from A-Companion S5.1):
//
//	"...active_path_topics(...) — On `bootstrapped`: insert all topics
//	 from path. On `advanced`: update current_position; mark topics as
//	 'explored' once past."
//
// Idempotency model:
//
//   - Envelope.event_id idempotency: at-least-once Pub/Sub delivery
//     produces duplicates. The shared idempotencyTracker drops them.
//   - Domain idempotency: `RecordPathTopics` is a SET-union — the
//     repo de-dupes naturally. Calling RecordPathTopics with the same
//     topics twice is a no-op.
//
// Topic resolution:
//
// Topics are derived from the local atom_index projection (NEVER
// cross-DB queried from chora_creation, per ddd-enforcement). When the
// atom_index has not yet hydrated for an atom_id (S1.3 reorder window),
// that atom contributes nothing to the projection on bootstrap; the
// follow-up HandleAdvance call re-resolves the atom_id at advance
// time, which catches the late-arriving projection.
package subscribers

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// LearningPathBootstrappedPayload mirrors the Protobuf-encoded body of
// `chora.consumption.learning_path.bootstrapped.v1`. Emitted by the
// EnrollmentCreatedSubscriber on first bootstrap. The full schema lives
// at chora-contracts/proto/events/consumption/learning_path.proto.
type LearningPathBootstrappedPayload struct {
	PathID       string
	CourseID     string
	LearnerGCID  string
	TenantID     string
	EnrollmentID string
	AtomIDs      []string
	OccurredAt   timeAlias
}

// LearningPathAdvancedPayload mirrors the Protobuf-encoded body of
// `chora.consumption.learning_path.advanced.v1`. Emitted by the
// SessionCompletionHandler when an MCQ atom is correctly answered.
type LearningPathAdvancedPayload struct {
	PathID       string
	CourseID     string
	LearnerGCID  string
	TenantID     string
	AtomID       string
	CurrentIndex int
	TotalAtoms   int
	OccurredAt   timeAlias
}

// ActivePathTopicsSubscriber projects the topic-set covered by a
// learner's active LearningPaths.
//
// CHO-2167: `topics` is the DOMAIN PORT, not the in-memory adapter. It was
// previously the concrete *inmem.ActivePathTopicsRepo, which is what made the
// durable pg adapter unbindable from the composition root.
type ActivePathTopicsSubscriber struct {
	topics active_path_topics.Repository
	atoms  atom_index.Repo
	envIDs *idempotencyTracker
}

// NewActivePathTopicsSubscriber constructs the subscriber over the port.
func NewActivePathTopicsSubscriber(topics active_path_topics.Repository, atoms atom_index.Repo) *ActivePathTopicsSubscriber {
	return &ActivePathTopicsSubscriber{
		topics: topics,
		atoms:  atoms,
		envIDs: newIdempotencyTracker(),
	}
}

// HandleBootstrap unions the topics of every atom in `payload.AtomIDs`
// into the projection. Tolerates missing atom_index entries (returns
// early after partial-resolve; HandleAdvance picks up the slack).
//
// Process-then-mark (CHO-2167, pattern per CHO-2130): the envelope id is only
// marked AFTER the projection write lands (or the drop is deliberate). The
// previous claim-first `markSeen` burned the id BEFORE the write — harmless
// against an in-memory map that cannot fail, but against Postgres it means a
// transient error ACKs the event and the redelivery (the only recovery) is
// dropped as a duplicate. The learner's topics would be gone for good.
func (s *ActivePathTopicsSubscriber) HandleBootstrap(ctx context.Context, env events.Envelope, p LearningPathBootstrappedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.envIDs.seen(env.EventID) {
		return nil // envelope-level dedup: at-least-once retry
	}
	topics, err := s.resolveTopics(ctx, p.AtomIDs)
	if err != nil {
		return err // TRANSIENT atom_index failure — NACK unmarked
	}
	if len(topics) == 0 {
		s.envIDs.mark(env.EventID)
		return nil // nothing resolvable yet — deliberate drop, HandleAdvance retries
	}
	if err := s.recordTopics(ctx, p.TenantID, p.LearnerGCID, p.PathID, topics); err != nil {
		return err
	}
	s.envIDs.mark(env.EventID)
	return nil
}

// HandleAdvance resolves the topic of the just-advanced atom and
// unions it into the projection. Triggered by
// `chora.consumption.learning_path.advanced.v1`. Useful for catching
// topics whose atom_index entries arrived AFTER the bootstrap event.
func (s *ActivePathTopicsSubscriber) HandleAdvance(ctx context.Context, env events.Envelope, p LearningPathAdvancedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.envIDs.seen(env.EventID) {
		return nil
	}
	if p.AtomID == "" {
		s.envIDs.mark(env.EventID)
		return nil
	}
	topics, err := s.resolveTopics(ctx, []string{p.AtomID})
	if err != nil {
		return err // TRANSIENT atom_index failure — NACK unmarked
	}
	if len(topics) == 0 {
		s.envIDs.mark(env.EventID)
		return nil
	}
	if err := s.recordTopics(ctx, p.TenantID, p.LearnerGCID, p.PathID, topics); err != nil {
		return err
	}
	s.envIDs.mark(env.EventID)
	return nil
}

// recordTopics writes through the port and fails loud. learning_path_id is part
// of the durable projection's PRIMARY KEY and is NOT NULL — dropping it (as the
// pre-CHO-2167 call did) fails every real write.
func (s *ActivePathTopicsSubscriber) recordTopics(ctx context.Context, tenantID, gcid, pathID string, topics []string) error {
	if err := s.topics.RecordPathTopics(ctx, tenantID, gcid, pathID, topics); err != nil {
		return fmt.Errorf("active_path_topics subscriber: record topics (path=%q): %w", pathID, err)
	}
	return nil
}

// resolveTopics looks each atom up in the atom_index and returns the
// dedup-by-set list of topic_tags.
//
// Not-found is LEGIT out-of-order hydration (the atom.created projection has
// not landed yet — the S1.3 reorder window): skip the atom, no error. Any OTHER
// lookup error is transient infra and is returned so the caller NACKs unmarked
// — ack-dropping it would silently shrink the learner's curiosity pool.
func (s *ActivePathTopicsSubscriber) resolveTopics(ctx context.Context, atomIDs []string) ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(atomIDs))
	for _, atomID := range atomIDs {
		atom, err := s.atoms.Get(ctx, atomID)
		if err != nil && !errors.Is(err, atom_index.ErrNotFound) {
			return nil, fmt.Errorf("active_path_topics subscriber: atom lookup %q: %w", atomID, err)
		}
		if atom == nil {
			continue // out-of-order hydration — deliberate skip
		}
		for _, tag := range atom.TopicTags {
			if tag == "" {
				continue
			}
			if _, dup := seen[tag]; dup {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
		}
	}
	return out, nil
}
