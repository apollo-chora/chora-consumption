// companion_suspension_projection_repo_test.go: RLS contract + SQL-shape +
// guard semantics of the ADR-254 D11 advisory projection adapter, against a
// recording stub Querier (per the multi-tenant-rls SKILL idiom in this
// package). Parse analysis of the SQL consts runs in
// companion_suspension_projection_prepare_smoke_integration_test.go.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const (
	suspTenantA = "11111111-1111-7111-8111-000000000001"
	suspTenantB = "11111111-1111-7111-8111-000000000002"
	suspActor   = "01970000-0000-7000-9000-000000000001"
	suspEventID = "01990000-0000-7000-8000-000000000001"
	suspRowID   = "01990000-0000-7000-8000-000000000010"
	suspChatKey = "companion_chat_turn_basic"
)

var suspT0 = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// stubs (prefixed susp* so they cannot collide with the other test fixtures
// in this package)
// ---------------------------------------------------------------------------

type suspStubRow struct {
	values []any
	err    error
}

func (r *suspStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("suspStubRow.Scan: argc mismatch")
	}
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			*tgt = r.values[i].(string)
		case *bool:
			*tgt = r.values[i].(bool)
		case *int64:
			*tgt = r.values[i].(int64)
		case *time.Time:
			*tgt = r.values[i].(time.Time)
		default:
			return errors.New("suspStubRow.Scan: unsupported dest type")
		}
	}
	return nil
}

type suspStubRows struct {
	rows    [][]any
	i       int
	scanErr error
	iterErr error
	closed  bool
}

func (r *suspStubRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *suspStubRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := &suspStubRow{values: r.rows[r.i-1]}
	return row.Scan(dest...)
}

func (r *suspStubRows) Close() error { r.closed = true; return nil }
func (r *suspStubRows) Err() error   { return r.iterErr }

type suspStubQuerier struct {
	execCalls    []string
	queryRowSQLs []string
	queryRowArgs [][]any
	nextRow      *suspStubRow
	querySQLs    []string
	queryArgs    [][]any
	nextRows     *suspStubRows
	queryErr     error
}

func (s *suspStubQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (s *suspStubQuerier) QueryRow(_ context.Context, sql string, args ...any) Row {
	s.queryRowSQLs = append(s.queryRowSQLs, sql)
	s.queryRowArgs = append(s.queryRowArgs, args)
	if s.nextRow != nil {
		return s.nextRow
	}
	return &suspStubRow{err: ErrNoRows}
}

func (s *suspStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	s.querySQLs = append(s.querySQLs, sql)
	s.queryArgs = append(s.queryArgs, args)
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.nextRows != nil {
		return s.nextRows, nil
	}
	return &suspStubRows{}, nil
}

type suspStubTxRunner struct {
	q *suspStubQuerier
}

func (r *suspStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &suspStubQuerier{}
	}
	return fn(ctx, r.q)
}

func suspTenantEngage() companion.SuspensionChanged {
	return companion.SuspensionChanged{
		EventID:      suspEventID,
		SuspensionID: suspRowID,
		Scope:        companion.SuspensionScopeTenant,
		TenantID:     suspTenantA,
		SkillKey:     suspChatKey,
		Engaged:      true,
		Version:      1,
		Reason:       "incident 42",
		ActorGCID:    suspActor,
		ChangedAt:    suspT0,
	}
}

func suspPlatformRelease() companion.SuspensionChanged {
	return companion.SuspensionChanged{
		EventID:      "01990000-0000-7000-8000-000000000002",
		SuspensionID: "01990000-0000-7000-8000-000000000020",
		Scope:        companion.SuspensionScopePlatform,
		TenantID:     "",
		SkillKey:     "",
		Engaged:      false,
		Version:      2,
		Reason:       "resolved",
		ActorGCID:    suspActor,
		ChangedAt:    suspT0.Add(time.Minute),
	}
}

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

func TestCompanionSuspensionProjectionRepo_ImplementsPort(t *testing.T) {
	var _ companion.SuspensionProjection = NewCompanionSuspensionProjectionRepo(&suspStubTxRunner{})
}

func TestCompanionSuspensionProjectionRepo_Apply_TenantScope_RLSFromEvent(t *testing.T) {
	tx := &suspStubTxRunner{q: &suspStubQuerier{nextRow: &suspStubRow{values: []any{int64(1)}}}}
	repo := NewCompanionSuspensionProjectionRepo(tx)

	// The caller context carries ANOTHER tenant (a pull subscriber has no
	// request tenant): the write must be scoped by the EVENT's tenant.
	ctx := tracing.WithTenantID(context.Background(), suspTenantB)
	applied, err := repo.Apply(ctx, suspTenantEngage())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !applied {
		t.Fatal("Apply: want applied=true when RETURNING yields a row")
	}
	if len(tx.q.execCalls) < 1 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id = '"+suspTenantA+"'") {
		t.Fatalf("RLS tenant GUC must be the EVENT tenant before the upsert; execCalls=%v", tx.q.execCalls)
	}
	if len(tx.q.queryRowSQLs) != 1 || !contains(tx.q.queryRowSQLs[0], "INSERT INTO companion_suspension_projection") {
		t.Fatalf("upsert SQL = %v", tx.q.queryRowSQLs)
	}
	args := tx.q.queryRowArgs[0]
	want := []any{suspRowID, "tenant", suspTenantA, suspChatKey, true, "incident 42", suspActor, int64(1), suspT0, suspEventID}
	if len(args) != len(want) {
		t.Fatalf("upsert args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("upsert arg[%d] = %v, want %v", i, args[i], want[i])
		}
	}
}

func TestCompanionSuspensionProjectionRepo_Apply_PlatformScope_UsesPlatformGUC(t *testing.T) {
	tx := &suspStubTxRunner{q: &suspStubQuerier{nextRow: &suspStubRow{values: []any{int64(2)}}}}
	repo := NewCompanionSuspensionProjectionRepo(tx)

	applied, err := repo.Apply(context.Background(), suspPlatformRelease())
	if err != nil || !applied {
		t.Fatalf("Apply = (%v, %v), want (true, nil)", applied, err)
	}
	if len(tx.q.execCalls) < 1 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id = '"+PlatformScopeTenantGUC+"'") {
		t.Fatalf("platform rows are written under the platform sentinel GUC; execCalls=%v", tx.q.execCalls)
	}
	args := tx.q.queryRowArgs[0]
	if args[1] != "platform" || args[2] != "" || args[3] != "" || args[4] != false || args[7] != int64(2) {
		t.Fatalf("platform release args = %v", args)
	}
}

func TestCompanionSuspensionProjectionRepo_Apply_StaleIsNotAppliedAndNotAnError(t *testing.T) {
	tx := &suspStubTxRunner{} // QueryRow defaults to ErrNoRows = guard refused the update
	repo := NewCompanionSuspensionProjectionRepo(tx)

	applied, err := repo.Apply(context.Background(), suspTenantEngage())
	if err != nil {
		t.Fatalf("stale Apply must not error: %v", err)
	}
	if applied {
		t.Fatal("stale Apply must report applied=false")
	}
}

func TestCompanionSuspensionProjectionRepo_Apply_DBErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	tx := &suspStubTxRunner{q: &suspStubQuerier{nextRow: &suspStubRow{err: boom}}}
	repo := NewCompanionSuspensionProjectionRepo(tx)

	applied, err := repo.Apply(context.Background(), suspTenantEngage())
	if applied || !errors.Is(err, boom) {
		t.Fatalf("Apply = (%v, %v), want (false, boom)", applied, err)
	}
}

func TestCompanionSuspensionProjectionRepo_Apply_InvalidEventNeverTouchesDB(t *testing.T) {
	tx := &suspStubTxRunner{}
	repo := NewCompanionSuspensionProjectionRepo(tx)

	bad := suspTenantEngage()
	bad.Reason = ""
	applied, err := repo.Apply(context.Background(), bad)
	if applied || !errors.Is(err, companion.ErrSuspensionReasonRequired) {
		t.Fatalf("Apply(bad) = (%v, %v)", applied, err)
	}
	if tx.q != nil && (len(tx.q.execCalls) > 0 || len(tx.q.queryRowSQLs) > 0) {
		t.Fatalf("invalid event must not open a transaction: %+v", tx.q)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func suspEngagedRows() *suspStubRows {
	return &suspStubRows{rows: [][]any{
		// suspension_id, scope, tenant_id, skill_key, engaged, reason, actor_gcid, source_version, changed_at
		{"01990000-0000-7000-8000-000000000030", "platform", "", "", true, "platform incident", suspActor, int64(1), suspT0},
		{suspRowID, "tenant", suspTenantA, suspChatKey, true, "incident 42", suspActor, int64(1), suspT0.Add(time.Minute)},
	}}
}

func TestCompanionSuspensionProjectionRepo_Status_AppliesRLSAndDecides(t *testing.T) {
	rows := suspEngagedRows()
	tx := &suspStubTxRunner{q: &suspStubQuerier{nextRows: rows}}
	repo := NewCompanionSuspensionProjectionRepo(tx)

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), suspTenantA), suspActor)
	st, err := repo.Status(ctx, suspTenantA, suspChatKey)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Paused || st.Scope != companion.SuspensionScopePlatform || st.Reason != "platform incident" {
		t.Fatalf("Status = %+v, want paused by the platform row (platform outranks tenant)", st)
	}
	if len(tx.q.execCalls) < 2 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id = '"+suspTenantA+"'") || !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Fatalf("RLS pair must land before the select; execCalls=%v", tx.q.execCalls)
	}
	if len(tx.q.querySQLs) != 1 || !contains(tx.q.querySQLs[0], "FROM companion_suspension_projection") {
		t.Fatalf("select SQL = %v", tx.q.querySQLs)
	}
	if len(tx.q.queryArgs[0]) != 1 || tx.q.queryArgs[0][0] != suspTenantA {
		t.Fatalf("select args = %v, want [tenant]", tx.q.queryArgs[0])
	}
	if !rows.closed {
		t.Fatal("rows must be closed")
	}
}

func TestCompanionSuspensionProjectionRepo_Status_NotPausedWhenNoRows(t *testing.T) {
	tx := &suspStubTxRunner{}
	repo := NewCompanionSuspensionProjectionRepo(tx)
	st, err := repo.Status(tracing.WithTenantID(context.Background(), suspTenantA), suspTenantA, suspChatKey)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Paused {
		t.Fatalf("Status = %+v, want not paused", st)
	}
}

func TestCompanionSuspensionProjectionRepo_Status_StampsTenantWhenCtxBare(t *testing.T) {
	tx := &suspStubTxRunner{}
	repo := NewCompanionSuspensionProjectionRepo(tx)
	if _, err := repo.Status(context.Background(), suspTenantA, suspChatKey); err != nil {
		t.Fatalf("Status with bare ctx must stamp the tenant itself: %v", err)
	}
	if len(tx.q.execCalls) < 1 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id = '"+suspTenantA+"'") {
		t.Fatalf("execCalls=%v", tx.q.execCalls)
	}
}

func TestCompanionSuspensionProjectionRepo_Status_TenantRequired(t *testing.T) {
	tx := &suspStubTxRunner{}
	repo := NewCompanionSuspensionProjectionRepo(tx)
	_, err := repo.Status(context.Background(), "", suspChatKey)
	if !errors.Is(err, ErrCompanionSuspensionTenantRequired) {
		t.Fatalf("err = %v, want ErrCompanionSuspensionTenantRequired", err)
	}
	if tx.q != nil && len(tx.q.execCalls) > 0 {
		t.Fatal("no SQL may run without a tenant")
	}
}

func TestCompanionSuspensionProjectionRepo_Status_TenantMismatchFailsLoud(t *testing.T) {
	tx := &suspStubTxRunner{}
	repo := NewCompanionSuspensionProjectionRepo(tx)
	_, err := repo.Status(tracing.WithTenantID(context.Background(), suspTenantB), suspTenantA, suspChatKey)
	if !errors.Is(err, ErrCompanionSuspensionTenantMismatch) {
		t.Fatalf("err = %v, want ErrCompanionSuspensionTenantMismatch", err)
	}
}

func TestCompanionSuspensionProjectionRepo_Status_QueryErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	tx := &suspStubTxRunner{q: &suspStubQuerier{queryErr: boom}}
	repo := NewCompanionSuspensionProjectionRepo(tx)
	_, err := repo.Status(tracing.WithTenantID(context.Background(), suspTenantA), suspTenantA, suspChatKey)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCompanionSuspensionProjectionRepo_Status_ScanAndIterErrorsPropagate(t *testing.T) {
	scanBoom := errors.New("scan boom")
	rows := suspEngagedRows()
	rows.scanErr = scanBoom
	repo := NewCompanionSuspensionProjectionRepo(&suspStubTxRunner{q: &suspStubQuerier{nextRows: rows}})
	if _, err := repo.Status(tracing.WithTenantID(context.Background(), suspTenantA), suspTenantA, suspChatKey); !errors.Is(err, scanBoom) {
		t.Fatalf("scan err = %v, want scan boom", err)
	}

	iterBoom := errors.New("iter boom")
	rows2 := suspEngagedRows()
	rows2.iterErr = iterBoom
	repo2 := NewCompanionSuspensionProjectionRepo(&suspStubTxRunner{q: &suspStubQuerier{nextRows: rows2}})
	if _, err := repo2.Status(tracing.WithTenantID(context.Background(), suspTenantA), suspTenantA, suspChatKey); !errors.Is(err, iterBoom) {
		t.Fatalf("iter err = %v, want iter boom", err)
	}
}

// ---------------------------------------------------------------------------
// SQL shape (review-via-test for migration 0112)
// ---------------------------------------------------------------------------

func TestCompanionSuspensionProjectionSQL_Shape(t *testing.T) {
	for _, must := range []string{
		"INSERT INTO companion_suspension_projection",
		"ON CONFLICT (suspension_id) DO UPDATE",
		"companion_suspension_projection.source_version < EXCLUDED.source_version",
		"companion_suspension_projection.changed_at < EXCLUDED.changed_at",
		"RETURNING source_version",
		"NULLIF($3, '')::uuid",
		"NULLIF($4, '')",
	} {
		if !contains(upsertCompanionSuspensionProjectionSQL, must) {
			t.Errorf("upsert SQL missing %q", must)
		}
	}
	for _, must := range []string{
		"FROM companion_suspension_projection",
		"engaged = TRUE",
		"scope = 'platform' OR tenant_id = $1::uuid",
	} {
		if !contains(selectEngagedCompanionSuspensionsSQL, must) {
			t.Errorf("select SQL missing %q", must)
		}
	}
	if PlatformScopeTenantGUC != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("PlatformScopeTenantGUC = %q", PlatformScopeTenantGUC)
	}
}
