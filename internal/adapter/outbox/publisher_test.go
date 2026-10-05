// Package outbox_test — Publisher adapter tests for chora-consumption D6.2.
//
// Publisher satisfies the existing `events.Publisher` port (Phyllis MVP
// interface in `services/chora-consumption/internal/adapter/events/publisher.go`)
// by writing each Publish call into the outbox_events table via the Store
// port instead of into the in-memory slice. A separate Dispatcher drains
// the outbox to Cloud Pub/Sub. This decouples emission from Pub/Sub
// availability: a crash between state-write and publish no longer loses
// the event because the row is durably committed before the HTTP request
// returns.
//
// Per `feedback_d6_resilience_first_class` B.6.2.a — producer-side durable
// emission for chora-consumption's `chora.consumption.*` stream.
package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

func newEnvelope(eventID, idem, tenant, gcid string, now time.Time) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: idem,
		TenantID:       tenant,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-deadbeefdeadbeefdeadbeefdeadbeef-1111111122222222-01",
		Tracestate:     "",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

func TestOutboxPublisher_Publish_WritesRowToStore(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		AggregateType: "atom_session",
	})

	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	env := newEnvelope("evt-1", "idem-1",
		"00000000-0000-0000-0000-0000000000aa",
		"00000000-0000-0000-0000-0000000000bb", now)

	payload := map[string]any{
		"session_id":   "sess-1",
		"completed_at": now.Format(time.RFC3339),
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != "chora.consumption.atom_session.completed.v1" {
		t.Errorf("row.Topic = %q; want chora.consumption.atom_session.completed.v1", row.Topic)
	}
	if row.TenantID != env.TenantID {
		t.Errorf("row.TenantID = %q; want %q", row.TenantID, env.TenantID)
	}
	if row.GCID != env.GCID {
		t.Errorf("row.GCID = %q; want %q", row.GCID, env.GCID)
	}
	if row.IdempotencyKey != env.IdempotencyKey {
		t.Errorf("row.IdempotencyKey = %q; want %q", row.IdempotencyKey, env.IdempotencyKey)
	}
	if row.EventType == "" {
		t.Errorf("row.EventType empty")
	}
	if row.AggregateType != "atom_session" {
		t.Errorf("row.AggregateType = %q; want atom_session", row.AggregateType)
	}
}

func TestOutboxPublisher_Publish_StampsEnvelopeFieldsAsMap(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	now := time.Date(2026, 5, 12, 11, 0, 0, 0, time.UTC)
	env := newEnvelope("evt-2", "idem-2", "00000000-0000-0000-0000-0000000000cc", "", now)

	if err := pub.Publish("chora.consumption.daily_dose.served.v1", env, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	e := rows[0].Envelope
	for _, key := range []string{
		"event_id", "idempotency_key", "tenant_id", "occurred_at", "published_at",
		"traceparent", "source_project", "source_service", "schema_version",
	} {
		if e[key] == "" {
			t.Errorf("envelope.%s empty; want non-empty (mandatory per CLAUDE.md §6)", key)
		}
	}
	if e["source_project"] != "chora-content" {
		t.Errorf("envelope.source_project = %q; want chora-content", e["source_project"])
	}
	if e["source_service"] != "chora-consumption" {
		t.Errorf("envelope.source_service = %q; want chora-consumption", e["source_service"])
	}
	if e["schema_version"] != "1" {
		t.Errorf("envelope.schema_version = %q; want 1", e["schema_version"])
	}
}

func TestOutboxPublisher_Publish_RejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	// missing tenant_id
	bad := newEnvelope("evt-3", "idem-3", "", "g", time.Now().UTC())
	err := pub.Publish("chora.consumption.atom_session.completed.v1", bad, map[string]any{"x": 1})
	if err == nil {
		t.Errorf("Publish(invalid envelope) = nil; want error")
	}
}

func TestOutboxPublisher_Publish_RejectsBadTopic(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := newEnvelope("evt-X", "idem-X", "t", "g", time.Now().UTC())
	// Non-canonical topic — must reject (chora-consumption only emits
	// chora.consumption.* events).
	err := pub.Publish("chora.gamification.events", env, map[string]any{"x": 1})
	if err == nil {
		t.Errorf("Publish(legacy topic) = nil; want canonical-topic error")
	}
}

func TestOutboxPublisher_Publish_RejectsMissingStore(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{})
	env := newEnvelope("evt-4", "idem-4", "t", "g", time.Now().UTC())
	err := pub.Publish("chora.consumption.atom_session.completed.v1", env, map[string]any{"x": 1})
	if err == nil {
		t.Errorf("Publish without store = nil err; want error")
	}
}

func TestOutboxPublisher_Publish_PayloadIsJSON(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	env := newEnvelope("evt-5", "idem-5", "t", "g", time.Now().UTC())
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, map[string]any{"score": 99, "verdict": "passed"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	var pl map[string]any
	if err := json.Unmarshal(rows[0].Payload, &pl); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, string(rows[0].Payload))
	}
	if pl["verdict"] != "passed" {
		t.Errorf("payload.verdict = %v; want passed", pl["verdict"])
	}
}

func TestOutboxPublisher_Publish_DefaultsAggregateType(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store}) // no AggregateType
	env := newEnvelope("evt-6", "idem-6", "t", "g", time.Now().UTC())
	if err := pub.Publish("chora.consumption.atom_session.started.v1", env, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) == 0 || rows[0].AggregateType == "" {
		t.Errorf("AggregateType not defaulted; got %v", rows)
	}
}

func TestOutboxPublisher_Publish_DerivesAggregateIDFromPayload(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	env := newEnvelope("evt-7", "idem-7", "t", "g", time.Now().UTC())
	payload := map[string]any{
		"session_id": "sess-deadbeef",
		"score":      88,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].AggregateID != "sess-deadbeef" {
		t.Errorf("AggregateID = %q; want sess-deadbeef (derived from payload.session_id)", rows[0].AggregateID)
	}
}

func TestOutboxPublisher_Publish_IdempotencyKeyRejectsDuplicate(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	env := newEnvelope("evt-A", "idem-DUPE", "t", "g", time.Now().UTC())
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Publish 1: %v", err)
	}
	// Second emit with same idempotency_key — rejected.
	env2 := newEnvelope("evt-B", "idem-DUPE", "t", "g", time.Now().UTC())
	err := pub.Publish("chora.consumption.atom_session.completed.v1", env2, map[string]any{"x": 2})
	if err == nil {
		t.Errorf("Publish dup idem = nil; want ErrDuplicateIdempotencyKey")
	}
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Errorf("err = %v; want ErrDuplicateIdempotencyKey", err)
	}
}

func TestOutboxPublisher_Publish_DerivesEventType(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	env := newEnvelope("evt-ET", "idem-ET", "t", "g", time.Now().UTC())
	if err := pub.Publish("chora.consumption.learning_path.completed.v1", env, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if !strings.Contains(rows[0].EventType, "learning_path") {
		t.Errorf("EventType = %q; expected to contain 'learning_path'", rows[0].EventType)
	}
}

// Compile-time check that Publisher satisfies events.Publisher.
var _ events.Publisher = (*outbox.Publisher)(nil)
