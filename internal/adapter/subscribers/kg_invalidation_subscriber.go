// kg_invalidation_subscriber.go — three Pub/Sub inbound subscribers that
// invalidate the KG hexagon fog cache (kg_hexagon_nodes) in response to
// upstream content and retention events.
//
// Subscribers:
//
//   - AtomPublishedKGSubscriber       chora.creation.atom.published.v1 (EXISTS in chora-489812)
//   - AtomRevisionUpdatedKGSubscriber chora.creation.atom.updated.v1 (canonical name; EXISTS + DLQ confirmed 2026-05-26)
//   - UserRetentionShiftedKGSubscriber chora.consumption.user_retention.shifted.v1 (NOT YET PROVISIONED — topic creation tracked in Jira, parent CHO-1452)
//
// Per agentic-resilience-d6 + ddd-enforcement:
//   - ACK (return nil) ONLY AFTER invalidation is persisted to the repo.
//   - Idempotent on event_id: duplicate events are dropped via the
//     shared idempotencyTracker (idempotent.Store, pod-death-safe).
//   - Publish chora.consumption.kg_hexagon_fog.invalidated.v1 per
//     affected row AFTER the repo mutation succeeds.
//
// Cross-DB queries FORBIDDEN: these subscribers operate only on
// chora_consumption tables (hexagon repo). atom_id provenance is
// carried in the inbound event envelope; no chora_creation reads.
//
// Per ddd-enforcement HARD RULE: inter-domain communication ONLY via
// Pub/Sub events — these subscribers are the canonical entry point.
package subscribers

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------------------------------------------------------------------------
// Port interfaces (narrow — only what the subscribers need)
// ---------------------------------------------------------------------------

// KGHexagonInvalidator is the narrow subset of userknowledgegraph.HexagonRepo
// that the invalidation subscribers require. Production wires
// inmem.HexagonRepo / pg.HexagonRepo; tests inject fakes.
type KGHexagonInvalidator interface {
	MarkInvalidatedByFocal(ctx context.Context, atomID string, reason userknowledgegraph.FogInvalidationReason) (count int, err error)
	MarkInvalidatedByUser(ctx context.Context, tenantID, userGCID string, reason userknowledgegraph.FogInvalidationReason) (count int, err error)
	// Find is used after MarkInvalidatedByFocal to gather rows for the
	// invalidation event payload (best-effort; failures are non-fatal).
	Find(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error)
	// FindInvalidatedOlderThan is not used by subscribers directly but
	// must be implemented by the same repo (used by the sweeper cron).
	FindInvalidatedOlderThan(ctx context.Context, cutoff string) ([]*userknowledgegraph.HexagonNode, error)
}

// KGInvalidationPublisher is the narrow event-publisher subset needed by
// the three invalidation subscribers. Implemented by *events.KGPublisher.
type KGInvalidationPublisher interface {
	PublishHexagonFogInvalidated(ctx context.Context, h *userknowledgegraph.HexagonNode) error
}

// ---------------------------------------------------------------------------
// Payload types (mirrors inbound proto payload shape)
// ---------------------------------------------------------------------------

// AtomPublishedKGPayload mirrors the fields consumed from
// chora.creation.atom.published.v1 for the KG invalidation path.
type AtomPublishedKGPayload struct {
	AtomID   string
	TenantID string
}

// AtomRevisionUpdatedKGPayload mirrors chora.creation.atom.updated.v1
// (canonical topic name confirmed 2026-05-26; plan used "revision_updated"
// but live infra uses the shorter "updated" suffix).
type AtomRevisionUpdatedKGPayload struct {
	AtomID   string
	TenantID string
}

// UserRetentionShiftedKGPayload mirrors chora.consumption.user_retention.shifted.v1.
type UserRetentionShiftedKGPayload struct {
	TenantID string
	UserGCID string
}

// ---------------------------------------------------------------------------
// AtomPublishedKGSubscriber
// ---------------------------------------------------------------------------

// AtomPublishedKGSubscriber invalidates kg_hexagon_nodes rows whose
// focal_atom_id OR neighbors_json references the published atom.
//
// Ack-after-process contract: returns nil ONLY after the repo mutation
// succeeds; errors propagate as Pub/Sub nacks (Pub/Sub retries with
// exponential back-off; DLQ after max redeliveries).
type AtomPublishedKGSubscriber struct {
	hexagons KGHexagonInvalidator
	pub      KGInvalidationPublisher
	tracker  *idempotencyTracker
}

// NewAtomPublishedKGSubscriber constructs the subscriber. Both deps required.
func NewAtomPublishedKGSubscriber(hexagons KGHexagonInvalidator, pub KGInvalidationPublisher) *AtomPublishedKGSubscriber {
	return &AtomPublishedKGSubscriber{
		hexagons: hexagons,
		pub:      pub,
		tracker:  newIdempotencyTracker(),
	}
}

// Handle processes a chora.creation.atom.published.v1 event.
//
// Process-then-mark (CHO-2130): seen() peek at entry, mark() only after the
// invalidation persisted — claim-first markSeen swallowed the redelivery
// after a transient repo failure, leaving stale fog hexagons until the next
// unrelated invalidation. Errors return UNMARKED (NACK); the invalidation
// UPDATE is idempotent on a duplicate.
//
// ctx MUST carry the event's tenant (tracing.WithTenantID, stamped by the
// push dispatch): the pg hexagon repo runs rls.ApplySession and fails loud
// without it. The lane's first live delivery (2026-08-16) DLQ-looped on
// exactly that, because the original signature minted a bare
// context.Background() here and the in-memory test repo never noticed.
func (s *AtomPublishedKGSubscriber) Handle(ctx context.Context, env events.Envelope, p AtomPublishedKGPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("atom_published_kg: %w", err)
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return fmt.Errorf("atom_published_kg: atom_id required")
	}
	if s.tracker.seen(env.EventID) {
		return nil // idempotent: already processed
	}

	count, err := s.hexagons.MarkInvalidatedByFocal(ctx, p.AtomID, userknowledgegraph.FogInvalidationReasonAtomPublished)
	if err != nil {
		return fmt.Errorf("atom_published_kg: mark_invalidated_by_focal: %w", err)
	}
	s.tracker.mark(env.EventID)
	// Best-effort: emit invalidated events for each affected row.
	// The count is the number of rows actually transitioned; we emit one
	// event per row to let downstream observability track per-hexagon
	// invalidation. Non-fatal — invalidation already persisted above.
	if count > 0 && s.pub != nil {
		// Emit a synthetic event for the focal row (we don't load all
		// affected rows here to avoid N+1; production wires a sweeper that
		// emits batch events). We emit exactly one event per subscriber call
		// carrying the atom_id as the focal identifier.
		_ = s.pub.PublishHexagonFogInvalidated(ctx, &userknowledgegraph.HexagonNode{
			FocalAtomID:        p.AtomID,
			TenantID:           env.TenantID,
			InvalidationReason: userknowledgegraph.FogInvalidationReasonAtomPublished,
		})
	}
	return nil
}

// ---------------------------------------------------------------------------
// AtomRevisionUpdatedKGSubscriber
// ---------------------------------------------------------------------------

// AtomRevisionUpdatedKGSubscriber invalidates kg_hexagon_nodes rows when
// an atom's content revision is updated. Same invalidation scope as
// AtomPublishedKGSubscriber; reason differs for audit trail.
type AtomRevisionUpdatedKGSubscriber struct {
	hexagons KGHexagonInvalidator
	pub      KGInvalidationPublisher
	tracker  *idempotencyTracker
}

// NewAtomRevisionUpdatedKGSubscriber constructs the subscriber.
func NewAtomRevisionUpdatedKGSubscriber(hexagons KGHexagonInvalidator, pub KGInvalidationPublisher) *AtomRevisionUpdatedKGSubscriber {
	return &AtomRevisionUpdatedKGSubscriber{
		hexagons: hexagons,
		pub:      pub,
		tracker:  newIdempotencyTracker(),
	}
}

// Handle processes a chora.creation.atom.updated.v1 event (canonical name).
// Process-then-mark per CHO-2130; ctx MUST carry the event tenant for RLS
// (see AtomPublishedKGSubscriber.Handle).
func (s *AtomRevisionUpdatedKGSubscriber) Handle(ctx context.Context, env events.Envelope, p AtomRevisionUpdatedKGPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("atom_revision_updated_kg: %w", err)
	}
	if strings.TrimSpace(p.AtomID) == "" {
		return fmt.Errorf("atom_revision_updated_kg: atom_id required")
	}
	if s.tracker.seen(env.EventID) {
		return nil
	}

	count, err := s.hexagons.MarkInvalidatedByFocal(ctx, p.AtomID, userknowledgegraph.FogInvalidationReasonAtomRevisionUpdated)
	if err != nil {
		return fmt.Errorf("atom_revision_updated_kg: mark_invalidated_by_focal: %w", err)
	}
	s.tracker.mark(env.EventID)
	if count > 0 && s.pub != nil {
		_ = s.pub.PublishHexagonFogInvalidated(ctx, &userknowledgegraph.HexagonNode{
			FocalAtomID:        p.AtomID,
			TenantID:           env.TenantID,
			InvalidationReason: userknowledgegraph.FogInvalidationReasonAtomRevisionUpdated,
		})
	}
	return nil
}

// ---------------------------------------------------------------------------
// UserRetentionShiftedKGSubscriber
// ---------------------------------------------------------------------------

// UserRetentionShiftedKGSubscriber invalidates ALL hexagons for the user
// when their retention profile shifts. The full user-level invalidation
// ensures the fog regenerates neighbours ranked against the updated
// retention signal (Ebbinghaus curve re-calibration).
//
// Topic: chora.consumption.user_retention.shifted.v1
//
// NOTE(2026-05-26): topic NOT YET PROVISIONED in chora-489812. This
// subscriber is complete and tested. Activation requires:
//  1. Pub/Sub topic created: chora.consumption.user_retention.shifted.v1
//  2. DLQ topic:             chora.dlq.consumption.user_retention.shifted.v1
//  3. Push subscription wired to chora-consumption /internal/pubsub/kg-retention
//     Tracked as separate Jira story under CHO-1452.
type UserRetentionShiftedKGSubscriber struct {
	hexagons KGHexagonInvalidator
	pub      KGInvalidationPublisher
	tracker  *idempotencyTracker
}

// NewUserRetentionShiftedKGSubscriber constructs the subscriber.
func NewUserRetentionShiftedKGSubscriber(hexagons KGHexagonInvalidator, pub KGInvalidationPublisher) *UserRetentionShiftedKGSubscriber {
	return &UserRetentionShiftedKGSubscriber{
		hexagons: hexagons,
		pub:      pub,
		tracker:  newIdempotencyTracker(),
	}
}

// Handle processes a chora.consumption.user_retention.shifted.v1 event.
// ctx MUST carry the event tenant for RLS (see AtomPublishedKGSubscriber.Handle).
func (s *UserRetentionShiftedKGSubscriber) Handle(ctx context.Context, env events.Envelope, p UserRetentionShiftedKGPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("user_retention_shifted_kg: %w", err)
	}
	gcid := strings.TrimSpace(p.UserGCID)
	if gcid == "" {
		// Prefer the payload field; fall back to envelope GCID.
		gcid = strings.TrimSpace(env.GCID)
	}
	if gcid == "" {
		return fmt.Errorf("user_retention_shifted_kg: user_gcid required")
	}
	tenantID := strings.TrimSpace(p.TenantID)
	if tenantID == "" {
		tenantID = env.TenantID
	}
	// Process-then-mark per CHO-2130 (see AtomPublishedKGSubscriber.Handle).
	if s.tracker.seen(env.EventID) {
		return nil
	}

	count, err := s.hexagons.MarkInvalidatedByUser(ctx, tenantID, gcid, userknowledgegraph.FogInvalidationReasonUserRetentionShift)
	if err != nil {
		return fmt.Errorf("user_retention_shifted_kg: mark_invalidated_by_user: %w", err)
	}
	s.tracker.mark(env.EventID)
	if count > 0 && s.pub != nil {
		_ = s.pub.PublishHexagonFogInvalidated(ctx, &userknowledgegraph.HexagonNode{
			TenantID:           tenantID,
			UserGCID:           gcid,
			InvalidationReason: userknowledgegraph.FogInvalidationReasonUserRetentionShift,
		})
	}
	return nil
}
