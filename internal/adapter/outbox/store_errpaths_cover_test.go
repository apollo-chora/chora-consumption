// Package outbox_test — error-path coverage for the PostgresStore SQL adapter +
// the Dispatcher's failure ladder. The primary suites (store_test.go,
// dispatcher_test.go, coverage_test.go) exercise the happy paths and the
// in-memory fallbacks; this file drives the driver-error branches that only
// fire when ExecContext / QueryContext / row Scan / rows.Err return errors, and
// the Dispatcher branches that fire when the backing Store itself fails.
//
// All stubs are hermetic (no real DB, no network) — they implement the public
// outbox.SQLDB / outbox.SQLRows / outbox.Store ports with controllable errors.
package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"

	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

// -----------------------------------------------------------------------------
// seqDB — a SQLDB whose ExecContext returns a scripted error per call index, so
// Deadletter (two Execs) and the single-Exec mutators can each be driven into
// their error branch independently.
// -----------------------------------------------------------------------------

type seqDB struct {
	execErrs  []error // execErrs[i] returned by the i-th ExecContext call
	execCount int

	queryErr  error
	queryRows outbox.SQLRows
}

func (d *seqDB) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	i := d.execCount
	d.execCount++
	if i < len(d.execErrs) {
		return nil, d.execErrs[i]
	}
	return nil, nil
}

func (d *seqDB) QueryContext(_ context.Context, _ string, _ ...any) (outbox.SQLRows, error) {
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	return d.queryRows, nil
}

// errRows is a SQLRows whose Scan and/or Err can be made to fail.
type errRows struct {
	advanced bool
	scanErr  error
	finalErr error
	// envelope text returned by Scan when scanErr is nil; lets us drive the
	// json.Unmarshal failure branch in FetchPending.
	envelope string
}

func (r *errRows) Next() bool {
	if r.advanced {
		return false
	}
	r.advanced = true
	return true
}

func (r *errRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	// Fill the 12 destinations FetchPending expects; only the envelope slot
	// (index 8) matters for the unmarshal-failure path.
	if len(dest) != 12 {
		return errors.New("errRows: unexpected scan dest count")
	}
	*dest[0].(*string) = "row-err"
	*dest[1].(*string) = ""
	*dest[2].(*string) = ""
	*dest[3].(*string) = "atom_session"
	*dest[4].(*string) = "sess-err"
	*dest[5].(*string) = "consumption.atom_session.completed"
	*dest[6].(*string) = "chora.consumption.atom_session.completed.v1"
	*dest[7].(*[]byte) = []byte(`{}`)
	*dest[8].(*string) = r.envelope
	*dest[9].(*string) = ""
	*dest[10].(*int) = 0
	*dest[11].(*time.Time) = time.Now().UTC()
	return nil
}

func (r *errRows) Close() error { return nil }
func (r *errRows) Err() error   { return r.finalErr }

// -----------------------------------------------------------------------------
// PostgresStore.FetchPending error branches
// -----------------------------------------------------------------------------

func TestPostgresStore_FetchPending_QueryError(t *testing.T) {
	t.Parallel()
	db := &seqDB{queryErr: errors.New("connection reset by peer")}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	rows, err := store.FetchPending(context.Background(), 10)
	if err == nil {
		t.Fatalf("FetchPending err = nil; want query error")
	}
	if rows != nil {
		t.Errorf("rows = %v; want nil on query error", rows)
	}
	if !strings.Contains(err.Error(), "FetchPending") {
		t.Errorf("err = %v; want wrapped with FetchPending", err)
	}
}

func TestPostgresStore_FetchPending_ScanError(t *testing.T) {
	t.Parallel()
	db := &seqDB{queryRows: &errRows{scanErr: errors.New("scan: sql type mismatch")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil {
		t.Fatalf("FetchPending err = nil; want scan error")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Errorf("err = %v; want wrapped scan error", err)
	}
}

func TestPostgresStore_FetchPending_EnvelopeUnmarshalError(t *testing.T) {
	t.Parallel()
	// Non-empty envelope text that is not valid JSON → json.Unmarshal fails,
	// which FetchPending wraps as "FetchPending envelope".
	db := &seqDB{queryRows: &errRows{envelope: "{not-json"}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil {
		t.Fatalf("FetchPending err = nil; want envelope unmarshal error")
	}
	if !strings.Contains(err.Error(), "envelope") {
		t.Errorf("err = %v; want wrapped envelope error", err)
	}
}

func TestPostgresStore_FetchPending_RowsErrAfterIteration(t *testing.T) {
	t.Parallel()
	// Scan succeeds (empty envelope so no unmarshal), but rows.Err() reports a
	// late iteration error → FetchPending must surface it as "FetchPending iter".
	db := &seqDB{queryRows: &errRows{envelope: "", finalErr: errors.New("driver: bad connection mid-stream")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	_, err := store.FetchPending(context.Background(), 10)
	if err == nil {
		t.Fatalf("FetchPending err = nil; want rows.Err() surfaced")
	}
	if !strings.Contains(err.Error(), "iter") {
		t.Errorf("err = %v; want wrapped iter error", err)
	}
}

// -----------------------------------------------------------------------------
// PostgresStore mutator error branches (MarkPublished / MarkFailed / Deadletter)
// -----------------------------------------------------------------------------

func TestPostgresStore_MarkPublished_ExecError(t *testing.T) {
	t.Parallel()
	db := &seqDB{execErrs: []error{errors.New("deadlock detected")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.MarkPublished(context.Background(), "row-X")
	if err == nil {
		t.Fatalf("MarkPublished err = nil; want exec error")
	}
	if !strings.Contains(err.Error(), "MarkPublished") {
		t.Errorf("err = %v; want wrapped MarkPublished", err)
	}
}

func TestPostgresStore_MarkFailed_ExecError(t *testing.T) {
	t.Parallel()
	db := &seqDB{execErrs: []error{errors.New("serialization failure")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w1"})
	err := store.MarkFailed(context.Background(), "row-Y", "boom")
	if err == nil {
		t.Fatalf("MarkFailed err = nil; want exec error")
	}
	if !strings.Contains(err.Error(), "MarkFailed") {
		t.Errorf("err = %v; want wrapped MarkFailed", err)
	}
}

func TestPostgresStore_Deadletter_InsertExecError(t *testing.T) {
	t.Parallel()
	// First Exec (the dead_letters INSERT) fails — Deadletter must wrap it and
	// NOT proceed to the UPDATE.
	db := &seqDB{execErrs: []error{errors.New("relation outbox_dead_letters lock timeout")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w-A"})
	err := store.Deadletter(context.Background(), "row-Z", "fatal", 5)
	if err == nil {
		t.Fatalf("Deadletter err = nil; want insert exec error")
	}
	if !strings.Contains(err.Error(), "Deadletter insert") {
		t.Errorf("err = %v; want wrapped 'Deadletter insert'", err)
	}
	if db.execCount != 1 {
		t.Errorf("Exec calls = %d; want 1 (insert failed → no update)", db.execCount)
	}
}

func TestPostgresStore_Deadletter_UpdateExecError(t *testing.T) {
	t.Parallel()
	// INSERT succeeds (nil), the status UPDATE (second Exec) fails — Deadletter
	// must surface the update error after having run both statements.
	db := &seqDB{execErrs: []error{nil, errors.New("update lock timeout")}}
	store := outbox.NewPostgresStore(db, outbox.PostgresStoreOptions{WorkerID: "w-A"})
	err := store.Deadletter(context.Background(), "row-Z", "fatal", 5)
	if err == nil {
		t.Fatalf("Deadletter err = nil; want update exec error")
	}
	if !strings.Contains(err.Error(), "Deadletter update") {
		t.Errorf("err = %v; want wrapped 'Deadletter update'", err)
	}
	if db.execCount != 2 {
		t.Errorf("Exec calls = %d; want 2 (insert ok → update attempted)", db.execCount)
	}
}

// -----------------------------------------------------------------------------
// Dispatcher.publishOne — Store-side failure branches.
//
// publishOne records the publish outcome via the Store. When the recording
// itself fails (MarkPublished / MarkFailed / Deadletter returning an error)
// publishOne has distinct branches that the in-memory happy-path store never
// triggers. failingStore lets each be driven independently.
// -----------------------------------------------------------------------------

// failingStore wraps an InMemoryStore but can be configured to fail one of the
// recording calls. FetchPending always returns whatever the embedded store has.
type failingStore struct {
	inner            *outbox.InMemoryStore
	failMarkPub      error
	failMarkFailed   error
	failDeadletter   error
	deadletterCalled bool
}

func (s *failingStore) Insert(ctx context.Context, row outbox.Row) error {
	return s.inner.Insert(ctx, row)
}
func (s *failingStore) FetchPending(ctx context.Context, limit int) ([]outbox.Row, error) {
	return s.inner.FetchPending(ctx, limit)
}
func (s *failingStore) MarkPublished(ctx context.Context, id string) error {
	if s.failMarkPub != nil {
		return s.failMarkPub
	}
	return s.inner.MarkPublished(ctx, id)
}
func (s *failingStore) MarkFailed(ctx context.Context, id, errMsg string) error {
	if s.failMarkFailed != nil {
		return s.failMarkFailed
	}
	return s.inner.MarkFailed(ctx, id, errMsg)
}
func (s *failingStore) Deadletter(ctx context.Context, id, reason string, attempt int) error {
	s.deadletterCalled = true
	if s.failDeadletter != nil {
		return s.failDeadletter
	}
	return s.inner.Deadletter(ctx, id, reason, attempt)
}

func newFailingStore(t *testing.T) *failingStore {
	t.Helper()
	return &failingStore{inner: outbox.NewInMemoryStore()}
}

func TestDispatcher_publishOne_MarkPublishedFailure_CountedAsFailed(t *testing.T) {
	t.Parallel()
	fs := newFailingStore(t)
	fs.failMarkPub = errors.New("mark published write timeout")
	insertRow(t, fs.inner, "mp1", "t1")

	bus := &recordingBus{}
	logger := &stubLogger{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: fs, Bus: bus, WorkerID: "w1", MaxAttempts: 5, Logger: logger,
	})
	n, err := d.DrainOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	// Publish to the bus succeeded, but MarkPublished failed → the row is NOT
	// counted as published (publishOne returns the mark error).
	if n != 0 {
		t.Errorf("published count = %d; want 0 (MarkPublished failed)", n)
	}
	if bus.callCount() != 1 {
		t.Errorf("bus calls = %d; want 1 (publish itself succeeded)", bus.callCount())
	}
	if len(logger.warns) == 0 {
		t.Errorf("expected a Warnf for mark_published_failed; got none")
	}
}

func TestDispatcher_publishOne_MarkFailedFailure_StillReturnsPubErr(t *testing.T) {
	t.Parallel()
	fs := newFailingStore(t)
	fs.failMarkFailed = errors.New("mark failed write timeout")
	insertRow(t, fs.inner, "mf1", "t1")

	// Transient publish failure (attempt 1 < MaxAttempts) so publishOne takes
	// the MarkFailed branch; MarkFailed itself errors → distinct warn path.
	bus := &recordingBus{fails: 1, failErr: errors.New("pubsub blip")}
	logger := &stubLogger{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: fs, Bus: bus, WorkerID: "w1", MaxAttempts: 5, Logger: logger,
	})
	n, err := d.DrainOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("published count = %d; want 0 (transient publish fail)", n)
	}
	foundWarn := false
	for _, w := range logger.warns {
		if strings.Contains(w, "mark_failed_failed") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected mark_failed_failed warn; warns=%v", logger.warns)
	}
}

func TestDispatcher_publishOne_DeadletterFailure_StillWarns(t *testing.T) {
	t.Parallel()
	fs := newFailingStore(t)
	fs.failDeadletter = errors.New("deadletter insert timeout")
	insertRow(t, fs.inner, "dl1", "t1")

	// MaxAttempts=1 so the first publish failure goes straight to the
	// deadletter branch; Deadletter then errors → the deadletter_failed warn.
	bus := &recordingBus{fails: 5, failErr: errors.New("permanent")}
	logger := &stubLogger{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: fs, Bus: bus, WorkerID: "w1", MaxAttempts: 1, Logger: logger,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if !fs.deadletterCalled {
		t.Errorf("Deadletter not called; max-attempts ladder did not reach deadletter")
	}
	foundWarn := false
	for _, w := range logger.warns {
		if strings.Contains(w, "deadletter_failed") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected deadletter_failed warn; warns=%v", logger.warns)
	}
}

// -----------------------------------------------------------------------------
// Dispatcher.DrainOnce — FetchPending error surfaces (not a context error).
// -----------------------------------------------------------------------------

// fetchErrStore fails FetchPending so DrainOnce takes its non-context error
// branch (wrapping the store error). All other methods are unused.
type fetchErrStore struct {
	err error
}

func (s *fetchErrStore) Insert(context.Context, outbox.Row) error { return nil }
func (s *fetchErrStore) FetchPending(context.Context, int) ([]outbox.Row, error) {
	return nil, s.err
}
func (s *fetchErrStore) MarkPublished(context.Context, string) error           { return nil }
func (s *fetchErrStore) MarkFailed(context.Context, string, string) error      { return nil }
func (s *fetchErrStore) Deadletter(context.Context, string, string, int) error { return nil }

func TestDispatcher_DrainOnce_FetchPendingError_Wrapped(t *testing.T) {
	t.Parallel()
	store := &fetchErrStore{err: errors.New("pgbouncer pool exhausted")}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
	})
	n, err := d.DrainOnce(context.Background(), 10)
	if err == nil {
		t.Fatalf("DrainOnce err = nil; want FetchPending error wrapped")
	}
	if n != 0 {
		t.Errorf("published = %d; want 0", n)
	}
	if !strings.Contains(err.Error(), "DrainOnce") {
		t.Errorf("err = %v; want wrapped with DrainOnce", err)
	}
}

// -----------------------------------------------------------------------------
// Dispatcher.Run — non-context drain error is logged and the loop continues.
// -----------------------------------------------------------------------------

func TestDispatcher_Run_NonContextDrainError_LogsAndContinues(t *testing.T) {
	t.Parallel()
	store := &fetchErrStore{err: errors.New("transient pool blip")}
	logger := &stubLogger{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: 5 * time.Millisecond, Logger: logger,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := d.Run(ctx, 10)
	// Run returns once the context expires.
	if err == nil || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)) {
		t.Errorf("Run err = %v; want context deadline/cancel", err)
	}
	// The non-context FetchPending error must have produced at least one
	// drain_error warn (the loop kept going rather than returning the error).
	foundWarn := false
	for _, w := range logger.warns {
		if strings.Contains(w, "drain_error") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected outbox_drain_error warn; warns=%v", logger.warns)
	}
}

// -----------------------------------------------------------------------------
// Compile-time guards: the test stubs satisfy the public ports.
// -----------------------------------------------------------------------------

var (
	_ outbox.SQLDB   = (*seqDB)(nil)
	_ outbox.SQLRows = (*errRows)(nil)
	_ outbox.Store   = (*failingStore)(nil)
	_ outbox.Store   = (*fetchErrStore)(nil)
)

// keep the cgcenvelope import live for any future envelope-shape assertions in
// this file; reconstructEnvelope produces cgcenvelope.Envelope on the bus.
var _ = cgcenvelope.Envelope{}
