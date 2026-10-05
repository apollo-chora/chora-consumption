// Package outbox is chora-consumption's D6.2 producer-side transactional
// outbox adapter for the canonical
// `chora.consumption.{aggregate}.{event_type}.v{N}` event streams (per
// `feedback_d6_resilience_first_class` + `.claude/skills/agentic-resilience-d6/SKILL.md`
// Pillar 2 + M12.3 W1b plan
// `docs/architecture/m12-3-outbox-templates-plan-2026-05-12.md`).
//
// Why it exists
// -------------
// Pre-D6.2 the chora-consumption HTTP handlers called `Publisher.Publish`
// against an in-memory adapter — production crashes between state-write and
// publish silently lost events. The legacy `cgcoutbox.CloudPublisher` wired
// in `cmd/server/main.go` already writes to `outbox_events` rows via the
// chora-go-common Recorder/Relay primitive, but the **dispatcher** that
// drains those rows had been deferred to "M14 Relay" and never landed.
// M12.3 W1b lands it NOW and aligns the table shape with the canonical
// chora-guardrail D6.2 reference (tenant_id / gcid / idempotency_key as
// load-bearing columns per Pillar 2 + Pillar 3 isolation indexing).
//
// Three actors:
//
//   - Store — port that wraps the outbox_events table.
//     InMemoryStore + PostgresStore implementations.
//   - Publisher — satisfies `events.Publisher` (the Phyllis MVP port) by
//     writing rows via Store. Drop-in replacement for InMemoryPublisher /
//     CloudPublisher when the caller wants the canonical D6.2 envelope path.
//   - Dispatcher — drains Store to a `pubsub.Bus` with retry + DLQ.
//
// Mirrored from:
//   - `services/chora-guardrail/internal/adapter/outbox/` (canonical M12.1)
//   - `services/chora-closure-orchestrator/.../adapter/pubsub/*.py`
//   - `services/chora-ai-kernel-orchestrator/.../adapter/events/publisher_outbox.py`
//
// Schema in `services/chora-consumption/migrations/0005_outbox.sql` (with the
// 2026-05-12 M12.3 W1b ALTER additions for canonical D6.2 fields).
//
// Per `feedback_no_inline_config`: no inline URLs/secrets — every
// dependency is injected via the public Config structs.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// CanonicalDomainPrefix is the legal prefix for every topic this package
// writes. Domain-level invariant: chora-consumption only emits
// chora.consumption.* events.
const CanonicalDomainPrefix = "chora.consumption."

// ErrDuplicateIdempotencyKey is returned by Store.Insert when the supplied
// row collides on idempotency_key. Callers MAY treat this as a no-op —
// the prior emission is already durably queued.
var ErrDuplicateIdempotencyKey = errors.New("outbox: duplicate idempotency_key")

// -----------------------------------------------------------------------------
// Row
// -----------------------------------------------------------------------------

// Row mirrors one row of outbox_events (chora_consumption) after the M12.3
// W1b ALTERs landed tenant_id / gcid / idempotency_key as top-level columns.
// Field names align with the migration column names so SQL marshalling is
// mechanical.
type Row struct {
	ID             string
	TenantID       string // D6.3 multi-tenant isolation indexing
	GCID           string // subject (may be empty for system events)
	AggregateType  string // e.g. "atom_session", "learning_path", "kg_map_cluster"
	AggregateID    string // domain entity ID
	EventType      string // e.g. "consumption.atom_session.completed"
	Topic          string // canonical chora.consumption.{aggregate}.{event_type}.v{N}
	Payload        []byte // Protobuf bytes (POC: JSON)
	Envelope       map[string]string
	IdempotencyKey string
	OccurredAt     time.Time
	RetryCount     int
	LastError      string
}

// DeadLetter mirrors one row of outbox_dead_letters.
type DeadLetter struct {
	RowID          string
	FailureReason  string
	AttemptCount   int
	WorkerID       string
	DeadletteredAt time.Time
}

// -----------------------------------------------------------------------------
// Store port
// -----------------------------------------------------------------------------

// Store is the port the Publisher + Dispatcher use to talk to outbox_events.
// Two implementations:
//
//   - InMemoryStore — hermetic for tests + local dev.
//   - PostgresStore — production; wraps a database/sql connection on
//     chora_consumption (where outbox_events lives).
type Store interface {
	// Insert writes a new pending row. Returns ErrDuplicateIdempotencyKey
	// when the row collides on idempotency_key.
	Insert(ctx context.Context, row Row) error

	// FetchPending claims up to limit rows in status='pending' ordered by
	// occurred_at ASC. Postgres uses SELECT ... FOR UPDATE SKIP LOCKED so
	// concurrent workers do not collide.
	FetchPending(ctx context.Context, limit int) ([]Row, error)

	// MarkPublished transitions the row to status='published'.
	MarkPublished(ctx context.Context, id string) error

	// MarkFailed records a transient publish failure: bumps retry_count,
	// stamps last_error + last_attempt_at, sets status='failed'.
	MarkFailed(ctx context.Context, id string, errMsg string) error

	// Deadletter inserts an outbox_dead_letters row + transitions the
	// events row to status='deadlettered'. attemptCount is captured for
	// audit / runbook context.
	Deadletter(ctx context.Context, id string, failureReason string, attemptCount int) error
}

// -----------------------------------------------------------------------------
// InMemoryStore
// -----------------------------------------------------------------------------

// InMemoryStore is the hermetic fixture used by tests + by main()'s fallback
// when CHORA_DB_DSN is unset. NOT safe for production — does not persist
// across process restart.
type InMemoryStore struct {
	mu                 sync.Mutex
	rows               map[string]*Row // id → row
	idempotency        map[string]string
	published          []string
	deadLetters        []DeadLetter
	deadletterWorkerID string
}

// NewInMemoryStore constructs an empty InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		rows:        make(map[string]*Row),
		idempotency: make(map[string]string),
	}
}

// Insert satisfies Store.
func (s *InMemoryStore) Insert(_ context.Context, row Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row.IdempotencyKey != "" {
		if _, dup := s.idempotency[row.IdempotencyKey]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateIdempotencyKey, row.IdempotencyKey)
		}
	}
	cp := row
	if cp.Envelope != nil {
		envCopy := make(map[string]string, len(cp.Envelope))
		for k, v := range cp.Envelope {
			envCopy[k] = v
		}
		cp.Envelope = envCopy
	}
	if cp.Payload != nil {
		cp.Payload = append([]byte(nil), cp.Payload...)
	}
	s.rows[cp.ID] = &cp
	if cp.IdempotencyKey != "" {
		s.idempotency[cp.IdempotencyKey] = cp.ID
	}
	return nil
}

// FetchPending satisfies Store.
func (s *InMemoryStore) FetchPending(_ context.Context, limit int) ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		pending = append(pending, r)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		return pending[i].OccurredAt.Before(pending[j].OccurredAt)
	})
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	out := make([]Row, 0, len(pending))
	for _, r := range pending {
		out = append(out, *r)
	}
	return out, nil
}

// MarkPublished satisfies Store.
func (s *InMemoryStore) MarkPublished(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; !ok {
		return fmt.Errorf("outbox: MarkPublished: row %q not found", id)
	}
	delete(s.rows, id)
	s.published = append(s.published, id)
	return nil
}

// MarkFailed satisfies Store.
func (s *InMemoryStore) MarkFailed(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return fmt.Errorf("outbox: MarkFailed: row %q not found", id)
	}
	r.RetryCount++
	r.LastError = errMsg
	return nil
}

// Deadletter satisfies Store.
func (s *InMemoryStore) Deadletter(_ context.Context, id, failureReason string, attemptCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return fmt.Errorf("outbox: Deadletter: row %q not found", id)
	}
	s.deadLetters = append(s.deadLetters, DeadLetter{
		RowID:          id,
		FailureReason:  failureReason,
		AttemptCount:   attemptCount,
		WorkerID:       s.deadletterWorkerID,
		DeadletteredAt: time.Now().UTC(),
	})
	delete(s.rows, r.ID)
	return nil
}

// SetWorkerID stamps the worker_id captured in DeadLetters created by this
// in-memory store. The Dispatcher calls this once at construction so the
// store can record the worker on Deadletter() rows without an extra
// parameter on the Store port (PostgresStore stores worker_id on its own
// struct).
func (s *InMemoryStore) SetWorkerID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadletterWorkerID = id
}

// Published returns the IDs of successfully MarkPublished'd rows in order.
func (s *InMemoryStore) Published() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.published...)
	return out
}

// DeadLetters returns the dead-letter rows recorded by Deadletter.
func (s *InMemoryStore) DeadLetters() []DeadLetter {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]DeadLetter(nil), s.deadLetters...)
	return out
}

// Compile-time check.
var _ Store = (*InMemoryStore)(nil)

// -----------------------------------------------------------------------------
// PostgresStore
// -----------------------------------------------------------------------------

// SQLRows is the minimal database/sql Rows surface used by PostgresStore.
type SQLRows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// SQLDB is the minimal database/sql surface required by PostgresStore.
// Production wires a pgxpool.Pool bridge pointing at chora_consumption.
// Tests stub via the SQLDB interface.
type SQLDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (SQLRows, error)
}

// defaultClaimReservationSeconds is the window during which a claimed-but-not-
// yet-published row is hidden from other workers' FetchPending. It must exceed
// the worst-case time to publish one DrainOnce batch; it also bounds how long a
// row claimed by a crashed worker waits before re-claim (the built-in reaper).
const defaultClaimReservationSeconds = 60

// PostgresStoreOptions tunes a PostgresStore at construction time.
type PostgresStoreOptions struct {
	// WorkerID identifies the dispatcher worker that owns Deadletter rows.
	// Required. Typically derived from the pod name (HOSTNAME).
	WorkerID string

	// ClaimReservationSeconds is the FetchPending claim reservation window (see
	// defaultClaimReservationSeconds). Defaults to 60 when <= 0.
	ClaimReservationSeconds int
}

// PostgresStore is the production Store backed by the outbox_events table
// in chora_consumption (per migration 0005_outbox.sql §M12.3 W1b).
type PostgresStore struct {
	db              SQLDB
	workerID        string
	claimReservSecs int
}

// NewPostgresStore constructs a PostgresStore. Panics on missing WorkerID.
func NewPostgresStore(db SQLDB, opts PostgresStoreOptions) *PostgresStore {
	if opts.WorkerID == "" {
		panic("outbox: NewPostgresStore: WorkerID required")
	}
	resv := opts.ClaimReservationSeconds
	if resv <= 0 {
		resv = defaultClaimReservationSeconds
	}
	return &PostgresStore{db: db, workerID: opts.WorkerID, claimReservSecs: resv}
}

// Insert satisfies Store.
func (s *PostgresStore) Insert(ctx context.Context, row Row) error {
	envJSON, err := json.Marshal(row.Envelope)
	if err != nil {
		return fmt.Errorf("outbox: marshal envelope: %w", err)
	}
	const q = `INSERT INTO outbox_events (
        id, tenant_id, gcid, aggregate_type, aggregate_id,
        event_type, topic, payload, envelope,
        idempotency_key, occurred_at, status
    ) VALUES (
        $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, 'pending'
    )`
	_, err = s.db.ExecContext(ctx, q,
		row.ID, nilIfEmpty(row.TenantID), nilIfEmpty(row.GCID),
		row.AggregateType, row.AggregateID,
		row.EventType, row.Topic, row.Payload, string(envJSON),
		nilIfEmpty(row.IdempotencyKey), row.OccurredAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s", ErrDuplicateIdempotencyKey, row.IdempotencyKey)
		}
		return fmt.Errorf("outbox: Insert: %w", err)
	}
	return nil
}

// FetchPending atomically CLAIMS up to `limit` pending rows and returns them.
//
// CHO-1615: this is a claiming UPDATE, not a bare SELECT. The pre-fix query was
// `SELECT ... FOR UPDATE SKIP LOCKED` via db.QueryContext — the row locks
// released the instant the statement returned and `status` stayed 'pending'
// through the publish+MarkPublished gap, so a second dispatcher draining the
// same table (a prod + *-dev co-deployment, or an HPA scaled >1) re-grabbed the
// same rows and double-published every event.
//
// The claim stamps last_attempt_at = now() inside a single statement
// (UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING ...).
// Because the bump commits with the statement, a concurrent worker's identical
// claim — gated by the reservation-window predicate — excludes the just-claimed
// rows. A row claimed by a crashed worker becomes re-claimable once the window
// elapses (the window IS the stuck-row reaper). No migration: last_attempt_at +
// status already exist (migration 0005_outbox.sql).
func (s *PostgresStore) FetchPending(ctx context.Context, limit int) ([]Row, error) {
	const q = `UPDATE outbox_events
        SET last_attempt_at = now()
        WHERE id IN (
            SELECT id FROM outbox_events
            -- includes 'failed' so a transient publish error retries, not strands (see chora-creation outbox)
            WHERE status IN ('pending', 'failed')
              AND (last_attempt_at IS NULL
                   OR last_attempt_at < now() - make_interval(secs => $2))
            ORDER BY occurred_at ASC
            LIMIT $1
            FOR UPDATE SKIP LOCKED
        )
        RETURNING id, COALESCE(tenant_id::TEXT, ''), COALESCE(gcid::TEXT, ''),
               aggregate_type, aggregate_id, event_type, topic,
               payload, envelope::TEXT,
               COALESCE(idempotency_key, ''), retry_count, occurred_at`
	rows, err := s.db.QueryContext(ctx, q, limit, s.claimReservSecs)
	if err != nil {
		return nil, fmt.Errorf("outbox: FetchPending: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			r       Row
			envStr  string
			occured time.Time
		)
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.GCID,
			&r.AggregateType, &r.AggregateID,
			&r.EventType, &r.Topic, &r.Payload, &envStr,
			&r.IdempotencyKey, &r.RetryCount, &occured,
		); err != nil {
			return nil, fmt.Errorf("outbox: FetchPending scan: %w", err)
		}
		r.OccurredAt = occured
		env := map[string]string{}
		if envStr != "" {
			if err := json.Unmarshal([]byte(envStr), &env); err != nil {
				return nil, fmt.Errorf("outbox: FetchPending envelope: %w", err)
			}
		}
		r.Envelope = env
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: FetchPending iter: %w", err)
	}
	return out, nil
}

// MarkPublished satisfies Store.
func (s *PostgresStore) MarkPublished(ctx context.Context, id string) error {
	const q = `UPDATE outbox_events
        SET status='published', published_at=now()
        WHERE id = $1`
	if _, err := s.db.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("outbox: MarkPublished: %w", err)
	}
	return nil
}

// MarkFailed satisfies Store.
func (s *PostgresStore) MarkFailed(ctx context.Context, id string, errMsg string) error {
	const q = `UPDATE outbox_events
        SET status='failed',
            retry_count = retry_count + 1,
            last_error = $2,
            last_attempt_at = now()
        WHERE id = $1`
	if _, err := s.db.ExecContext(ctx, q, id, truncate(errMsg, 1000)); err != nil {
		return fmt.Errorf("outbox: MarkFailed: %w", err)
	}
	return nil
}

// Deadletter satisfies Store.
func (s *PostgresStore) Deadletter(ctx context.Context, id, failureReason string, attemptCount int) error {
	const insertQ = `INSERT INTO outbox_dead_letters
        (outbox_event_id, failure_reason, attempt_count, worker_id)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (outbox_event_id) DO NOTHING`
	if _, err := s.db.ExecContext(ctx, insertQ, id, truncate(failureReason, 1000), attemptCount, s.workerID); err != nil {
		return fmt.Errorf("outbox: Deadletter insert: %w", err)
	}
	const updateQ = `UPDATE outbox_events
        SET status='deadlettered',
            last_attempt_at = now()
        WHERE id = $1`
	if _, err := s.db.ExecContext(ctx, updateQ, id); err != nil {
		return fmt.Errorf("outbox: Deadletter update: %w", err)
	}
	return nil
}

// Compile-time check.
var _ Store = (*PostgresStore)(nil)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// nilIfEmpty returns nil when s is empty so the Postgres driver writes
// a SQL NULL rather than an empty string (tenant_id / gcid columns are
// UUID typed; empty strings would raise type errors).
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation returns true if the error is a Postgres unique violation.
// Loose substring check so we don't bind to pq/pgx at compile time. Real
// production wiring layers a driver-aware predicate.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "23505") || contains(msg, "duplicate key value violates unique constraint")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
