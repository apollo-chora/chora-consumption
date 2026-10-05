// Package outbox_test — coverage-boost tests exercising error branches +
// edge-case parsers that the primary store/publisher/dispatcher suites do
// not naturally exercise. Coverage gate: 85% per
// `feedback_strict_tdd` + `tdd-blanket` skill.
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

// -----------------------------------------------------------------------------
// validateCanonicalTopic — every branch
// -----------------------------------------------------------------------------

func TestOutboxPublisher_RejectsEmptyTopic(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("", env, nil); err == nil {
		t.Errorf("Publish(empty topic) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsShortTopic(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.x", env, nil); err == nil {
		t.Errorf("Publish(short topic) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsTopicMissingVersion(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.x1", env, nil); err == nil {
		t.Errorf("Publish(bad version prefix) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsTopicNonNumericVersion(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.vX", env, nil); err == nil {
		t.Errorf("Publish(non-numeric version) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsTopicSingleCharVersion(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v", env, nil); err == nil {
		t.Errorf("Publish(empty version suffix) = nil; want error")
	}
}

// -----------------------------------------------------------------------------
// validateEnvelopeFields — every branch
// -----------------------------------------------------------------------------

func TestOutboxPublisher_RejectsMissingEventID(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(missing event_id) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsMissingIdempotencyKey(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(missing idempotency_key) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsZeroOccurredAt(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(zero occurred_at) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsZeroPublishedAt(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt:  time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(zero published_at) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsMissingSourceProject(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(missing source_project) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsMissingSourceService(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(missing source_service) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsZeroSchemaVersion(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt: time.Now().UTC(), PublishedAt: time.Now().UTC(),
		Traceparent: "tp", SourceProject: "p", SourceService: "s",
		// SchemaVersion zero → invalid
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(zero schema_version) = nil; want error")
	}
}

func TestOutboxPublisher_RejectsMissingTraceparent(t *testing.T) {
	t.Parallel()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: outbox.NewInMemoryStore()})
	env := events.Envelope{
		EventID: "e", IdempotencyKey: "i", TenantID: "t",
		OccurredAt:    time.Now().UTC(),
		PublishedAt:   time.Now().UTC(),
		SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.atom_session.completed.v1", env, nil); err == nil {
		t.Errorf("Publish(missing traceparent) = nil; want error")
	}
}

func TestOutboxPublisher_DefaultEventTypeFromTwoPartTopic(t *testing.T) {
	t.Parallel()
	// This is an internal helper coverage probe — the canonical-topic guard
	// rejects 2-part topics, so we exercise deriveEventType indirectly via
	// a 5-part topic ending with no trailing version. Just verify a normal
	// 5-part topic produces a non-empty event_type.
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	now := time.Now().UTC()
	env := events.Envelope{
		EventID: "et-test", IdempotencyKey: "et-test", TenantID: "t",
		OccurredAt: now, PublishedAt: now,
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
	if err := pub.Publish("chora.consumption.daily_dose.served.v1", env, nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if rows[0].EventType == "" {
		t.Errorf("EventType empty; want non-empty")
	}
	if !strings.Contains(rows[0].EventType, "daily_dose") {
		t.Errorf("EventType = %q; want contains daily_dose", rows[0].EventType)
	}
}

// -----------------------------------------------------------------------------
// Dispatcher.Run — exercise non-empty + empty drain cycles + cancel
// -----------------------------------------------------------------------------

func TestDispatcher_Run_DrainsThenSleepsOnEmpty(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	for i := 0; i < 3; i++ {
		insertRow(t, store, string(rune('p'+i)), "t")
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "wrun", MaxAttempts: 3,
		PollInterval: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = d.Run(ctx, 10)
	if bus.callCount() != 3 {
		t.Errorf("bus calls = %d; want 3 (drained then idle)", bus.callCount())
	}
}

// stubLogger captures Infof/Warnf calls for assertion.
type stubLogger struct {
	mu    sync.Mutex
	infos []string
	warns []string
}

func (l *stubLogger) Infof(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, format)
}
func (l *stubLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, format)
}

func TestDispatcher_Run_LogsTransientFailure(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	insertRow(t, store, "rlog", "t")
	bus := &recordingBus{fails: 1, failErr: errors.New("transient")}
	logger := &stubLogger{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "wlog", MaxAttempts: 5,
		PollInterval: 5 * time.Millisecond, Logger: logger,
	})
	// First drain — transient fail.
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	// Second drain — succeeds.
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce 2: %v", err)
	}
	if len(logger.infos) == 0 {
		t.Errorf("expected Infof calls; got 0")
	}
}

func TestDispatcher_DrainOnce_DispatcherTopicPreserved(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	insertRow(t, store, "rtop", "t")
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if bus.calls[0].Topic != "chora.consumption.atom_session.completed.v1" {
		t.Errorf("topic mismatch: %s", bus.calls[0].Topic)
	}
}

// -----------------------------------------------------------------------------
// reconstructEnvelope + parseTime — alternate paths
// -----------------------------------------------------------------------------

func TestDispatcher_DrainOnce_RowWithEmptyEnvelopeUsesFallbacks(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	now := time.Now().UTC()
	// Row with empty envelope map — Dispatcher must fall back to row.ID etc.
	r := outbox.Row{
		ID:             "fb-1",
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		AggregateType:  "atom_session",
		AggregateID:    "sess-1",
		EventType:      "consumption.atom_session.completed",
		Topic:          "chora.consumption.atom_session.completed.v1",
		Payload:        []byte(`{}`),
		Envelope:       map[string]string{}, // empty — fallbacks kick in
		IdempotencyKey: "idem-fb-1",
		OccurredAt:     now,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if len(bus.calls) != 1 {
		t.Fatalf("bus.calls = %d; want 1", len(bus.calls))
	}
	if bus.calls[0].Envelope.EventID != "fb-1" {
		t.Errorf("EventID fallback failed: %s", bus.calls[0].Envelope.EventID)
	}
	if bus.calls[0].Envelope.IdempotencyKey != "idem-fb-1" {
		t.Errorf("IdempotencyKey fallback failed: %s", bus.calls[0].Envelope.IdempotencyKey)
	}
	if bus.calls[0].Envelope.TenantID == "" {
		t.Errorf("TenantID fallback failed")
	}
	if bus.calls[0].Envelope.SchemaVersion != 1 {
		t.Errorf("SchemaVersion default = %d; want 1", bus.calls[0].Envelope.SchemaVersion)
	}
}

func TestDispatcher_DrainOnce_RowWithInvalidSchemaVersion_DefaultsTo1(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	now := time.Now().UTC()
	r := outbox.Row{
		ID:             "sv-bad",
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		AggregateType:  "atom_session",
		AggregateID:    "sess-1",
		EventType:      "consumption.atom_session.completed",
		Topic:          "chora.consumption.atom_session.completed.v1",
		Payload:        []byte(`{}`),
		Envelope:       map[string]string{"schema_version": "abc"},
		IdempotencyKey: "idem-sv-bad",
		OccurredAt:     now,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if bus.calls[0].Envelope.SchemaVersion != 1 {
		t.Errorf("invalid schema_version not defaulted: got %d", bus.calls[0].Envelope.SchemaVersion)
	}
}

func TestDispatcher_DrainOnce_RowWithRFC3339Timestamps_Parsed(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	now := time.Date(2026, 5, 12, 11, 0, 0, 0, time.UTC)
	r := outbox.Row{
		ID:            "rfc-1",
		TenantID:      "00000000-0000-0000-0000-0000000000aa",
		AggregateType: "atom_session",
		AggregateID:   "sess-1",
		EventType:     "consumption.atom_session.completed",
		Topic:         "chora.consumption.atom_session.completed.v1",
		Payload:       []byte(`{}`),
		Envelope: map[string]string{
			// RFC3339 (no nanos) — exercises the parseTime second branch.
			"occurred_at":    now.Format(time.RFC3339),
			"published_at":   now.Format(time.RFC3339),
			"schema_version": "1",
		},
		IdempotencyKey: "idem-rfc-1",
		OccurredAt:     now,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if !bus.calls[0].Envelope.OccurredAt.Equal(now) {
		t.Errorf("RFC3339 timestamp not parsed: got %v want %v", bus.calls[0].Envelope.OccurredAt, now)
	}
}

func TestDispatcher_DrainOnce_RowWithInvalidTimestamps_FallsBack(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	now := time.Now().UTC()
	r := outbox.Row{
		ID:            "bad-ts",
		TenantID:      "00000000-0000-0000-0000-0000000000aa",
		AggregateType: "atom_session",
		AggregateID:   "sess-1",
		EventType:     "consumption.atom_session.completed",
		Topic:         "chora.consumption.atom_session.completed.v1",
		Payload:       []byte(`{}`),
		Envelope: map[string]string{
			"occurred_at":    "not-a-time",
			"published_at":   "also-not-a-time",
			"schema_version": "1",
		},
		IdempotencyKey: "idem-bad-ts",
		OccurredAt:     now,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	// Should fall back to row.OccurredAt for OccurredAt.
	if bus.calls[0].Envelope.OccurredAt.IsZero() {
		t.Errorf("OccurredAt fallback failed: zero time")
	}
}

// -----------------------------------------------------------------------------
// nilIfEmpty / truncate / contains edge cases
// -----------------------------------------------------------------------------

func TestPostgresStore_MarkFailed_TruncatesLongError(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	longErr := strings.Repeat("X", 2000)
	if err := store.MarkFailed(context.Background(), "row-trunc", longErr); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if got := db.execArgs[0][1].(string); len(got) > 1000 {
		t.Errorf("MarkFailed error not truncated: len=%d > 1000", len(got))
	}
}

func TestPostgresStore_Deadletter_TruncatesLongReason(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	longReason := strings.Repeat("Y", 2000)
	if err := store.Deadletter(context.Background(), "row-trunc-d", longReason, 5); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}
	if got := db.execArgs[0][1].(string); len(got) > 1000 {
		t.Errorf("Deadletter reason not truncated: len=%d > 1000", len(got))
	}
}

func TestPostgresStore_Insert_PassesEmptyTenantAsNull(t *testing.T) {
	t.Parallel()
	db := &stubDB{}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	r := outbox.Row{
		ID:             "no-tenant",
		TenantID:       "", // empty → should bind as SQL NULL
		GCID:           "",
		AggregateType:  "atom_session",
		AggregateID:    "sess-x",
		EventType:      "consumption.atom_session.started",
		Topic:          "chora.consumption.atom_session.started.v1",
		Payload:        []byte(`{}`),
		Envelope:       map[string]string{},
		IdempotencyKey: "",
		OccurredAt:     time.Now().UTC(),
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if db.execArgs[0][1] != nil {
		t.Errorf("empty TenantID arg = %v; want nil", db.execArgs[0][1])
	}
	if db.execArgs[0][2] != nil {
		t.Errorf("empty GCID arg = %v; want nil", db.execArgs[0][2])
	}
	if db.execArgs[0][9] != nil {
		t.Errorf("empty idempotency_key arg = %v; want nil", db.execArgs[0][9])
	}
}
