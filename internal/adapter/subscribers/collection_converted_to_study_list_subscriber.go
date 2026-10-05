// collection_converted_to_study_list_subscriber.go — consumes
// `chora.creation.collection.converted_to_study_list.v1` and derives a
// spaced-repetition LearningPath from a curated chora-creation Collection.
//
// ADR-233 (study-list provenance + the consumption/distribution boundary) +
// spec-001 US5 (FR-030/FR-031, T065-T070).
//
// Boundary + safety notes — every one of these is load-bearing:
//
//   - NO consent re-gate. The atom_ids on the event are ALREADY consent-gated by
//     chora-creation (ADR-233 D10: add-time gate + convert-time gate; D11:
//     per-atom exclusions named in the convert response, zero survivors → 409
//     upstream). The payload is AUTHORITATIVE. Re-gating here would need a
//     chora_creation read — FORBIDDEN (cross-DB, ddd-enforcement HARD RULE #1).
//
//   - NO sm2_states seeding. Explicitly REJECTED in ADR-233's alternatives:
//     it fabricates review history for atoms the learner has never seen and
//     corrupts SM-2's easiness-factor / interval semantics. New material enters
//     via the dose's CURIOSITY slot and earns its SM-2 row on first answer. This
//     subscriber therefore holds NO retention/SM-2 collaborator at all (guarded
//     by TestCollectionConverted_DoesNotSeedSM2).
//
//   - 🔴 IT MUST PUBLISH learning_path.bootstrapped.v1. That event is what feeds
//     `active_path_topics`, which feeds the daily dose's CURIOSITY slot. The REST
//     learning_path.New() lane does NOT emit it. Without this publish the study
//     list reaches NO dose slot at all — it is INERT, and "converted to a study
//     list" becomes a lie. This is the single most important line in the file.
//
//   - Idempotency is the DURABLE domain anchor (study_list_event_id), never an
//     in-process tracker alone — the EnrollmentCreatedSubscriber R1 lesson: a
//     claim-first tracker burns its dedupe key BEFORE the write is durable, so a
//     transient failure swallows the redelivery and strands the conversion
//     forever.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// TopicCollectionConvertedToStudyList is the inbound topic this subscriber owns.
const TopicCollectionConvertedToStudyList = "chora.creation.collection.converted_to_study_list.v1"

// CollectionConvertedToStudyListPayload mirrors
// chora.creation.v1.CollectionConvertedToStudyList (BINARY, schema-bound).
// See chora-contracts/proto/events/creation/collection.proto.
type CollectionConvertedToStudyListPayload struct {
	CollectionID string
	OwnerGCID    string
	// AtomIDs is the collection's ENTITLED atom set — already consent-gated by
	// chora-creation. Order preserved from the collection.
	AtomIDs []string
	// StudyListEventID is the durable idempotency anchor (defaults to the
	// event_id of the convert event, per the proto).
	StudyListEventID string
}

// CollectionConvertedToStudyListSubscriber derives the study-list LearningPath.
//
// Fields are deliberately minimal: a path repo and a publisher. Any SM-2 /
// retention collaborator here would be an ADR-233 violation (see file header).
type CollectionConvertedToStudyListSubscriber struct {
	paths     learning_path.Repo
	publisher events.Publisher
	tracker   *idempotencyTracker
}

// NewCollectionConvertedToStudyListSubscriber constructs the subscriber.
func NewCollectionConvertedToStudyListSubscriber(paths learning_path.Repo, publisher events.Publisher) *CollectionConvertedToStudyListSubscriber {
	return &CollectionConvertedToStudyListSubscriber{
		paths:     paths,
		publisher: publisher,
		tracker:   newIdempotencyTracker(),
	}
}

// Handle derives (or additively re-syncs) the study-list path.
//
// Idempotency is LAYERED, authoritative-first:
//
//  1. The DURABLE domain anchor — GetByStudyListEventID(study_list_event_id).
//     Survives pod restart AND replica fan-out because it is a row in
//     chora_consumption, not process memory. This is the authoritative check.
//  2. The event-id tracker as a cheap in-front peek, using the claim-first-SAFE
//     seen → process → mark split (never the legacy claim-first markSeen, which
//     burns the key before the side effect lands: a transient failure then
//     ack-drops the redelivery and the conversion is lost forever).
//
// Errors return UNMARKED (→ NACK → Pub/Sub redelivery). Duplicates are absorbed
// by the durable anchor + the idempotent AppendAtom, so replay is always safe.
func (s *CollectionConvertedToStudyListSubscriber) Handle(env events.Envelope, p CollectionConvertedToStudyListPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("collection_converted subscriber: %w", err)
	}
	if strings.TrimSpace(p.CollectionID) == "" {
		return errors.New("collection_converted subscriber: collection_id required")
	}

	// The owner rides the payload; fall back to the envelope gcid (the converting
	// actor) when the producer omitted it.
	owner := strings.TrimSpace(p.OwnerGCID)
	if owner == "" {
		owner = strings.TrimSpace(env.GCID)
	}
	if owner == "" {
		return errors.New("collection_converted subscriber: owner_gcid required (payload or envelope gcid)")
	}

	// The dedupe anchor defaults to the event_id (per the proto contract).
	anchor := strings.TrimSpace(p.StudyListEventID)
	if anchor == "" {
		anchor = strings.TrimSpace(env.EventID)
	}

	// 🔴 RLS: Pub/Sub push requests carry NO tenant middleware context. The pg
	// repos run rls.ApplySession, which reads the tenant from the CONTEXT — a bare
	// context.Background() yields rls.ErrNoTenantContext, so EVERY event would 500
	// → Pub/Sub retry → DLQ (the CHO-1612 bug, which silently drained an entire
	// curriculum projection). Stamp the envelope tenant so `SET LOCAL
	// chora.tenant_id` lands. Mirrors course_content_subscriber.go.
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), env.TenantID), owner)

	// (2) cheap in-front peek — non-claiming.
	if s.tracker.seen(env.EventID) {
		return nil // duplicate — already converted
	}

	// (1) THE authoritative check: the durable domain anchor.
	if anchor != "" {
		if existing, err := s.paths.GetByStudyListEventID(ctx, anchor); err == nil && existing != nil {
			s.tracker.mark(env.EventID)
			return nil // this exact conversion already landed — ack, no-op
		}
	}

	// Get-or-create by (tenant, owner, source collection). ADR-233 D4: a
	// re-convert RE-SYNCS the existing path; it never forks a second one.
	path, err := s.paths.GetBySourceCollection(ctx, env.TenantID, owner, p.CollectionID)
	created := false
	if err != nil || path == nil {
		path, err = learning_path.NewFromCollection(learning_path.StudyListParams{
			TenantID:         env.TenantID,
			OwnerGCID:        owner,
			CollectionID:     p.CollectionID,
			AtomIDs:          p.AtomIDs,
			StudyListEventID: anchor,
			Now:              env.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("collection_converted subscriber: %w", err)
		}
		created = true
	} else {
		// ADR-233 D4 — ADDITIVE re-sync. AppendAtom is idempotent and already
		// reopens a completed path. Atoms REMOVED from the source are NEVER
		// retracted (the learner may have progress in them), and CurrentIndex /
		// existing progress is untouched.
		for _, atomID := range p.AtomIDs {
			path.AppendAtom(atomID, env.OccurredAt)
		}
		// Re-anchor to the conversion event we are applying, so a redelivery of
		// THIS event is caught by the durable anchor with no TTL.
		path.StudyListEventID = anchor
	}

	if err := s.paths.Save(ctx, path); err != nil {
		return fmt.Errorf("collection_converted subscriber: save path: %w", err)
	}

	// 🔴 THE LOAD-BEARING PUBLISH. `learning_path.bootstrapped.v1` →
	// active_path_topics → the daily dose's CURIOSITY slot. The REST
	// learning_path.New() lane does NOT emit this, so without it the derived study
	// list reaches NO dose slot and the conversion is inert.
	outbound := events.NewEnvelope(
		env.TenantID,
		owner,
		env.Traceparent,
		env.Tracestate,
		"learning_path:"+path.PathID,
	)
	if err := s.publisher.Publish(events.TopicLearningPathBootstrapped, outbound, map[string]any{
		"path_id":        path.PathID,
		"learner_gcid":   owner,
		"source_type":    learning_path.SourceTypeCollection,
		"source_id":      p.CollectionID,
		"collection_id":  p.CollectionID,
		"traversal_mode": learning_path.TraversalModeSpaced,
		// atom_ids — REQUIRED by ActivePathTopicsSubscriber, which resolves each
		// atom's topic_tags from atom_index to seed the dose's CURIOSITY slot.
		// Publishing only atom_count (an int) made the study list INERT: the
		// consumer decoded an empty atom list and wrote nothing. Proven live —
		// the push arrived, ran in 810µs and produced zero rows.
		"atom_ids":          path.AtomIDs,
		"atom_count":        len(path.AtomIDs),
		"created":           created,
		"upstream_event_id": env.EventID,
		"source_event":      TopicCollectionConvertedToStudyList,
	}); err != nil {
		// Fail LOUD: the path is durably saved, but an unpublished bootstrapped
		// event means an INERT study list. NACK so Pub/Sub redelivers — the
		// re-run is safe (the anchor + idempotent AppendAtom absorb it) and the
		// event gets a second chance to land.
		return fmt.Errorf("collection_converted subscriber: publish bootstrapped: %w", err)
	}

	s.tracker.mark(env.EventID)
	return nil
}
