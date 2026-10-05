// campaign_emitter_test.go — WS-C1 (CHO-2080) gate for the campaign event
// emitter: every emission must ride the canonical topic with an
// envelope-complete Envelope (fail-loud idempotency keys per the proto docs)
// and a payload whose KEY SET matches the protomarshal encoder exactly (the
// round-trip tests in protomarshal own the wire bytes; this test owns the
// producer side of that contract).
package events

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

type capturedPublish struct {
	topic   string
	env     Envelope
	payload map[string]any
}

type fakePublisher struct {
	calls []capturedPublish
}

func (f *fakePublisher) Publish(topic string, env Envelope, payload map[string]any) error {
	f.calls = append(f.calls, capturedPublish{topic: topic, env: env, payload: payload})
	return nil
}

func assertEnvelopeComplete(t *testing.T, env Envelope, tenant, gcid string) {
	t.Helper()
	if env.EventID == "" || env.IdempotencyKey == "" {
		t.Errorf("envelope ids missing: %+v", env)
	}
	if env.TenantID != tenant || env.GCID != gcid {
		t.Errorf("envelope identity = %s/%s", env.TenantID, env.GCID)
	}
	if env.OccurredAt.IsZero() || env.PublishedAt.IsZero() {
		t.Errorf("envelope times missing: %+v", env)
	}
	if env.Traceparent == "" {
		t.Error("traceparent must be ensured, never empty")
	}
	if env.SourceProject != SourceProject || env.SourceService != SourceService {
		t.Errorf("envelope provenance = %s/%s", env.SourceProject, env.SourceService)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("schema_version = %d", env.SchemaVersion)
	}
}

func assertPayloadKeys(t *testing.T, payload map[string]any, want []string) {
	t.Helper()
	if len(payload) != len(want) {
		t.Errorf("payload has %d keys, want %d: %v", len(payload), len(want), payload)
	}
	for _, k := range want {
		if _, ok := payload[k]; !ok {
			t.Errorf("payload missing key %q", k)
		}
	}
}

const (
	emitTenant  = "01971a00-0000-7000-8000-0000000000a1"
	emitGCID    = "01971a00-0000-7000-8000-0000000000b1"
	emitGoal    = "01971a00-0000-7000-8000-0000000000c1"
	emitConcept = "01971a00-0000-7000-8000-0000000000d1"
)

var emitNow = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

func newTestEmitter(pub *fakePublisher) *CampaignEmitter {
	return NewCampaignEmitter(pub, func() time.Time { return emitNow })
}

func TestCampaignEmitter_RungCleared(t *testing.T) {
	pub := &fakePublisher{}
	e := newTestEmitter(pub)

	err := e.CampaignRungCleared(context.Background(), campaign.RungClearedEvent{
		TenantID: emitTenant, LearnerGCID: emitGCID, GoalID: emitGoal,
		ConceptID: emitConcept, ConceptKey: "photosynthesis",
		Rung: campaign.RungEvaluation, IsRefresher: true, CorrectAnswers: 1,
		ClearedAt: emitNow.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("CampaignRungCleared: %v", err)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("publishes = %d", len(pub.calls))
	}
	c := pub.calls[0]
	if c.topic != TopicCampaignRungCleared {
		t.Errorf("topic = %q", c.topic)
	}
	assertEnvelopeComplete(t, c.env, emitTenant, emitGCID)
	assertPayloadKeys(t, c.payload, []string{
		"goal_id", "tenant_id", "learner_gcid", "concept_id", "concept_key",
		"rung", "is_refresher", "correct_answers", "cleared_at",
	})
	if c.payload["rung"] != int(campaign.RungEvaluation) {
		t.Errorf("rung payload = %v (ladder int expected)", c.payload["rung"])
	}
}

func TestCampaignEmitter_NodeWon(t *testing.T) {
	pub := &fakePublisher{}
	e := newTestEmitter(pub)

	if err := e.CampaignNodeWon(context.Background(), campaign.NodeWonEvent{
		TenantID: emitTenant, LearnerGCID: emitGCID, GoalID: emitGoal,
		ConceptID: emitConcept, ConceptKey: "photosynthesis", WonAt: emitNow,
	}); err != nil {
		t.Fatalf("CampaignNodeWon: %v", err)
	}
	c := pub.calls[0]
	if c.topic != TopicCampaignNodeWon {
		t.Errorf("topic = %q", c.topic)
	}
	assertEnvelopeComplete(t, c.env, emitTenant, emitGCID)
	assertPayloadKeys(t, c.payload, []string{
		"goal_id", "tenant_id", "learner_gcid", "concept_id", "concept_key", "won_at",
	})
}

func TestCampaignEmitter_FocusAssigned(t *testing.T) {
	pub := &fakePublisher{}
	e := newTestEmitter(pub)

	if err := e.FocusAssigned(context.Background(), CampaignFocusAssignedInput{
		TenantID: emitTenant, LearnerGCID: emitGCID, GoalID: emitGoal,
		ConceptID: emitConcept, ConceptKey: "photosynthesis",
		PreviousConceptID: "", CompanionID: "01971a00-0000-7000-8000-0000000000e1",
		AssignedAt: emitNow, Traceparent: "", Tracestate: "",
	}); err != nil {
		t.Fatalf("FocusAssigned: %v", err)
	}
	c := pub.calls[0]
	if c.topic != TopicCampaignFocusAssigned {
		t.Errorf("topic = %q", c.topic)
	}
	assertEnvelopeComplete(t, c.env, emitTenant, emitGCID)
	assertPayloadKeys(t, c.payload, []string{
		"goal_id", "tenant_id", "learner_gcid", "concept_id", "concept_key",
		"previous_concept_id", "companion_id", "assigned_at",
	})
}

func TestCampaignEmitter_GoalSealed(t *testing.T) {
	pub := &fakePublisher{}
	e := newTestEmitter(pub)

	if err := e.GoalSealed(context.Background(), CampaignGoalSealedInput{
		TenantID: emitTenant, LearnerGCID: emitGCID, GoalID: emitGoal,
		RootConceptID: emitConcept, RootConceptKey: "photosynthesis",
		NodesWon: 7, IsReseal: true, SealedAt: emitNow,
	}); err != nil {
		t.Fatalf("GoalSealed: %v", err)
	}
	c := pub.calls[0]
	if c.topic != TopicCampaignGoalSealed {
		t.Errorf("topic = %q", c.topic)
	}
	assertEnvelopeComplete(t, c.env, emitTenant, emitGCID)
	assertPayloadKeys(t, c.payload, []string{
		"goal_id", "tenant_id", "learner_gcid", "root_concept_id", "root_concept_key",
		"nodes_won", "is_reseal", "sealed_at",
	})
	if c.payload["is_reseal"] != true {
		t.Errorf("is_reseal payload = %v", c.payload["is_reseal"])
	}
}

func TestCampaignEmitter_NodeRevealed(t *testing.T) {
	pub := &fakePublisher{}
	e := newTestEmitter(pub)

	const wonEventID = "01971a00-0000-7000-8000-0000000000e9"
	if err := e.NodeRevealed(context.Background(), CampaignNodeRevealedInput{
		TenantID: emitTenant, LearnerGCID: emitGCID, GoalID: emitGoal,
		ConceptID: emitConcept, ConceptKey: "photosynthesis",
		WonEventID: wonEventID, RevealedAt: emitNow,
		Traceparent: "00-11111111111111111111111111111111-2222222222222222-01",
	}); err != nil {
		t.Fatalf("NodeRevealed: %v", err)
	}
	c := pub.calls[0]
	if c.topic != TopicCampaignNodeRevealed {
		t.Errorf("topic = %q; want %q", c.topic, TopicCampaignNodeRevealed)
	}
	assertEnvelopeComplete(t, c.env, emitTenant, emitGCID)
	assertPayloadKeys(t, c.payload, []string{
		"goal_id", "tenant_id", "learner_gcid", "focal_concept_id", "focal_concept_key",
		"suggestion_count", "won_event_id", "revealed_at",
	})
	// suggestion_count is unknown at trigger → 0 (the fog orchestrator counts).
	if c.payload["suggestion_count"] != 0 {
		t.Errorf("suggestion_count = %v; want 0 (unknown at trigger)", c.payload["suggestion_count"])
	}
	if c.payload["won_event_id"] != wonEventID {
		t.Errorf("won_event_id = %v; want %s", c.payload["won_event_id"], wonEventID)
	}
	// Idempotency anchors on the won event id so a crash-window re-publish
	// dedupes downstream.
	wantIdem := TopicCampaignNodeRevealed + ":" + wonEventID
	if c.env.IdempotencyKey != wantIdem {
		t.Errorf("idempotency_key = %s; want %s", c.env.IdempotencyKey, wantIdem)
	}
}
