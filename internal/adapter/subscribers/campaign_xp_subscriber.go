// campaign_xp_subscriber.go — WS-C5 (CHO-2084, ADR-227 D10 + addendum #6):
// campaign conquest XP.
//
// CampaignXPSubscriber consumes the three XP-bearing campaign topics
// (rung_cleared / node_won / goal_sealed) and folds each into an idempotent
// growth.Service.AwardExp call. Routing is GOAL-FIRST (addendum #6): the
// award lands on the companion bound to the event's goal via ResolveForGoal
// — the first real caller of that port — with its documented fallthrough to
// the learner's active companion when the goal is unbonded.
//
// Sources (identity exp_source_def migration 0036; value/cap parity pinned
// on BOTH sides per the ADR-218 D6 contract — curve.go + exp_rules_test.go):
//
//	rung_cleared is_refresher=false → campaign_rung_cleared   (small, flat)
//	rung_cleared is_refresher=true  → campaign_rung_refreshed (reduced)
//	node_won                        → campaign_node_won        (medium)
//	goal_sealed                     → campaign_goal_sealed     (tier-S)
//
// Seal spacing (D10 "tier-S ~1/week"): exp_source_def carries DAILY caps
// only, so the weekly window is a code semantic here — a prior
// campaign_goal_sealed ledger award younger than the configured spacing
// skips the award (ack, loud log; no NACK loop). The domain's own re-seal
// clock already spaces seals ≥7d PER GOAL; this guard closes the
// multi-goal seal-spam hole per companion. History read errors NACK
// (fail-loud) — never award-anyway.
//
// Idempotency: the DB is the SOLE anchor — AwardExpTx's
// UNIQUE(companion_id, source, idempotency_key) with the upstream event id
// as the key; a duplicate delivery re-runs the award and lands on the
// conflict (Duplicate=true → ack). Deliberately NO mark-before-process
// tracker here: the WS-C5 live smoke (2026-07-09) proved that pattern's
// loss window — mark → award fails (transient 23514) → NACK → retry sees
// "seen" → silent ack → the award is gone forever. Re-running the award on
// every delivery is safe (DB dedupes) and can never drop XP.
//
// hex_expand seam (addendum #6): campaign reveals ride the
// concept_suggestions pipeline and emit NO legacy kg.hexagon_expanded.v1
// (see campaign_node_won_subscriber.go header); nothing publishes that
// legacy topic anymore, so the hex_expand source is dormant and cannot
// double-fire with any campaign source. WS-C5 decision: ACCEPT the
// catalogue row as-is (dormant, harmless) — no suppression required.
//
// Bare PersonalCompletedAt NEVER awards (D10 / CHO-2084 AC-1): no
// personal-completion source exists in the canonical vocabulary; the only
// seal-shaped source is campaign_goal_sealed, fed exclusively by the
// domain-verified goal_sealed.v1 (EvaluateSeal enforces frontier-clear
// before emission). Merge/split lineage (D14, WS-C6 seam): children
// materialise WITHOUT campaign events, so no award path exists to re-fire —
// XP only ever rides a domain-emitted event id, once.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// DefaultSealXPMinSpacing is the D10 "tier-S ~1/week" window applied to
// campaign_goal_sealed awards per companion when no explicit spacing is
// configured (cmd/server env CHORA_CAMPAIGN_SEAL_XP_MIN_SPACING).
const DefaultSealXPMinSpacing = 7 * 24 * time.Hour

// Campaign XP source tokens — identity exp_source_def rows (migration 0036).
const (
	sourceCampaignRungCleared   = "campaign_rung_cleared"
	sourceCampaignRungRefreshed = "campaign_rung_refreshed"
	sourceCampaignNodeWon       = "campaign_node_won"
	sourceCampaignGoalSealed    = "campaign_goal_sealed"
)

// GoalCompanionResolver is the goal-routed EXP attribution port (addendum
// #6). *RepoCompanionResolver satisfies it.
type GoalCompanionResolver interface {
	ResolveForGoal(ctx context.Context, tenantID, ownerGCID, goalID string) (companionID string, err error)
}

// GrowthAwardHistory is the narrow ledger-read slice backing the seal
// spacing guard. *pg.GrowthRepo satisfies it; the zero time means "never
// awarded".
type GrowthAwardHistory interface {
	LastAwardAt(ctx context.Context, tenantID, companionID, source string) (time.Time, error)
}

// CampaignLineageHistory is the D14 lineage re-award refusal read (WS-C6,
// mig 0077: "did XP already flow to an ancestor?"). It folds the node's
// TOMBSTONED ladder history — its own pre-merge/pre-split rows plus every
// concept_node_lineage ancestor's — into (max rungs ever cleared, ever won).
// A node with no lineage and no tombstones reads (0, false). Implemented by
// *pg.CampaignLineageHistoryRepo (same-DB recursive walk).
type CampaignLineageHistory interface {
	PriorLadderState(ctx context.Context, tenantID, learnerGCID, conceptID string) (maxRungsCleared int, everWon bool, err error)
}

// CampaignRungClearedPayload mirrors the BINARY-proto
// chora.consumption.campaign.rung_cleared.v1 body (decoded at the push
// handler; EventID/Traceparent/Tracestate come from its embedded envelope).
type CampaignRungClearedPayload struct {
	TenantID       string
	LearnerGCID    string
	GoalID         string
	ConceptID      string
	ConceptKey     string
	Rung           int
	IsRefresher    bool
	CorrectAnswers int
	EventID        string
	Traceparent    string
	Tracestate     string
}

// CampaignGoalSealedPayload mirrors the BINARY-proto
// chora.consumption.campaign.goal_sealed.v1 body.
type CampaignGoalSealedPayload struct {
	TenantID       string
	LearnerGCID    string
	GoalID         string
	RootConceptID  string
	RootConceptKey string
	NodesWon       int
	EventID        string
	Traceparent    string
	Tracestate     string
}

// CampaignXPSubscriber folds campaign conquest events into growth awards.
type CampaignXPSubscriber struct {
	growth   AwardExpPort
	resolver GoalCompanionResolver
	history  GrowthAwardHistory
	lineage  CampaignLineageHistory
	spacing  time.Duration
	now      func() time.Time
}

// NewCampaignXPSubscriber wires the subscriber. The award port, resolver,
// history and lineage are mandatory (feedback_no_stubs_real_wiring) — a nil
// dep is a wiring bug, so panic at construction rather than fail-open at
// consume time. Non-positive spacing takes DefaultSealXPMinSpacing; a nil
// clock takes time.Now.
func NewCampaignXPSubscriber(
	g AwardExpPort,
	resolver GoalCompanionResolver,
	history GrowthAwardHistory,
	lineage CampaignLineageHistory,
	sealMinSpacing time.Duration,
	now func() time.Time,
) *CampaignXPSubscriber {
	if g == nil || resolver == nil || history == nil || lineage == nil {
		panic("subscribers: CampaignXPSubscriber requires award port, resolver, history and lineage")
	}
	if sealMinSpacing <= 0 {
		sealMinSpacing = DefaultSealXPMinSpacing
	}
	if now == nil {
		now = time.Now
	}
	return &CampaignXPSubscriber{
		growth:   g,
		resolver: resolver,
		history:  history,
		lineage:  lineage,
		spacing:  sealMinSpacing,
		now:      now,
	}
}

// HandleRungCleared awards campaign_rung_cleared (first clear) or the
// reduced campaign_rung_refreshed (is_refresher=true) to the goal's
// companion. Returning an error NACKs (redelivery → DLQ); nil acks.
func (s *CampaignXPSubscriber) HandleRungCleared(ctx context.Context, p CampaignRungClearedPayload) error {
	if err := validateCampaignXPPayload("rung_cleared", p.EventID, p.TenantID, p.LearnerGCID, p.GoalID); err != nil {
		return err
	}
	source := sourceCampaignRungCleared
	if p.IsRefresher {
		source = sourceCampaignRungRefreshed
	} else {
		// D14 lineage refusal (WS-C6): a merge lowers the ladder to the AND
		// of rungs, so the re-climb re-emits first-clear events for ground an
		// ancestor already earned. Refresher events skip this read — they are
		// live-ladder re-warms, already reduced-priced by the domain.
		maxRungs, _, err := s.lineage.PriorLadderState(ctx, p.TenantID, p.LearnerGCID, p.ConceptID)
		if err != nil {
			return fmt.Errorf("campaign_xp: lineage guard read (rung_cleared): %w", err)
		}
		if p.Rung <= maxRungs {
			log.Printf("consumption: campaign rung XP REFUSED (lineage D14: rung %d <= ancestor max %d): concept=%s goal=%s event=%s",
				p.Rung, maxRungs, p.ConceptID, p.GoalID, p.EventID)
			return nil
		}
	}
	return s.award(ctx, awardSpec{
		tenantID:    p.TenantID,
		ownerGCID:   p.LearnerGCID,
		goalID:      p.GoalID,
		source:      source,
		topic:       events.TopicCampaignRungCleared,
		eventID:     p.EventID,
		traceparent: p.Traceparent,
		tracestate:  p.Tracestate,
	})
}

// HandleNodeWon awards campaign_node_won (the medium win-moment bonus) to
// the goal's companion. Payload is shared with the WS-C4 reveal consumer —
// both decode the same node_won.v1 topic on independent subscriptions.
func (s *CampaignXPSubscriber) HandleNodeWon(ctx context.Context, p CampaignNodeWonPayload) error {
	if err := validateCampaignXPPayload("node_won", p.EventID, p.TenantID, p.LearnerGCID, p.GoalID); err != nil {
		return err
	}
	// D14 lineage refusal (WS-C6): a node whose lineage was ever won already
	// paid its win bonus — a post-merge re-win earns retention warmth, not XP.
	_, everWon, err := s.lineage.PriorLadderState(ctx, p.TenantID, p.LearnerGCID, p.ConceptID)
	if err != nil {
		return fmt.Errorf("campaign_xp: lineage guard read (node_won): %w", err)
	}
	if everWon {
		log.Printf("consumption: campaign node_won XP REFUSED (lineage D14: ancestor already won): concept=%s goal=%s event=%s",
			p.ConceptID, p.GoalID, p.EventID)
		return nil
	}
	return s.award(ctx, awardSpec{
		tenantID:    p.TenantID,
		ownerGCID:   p.LearnerGCID,
		goalID:      p.GoalID,
		source:      sourceCampaignNodeWon,
		topic:       events.TopicCampaignNodeWon,
		eventID:     p.EventID,
		traceparent: p.Traceparent,
		tracestate:  p.Tracestate,
	})
}

// HandleGoalSealed awards the tier-S campaign_goal_sealed bonus, gated by
// the per-companion spacing window (D10 ~1/week). Inside the window the
// event acks with a loud skip log — a NACK loop would only DLQ a
// legitimately-spaced seal.
func (s *CampaignXPSubscriber) HandleGoalSealed(ctx context.Context, p CampaignGoalSealedPayload) error {
	if err := validateCampaignXPPayload("goal_sealed", p.EventID, p.TenantID, p.LearnerGCID, p.GoalID); err != nil {
		return err
	}
	companionID, err := s.resolver.ResolveForGoal(ctx, p.TenantID, p.LearnerGCID, p.GoalID)
	if errors.Is(err, ErrNoCompanion) {
		return nil
	}
	if err != nil {
		return err
	}
	last, err := s.history.LastAwardAt(ctx, p.TenantID, companionID, sourceCampaignGoalSealed)
	if err != nil {
		return fmt.Errorf("campaign_xp: seal spacing read: %w", err)
	}
	if !last.IsZero() {
		if since := s.now().Sub(last); since < s.spacing {
			log.Printf("consumption: campaign seal XP SKIPPED (spacing %v < %v): companion=%s goal=%s event=%s",
				since.Round(time.Minute), s.spacing, companionID, p.GoalID, p.EventID)
			return nil
		}
	}
	return s.awardTo(ctx, companionID, awardSpec{
		tenantID:    p.TenantID,
		ownerGCID:   p.LearnerGCID,
		goalID:      p.GoalID,
		source:      sourceCampaignGoalSealed,
		topic:       events.TopicCampaignGoalSealed,
		eventID:     p.EventID,
		traceparent: p.Traceparent,
		tracestate:  p.Tracestate,
	})
}

// awardSpec carries one award's routing + attribution facts.
type awardSpec struct {
	tenantID    string
	ownerGCID   string
	goalID      string
	source      string
	topic       string
	eventID     string
	traceparent string
	tracestate  string
}

// award resolves the goal's companion then awards. ErrNoCompanion drops
// silently (ladder progress is computed for everyone; XP is a
// Companion-layer moment — ADR-204 Constraint-1 via ADR-227 D11).
func (s *CampaignXPSubscriber) award(ctx context.Context, spec awardSpec) error {
	companionID, err := s.resolver.ResolveForGoal(ctx, spec.tenantID, spec.ownerGCID, spec.goalID)
	if errors.Is(err, ErrNoCompanion) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.awardTo(ctx, companionID, spec)
}

// awardTo performs the idempotent growth award (RequestedDelta 0 = the
// resolver-priced value, ADR-218 D6).
func (s *CampaignXPSubscriber) awardTo(ctx context.Context, companionID string, spec awardSpec) error {
	resp, err := s.growth.AwardExp(ctx, growth.AwardExpInput{
		TenantID:       spec.tenantID,
		CompanionID:    companionID,
		OwnerGCID:      spec.ownerGCID,
		Source:         spec.source,
		RequestedDelta: 0,
		IdempotencyKey: spec.eventID,
		SourceEventID:  spec.eventID,
		SourceTopic:    spec.topic,
		Traceparent:    spec.traceparent,
		Tracestate:     spec.tracestate,
	})
	if err != nil {
		return fmt.Errorf("campaign_xp: award %s: %w", spec.source, err)
	}
	if resp.Skipped {
		// Rule outcome, not a failure (e.g. unhatched_refresher, CHO-2239;
		// source_disabled, ADR-218 D6): ack with a loud log — a NACK loop
		// would only DLQ a correctly-suppressed award.
		log.Printf("campaign_xp: award %s to %s SKIPPED by rule (%s) event=%s",
			spec.source, companionID, resp.SkipReason, spec.eventID)
	}
	return nil
}

// validateCampaignXPPayload fail-louds on a payload missing its identity or
// idempotency anchor (NACK → DLQ after max attempts; never a silent drop of
// a malformed verified event).
func validateCampaignXPPayload(kind, eventID, tenantID, gcid, goalID string) error {
	switch {
	case eventID == "":
		return fmt.Errorf("campaign_xp %s: envelope event_id required (idempotency anchor)", kind)
	case tenantID == "":
		return fmt.Errorf("campaign_xp %s: tenant_id required", kind)
	case gcid == "":
		return fmt.Errorf("campaign_xp %s: learner_gcid required", kind)
	case goalID == "":
		return fmt.Errorf("campaign_xp %s: goal_id required (addendum #6 goal routing)", kind)
	}
	return nil
}
