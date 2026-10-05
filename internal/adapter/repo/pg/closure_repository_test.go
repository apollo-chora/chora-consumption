// closure_repository_test.go — pgx adapter tests for the durable
// ClosureRepository (W0-F1 durability + W0-F5 error-honesty, CHO-2198).
//
// Unit tests against the SQL emit + scan surface — no live DB. The
// critical assertions here are the W0-F5 ones: a genuine backing-store
// error from either Pseudonymise or IsPseudonymised must come back as a
// non-nil error, never get coerced into a false/zero "everything is fine"
// result (the swallowed-error trap the in-memory port's original
// `IsPseudonymised(gcid string) bool` signature made structurally
// impossible to avoid).
//
// Local closureStub* types (not the package's existing stubQuerier /
// stubRow / stubTxRunner from user_kg_test.go / growth_coverage_test.go):
// this package's canonical stubQuerier.QueryRow always returns nil and
// isn't independently configurable per test case, which these tests need
// (scripted Scan success / ErrNoRows / genuine error per call).
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/config"
)

const (
	closureTestTenantID = "01970000-0000-7000-8000-000000000001"
	closureTestGCID     = "01970000-0000-7000-9000-000000000001"
)

func closureSpecFixture() []config.TableSpec {
	return []config.TableSpec{
		{
			Table: "atomic_session",
			Columns: []config.ColumnSpec{
				{Column: "notes", Strategy: "drop", Value: ""},
				{Column: "actor_display_name", Strategy: "tombstone_string", Value: "Former member"},
			},
		},
		{
			Table: "learning_path",
			Columns: []config.ColumnSpec{
				{Column: "learner_display_name", Strategy: "tombstone_string", Value: "Former member"},
			},
		},
	}
}

// -----------------------------------------------------------------------------
// Local stubs
// -----------------------------------------------------------------------------

type closureStubRow struct {
	scan func(dest ...any) error
}

func (r closureStubRow) Scan(dest ...any) error { return r.scan(dest...) }

// closureStubQuerier captures the emitted SET LOCAL / SELECT / INSERT SQL +
// args and lets each test script the QueryRow response.
type closureStubQuerier struct {
	execCalls []string
	rowSQL    string
	rowArgs   []any
	rowFn     func() Row
}

func (q *closureStubQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.execCalls = append(q.execCalls, sql)
	return rls.CommandTag{}, nil
}

func (q *closureStubQuerier) QueryRow(_ context.Context, sql string, args ...any) Row {
	q.rowSQL = sql
	q.rowArgs = args
	if q.rowFn == nil {
		return closureStubRow{scan: func(dest ...any) error { return ErrNoRows }}
	}
	return q.rowFn()
}

func (q *closureStubQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, errors.New("closureStubQuerier: Query not supported")
}

type closureStubTxRunner struct {
	q *closureStubQuerier
}

func (t *closureStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &closureStubQuerier{}
	}
	return fn(ctx, t.q)
}

// -----------------------------------------------------------------------------
// Pseudonymise
// -----------------------------------------------------------------------------

func TestClosureRepository_Pseudonymise_EmitsInsertOnConflictReturningID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &closureStubQuerier{rowFn: func() Row {
		return closureStubRow{scan: func(dest ...any) error {
			*(dest[0].(*string)) = uuid.NewString()
			return nil
		}}
	}}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), closureTestTenantID, closureTestGCID, closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if rows != 3 {
		t.Fatalf("expected rows_touched=3 (2+1 columns); got %d", rows)
	}

	wants := []string{"INSERT INTO closure_pseudonymisation_state", "ON CONFLICT", "DO NOTHING", "RETURNING"}
	for _, w := range wants {
		if !contains(q.rowSQL, w) {
			t.Errorf("Pseudonymise SQL missing %q; got:\n%s", w, q.rowSQL)
		}
	}
	// rls.ApplySession must fire (SET LOCAL chora.tenant_id) before the insert.
	if len(q.execCalls) == 0 || !contains(q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("expected SET LOCAL chora.tenant_id before the insert; execCalls=%v", q.execCalls)
	}
}

func TestClosureRepository_Pseudonymise_MintsUUIDv7ForID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &closureStubQuerier{rowFn: func() Row {
		return closureStubRow{scan: func(dest ...any) error {
			*(dest[0].(*string)) = uuid.NewString()
			return nil
		}}
	}}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	if _, err := repo.Pseudonymise(context.Background(), closureTestTenantID, closureTestGCID, nil); err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	if len(q.rowArgs) == 0 {
		t.Fatalf("expected query args")
	}
	idArg, ok := q.rowArgs[0].(string)
	if !ok || idArg == "" {
		t.Fatalf("expected non-empty string id as first arg; got %T %v", q.rowArgs[0], q.rowArgs[0])
	}
	parsed, err := uuid.Parse(idArg)
	if err != nil {
		t.Fatalf("minted id %q is not a valid UUID: %v", idArg, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("minted id %q is not UUIDv7 (version=%d)", idArg, parsed.Version())
	}
}

func TestClosureRepository_Pseudonymise_ReturnsZeroWhenConflictFires(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	// ON CONFLICT DO NOTHING suppresses the RETURNING row — the stub
	// Querier signals that exactly as the pg.Querier seam does: ErrNoRows
	// (default closureStubQuerier.QueryRow with no rowFn set).
	q := &closureStubQuerier{}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), closureTestTenantID, closureTestGCID, closureSpecFixture())
	if err != nil {
		t.Fatalf("Pseudonymise: expected idempotent no-op, got error: %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on idempotent replay; got %d", rows)
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyTenantID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &closureStubQuerier{}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})
	_, err := repo.Pseudonymise(context.Background(), "", closureTestGCID, nil)
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if q.rowSQL != "" {
		t.Fatalf("must not emit SQL on validation failure; got %q", q.rowSQL)
	}
}

func TestClosureRepository_Pseudonymise_RejectsEmptyGCID(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	q := &closureStubQuerier{}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})
	_, err := repo.Pseudonymise(context.Background(), closureTestTenantID, "", nil)
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if q.rowSQL != "" {
		t.Fatalf("must not emit SQL on validation failure; got %q", q.rowSQL)
	}
}

// TestClosureRepository_Pseudonymise_PropagatesScanError is the W0-F5
// fail-loud proof for Pseudonymise: a genuine backing-store error (NOT
// ErrNoRows) must come back as a non-nil error, never as a silent
// "idempotent no-op" (0, nil) — conflating "I don't know" with "already
// done" would let the closure saga believe this domain acked when it did
// not (reusable_gotcha_swallowed_error_damage_is_decided_by_the_caller).
func TestClosureRepository_Pseudonymise_PropagatesScanError(t *testing.T) {
	t.Skip("disarmed 2026-08-14: Pseudonymise now fails closed via ErrClosureExecutorUnbuilt, so the ack-row mechanics below are unreachable. This test pins the contract the REAL per-table executor must honour (CHO-2198); un-skip it with that executor.")
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	q := &closureStubQuerier{rowFn: func() Row {
		return closureStubRow{scan: func(dest ...any) error { return boom }}
	}}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	rows, err := repo.Pseudonymise(context.Background(), closureTestTenantID, closureTestGCID, closureSpecFixture())
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (rows=%d)", rows)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected rows=0 on error; got %d", rows)
	}
}

// -----------------------------------------------------------------------------
// IsPseudonymised
// -----------------------------------------------------------------------------

func TestClosureRepository_IsPseudonymised_TrueWhenRowExists(t *testing.T) {
	t.Parallel()
	q := &closureStubQuerier{rowFn: func() Row {
		return closureStubRow{scan: func(dest ...any) error {
			*(dest[0].(*int)) = 1
			return nil
		}}
	}}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), closureTestTenantID, closureTestGCID)
	if err != nil {
		t.Fatalf("IsPseudonymised: %v", err)
	}
	if !got {
		t.Fatalf("expected true when a row exists")
	}
	if !contains(q.rowSQL, "FROM closure_pseudonymisation_state") {
		t.Errorf("IsPseudonymised SQL malformed; got:\n%s", q.rowSQL)
	}
}

func TestClosureRepository_IsPseudonymised_FalseWhenNoRows(t *testing.T) {
	t.Parallel()
	q := &closureStubQuerier{} // defaults to ErrNoRows
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), closureTestTenantID, closureTestGCID)
	if err != nil {
		t.Fatalf("IsPseudonymised: expected nil error on a clean miss; got %v", err)
	}
	if got {
		t.Fatalf("expected false when no row exists")
	}
}

// TestClosureRepository_IsPseudonymised_PropagatesQueryError is the core
// W0-F5 proof for IsPseudonymised: this is exactly the method the original
// `IsPseudonymised(gcid string) bool` signature could NOT have implemented
// honestly against Postgres (no ctx, no error return). A real
// backing-store error must be reported, not folded into `false`.
func TestClosureRepository_IsPseudonymised_PropagatesQueryError(t *testing.T) {
	t.Parallel()
	boom := errors.New("pg: connection reset by peer")
	q := &closureStubQuerier{rowFn: func() Row {
		return closureStubRow{scan: func(dest ...any) error { return boom }}
	}}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})

	got, err := repo.IsPseudonymised(context.Background(), closureTestTenantID, closureTestGCID)
	if err == nil {
		t.Fatalf("expected error to propagate, got nil (got=%v)", got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped sentinel error; got %v", err)
	}
	if got {
		t.Fatalf("expected false alongside the error (never claim true on failure)")
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()
	q := &closureStubQuerier{}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})
	_, err := repo.IsPseudonymised(context.Background(), "", closureTestGCID)
	if err == nil {
		t.Fatalf("expected error on empty tenant_id")
	}
	if q.rowSQL != "" {
		t.Fatalf("must not emit SQL on validation failure; got %q", q.rowSQL)
	}
}

func TestClosureRepository_IsPseudonymised_RejectsEmptyGCID(t *testing.T) {
	t.Parallel()
	q := &closureStubQuerier{}
	repo := NewClosureRepository(&closureStubTxRunner{q: q})
	_, err := repo.IsPseudonymised(context.Background(), closureTestTenantID, "")
	if err == nil {
		t.Fatalf("expected error on empty gcid")
	}
	if q.rowSQL != "" {
		t.Fatalf("must not emit SQL on validation failure; got %q", q.rowSQL)
	}
}

// ---------------------------------------------------------------------------
// Disarm: the no-op must not report a success it did not perform
// ---------------------------------------------------------------------------
//
// EVIDENCE THIS EXISTS (2026-08-14). Pseudonymise executes no per-table UPDATE
// at all; it writes one ack row and reports rows_touched as a DECLARED-INTENT
// count, the sum of columns listed in PII_Closure_Map.yaml. Verified across the
// platform: 9 services carry a closure repo, 0 of them contain any UPDATE, over
// 73 declared tables and 123 columns. Verified never exercised: lifetime inserts
// on closure_pseudonymisation_state are 0 (control: companion_memory_recall 85).
//
// WHY THAT IS WORSE THAN DEAD CODE. Closure IS reachable, from the admin
// account-lifecycle surface in chora-web. On the first real closure the
// subscriber would publish status "ok" with a non-zero rows_touched under
// chora_imda_dimension "accountability", and the account would reach
// ACCOUNT_STATE_PSEUDONYMIZED, which the contract defines as "PII fields
// tokenized per PII_Closure_Map". Zero fields would have been tokenized. That is
// a false compliance attestation, and absent code would at least fail loudly.
//
// So until an executor exists this fails CLOSED. The saga must not be able to
// reach a pseudonymised end state on the strength of work nobody did.

func TestPseudonymise_RefusesToReportAnErasureItDidNotPerform(t *testing.T) {
	repo := NewClosureRepository(&stubTxRunner{})
	spec := []config.TableSpec{{
		Table:   "companion_memory_recall",
		Columns: []config.ColumnSpec{{Column: "content_text", Strategy: "drop"}},
	}}

	ctx := tracing.WithTenantID(context.Background(), "t-1")
	n, err := repo.Pseudonymise(ctx, "t-1", "gcid-1", spec)
	if err == nil {
		t.Fatal("Pseudonymise reported success without tokenising anything; " +
			"the saga would mark the account PSEUDONYMIZED with the data intact")
	}
	if n != 0 {
		t.Errorf("rows = %d, want 0; a declared-intent count reads as work done", n)
	}
	if !errors.Is(err, ErrClosureExecutorUnbuilt) {
		t.Errorf("err = %v, want ErrClosureExecutorUnbuilt so the cause is legible", err)
	}
}

func TestPseudonymise_ErrorNamesTheGapAndItsScope(t *testing.T) {
	// Whoever hits this in a log must not have to rediscover the finding.
	msg := ErrClosureExecutorUnbuilt.Error()
	for _, want := range []string{"PII_Closure_Map", "not implemented"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text missing %q: %s", want, msg)
		}
	}
}
