// campaign_emitter.go — envelope-building emitter for the 5 ADR-227
// campaign.*.v1 events (WS-C1, CHO-2080). Implements campaign.EventSink for
// the grader (rung_cleared / node_won) and offers FocusAssigned / GoalSealed
// for the goals campaign handlers. node_revealed's emitter lands with its
// WS-C4 reveal trigger.
//
// Every event carries goal_id (Verification addendum #6) and rides the
// transactional-outbox Publisher (persist-then-emit posture; the outbox row
// is durable-pending once Publish returns). Idempotency keys follow the
// proto docs so Schema-Registry-visible redeliveries dedupe cleanly.
package events

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

// CampaignEmitter builds envelope-complete campaign events.
type CampaignEmitter struct {
	pub Publisher
	now func() time.Time
}

// NewCampaignEmitter wires the emitter; now stamps PublishedAt (nil →
// time.Now UTC).
func NewCampaignEmitter(pub Publisher, now func() time.Time) *CampaignEmitter {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CampaignEmitter{pub: pub, now: now}
}

// Compile-time check: the emitter is the grader's sink.
var _ campaign.EventSink = (*CampaignEmitter)(nil)

func (e *CampaignEmitter) envelope(tenantID, gcid, idempotencyKey string, occurredAt time.Time, traceparent, tracestate string) Envelope {
	return Envelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: idempotencyKey,
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     occurredAt.UTC(),
		PublishedAt:    e.now().UTC(),
		Traceparent:    tracing.EnsureTraceparent(traceparent),
		Tracestate:     tracestate,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
}

// CampaignRungCleared emits chora.consumption.campaign.rung_cleared.v1
// (campaign.EventSink). Refreshers share the topic with is_refresher=true —
// chora-identity prices them reduced (D10).
func (e *CampaignEmitter) CampaignRungCleared(ctx context.Context, ev campaign.RungClearedEvent) error {
	_ = ctx // outbox Publish is durably ctx-free (see outbox/publisher.go)
	idem := fmt.Sprintf("%s:%s:%s:%d:%s", TopicCampaignRungCleared, ev.GoalID, ev.ConceptID, int(ev.Rung), ev.ClearedAt.UTC().Format(time.RFC3339Nano))
	env := e.envelope(ev.TenantID, ev.LearnerGCID, idem, ev.ClearedAt, tracing.TraceparentFromContext(ctx), "")
	return e.pub.Publish(TopicCampaignRungCleared, env, map[string]any{
		"goal_id":         ev.GoalID,
		"tenant_id":       ev.TenantID,
		"learner_gcid":    ev.LearnerGCID,
		"concept_id":      ev.ConceptID,
		"concept_key":     ev.ConceptKey,
		"rung":            int(ev.Rung),
		"is_refresher":    ev.IsRefresher,
		"correct_answers": ev.CorrectAnswers,
		"cleared_at":      ev.ClearedAt.UTC(),
	})
}

// CampaignNodeWon emits chora.consumption.campaign.node_won.v1
// (campaign.EventSink). The D9 ratchet makes re-emission structurally
// impossible; WS-C4's reveal consumer anchors on this event's event_id.
func (e *CampaignEmitter) CampaignNodeWon(ctx context.Context, ev campaign.NodeWonEvent) error {
	idem := fmt.Sprintf("%s:%s:%s:%s", TopicCampaignNodeWon, ev.GoalID, ev.ConceptID, ev.WonAt.UTC().Format(time.RFC3339Nano))
	env := e.envelope(ev.TenantID, ev.LearnerGCID, idem, ev.WonAt, tracing.TraceparentFromContext(ctx), "")
	return e.pub.Publish(TopicCampaignNodeWon, env, map[string]any{
		"goal_id":      ev.GoalID,
		"tenant_id":    ev.TenantID,
		"learner_gcid": ev.LearnerGCID,
		"concept_id":   ev.ConceptID,
		"concept_key":  ev.ConceptKey,
		"won_at":       ev.WonAt.UTC(),
	})
}

// CampaignNodeRevealedInput feeds NodeRevealed (subscriber-driven; fired after
// a free-on-win reveal request publishes, carrying the won event's trace
// headers). WonEventID anchors idempotency on the win.
type CampaignNodeRevealedInput struct {
	TenantID    string
	LearnerGCID string
	GoalID      string
	ConceptID   string
	ConceptKey  string
	WonEventID  string
	RevealedAt  time.Time
	Traceparent string
	Tracestate  string
}

// NodeRevealed emits chora.consumption.campaign.node_revealed.v1 (WS-C7,
// CHO-2083 follow-up): the mirror of the free-on-win reveal that CHO-2083's
// subscriber publishes. suggestion_count is 0 — the fog orchestrator decides
// the count downstream, unknown at trigger. The idempotency key anchors on the
// won event id so a crash-window re-publish of the reveal dedupes cleanly
// (matching the reveal request's own deterministic key).
func (e *CampaignEmitter) NodeRevealed(_ context.Context, in CampaignNodeRevealedInput) error {
	idem := fmt.Sprintf("%s:%s", TopicCampaignNodeRevealed, in.WonEventID)
	env := e.envelope(in.TenantID, in.LearnerGCID, idem, in.RevealedAt, in.Traceparent, in.Tracestate)
	return e.pub.Publish(TopicCampaignNodeRevealed, env, map[string]any{
		"goal_id":           in.GoalID,
		"tenant_id":         in.TenantID,
		"learner_gcid":      in.LearnerGCID,
		"focal_concept_id":  in.ConceptID,
		"focal_concept_key": in.ConceptKey,
		"suggestion_count":  0,
		"won_event_id":      in.WonEventID,
		"revealed_at":       in.RevealedAt.UTC(),
	})
}

// CampaignFocusAssignedInput feeds FocusAssigned (handler-driven; carries the
// request's trace headers).
type CampaignFocusAssignedInput struct {
	TenantID          string
	LearnerGCID       string
	GoalID            string
	ConceptID         string
	ConceptKey        string
	PreviousConceptID string
	CompanionID       string
	AssignedAt        time.Time
	Traceparent       string
	Tracestate        string
}

// FocusAssigned emits chora.consumption.campaign.focus_assigned.v1 (D11).
func (e *CampaignEmitter) FocusAssigned(_ context.Context, in CampaignFocusAssignedInput) error {
	idem := fmt.Sprintf("%s:%s:%s:%s", TopicCampaignFocusAssigned, in.GoalID, in.ConceptID, in.AssignedAt.UTC().Format(time.RFC3339Nano))
	env := e.envelope(in.TenantID, in.LearnerGCID, idem, in.AssignedAt, in.Traceparent, in.Tracestate)
	return e.pub.Publish(TopicCampaignFocusAssigned, env, map[string]any{
		"goal_id":             in.GoalID,
		"tenant_id":           in.TenantID,
		"learner_gcid":        in.LearnerGCID,
		"concept_id":          in.ConceptID,
		"concept_key":         in.ConceptKey,
		"previous_concept_id": in.PreviousConceptID,
		"companion_id":        in.CompanionID,
		"assigned_at":         in.AssignedAt.UTC(),
	})
}

// CampaignGoalSealedInput feeds GoalSealed (handler-driven).
type CampaignGoalSealedInput struct {
	TenantID       string
	LearnerGCID    string
	GoalID         string
	RootConceptID  string
	RootConceptKey string
	NodesWon       int
	IsReseal       bool
	SealedAt       time.Time
	Traceparent    string
	Tracestate     string
}

// GoalSealed emits chora.consumption.campaign.goal_sealed.v1 (D3): only ever
// called after campaign.EvaluateSeal verified the frontier-clear — tier-S XP
// rides that verification, never a bare declaration.
func (e *CampaignEmitter) GoalSealed(_ context.Context, in CampaignGoalSealedInput) error {
	idem := fmt.Sprintf("%s:%s:%s", TopicCampaignGoalSealed, in.GoalID, in.SealedAt.UTC().Format(time.RFC3339Nano))
	env := e.envelope(in.TenantID, in.LearnerGCID, idem, in.SealedAt, in.Traceparent, in.Tracestate)
	return e.pub.Publish(TopicCampaignGoalSealed, env, map[string]any{
		"goal_id":          in.GoalID,
		"tenant_id":        in.TenantID,
		"learner_gcid":     in.LearnerGCID,
		"root_concept_id":  in.RootConceptID,
		"root_concept_key": in.RootConceptKey,
		"nodes_won":        in.NodesWon,
		"is_reseal":        in.IsReseal,
		"sealed_at":        in.SealedAt.UTC(),
	})
}
