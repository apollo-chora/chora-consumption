// concept_suggestion_emitted_subscriber.go — consumes
// chora.consumption.concept_suggestion.emitted.v1 (emitted by the concept-shaped
// fog orchestrator) and persists each proposed concept/edge as a PENDING
// Suggestion for the learner to curate (ADR-212 WS-4, D4).
//
// Per ddd-enforcement chora-consumption never reads chora_ai_kernel — this is
// the event seam. The whole batch is persisted in ONE tx (repo.CreateBatch), so
// a partial failure rolls back and Pub/Sub redelivery re-ingests cleanly; the
// learner also sees the suggestion batch appear atomically.
//
// Idempotency: keyed on the emitted event id via idempotent.Store.Process — the
// key COMMITS only when the whole batch persists, so a transient pg failure
// leaves it unclaimed and redelivery reprocesses (no lost suggestions); a
// redelivered event is skipped. Fail-loud: a malformed item (empty title / bad
// edge class) errors → NACK → DLQ after maxDeliveryAttempts, never a silently
// fabricated/partial suggestion. An EMPTY batch (fog found nothing to suggest)
// is a valid no-op (ack).
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"time"
)

// SuggestedConceptPayload mirrors one repeated concept of the Protobuf-encoded
// chora.consumption.concept_suggestion.emitted.v1 body (decoded at the push handler).
type SuggestedConceptPayload struct {
	Title     string
	Rationale string
	AtomRefs  []string
}

// SuggestedEdgePayload mirrors one repeated edge of the emitted body.
type SuggestedEdgePayload struct {
	SourceConceptID string
	TargetConceptID string
	EdgeClass       string // "hierarchy" | "lateral"
	Rationale       string
}

// ConceptSuggestionEmittedPayload mirrors the emitted.v1 body.
type ConceptSuggestionEmittedPayload struct {
	TenantID       string
	LearnerGCID    string
	CompanionID    string
	MapTheme       string
	FocalConceptID string
	ModelUsed      string
	RunID          string
	Concepts       []SuggestedConceptPayload
	Edges          []SuggestedEdgePayload
}

// ConceptSuggestionEmittedSubscriber persists proposed concepts/edges as pending
// Suggestions.
type ConceptSuggestionEmittedSubscriber struct {
	repo  conceptgraph.SuggestionRepository
	inbox idempotent.Store
	ttl   time.Duration
}

// NewConceptSuggestionEmittedSubscriber constructs the subscriber with an
// in-memory inbox (production wires a PostgresStore via …WithStore for cross-pod
// dedup).
func NewConceptSuggestionEmittedSubscriber(repo conceptgraph.SuggestionRepository) *ConceptSuggestionEmittedSubscriber {
	return NewConceptSuggestionEmittedSubscriberWithStore(repo, idempotent.NewMemoryStore(), inboxTTL)
}

// NewConceptSuggestionEmittedSubscriberWithStore allows injecting a durable
// inbox + TTL.
func NewConceptSuggestionEmittedSubscriberWithStore(repo conceptgraph.SuggestionRepository, store idempotent.Store, ttl time.Duration) *ConceptSuggestionEmittedSubscriber {
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &ConceptSuggestionEmittedSubscriber{repo: repo, inbox: store, ttl: ttl}
}

// Handle persists the proposed concepts/edges as pending Suggestions. The whole
// emitted batch is one idempotent unit keyed on the event id.
func (s *ConceptSuggestionEmittedSubscriber) Handle(ctx context.Context, env events.Envelope, p ConceptSuggestionEmittedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.LearnerGCID) == "" {
		return errors.New("subscribers: concept_suggestion.emitted payload missing tenant_id/learner_gcid")
	}
	key := "concept_suggestion.emitted:" + env.EventID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.persist(ctx, env, p)
	})
}

func (s *ConceptSuggestionEmittedSubscriber) persist(ctx context.Context, env events.Envelope, p ConceptSuggestionEmittedPayload) error {
	// Tenant + GCID on ctx so the pg repo's rls.ApplySession scopes correctly.
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)

	// Durable cross-pod idempotency: because CreateBatch is all-or-nothing, a live
	// suggestion for this event id means the whole batch already landed (the
	// in-memory inbox only guards same-pod redelivery). Probe + skip.
	if existing, err := s.repo.GetBySourceEvent(ctx, p.TenantID, p.LearnerGCID, env.EventID); err != nil {
		return fmt.Errorf("subscribers: probe existing suggestions for event %q: %w", env.EventID, err)
	} else if existing != nil {
		return nil
	}

	sugs := make([]*conceptgraph.Suggestion, 0, len(p.Concepts)+len(p.Edges))
	for _, c := range p.Concepts {
		sug, err := conceptgraph.NewConceptSuggestion(conceptgraph.NewConceptSuggestionInput{
			TenantID: p.TenantID, LearnerGCID: p.LearnerGCID, Title: c.Title, AtomRefs: c.AtomRefs,
			Rationale: c.Rationale, ModelID: p.ModelUsed, RunID: p.RunID,
			SourceCompanionID: p.CompanionID, SourceEventID: env.EventID,
			FocalConceptID: p.FocalConceptID, Now: env.OccurredAt,
		})
		if err != nil {
			// Fail-loud: a malformed suggested concept is a producer bug → NACK.
			return fmt.Errorf("subscribers: build concept suggestion %q: %w", c.Title, err)
		}
		sugs = append(sugs, sug)
	}
	for _, e := range p.Edges {
		sug, err := conceptgraph.NewEdgeSuggestion(conceptgraph.NewEdgeSuggestionInput{
			TenantID: p.TenantID, LearnerGCID: p.LearnerGCID,
			SourceConceptID: e.SourceConceptID, TargetConceptID: e.TargetConceptID,
			Class:     conceptgraph.EdgeClass(strings.TrimSpace(e.EdgeClass)),
			Rationale: e.Rationale, ModelID: p.ModelUsed, RunID: p.RunID,
			SourceCompanionID: p.CompanionID, SourceEventID: env.EventID,
			FocalConceptID: p.FocalConceptID, Now: env.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("subscribers: build edge suggestion %s->%s: %w", e.SourceConceptID, e.TargetConceptID, err)
		}
		sugs = append(sugs, sug)
	}
	if len(sugs) == 0 {
		// Valid no-op: the fog found nothing to suggest around the focal concept.
		return nil
	}
	return s.repo.CreateBatch(ctx, sugs)
}
