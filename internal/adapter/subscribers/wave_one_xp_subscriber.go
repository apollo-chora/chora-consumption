// wave_one_xp_subscriber.go — F-I3 (CHO-2090, ADR-228 D4 Wave 1): "the power
// of a little bit" companion-EXP sources.
//
// WaveOneXPSubscriber folds three verified platform events into idempotent
// growth.Service.AwardExp calls:
//
//	chora.consumption.weakness.grown.v1          → weakness_grown    (tier B)
//	chora.delivery.submission.graded.v1          → submission_graded (tier A)
//	chora.delivery.module_progress.completed.v1  → module_completed  (tier B)
//
// Sources (identity exp_source_def migration 0037; value/cap parity pinned
// on BOTH sides per the ADR-218 D6 contract — curve.go + exp_rules_test.go).
// Pricing is FLAT per event (ADR-203 L16 / the ADR-227 anti-farming
// doctrine): the award never reads points, accuracy or content difficulty —
// caps + calendar time are the defence.
//
// Attribution (GQ-26): none of the Wave-1 events carries a goal_id (the
// graded + module events are course-bound; weakness.grown is edge-scoped),
// so the award routes to the learner's ACTIVE COMPANION via
// CompanionResolver.ResolveActiveCompanion — goal-tagging arrives with the
// ADR-216 aspiration link (ADR-228 D4 note). Because the active-companion
// path excludes pre-hatch eggs (F-I1.4 bind-to-warm), a Wave-1 event never
// warms an egg — eggs earn only via exact goal attribution (campaign XP).
// ErrNoCompanion drops silently (ack): XP is a Companion-layer moment
// (ADR-204 Constraint-1 via ADR-227 D11).
//
// Idempotency: the DB is the SOLE anchor — AwardExpTx's
// UNIQUE(companion_id, source, idempotency_key) (consumption migration 0032)
// with the upstream envelope event_id as the key; a redelivery re-runs the
// award and lands on the conflict (Duplicate=true → ack). Deliberately NO
// mark-before-process tracker, mirroring the WS-C5 campaign XP subscriber:
// the mark-first pattern's loss window (mark → transient award failure →
// NACK → retry sees "seen" → silent ack) drops XP forever, while re-running
// the award on every delivery is safe (the ledger dedupes) and can never
// lose one. This IS the seen→process→mark discipline taken to its limit —
// there is no subscriber-side mark at all; the processing itself is the
// durable mark.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// Wave-1 XP source tokens — identity exp_source_def rows (migration 0037).
const (
	sourceWeaknessGrown    = "weakness_grown"
	sourceSubmissionGraded = "submission_graded"
	sourceModuleCompleted  = "module_completed"
)

// TopicModuleProgressCompleted is the delivery-owned module-completion topic
// the module_completed source binds. NOTE (verified 2026-07-10): chora-delivery
// does NOT emit this event yet — the W7 StudentModuleProgress projection
// computes is_complete without publishing. This consumer ships DARK until the
// delivery-side emitter lands; see the CHO-2090 report.
const TopicModuleProgressCompleted = "chora.delivery.module_progress.completed.v1"

// topicSubmissionGradedXP is the delivery graded topic (BINARY proto) the
// submission_graded source rides — the SAME delivery the derived Growth-Edge
// path consumes (one subscription, two legs).
const topicSubmissionGradedXP = "chora.delivery.submission.graded.v1"

// ModuleCompletedPayload mirrors the consumed fields of
// chora.delivery.module_progress.completed.v1 (JSON, schemaless — decoded at
// the push handler; the envelope rides in Pub/Sub attributes).
type ModuleCompletedPayload struct {
	ModuleID    string
	CourseID    string
	TenantID    string
	LearnerGCID string
}

// WaveOneXPSubscriber folds the three Wave-1 verified events into growth
// awards.
type WaveOneXPSubscriber struct {
	growth   AwardExpPort
	resolver CompanionResolver
}

// NewWaveOneXPSubscriber wires the subscriber. Both deps are mandatory
// (feedback_no_stubs_real_wiring) — a nil dep is a wiring bug, so panic at
// construction rather than fail-open at consume time.
func NewWaveOneXPSubscriber(g AwardExpPort, resolver CompanionResolver) *WaveOneXPSubscriber {
	if g == nil || resolver == nil {
		panic("subscribers: WaveOneXPSubscriber requires award port and resolver")
	}
	return &WaveOneXPSubscriber{growth: g, resolver: resolver}
}

// HandleWeaknessGrown awards weakness_grown for one active→grown Growth-Edge
// transition. Returning an error NACKs (redelivery → DLQ); nil acks.
func (s *WaveOneXPSubscriber) HandleWeaknessGrown(ctx context.Context, env events.Envelope, p WeaknessGrownPayload) error {
	tenantID, gcid := fallbackIdentity(p.TenantID, p.LearnerGCID, env)
	if err := validateWaveOneXP("weakness_grown", env.EventID, tenantID, gcid); err != nil {
		return err
	}
	return s.award(ctx, tenantID, gcid, sourceWeaknessGrown, events.TopicWeaknessGrown, env)
}

// HandleSubmissionGraded awards submission_graded for one graded R+
// assessment submission — flat, regardless of score (verified effort; the
// evidence-quality judgement lives in the derived Growth-Edge leg, never in
// the XP amount).
func (s *WaveOneXPSubscriber) HandleSubmissionGraded(ctx context.Context, env events.Envelope, p SubmissionGradedEvidence) error {
	tenantID, gcid := fallbackIdentity(p.TenantID, p.LearnerGCID, env)
	if err := validateWaveOneXP("submission_graded", env.EventID, tenantID, gcid); err != nil {
		return err
	}
	return s.award(ctx, tenantID, gcid, sourceSubmissionGraded, topicSubmissionGradedXP, env)
}

// HandleModuleCompleted awards module_completed for one W7
// StudentModuleProgress module completion. A module event without its
// module_id is malformed producer output — fail loud (NACK → DLQ) rather
// than award off a hollow event.
func (s *WaveOneXPSubscriber) HandleModuleCompleted(ctx context.Context, env events.Envelope, p ModuleCompletedPayload) error {
	tenantID, gcid := fallbackIdentity(p.TenantID, p.LearnerGCID, env)
	if err := validateWaveOneXP("module_completed", env.EventID, tenantID, gcid); err != nil {
		return err
	}
	if strings.TrimSpace(p.ModuleID) == "" {
		return errors.New("wave_one_xp module_completed: module_id required")
	}
	return s.award(ctx, tenantID, gcid, sourceModuleCompleted, TopicModuleProgressCompleted, env)
}

// award resolves the learner's active companion then performs the idempotent
// growth award (RequestedDelta 0 = the resolver-priced flat value, ADR-218
// D6). ErrNoCompanion drops silently; resolver read errors NACK (fail-loud —
// never a silent EXP drop).
func (s *WaveOneXPSubscriber) award(ctx context.Context, tenantID, gcid, source, topic string, env events.Envelope) error {
	companionID, err := s.resolver.ResolveActiveCompanion(ctx, tenantID, gcid)
	if errors.Is(err, ErrNoCompanion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wave_one_xp: resolve active companion for %s: %w", source, err)
	}
	_, err = s.growth.AwardExp(ctx, growth.AwardExpInput{
		TenantID:       tenantID,
		CompanionID:    companionID,
		OwnerGCID:      gcid,
		Source:         source,
		RequestedDelta: 0,
		IdempotencyKey: env.EventID,
		SourceEventID:  env.EventID,
		SourceTopic:    topic,
		// AwardExp hard-requires a W3C trace; a missing upstream traceparent
		// must not drop verified XP — mint one (mirrors publishGrown).
		Traceparent: tracing.EnsureTraceparent(env.Traceparent),
		Tracestate:  env.Tracestate,
	})
	if err != nil {
		return fmt.Errorf("wave_one_xp: award %s: %w", source, err)
	}
	return nil
}

// validateWaveOneXP fail-louds on an event missing its idempotency anchor or
// learner identity (NACK → DLQ after max attempts; never a silent drop of a
// malformed verified event).
func validateWaveOneXP(kind, eventID, tenantID, gcid string) error {
	switch {
	case strings.TrimSpace(eventID) == "":
		return fmt.Errorf("wave_one_xp %s: envelope event_id required (idempotency anchor)", kind)
	case strings.TrimSpace(tenantID) == "":
		return fmt.Errorf("wave_one_xp %s: tenant_id required", kind)
	case strings.TrimSpace(gcid) == "":
		return fmt.Errorf("wave_one_xp %s: learner gcid required", kind)
	}
	return nil
}
