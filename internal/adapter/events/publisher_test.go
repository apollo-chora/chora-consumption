package events

import (
	"strings"
	"testing"
	"time"
)

const (
	tTenant      = "01970000-0000-7000-8000-000000000001"
	tGCID        = "01970000-0000-7000-9000-000000000001"
	tTraceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	tTracestate  = "rojo=00f067aa0ba902b7"
)

// TestPublish_HappyPath_Envelope — envelope satisfies the mandatory shape
// per chora-contracts/proto/events/envelope.proto.
func TestPublish_HappyPath_Envelope(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	err := p.Publish(TopicAtomSessionCompleted, env, map[string]any{
		"atom_id": "01970000-0000-7000-a000-000000000001",
		"grade":   "i_remember",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	events := p.Events()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	got := events[0].Envelope
	if got.EventID == "" {
		t.Error("EventID empty")
	}
	if got.IdempotencyKey == "" {
		t.Error("IdempotencyKey empty")
	}
	if got.TenantID != tTenant {
		t.Errorf("TenantID = %q, want %q", got.TenantID, tTenant)
	}
	if got.GCID != tGCID {
		t.Errorf("GCID = %q, want %q", got.GCID, tGCID)
	}
	if got.OccurredAt.IsZero() {
		t.Error("OccurredAt zero")
	}
	if got.PublishedAt.IsZero() {
		t.Error("PublishedAt zero")
	}
	if got.Traceparent != tTraceparent {
		t.Errorf("Traceparent = %q, want %q", got.Traceparent, tTraceparent)
	}
	if got.Tracestate != tTracestate {
		t.Errorf("Tracestate = %q, want %q", got.Tracestate, tTracestate)
	}
	if got.SourceProject != SourceProject {
		t.Errorf("SourceProject = %q, want %q", got.SourceProject, SourceProject)
	}
	if got.SourceService != SourceService {
		t.Errorf("SourceService = %q, want %q", got.SourceService, SourceService)
	}
	if got.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", got.SchemaVersion)
	}
}

// TestPublish_RejectsMissingTraceparent enforces the CLAUDE.md §6 rule:
// "W3C traceparent/tracestate in event envelope mandatory".
func TestPublish_RejectsMissingTraceparent(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, "", "", "")
	err := p.Publish(TopicAtomSessionCompleted, env, map[string]any{})
	if err == nil {
		t.Error("expected error for missing traceparent")
	}
	if !strings.Contains(err.Error(), "traceparent") {
		t.Errorf("error = %v, want to mention traceparent", err)
	}
}

// TestPublish_RejectsMissingTenant — tenant_id must always be present.
func TestPublish_RejectsMissingTenant(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope("", tGCID, tTraceparent, tTracestate, "")
	err := p.Publish(TopicAtomSessionCompleted, env, map[string]any{})
	if err == nil {
		t.Error("expected error for missing tenant_id")
	}
}

// TestPublish_RejectsMissingTopic — topic name required.
func TestPublish_RejectsMissingTopic(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	err := p.Publish("", env, nil)
	if err == nil {
		t.Error("expected error for empty topic")
	}
}

// TestPublish_IdempotencyKeyDefaultsToEventID — empty idempotency_key
// auto-fills with event_id per envelope.proto docstring "Often equals event_id".
func TestPublish_IdempotencyKeyDefaultsToEventID(t *testing.T) {
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	if env.IdempotencyKey != env.EventID {
		t.Errorf("IdempotencyKey = %q, want = EventID %q",
			env.IdempotencyKey, env.EventID)
	}
}

// TestPublish_IdempotencyKeyExplicit — explicit key is preserved.
func TestPublish_IdempotencyKeyExplicit(t *testing.T) {
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "session-123-grade-1")
	if env.IdempotencyKey != "session-123-grade-1" {
		t.Errorf("IdempotencyKey = %q, want session-123-grade-1", env.IdempotencyKey)
	}
}

// TestPublish_IdempotencyKeyUniquenessAcrossEvents — distinct events get
// distinct event_ids by default (UUIDv7 collision rate 0).
func TestPublish_IdempotencyKeyUniquenessAcrossEvents(t *testing.T) {
	p := NewInMemoryPublisher()
	for i := 0; i < 10; i++ {
		env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
		if err := p.Publish(TopicDailyDoseServed, env, nil); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	events := p.Events()
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.Envelope.EventID] {
			t.Errorf("duplicate event_id %q (UUIDv7 expected unique)", e.Envelope.EventID)
		}
		seen[e.Envelope.EventID] = true
	}
}

// TestPublish_TraceContextPropagated — multiple events from the same handler
// invocation share traceparent (caller responsibility).
func TestPublish_TraceContextPropagated(t *testing.T) {
	p := NewInMemoryPublisher()
	env1 := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	env2 := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	p.Publish(TopicAtomSessionCompleted, env1, nil)
	p.Publish(TopicDailyDoseServed, env2, nil)
	events := p.Events()
	if len(events) != 2 {
		t.Fatalf("count = %d, want 2", len(events))
	}
	for i, e := range events {
		if e.Envelope.Traceparent != tTraceparent {
			t.Errorf("event[%d] traceparent = %q, want %q", i, e.Envelope.Traceparent, tTraceparent)
		}
	}
}

// TestPublish_EventsSliceIsCopy — Events() returns a defensive copy.
func TestPublish_EventsSliceIsCopy(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	p.Publish(TopicAtomSessionCompleted, env, nil)
	got := p.Events()
	got[0].Topic = "TAMPERED"
	got2 := p.Events()
	if got2[0].Topic == "TAMPERED" {
		t.Error("Events() returned shared slice — should be defensive copy")
	}
}

// TestPublish_ChannelFanOut — events flow into the channel for observers.
func TestPublish_ChannelFanOut(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	if err := p.Publish(TopicDailyDoseServed, env, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-p.Channel():
		if ev.Topic != TopicDailyDoseServed {
			t.Errorf("topic = %q, want %q", ev.Topic, TopicDailyDoseServed)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected event on channel within 100ms")
	}
}

// TestPublish_EnforcesSchemaVersion — schema_version must be >= 1.
func TestPublish_EnforcesSchemaVersion(t *testing.T) {
	p := NewInMemoryPublisher()
	env := NewEnvelope(tTenant, tGCID, tTraceparent, tTracestate, "")
	env.SchemaVersion = 0
	if err := p.Publish(TopicAtomSessionCompleted, env, nil); err == nil {
		t.Error("expected error for schema_version=0")
	}
}

// TestTopicNamesPinned — topic strings are pinned to spec values.
func TestTopicNamesPinned(t *testing.T) {
	if TopicAtomSessionCompleted != "chora.consumption.atom_session.completed.v1" {
		t.Errorf("TopicAtomSessionCompleted = %q (drift)", TopicAtomSessionCompleted)
	}
	if TopicDailyDoseServed != "chora.consumption.daily_dose.served.v1" {
		t.Errorf("TopicDailyDoseServed = %q (drift)", TopicDailyDoseServed)
	}
}
