// derived_weakness_projector_grown_test.go — ADR-196 B1: the projector publishes
// chora.consumption.weakness.grown.v1 once per active→grown transition reported
// by a recover path, with recovery_source set per path, the trigger's W3C trace
// propagated, and an edge:grown_at idempotency key. Nil-safe when no publisher is
// wired; a publish failure NACKs (and rolls the fold back in production).
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// dwGrownCapture records same-tx grown emits (implements GrownOutbox).
type dwGrownCapture struct{ events []events.Event }

func (c *dwGrownCapture) PublishGrownInTx(_ context.Context, topic string, env events.Envelope, payload map[string]any) error {
	c.events = append(c.events, events.Event{Topic: topic, Envelope: env, Payload: payload})
	return nil
}

// failingGrownOutbox always fails — for the NACK path.
type failingGrownOutbox struct{ err error }

func (f failingGrownOutbox) PublishGrownInTx(context.Context, string, events.Envelope, map[string]any) error {
	return f.err
}

// dwGrownEdge is a fixture transition with a grown_at DISTINCT from the trigger
// envelope's occurred_at, so tests can prove grown_at flows from the edge (not
// re-derived from the trigger).
func dwGrownEdge(id string) lw.GrownEdge {
	return lw.GrownEdge{
		ID:            id,
		ConceptKey:    "multiplication-tables",
		ConceptLabel:  "Multiplication Tables",
		FinalStrength: 0.05,
		Tags:          []string{"arithmetic"},
		GrownAt:       time.Date(2026, 6, 28, 8, 30, 0, 0, time.UTC),
	}
}

func grownEvents(pub *dwGrownCapture) []events.Event {
	var out []events.Event
	for _, e := range pub.events {
		if e.Topic == events.TopicWeaknessGrown {
			out = append(out, e)
		}
	}
	return out
}

func TestProjector_GrownConceptKey_EmitsRewardEvent(t *testing.T) {
	pub := &dwGrownCapture{}
	repo := &dwFakeRepo{grownOnRecover: []lw.GrownEdge{dwGrownEdge("0197aaaa-0000-7000-8000-00000000aaaa")}}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.92}}
	p := newDWProjector(repo, acc).WithGrownOutbox(pub)

	env := dwEnv("ev-grown-ck")
	if err := p.HandleLiveQuizScoreAwarded(context.Background(), env, dwScorePayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	evs := grownEvents(pub)
	if len(evs) != 1 {
		t.Fatalf("weakness.grown events = %d, want 1", len(evs))
	}
	ev := evs[0]

	// Payload.
	if got := ev.Payload["growth_edge_id"]; got != "0197aaaa-0000-7000-8000-00000000aaaa" {
		t.Fatalf("growth_edge_id = %v", got)
	}
	if got := ev.Payload["recovery_source"]; got != lw.RecoverySourceConceptKey {
		t.Fatalf("recovery_source = %v, want concept_key", got)
	}
	if got := ev.Payload["concept_key"]; got != "multiplication-tables" {
		t.Fatalf("concept_key = %v", got)
	}
	if got := ev.Payload["concept_label"]; got != "Multiplication Tables" {
		t.Fatalf("concept_label = %v", got)
	}
	if got, ok := ev.Payload["final_strength"].(float64); !ok || got != 0.05 {
		t.Fatalf("final_strength = %v", ev.Payload["final_strength"])
	}
	if got := ev.Payload["tenant_id"]; got != dwTenant {
		t.Fatalf("tenant_id = %v", got)
	}
	if got := ev.Payload["learner_gcid"]; got != dwGCID {
		t.Fatalf("learner_gcid = %v", got)
	}
	if tags, ok := ev.Payload["tags"].([]string); !ok || len(tags) != 1 || tags[0] != "arithmetic" {
		t.Fatalf("tags = %v", ev.Payload["tags"])
	}
	wantGrownAt := time.Date(2026, 6, 28, 8, 30, 0, 0, time.UTC)
	if got, ok := ev.Payload["grown_at"].(time.Time); !ok || !got.Equal(wantGrownAt) {
		t.Fatalf("grown_at = %v, want %v", ev.Payload["grown_at"], wantGrownAt)
	}

	// Envelope: tenant/gcid stamped, occurred_at == the TRIGGER's occurred_at,
	// traceparent propagated from the trigger, idempotency_key = edge:grown_at.
	if ev.Envelope.TenantID != dwTenant || ev.Envelope.GCID != dwGCID {
		t.Fatalf("envelope identity = %q/%q", ev.Envelope.TenantID, ev.Envelope.GCID)
	}
	if !ev.Envelope.OccurredAt.Equal(env.OccurredAt) {
		t.Fatalf("envelope occurred_at = %v, want trigger %v", ev.Envelope.OccurredAt, env.OccurredAt)
	}
	if ev.Envelope.Traceparent != env.Traceparent {
		t.Fatalf("traceparent = %q, want propagated %q", ev.Envelope.Traceparent, env.Traceparent)
	}
	if ev.Envelope.PublishedAt.IsZero() {
		t.Fatal("published_at must be set")
	}
	if ev.Envelope.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d", ev.Envelope.SchemaVersion)
	}
	if ev.Envelope.SourceService != events.SourceService {
		t.Fatalf("source_service = %q", ev.Envelope.SourceService)
	}
	if _, err := uuid.Parse(ev.Envelope.EventID); err != nil {
		t.Fatalf("event_id not a uuid: %q", ev.Envelope.EventID)
	}
	wantKey := "chora.consumption.weakness.grown:0197aaaa-0000-7000-8000-00000000aaaa:" + wantGrownAt.Format(time.RFC3339Nano)
	if ev.Envelope.IdempotencyKey != wantKey {
		t.Fatalf("idempotency_key = %q, want %q", ev.Envelope.IdempotencyKey, wantKey)
	}
}

func TestProjector_GrownDrillAtom_EmitsDrillSource(t *testing.T) {
	pub := &dwGrownCapture{}
	repo := &dwFakeRepo{grownOnDrill: []lw.GrownEdge{dwGrownEdge("0197bbbb-0000-7000-8000-00000000bbbb")}}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.88}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms()).WithGrownOutbox(pub)

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-grown-drill"), dwSessionPayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := grownEvents(pub)
	if len(evs) != 1 {
		t.Fatalf("weakness.grown events = %d, want 1", len(evs))
	}
	if got := evs[0].Payload["recovery_source"]; got != lw.RecoverySourceDrillAtom {
		t.Fatalf("recovery_source = %v, want drill_atom", got)
	}
	if got := evs[0].Payload["growth_edge_id"]; got != "0197bbbb-0000-7000-8000-00000000bbbb" {
		t.Fatalf("growth_edge_id = %v", got)
	}
}

func TestProjector_BothPathsGrow_EmitsTwoWithDistinctSources(t *testing.T) {
	pub := &dwGrownCapture{}
	repo := &dwFakeRepo{
		grownOnRecover: []lw.GrownEdge{dwGrownEdge("edge-ck")},
		grownOnDrill:   []lw.GrownEdge{dwGrownEdge("edge-da")},
	}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"fractions": 0.9}}
	p := newDWProjector(repo, acc).WithAtomIndex(dwMCQAtoms()).WithGrownOutbox(pub)

	if err := p.HandleAtomSessionCompleted(context.Background(), dwEnv("ev-grown-both"), dwSessionPayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := grownEvents(pub)
	if len(evs) != 2 {
		t.Fatalf("weakness.grown events = %d, want 2", len(evs))
	}
	bySource := map[string]string{}
	for _, e := range evs {
		bySource[e.Payload["recovery_source"].(string)] = e.Payload["growth_edge_id"].(string)
	}
	if bySource[lw.RecoverySourceConceptKey] != "edge-ck" {
		t.Fatalf("concept_key edge = %q", bySource[lw.RecoverySourceConceptKey])
	}
	if bySource[lw.RecoverySourceDrillAtom] != "edge-da" {
		t.Fatalf("drill_atom edge = %q", bySource[lw.RecoverySourceDrillAtom])
	}
}

func TestProjector_RecoveredButNotGrown_NoEmit(t *testing.T) {
	pub := &dwGrownCapture{}
	repo := &dwFakeRepo{} // grownOnRecover/grownOnDrill empty — recovered but did not grow
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.95}}
	p := newDWProjector(repo, acc).WithGrownOutbox(pub)

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-nogrow"), dwScorePayload(true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := len(grownEvents(pub)); got != 0 {
		t.Fatalf("no grow must emit nothing; got %d", got)
	}
	// The recover still ran.
	if len(repo.recovers) != 1 {
		t.Fatalf("recover should still be called; recovers = %d", len(repo.recovers))
	}
}

func TestProjector_NoPublisherWired_NoPanicNoEmit(t *testing.T) {
	repo := &dwFakeRepo{grownOnRecover: []lw.GrownEdge{dwGrownEdge("edge-x")}}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.9}}
	p := newDWProjector(repo, acc) // no WithGrownOutbox

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-nopub"), dwScorePayload(true)); err != nil {
		t.Fatalf("Handle must not error without a grown publisher: %v", err)
	}
}

func TestProjector_GrownPublishError_Nacks(t *testing.T) {
	repo := &dwFakeRepo{grownOnRecover: []lw.GrownEdge{dwGrownEdge("edge-x")}}
	acc := &dwFakeAccuracy{accuracy: map[string]float64{"multiplication tables": 0.9}}
	p := newDWProjector(repo, acc).WithGrownOutbox(failingGrownOutbox{err: errors.New("boom")})

	if err := p.HandleLiveQuizScoreAwarded(context.Background(), dwEnv("ev-puberr"), dwScorePayload(true)); err == nil {
		t.Fatal("a grown-publish failure must surface (Pub/Sub NACK)")
	}
}
