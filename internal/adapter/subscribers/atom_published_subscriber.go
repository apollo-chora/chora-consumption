// atom_published_subscriber.go — projects atom PLAYABILITY from
// chora.creation.atom.published.v1 into the local atom_index projection
// (CHO-1968).
//
// This is the 3rd consumer of atom.published.v1 in chora-consumption:
//
//   - CompanionGrowthSubscriber.HandleAtomPublished — awards authoring EXP
//     (companion_growth_subscriber.go), unrelated + untouched.
//   - AtomPublishedKGSubscriber                    — invalidates the KG hexagon
//     fog cache (kg_invalidation_subscriber.go), unrelated + untouched.
//   - AtomPublishedSubscriber (this file)          — flips atom_index DRAFT →
//     PUBLISHED and stamps the answer key so the daily dose can filter to
//     published + answerable atoms only.
//
// Why a dedicated flip (vs reusing atom.created): the atom.created projection
// seeds a DRAFT row whose answer key is empty/unreliable (the atom may still be
// authored when created). atom.published is the authoritative signal that the
// atom is serveable AND carries the finalised answer key. MarkPublished is a
// TARGETED update (status + key only) so the title / topic_tags / course_id the
// created event projected survive the flip.
//
// Per ddd-enforcement: cross-DB queries FORBIDDEN — atom provenance rides the
// inbound event; no chora_creation reads. Per agentic-resilience-d6: the repo
// mutation must succeed before the message is acked (a MarkPublished error
// propagates as a Pub/Sub nack).
package subscribers

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// AtomPublishedIndexPayload mirrors the fields of chora.creation.atom.published.v1
// the playability projection needs. Named distinctly from the growth-EXP
// AtomPublishedPayload (same package) which only needs the author gcid.
//
// Status + HasOpenEndedQuestion are projected for completeness / observability;
// the flip always sets atom_index.StatusPublished (the topic is semantically
// "always published" — see atom.proto) and derives answerability from AtomType.
type AtomPublishedIndexPayload struct {
	AtomID               string
	TenantID             string
	AtomType             string // domain short type (e.g. "mcq", "essay") — protodecode.atomTypeToDomain
	Status               string // event-declared status (always "published" for this topic)
	CorrectOptionID      string // stable MCQ answer-key option id; "" for non-MCQ
	AnswerCount          int
	HasOpenEndedQuestion bool
	CognitiveLevel       string // ORIGINAL-Bloom label (WS-C3); "" = unknown
}

// AtomPublishedSubscriber flips the atom_index projection to PUBLISHED.
type AtomPublishedSubscriber struct {
	repo    atom_index.Repo
	tracker *idempotencyTracker
}

// NewAtomPublishedSubscriber constructs the subscriber. repo is the atom_index
// projection repo (MarkPublished does the targeted flip).
func NewAtomPublishedSubscriber(repo atom_index.Repo) *AtomPublishedSubscriber {
	return &AtomPublishedSubscriber{repo: repo, tracker: newIdempotencyTracker()}
}

// Handle flips the atom to PUBLISHED + stamps the answer key. Envelope is
// validated FIRST; duplicate event_ids are deduped before the repo mutation;
// a MarkPublished error propagates (nack) so the atom is never silently left
// draft. ctx carries the RLS tenant session for the pg-backed repo (set by the
// push dispatcher from the event tenant).
//
// Process-then-mark (CHO-2130): seen() peek at entry, mark() only after the
// flip landed — the previous claim-first markSeen burned the key before
// MarkPublished, so a transient failure swallowed the redelivery and left the
// atom stuck DRAFT (never serveable). Errors return UNMARKED (NACK); the
// targeted MarkPublished update absorbs a duplicate flip.
func (s *AtomPublishedSubscriber) Handle(ctx context.Context, env events.Envelope, p AtomPublishedIndexPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("atom_published subscriber: %w", err)
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return fmt.Errorf("atom_published subscriber: atom_id required")
	}
	if s.tracker.seen(env.EventID) {
		return nil // duplicate — already flipped
	}
	if err := s.repo.MarkPublished(ctx, p.AtomID, atom_index.StatusPublished, p.AtomType, p.CorrectOptionID, p.AnswerCount, p.CognitiveLevel); err != nil {
		return fmt.Errorf("atom_published subscriber: mark published: %w", err)
	}
	s.tracker.mark(env.EventID)
	return nil
}
