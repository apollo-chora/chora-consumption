// Package outbox_test — additional Dispatcher.Run / DrainOnce control-flow
// coverage + Publisher encode-failure coverage. These exercise the branches the
// happy-path suites skip: the mid-loop context cancel inside DrainOnce, Run's
// pre-cancelled fast-exit and its context-error return, and the Publisher's
// fail-loud behaviour when a SUPPORTED topic's payload carries a wrong-typed or
// unmarshalable field.
package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

// firstThenCancelBus publishes the first row successfully, cancels the supplied
// context, then keeps succeeding — so the second row in the SAME DrainOnce batch
// hits the loop's per-row `case <-ctx.Done()` guard before it is published.
type firstThenCancelBus struct {
	cancel context.CancelFunc
	count  int64
}

func (b *firstThenCancelBus) Publish(_ context.Context, _ string, _ cgcenvelope.Envelope, _ []byte) error {
	if atomic.AddInt64(&b.count, 1) == 1 {
		b.cancel()
	}
	return nil
}

func TestDispatcher_DrainOnce_CancelMidBatch_StopsBeforeNextRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	// Two rows; cancelling after the first publish must abort before the second.
	insertRow(t, store, "c1", "t1")
	insertRow(t, store, "c2", "t1")

	ctx, cancel := context.WithCancel(context.Background())

	// firstThenCancelBus publishes the first row, cancels ctx, then the loop's
	// per-row select sees ctx.Done() before publishing the second row.
	bus := &firstThenCancelBus{cancel: cancel}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	published, err := d.DrainOnce(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("DrainOnce err = %v; want context.Canceled (mid-batch)", err)
	}
	// First row published; the loop aborted before the second.
	if published != 1 {
		t.Errorf("published = %d; want 1 (cancel after first row)", published)
	}
}

func TestDispatcher_Run_PreCancelledContext_ReturnsImmediately(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	insertRow(t, store, "pc1", "t1")
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: time.Second, // long, to prove we never sleep
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Run(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Run(pre-cancelled) err = %v; want context.Canceled", err)
	}
	// Never drained — the top-of-loop ctx.Err() check returns before FetchPending.
	if bus.callCount() != 0 {
		t.Errorf("bus calls = %d; want 0 (returned before draining)", bus.callCount())
	}
}

// runCancelStore returns one row then cancels the loop's context via the
// FetchPending side effect, so DrainOnce surfaces context.Canceled and Run takes
// its `errors.Is(err, context.Canceled) → return err` branch.
type runCancelStore struct {
	inner  *outbox.InMemoryStore
	cancel context.CancelFunc
}

func (s *runCancelStore) Insert(ctx context.Context, row outbox.Row) error {
	return s.inner.Insert(ctx, row)
}
func (s *runCancelStore) FetchPending(ctx context.Context, limit int) ([]outbox.Row, error) {
	// Cancel before returning rows so DrainOnce's per-row select sees Done().
	s.cancel()
	return s.inner.FetchPending(ctx, limit)
}
func (s *runCancelStore) MarkPublished(ctx context.Context, id string) error {
	return s.inner.MarkPublished(ctx, id)
}
func (s *runCancelStore) MarkFailed(ctx context.Context, id, m string) error {
	return s.inner.MarkFailed(ctx, id, m)
}
func (s *runCancelStore) Deadletter(ctx context.Context, id, r string, a int) error {
	return s.inner.Deadletter(ctx, id, r, a)
}

func TestDispatcher_Run_DrainContextError_Returned(t *testing.T) {
	t.Parallel()
	inner := outbox.NewInMemoryStore()
	insertRow(t, inner, "rc1", "t1")
	ctx, cancel := context.WithCancel(context.Background())
	store := &runCancelStore{inner: inner, cancel: cancel}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: time.Second,
	})
	err := d.Run(ctx, 10)
	// DrainOnce returns context.Canceled mid-batch; Run must propagate it (not
	// log-and-continue) because errors.Is(err, context.Canceled) is true.
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v; want context.Canceled propagated from DrainOnce", err)
	}
}

// -----------------------------------------------------------------------------
// Publisher.Publish — fail-loud encode errors for SUPPORTED topics.
//
// For a topic WITH a binary encoder, a wrong-typed payload field is a REAL
// encoding error (not ErrUnsupportedTopic) and must surface from Publish as
// "outbox: marshal payload: ..." rather than silently JSON-falling-back.
// -----------------------------------------------------------------------------

func supportedEnv(now time.Time) events.Envelope {
	return events.Envelope{
		EventID: "enc-e", IdempotencyKey: "enc-i", TenantID: "t",
		OccurredAt: now, PublishedAt: now,
		Traceparent: "tp", SourceProject: "p", SourceService: "s", SchemaVersion: 1,
	}
}

func TestOutboxPublisher_Publish_SupportedTopicWrongType_FailsLoud(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	now := time.Now().UTC()
	// hatched.v1 IS a supported encoder; display_name must be a string. An int
	// here is a real type error → encodeOutboxPayload returns a non-unsupported
	// error → Publish wraps + returns it; NO row written.
	payload := map[string]any{"display_name": 12345}
	err := pub.Publish("chora.consumption.companion.hatched.v1", supportedEnv(now), payload)
	if err == nil {
		t.Fatalf("Publish(wrong-typed supported payload) = nil; want fail-loud encode error")
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 0 {
		t.Errorf("rows written = %d; want 0 (encode failed before insert)", len(rows))
	}
}

func TestOutboxPublisher_Publish_UnsupportedTopicUnmarshalablePayload_FailsLoud(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store})
	now := time.Now().UTC()
	// A canonical-but-unsupported topic falls through to the JSON fallback;
	// a payload value json.Marshal cannot encode (a func) drives the fallback's
	// own marshal-error branch — Publish must fail loud, not panic.
	payload := map[string]any{"bad": func() {}}
	err := pub.Publish("chora.consumption.kg_map_cluster.merged.v1", supportedEnv(now), payload)
	if err == nil {
		t.Fatalf("Publish(unmarshalable fallback payload) = nil; want fail-loud json error")
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 0 {
		t.Errorf("rows written = %d; want 0 (json fallback failed before insert)", len(rows))
	}
}
