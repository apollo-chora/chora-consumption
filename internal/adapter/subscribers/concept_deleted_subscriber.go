// concept_deleted_subscriber.go — the edge-cleanup cascade for a concept
// soft-delete (CHO-2324). Consumes chora.consumption.concept.deleted.v1 (emitted
// by chora-consumption's own concept delete handler) and soft-deletes every
// concept_edges row incident to the deleted concept, so a removed concept leaves
// no dangling relations.
//
// Per ADR-212 edges are a SEPARATE aggregate from the ConceptNode; the delete
// handler deliberately does NOT cascade inline. This subscriber IS the cascade,
// decoupled via the event so the edge write stays inside the edge aggregate's own
// transaction rather than a cross-aggregate write from the concept handler.
//
// Ack-after-processing (agentic-resilience-d6 Pillar 2): return nil ONLY after
// the cascade persists. The cascade is idempotent at the DB (live-only WHERE), so
// a redelivery soft-deletes 0 additional rows even if the in-pod tracker missed.
package subscribers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// EdgeCascadeDeleter is the narrow port the cascade needs — satisfied by
// pg.EdgeRepo (conceptgraph.EdgeRepository). SoftDeleteByConcept soft-deletes
// every LIVE edge incident to the concept (source OR target), tenant+learner
// scoped, and returns the count soft-deleted.
type EdgeCascadeDeleter interface {
	SoftDeleteByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string, now time.Time) (int64, error)
}

// ConceptDeletedPayload mirrors the fields consumed from
// chora.consumption.concept.deleted.v1.
type ConceptDeletedPayload struct {
	ConceptID   string
	TenantID    string
	LearnerGCID string
}

// ConceptDeletedSubscriber cascades the incident-edge cleanup on a concept
// soft-delete.
type ConceptDeletedSubscriber struct {
	edges   EdgeCascadeDeleter
	tracker *idempotencyTracker
}

// NewConceptDeletedSubscriber constructs the subscriber. edges is required.
func NewConceptDeletedSubscriber(edges EdgeCascadeDeleter) *ConceptDeletedSubscriber {
	return &ConceptDeletedSubscriber{edges: edges, tracker: newIdempotencyTracker()}
}

// Handle processes a chora.consumption.concept.deleted.v1 event: it soft-deletes
// every edge incident to the concept. Scope (tenant + learner) comes from the
// payload (preferred) or the envelope; the RLS session GUCs are stamped on the ctx
// so the repo's ApplySession authorises the write (a push request carries no
// tenant middleware).
//
// Process-then-mark (CHO-2130): a transient repo failure returns UNMARKED so the
// redelivery re-runs (the cascade is idempotent). Fail-loud on a bad envelope or a
// missing concept id → NACK.
func (s *ConceptDeletedSubscriber) Handle(env events.Envelope, p ConceptDeletedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("concept_deleted: %w", err)
	}
	conceptID := strings.TrimSpace(p.ConceptID)
	if conceptID == "" {
		return fmt.Errorf("concept_deleted: concept_id required")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(env.TenantID)
	}
	learnerGCID := strings.TrimSpace(p.LearnerGCID)
	if learnerGCID == "" {
		learnerGCID = strings.TrimSpace(env.GCID)
	}
	if tenantID == "" || learnerGCID == "" {
		return fmt.Errorf("concept_deleted: tenant_id and learner_gcid required for the RLS-scoped cascade")
	}
	if s.tracker.seen(env.EventID) {
		return nil // idempotent: already processed in this pod
	}

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), tenantID), learnerGCID)
	if _, err := s.edges.SoftDeleteByConcept(ctx, tenantID, learnerGCID, conceptID, time.Now().UTC()); err != nil {
		return fmt.Errorf("concept_deleted: soft_delete_by_concept: %w", err)
	}
	s.tracker.mark(env.EventID)
	return nil
}
