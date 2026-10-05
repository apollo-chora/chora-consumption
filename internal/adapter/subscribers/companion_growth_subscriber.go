// companion_growth_subscriber.go — ADR-149 Iter G.2.
//
// CompanionGrowthSubscriber consumes the 7 EXP source topics and calls
// growth.Service.AwardExp idempotently. ProvisionEggSubscriber consumes
// chora.tenancy.companion_egg.payment_succeeded.v1 and provisions a Stage-0
// companion_instances row.
//
// Per ddd-enforcement HARD RULE: chora-consumption never queries cross-DB.
// EXP source events arrive from chora-creation / chora-sharing / chora-
// consumption itself / chora-tenancy via Pub/Sub. The subscriber translates
// the cross-domain event into a domain growth.AwardExpInput call.
//
// Subscribers MUST be safe under at-least-once Pub/Sub redelivery: the
// idempotency_key is the upstream envelope's event_id, and AwardExpTx
// enforces UNIQUE(companion_id, source, idempotency_key) at the DB layer.
//
// Ordering contract (CHO-2107): every handler is process-then-mark — a
// non-marking tracker.seen check at entry, side effect, then tracker.mark
// only on success. Never mark before processing: a failure after the mark
// swallows the redelivery as a duplicate and loses the event. The in-memory
// tracker is an optimisation only; the DB UNIQUE anchors above are the sole
// durable dedupe.
package subscribers

import (
	"context"
	"errors"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// CompanionResolver maps an upstream (tenant, gcid) into the active
// dispatched Companion receiving EXP. The Iter G.2 implementation uses a
// simple "first non-deleted Companion" lookup; the agentic runtime supplies
// the actual dispatched Companion ID at conv_turn via the gRPC path.
//
// Returns ErrNoCompanion when no Companion exists yet (subscriber drops the
// event silently — provisioning happens via CompanionEggSubscriber).
type CompanionResolver interface {
	ResolveActiveCompanion(ctx context.Context, tenantID, ownerGCID string) (companionID string, err error)
}

// ErrNoCompanion is returned by CompanionResolver when no Companion exists for
// the supplied owner. Subscribers treat this as a non-fatal "drop event".
var ErrNoCompanion = errors.New("subscribers: no active companion for owner")

// AwardExpPort is the slice of growth.Service the subscriber uses.
type AwardExpPort interface {
	AwardExp(ctx context.Context, in growth.AwardExpInput) (*growth.AwardExpResponse, error)
}

// CompanionGrowthSubscriber handles the 7 EXP source topics.
type CompanionGrowthSubscriber struct {
	growth   AwardExpPort
	resolver CompanionResolver
	tracker  *idempotencyTracker
}

// NewCompanionGrowthSubscriber constructs the subscriber.
func NewCompanionGrowthSubscriber(g AwardExpPort, resolver CompanionResolver) *CompanionGrowthSubscriber {
	return &CompanionGrowthSubscriber{
		growth:   g,
		resolver: resolver,
		tracker:  newIdempotencyTracker(),
	}
}

// HandleAtomSessionCompleted awards EXP for atom_session + ebbinghaus_review.
//
// CHO-2012 (ADR-218 D6): the default award value is RESOLVED by the growth
// service through the ExpRuleResolver (RequestedDelta 0 = "use the resolved
// value"); the subscriber no longer hardcodes per-source literals. The one
// code-side mapping that remains: an INCORRECT answer awards an explicit 1
// (the resolver carries a single per-source value — the correct-answer
// award; the incorrect reduction is a fixed code semantic, ADR-149).
func (s *CompanionGrowthSubscriber) HandleAtomSessionCompleted(ctx context.Context, env events.Envelope, p AtomSessionCompletedPayload) error {
	// AtomSessionCompletedPayload is defined in topic_accuracy_subscriber.go
	// and shared across subscribers in this package. ReviewDue field added
	// in Iter G.2 for ADR-149 ebbinghaus_review path.
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	// Process-then-mark (CHO-2107): non-marking seen check at entry; the key
	// is recorded ONLY after processing succeeds (or the event is
	// deliberately dropped). Marking before processing burned the dedupe key
	// on a transient failure, so the NACKed event's redelivery was swallowed
	// as a duplicate and the award silently lost. Errors return UNMARKED
	// (NACK → redelivery). The rare concurrent-redelivery double-process
	// this admits is absorbed by the durable DB anchor:
	// companion_growth_events UNIQUE (companion_id, source, idempotency_key)
	// (migration 0032; pg.GrowthRepo.AwardExpTx ON CONFLICT DO NOTHING).
	dedupeKey := "atom_session:" + env.EventID
	if s.tracker.seen(dedupeKey) {
		return nil
	}
	companionID, err := s.resolver.ResolveActiveCompanion(ctx, env.TenantID, p.LearnerGCID)
	if errors.Is(err, ErrNoCompanion) {
		s.tracker.mark(dedupeKey) // deliberate drop — processing concluded
		return nil
	}
	if err != nil {
		return err
	}
	delta := 1 // incorrect answer — explicit reduced award
	if p.IsCorrect {
		delta = 0 // resolved default (parity value 3)
	}
	if _, err := s.growth.AwardExp(ctx, growth.AwardExpInput{
		TenantID:        env.TenantID,
		CompanionID:     companionID,
		OwnerGCID:       p.LearnerGCID,
		Source:          "atom_session",
		RequestedDelta:  delta,
		IdempotencyKey:  env.EventID,
		SourceEventID:   env.EventID,
		SourceTopic:     events.TopicAtomSessionCompleted,
		SourceSessionID: p.SessionID,
		Traceparent:     env.Traceparent,
		Tracestate:      env.Tracestate,
	}); err != nil {
		return err
	}
	if p.ReviewDue {
		// Ebbinghaus review fires as a second AwardExp on the same upstream
		// event — distinct (source, idempotency_key) pairs ensure both
		// land cleanly through the UNIQUE constraint.
		if _, err := s.growth.AwardExp(ctx, growth.AwardExpInput{
			TenantID:        env.TenantID,
			CompanionID:     companionID,
			OwnerGCID:       p.LearnerGCID,
			Source:          "ebbinghaus_review",
			RequestedDelta:  0, // resolved default (parity 5)
			IdempotencyKey:  env.EventID + ":review",
			SourceEventID:   env.EventID,
			SourceTopic:     events.TopicAtomSessionCompleted,
			SourceSessionID: p.SessionID,
			Traceparent:     env.Traceparent,
			Tracestate:      env.Tracestate,
		}); err != nil {
			return err
		}
	}
	s.tracker.mark(dedupeKey)
	return nil
}

// KGHexagonExpandedPayload mirrors chora.consumption.kg.hexagon_expanded.v1.
type KGHexagonExpandedPayload struct {
	LearnerGCID string
}

// HandleHexagonExpanded awards EXP for KG hexagon expansion (curiosity).
func (s *CompanionGrowthSubscriber) HandleHexagonExpanded(ctx context.Context, env events.Envelope, p KGHexagonExpandedPayload) error {
	return s.awardSimple(ctx, env, p.LearnerGCID, "hex_expand", 0,
		"chora.consumption.kg.hexagon_expanded.v1")
}

// KGJunctionAcceptedPayload mirrors chora.consumption.kg.junction_accepted.v1.
type KGJunctionAcceptedPayload struct {
	LearnerGCID string
}

// HandleJunctionAccepted awards EXP for rare KG junction acceptance.
func (s *CompanionGrowthSubscriber) HandleJunctionAccepted(ctx context.Context, env events.Envelope, p KGJunctionAcceptedPayload) error {
	return s.awardSimple(ctx, env, p.LearnerGCID, "junction_accepted", 0,
		"chora.consumption.kg.junction_accepted.v1")
}

// DailyDoseServedPayload mirrors chora.consumption.daily_dose.served.v1.
type DailyDoseServedPayload struct {
	LearnerGCID string
}

// HandleDailyDoseServed awards EXP for Daily Dose engagement (bond).
func (s *CompanionGrowthSubscriber) HandleDailyDoseServed(ctx context.Context, env events.Envelope, p DailyDoseServedPayload) error {
	return s.awardSimple(ctx, env, p.LearnerGCID, "daily_dose_open", 0,
		events.TopicDailyDoseServed)
}

// PostCreatedPayload mirrors chora.sharing.post.created.v1.
type PostCreatedPayload struct {
	AuthorGCID string
}

// HandlePostCreated awards social_share EXP iff the post's author_gcid
// matches the active Companion's owner. Per ADR-149: filter at this layer.
func (s *CompanionGrowthSubscriber) HandlePostCreated(ctx context.Context, env events.Envelope, p PostCreatedPayload) error {
	if p.AuthorGCID == "" {
		return nil
	}
	return s.awardSimple(ctx, env, p.AuthorGCID, "social_share", 0,
		"chora.sharing.post.created.v1")
}

// ReactionAddedPayload mirrors chora.sharing.reaction.added.v1.
type ReactionAddedPayload struct {
	TargetGCID string
}

// HandleReactionAdded awards social_reaction EXP to the post's TARGET owner.
func (s *CompanionGrowthSubscriber) HandleReactionAdded(ctx context.Context, env events.Envelope, p ReactionAddedPayload) error {
	if p.TargetGCID == "" {
		return nil
	}
	return s.awardSimple(ctx, env, p.TargetGCID, "social_reaction", 0,
		"chora.sharing.reaction.added.v1")
}

// AtomPublishedPayload mirrors chora.creation.atom.published.v1.
type AtomPublishedPayload struct {
	AuthorGCID string
}

// HandleAtomPublished awards atom_authored EXP to the atom author.
func (s *CompanionGrowthSubscriber) HandleAtomPublished(ctx context.Context, env events.Envelope, p AtomPublishedPayload) error {
	if p.AuthorGCID == "" {
		return nil
	}
	return s.awardSimple(ctx, env, p.AuthorGCID, "atom_authored", 0,
		"chora.creation.atom.published.v1")
}

// awardSimple is the shared award path for non-conditional EXP sources.
//
// Process-then-mark (CHO-2107): seen check at entry, mark only after the
// award succeeded; errors return unmarked so the redelivery re-processes.
// Concurrent-redelivery double-process is absorbed by companion_growth_events
// UNIQUE (companion_id, source, idempotency_key) — migration 0032.
func (s *CompanionGrowthSubscriber) awardSimple(ctx context.Context, env events.Envelope, ownerGCID, source string, delta int, sourceTopic string) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	dedupeKey := source + ":" + env.EventID
	if s.tracker.seen(dedupeKey) {
		return nil
	}
	companionID, err := s.resolver.ResolveActiveCompanion(ctx, env.TenantID, ownerGCID)
	if errors.Is(err, ErrNoCompanion) {
		s.tracker.mark(dedupeKey) // deliberate drop — processing concluded
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.growth.AwardExp(ctx, growth.AwardExpInput{
		TenantID:       env.TenantID,
		CompanionID:    companionID,
		OwnerGCID:      ownerGCID,
		Source:         source,
		RequestedDelta: delta,
		IdempotencyKey: env.EventID,
		SourceEventID:  env.EventID,
		SourceTopic:    sourceTopic,
		Traceparent:    env.Traceparent,
		Tracestate:     env.Tracestate,
	}); err != nil {
		return err
	}
	s.tracker.mark(dedupeKey)
	return nil
}

// ---------------------------------------------------------------------------
// ProvisionEggSubscriber — chora.tenancy.companion_egg.payment_succeeded.v1
// ---------------------------------------------------------------------------

// EggPaymentSucceededPayload mirrors chora.tenancy.companion_egg.payment_succeeded.v1.
type EggPaymentSucceededPayload struct {
	PurchaseID            string
	PurchaserGCID         string
	TargetTenantID        string
	EggSku                string
	StripeSessionID       string
	StripePaymentIntentID string
	AmountCentsPaid       int64
	Currency              string
	SuggestedFocalAtomID  string
	PaidAt                time.Time
}

// ProvisionEggPort is the slice of growth.Service used by this subscriber.
type ProvisionEggPort interface {
	ProvisionEgg(ctx context.Context, in growth.ProvisionEggInput) (*growth.CompanionGrowthRow, error)
}

// ProvisionEggSubscriber provisions Stage-0 Companions on egg-payment events.
type ProvisionEggSubscriber struct {
	growth  ProvisionEggPort
	tracker *idempotencyTracker
}

// NewProvisionEggSubscriber constructs the subscriber.
func NewProvisionEggSubscriber(g ProvisionEggPort) *ProvisionEggSubscriber {
	return &ProvisionEggSubscriber{growth: g, tracker: newIdempotencyTracker()}
}

// Handle provisions a Stage-0 row. Idempotent on (purchase_id) via the
// growth service's repo-level dedupe (uq_companion_instances_egg_purchase_id,
// migration 0033) + the local event_id tracker.
//
// Process-then-mark (CHO-2107): the event_id is marked seen ONLY after
// ProvisionEgg succeeded; a failure returns unmarked (NACK → redelivery) so
// the purchased egg is never silently lost. Concurrent-redelivery
// double-process is absorbed by the repo's ON CONFLICT (egg_purchase_id)
// DO NOTHING lookup-then-insert.
func (s *ProvisionEggSubscriber) Handle(ctx context.Context, env events.Envelope, p EggPaymentSucceededPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	dedupeKey := "egg_payment:" + env.EventID
	if s.tracker.seen(dedupeKey) {
		return nil
	}
	now := p.PaidAt
	if now.IsZero() {
		now = env.OccurredAt
	}
	if _, err := s.growth.ProvisionEgg(ctx, growth.ProvisionEggInput{
		TenantID:             p.TargetTenantID,
		OwnerGCID:            p.PurchaserGCID,
		EggSku:               p.EggSku,
		EggPurchaseID:        p.PurchaseID,
		EggSource:            "purchase",
		Now:                  now,
		SoftExpiryAt:         now.AddDate(0, 0, 30),
		HardExpiryAt:         now.AddDate(0, 0, 60),
		SuggestedFocalAtomID: p.SuggestedFocalAtomID,
		StripeSessionID:      p.StripeSessionID,
		AmountCents:          p.AmountCentsPaid,
		Currency:             p.Currency,
		// Thread the inbound payments trace context onto the emitted
		// egg_purchased.v1 envelope (CHO-2028) — validateInboundEnvelope
		// above guarantees Traceparent is non-empty.
		Traceparent: env.Traceparent,
		Tracestate:  env.Tracestate,
	}); err != nil {
		return err
	}
	s.tracker.mark(dedupeKey)
	return nil
}
