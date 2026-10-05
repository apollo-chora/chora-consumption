// goal_graduation_subscriber.go — §3 Goal graduation, event-driven half
// (CHO-1962, ADR-204 §9). Consumes chora.consumption.weakness.grown.v1 (which
// chora-consumption itself emits on every active→grown Growth-Edge transition)
// and reconciles the learner's ACTIVE Goals against their current mastered set:
//
//   - whole concept_set mastered → Goal.Graduate (active→achieved) + emit
//     chora.consumption.goal.graduated.v1
//   - mastered share moved but < 100% → persist the new high-water mark + emit
//     chora.consumption.goal.progress_updated.v1
//
// The mastered count is the SAME lw.CountMastered the A+ %-ring renders, so a
// Goal graduates at exactly the share the FE shows as 100% (no drift).
//
// Idempotency is layered: the inbox dedups a redelivered inbound event; the
// reconcile is itself replay-safe (Graduate no-ops once achieved + an achieved
// Goal is filtered out of the active set, so it can never re-graduate; a progress
// re-fire is gated by the persisted MasteredConceptCount high-water mark).
//
// Persist-then-emit MIRRORS the weakness.grown precedent (publishGrown): the A+
// FE celebrates by reading status='achieved' directly, so the cross-domain reward
// event is best-effort (a rare publish failure after the status write is not
// re-emitted, exactly as for weakness.grown). Nil-safe publisher — without it
// the status flip still persists, just no event.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// WeaknessGrownPayload mirrors the consumed fields of
// chora.consumption.weakness.grown.v1 (decoded at the push handler from the
// binary proto). Only the identity is needed — the subscriber re-derives the
// learner's FULL mastered-concept set from the LearnerWeakness repo, so which
// single edge grew is immaterial (any grow may complete any goal).
type WeaknessGrownPayload struct {
	GrowthEdgeID string
	TenantID     string
	LearnerGCID  string
	ConceptKey   string // carried for logging / trace only
}

// GoalGraduationSubscriber reconciles a learner's active Goals on each grow.
type GoalGraduationSubscriber struct {
	goals    goal.Repository
	mastered lw.GrownEdgeLister
	pub      events.Publisher // optional (nil ⇒ persist only, no reward emit)
	inbox    idempotent.Store
	ttl      time.Duration
	now      func() time.Time
}

// NewGoalGraduationSubscriber constructs the subscriber with an in-memory inbox
// (graduation is idempotent at the domain layer; the inbox just avoids redundant
// reprocessing within a pod's lifetime). goals + mastered are required.
func NewGoalGraduationSubscriber(goals goal.Repository, mastered lw.GrownEdgeLister) *GoalGraduationSubscriber {
	return &GoalGraduationSubscriber{
		goals:    goals,
		mastered: mastered,
		inbox:    idempotent.NewMemoryStore(),
		ttl:      inboxTTL,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// WithPublisher attaches the outbox publisher for the goal.* events
// (builder-style, nil-safe). Without it, graduation/progress still PERSIST.
func (s *GoalGraduationSubscriber) WithPublisher(pub events.Publisher) *GoalGraduationSubscriber {
	s.pub = pub
	return s
}

// WithClock overrides the clock (tests). nil-safe.
func (s *GoalGraduationSubscriber) WithClock(now func() time.Time) *GoalGraduationSubscriber {
	if now != nil {
		s.now = now
	}
	return s
}

// Handle reconciles the learner's active Goals against their current mastered
// set. Idempotent per inbound event via the inbox; the reconcile is replay-safe.
func (s *GoalGraduationSubscriber) Handle(ctx context.Context, env events.Envelope, p WeaknessGrownPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	tenantID, gcid := fallbackIdentity(p.TenantID, p.LearnerGCID, env)
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" {
		return errors.New("subscribers: goal-graduation missing tenant_id/learner_gcid")
	}
	key := "goal.graduation:" + tenantID + "|" + gcid + "|" + env.EventID
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.reconcile(ctx, env, tenantID, gcid)
	})
}

func (s *GoalGraduationSubscriber) reconcile(ctx context.Context, env events.Envelope, tenantID, gcid string) error {
	// RLS: scope ctx from the payload identity so the pg repos read/write the
	// right tenant + learner (mirrors weakness_analyzed_subscriber.upsertEdges).
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)

	mastered, err := lw.MasteredConceptKeys(ctx, s.mastered, tenantID, gcid)
	if err != nil {
		return fmt.Errorf("subscribers: goal-graduation derive mastered set: %w", err)
	}
	goals, err := s.goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return fmt.Errorf("subscribers: goal-graduation list goals: %w", err)
	}

	now := s.now()
	for _, g := range goals {
		if g == nil || g.Status != goal.StatusActive {
			continue // only ACTIVE goals graduate / progress
		}
		total := len(g.ConceptSet)
		if total == 0 {
			continue // curiosity / no verifiable concept set — never auto-graduates
		}
		masteredCount := lw.CountMastered(g.ConceptSet, mastered)

		switch {
		case masteredCount >= total:
			if err := s.graduate(ctx, env, g, masteredCount, total, now); err != nil {
				return err
			}
		case masteredCount != g.MasteredConceptCount:
			if err := s.progress(ctx, env, g, masteredCount, total, now); err != nil {
				return err
			}
		default:
			// Unchanged high-water mark — nothing to persist or emit.
		}
	}
	return nil
}

// graduate transitions the Goal active→achieved, persists (status + count), then
// emits goal.graduated.v1. Persist-then-emit: a publish failure NACKs, but on
// redelivery the Goal is achieved (filtered out) so it never re-graduates — the
// event is best-effort, exactly like weakness.grown.
func (s *GoalGraduationSubscriber) graduate(ctx context.Context, env events.Envelope, g *goal.Goal, masteredCount, total int, now time.Time) error {
	if err := g.Graduate(now); err != nil {
		return fmt.Errorf("subscribers: graduate goal %s: %w", g.GoalID, err)
	}
	if err := g.RecordMastery(masteredCount, now); err != nil {
		return fmt.Errorf("subscribers: record mastery on graduate %s: %w", g.GoalID, err)
	}
	if err := s.goals.Update(ctx, g); err != nil {
		return fmt.Errorf("subscribers: persist graduated goal %s: %w", g.GoalID, err)
	}
	return s.publishGraduated(env, g, masteredCount, total)
}

// progress persists the new mastered-count high-water mark then emits
// goal.progress_updated.v1. The persisted count gates re-emit (a redelivery that
// re-derives the same count is a no-op).
func (s *GoalGraduationSubscriber) progress(ctx context.Context, env events.Envelope, g *goal.Goal, masteredCount, total int, now time.Time) error {
	if err := g.RecordMastery(masteredCount, now); err != nil {
		return fmt.Errorf("subscribers: record mastery %s: %w", g.GoalID, err)
	}
	if err := s.goals.Update(ctx, g); err != nil {
		return fmt.Errorf("subscribers: persist goal progress %s: %w", g.GoalID, err)
	}
	return s.publishProgress(env, g, masteredCount, total)
}

// publishGraduated emits chora.consumption.goal.graduated.v1 (nil-safe). The
// idempotency_key (goal_id + persisted graduated_at) makes any duplicate publish
// an outbox no-op; the trigger's W3C trace is propagated (one span, same trace).
func (s *GoalGraduationSubscriber) publishGraduated(env events.Envelope, g *goal.Goal, masteredCount, total int) error {
	if s.pub == nil {
		return nil
	}
	graduatedAt := g.UpdatedAt.UTC()
	target := ""
	if g.ChoraTargetRef != nil {
		target = *g.ChoraTargetRef
	}
	outEnv := events.Envelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: "chora.consumption.goal.graduated:" + g.GoalID + ":" + graduatedAt.Format(time.RFC3339Nano),
		TenantID:       g.TenantID,
		GCID:           g.LearnerGCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    s.now(),
		Traceparent:    tracing.EnsureTraceparent(env.Traceparent),
		Tracestate:     env.Tracestate,
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"goal_id":           g.GoalID,
		"tenant_id":         g.TenantID,
		"learner_gcid":      g.LearnerGCID,
		"kind":              string(g.Kind),
		"chora_target_ref":  target,
		"mastered_concepts": masteredCount,
		"total_concepts":    total,
		"graduated_at":      graduatedAt,
	}
	if err := s.pub.Publish(events.TopicGoalGraduated, outEnv, payload); err != nil {
		return fmt.Errorf("subscribers: publish goal.graduated for %s: %w", g.GoalID, err)
	}
	return nil
}

// publishProgress emits chora.consumption.goal.progress_updated.v1 (nil-safe).
// The idempotency_key (goal_id + mastered_count + trigger occurred_at) keeps a
// redelivered inbound event from re-emitting.
func (s *GoalGraduationSubscriber) publishProgress(env events.Envelope, g *goal.Goal, masteredCount, total int) error {
	if s.pub == nil {
		return nil
	}
	occurredAt := env.OccurredAt.UTC()
	outEnv := events.Envelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("chora.consumption.goal.progress_updated:%s:%d:%s", g.GoalID, masteredCount, occurredAt.Format(time.RFC3339Nano)),
		TenantID:       g.TenantID,
		GCID:           g.LearnerGCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    s.now(),
		Traceparent:    tracing.EnsureTraceparent(env.Traceparent),
		Tracestate:     env.Tracestate,
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"goal_id":           g.GoalID,
		"tenant_id":         g.TenantID,
		"learner_gcid":      g.LearnerGCID,
		"mastered_concepts": masteredCount,
		"total_concepts":    total,
		"progress_percent":  goal.ProgressPercent(masteredCount, total),
		"occurred_at":       occurredAt,
	}
	if err := s.pub.Publish(events.TopicGoalProgressUpdated, outEnv, payload); err != nil {
		return fmt.Errorf("subscribers: publish goal.progress_updated for %s: %w", g.GoalID, err)
	}
	return nil
}
