// weakness_outputs_generated_subscriber.go - consumes
// chora.consumption.weakness.outputs_generated.v1 (emitted by the ai-kernel
// weakness-analyser crew from inside its DETACHED generation task) and projects
// the generated artifacts into growth_edge_outputs so they reach the learner
// who paid mana for them (ADR-205 WS-7, CHO-2348).
//
// The gap this closes: the crew generated study_aids + a critic-gated
// practice_test, metered both, and then discarded the result. The two kinds
// appeared nowhere outside the orchestrator.
//
// Per ddd-enforcement chora-consumption never reads chora_ai_kernel: this is the
// event seam. The whole batch persists in ONE tx (repo.CreateBatch), so a
// partial failure rolls back and redelivery re-ingests cleanly, and the learner
// sees a delivery appear atomically rather than half-populated.
//
// Idempotency is two-layered because Pub/Sub is at-least-once and a duplicated
// paid-for artifact on the surface is a visible defect:
//   - the inbox key (same-pod, keyed on the event id), and
//   - the partial unique index behind CreateBatch's ON CONFLICT DO NOTHING
//     (cross-pod, and the one that actually holds).
//
// Fail-loud: an unknown kind or malformed content errors -> NACK -> DLQ after
// maxDeliveryAttempts, never a silently dropped artifact. An EMPTY outputs list
// is a valid no-op ack: it means every selected kind was screened out or
// soft-failed, which is a real outcome the producer deliberately still emits so
// this side can tell it apart from "not generated yet".
package subscribers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	geo "github.com/apollo-chora/chora-consumption/internal/domain/growth_edge_output"
)

// GeneratedOutputPayload mirrors one repeated GeneratedLearnerOutput of the
// Protobuf-encoded body (decoded at the push handler).
type GeneratedOutputPayload struct {
	Kind        string
	ContentJSON string
	Metered     bool
}

// WeaknessOutputsGeneratedPayload mirrors the outputs_generated.v1 body.
type WeaknessOutputsGeneratedPayload struct {
	UploadID    string
	TenantID    string
	LearnerGCID string
	Outputs     []GeneratedOutputPayload
	GeneratedAt time.Time
}

// WeaknessOutputsGeneratedSubscriber projects generated artifacts for the learner.
type WeaknessOutputsGeneratedSubscriber struct {
	repo  geo.Repository
	inbox idempotent.Store
	ttl   time.Duration
	newID func() string
}

// NewWeaknessOutputsGeneratedSubscriber constructs the subscriber with an
// in-memory inbox (production wires a PostgresStore via ...WithStore).
func NewWeaknessOutputsGeneratedSubscriber(repo geo.Repository) *WeaknessOutputsGeneratedSubscriber {
	return NewWeaknessOutputsGeneratedSubscriberWithStore(repo, idempotent.NewMemoryStore(), inboxTTL)
}

// NewWeaknessOutputsGeneratedSubscriberWithStore allows injecting a durable
// inbox + TTL.
func NewWeaknessOutputsGeneratedSubscriberWithStore(
	repo geo.Repository, store idempotent.Store, ttl time.Duration,
) *WeaknessOutputsGeneratedSubscriber {
	if store == nil {
		store = idempotent.NewMemoryStore()
	}
	if ttl <= 0 {
		ttl = inboxTTL
	}
	return &WeaknessOutputsGeneratedSubscriber{
		repo: repo, inbox: store, ttl: ttl, newID: domain.NewUUIDv7,
	}
}

// Handle projects the delivery. The whole batch is one idempotent unit keyed on
// the event id.
func (s *WeaknessOutputsGeneratedSubscriber) Handle(
	ctx context.Context, env events.Envelope, p WeaknessOutputsGeneratedPayload,
) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if strings.TrimSpace(p.TenantID) == "" || strings.TrimSpace(p.LearnerGCID) == "" {
		return errors.New("subscribers: weakness.outputs_generated payload missing tenant_id/learner_gcid")
	}
	if strings.TrimSpace(p.UploadID) == "" {
		// Without the upload id the artifacts cannot be correlated to anything
		// the learner can open, so they would be undiscoverable rows.
		return errors.New("subscribers: weakness.outputs_generated payload missing upload_id")
	}
	key := "weakness.outputs_generated:" + env.EventID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.persist(ctx, env, p)
	})
}

func (s *WeaknessOutputsGeneratedSubscriber) persist(
	ctx context.Context, env events.Envelope, p WeaknessOutputsGeneratedPayload,
) error {
	// Tenant + GCID on ctx so the pg repo's rls.ApplySession scopes correctly.
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, p.TenantID), p.LearnerGCID)

	if len(p.Outputs) == 0 {
		// Valid no-op: every selected kind was screened out or soft-failed. The
		// producer still emits so this side can distinguish it from "not yet".
		return nil
	}

	generatedAt := p.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = env.OccurredAt
	}

	outs := make([]geo.Output, 0, len(p.Outputs))
	for _, o := range p.Outputs {
		kind, err := geo.ParseKind(strings.TrimSpace(o.Kind))
		if err != nil {
			// Fail-loud: an unknown kind is a producer/contract skew. Dropping it
			// would recreate the WS-7 gap one artifact at a time.
			return fmt.Errorf("subscribers: weakness.outputs_generated upload %s: %w", p.UploadID, err)
		}
		out, err := geo.New(geo.NewArgs{
			OutputID:      s.newID(),
			TenantID:      p.TenantID,
			LearnerGCID:   p.LearnerGCID,
			UploadID:      p.UploadID,
			Kind:          kind,
			Content:       json.RawMessage(o.ContentJSON),
			Metered:       o.Metered,
			SourceEventID: env.EventID,
			GeneratedAt:   generatedAt,
		})
		if err != nil {
			return fmt.Errorf("subscribers: build generated output %s/%s: %w", p.UploadID, o.Kind, err)
		}
		outs = append(outs, out)
	}

	_, err := s.repo.CreateBatch(ctx, outs)
	return err
}
