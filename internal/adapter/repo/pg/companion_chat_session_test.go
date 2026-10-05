// companion_chat_session_test.go — ADR-154 conversational chat pgx adapter
// RLS contract + SQL-shape verification (per multi-tenant-rls SKILL).
//
// Production atomic-transaction behaviour (Get + Save + MarkClosed) is
// exercised under integration_test.go against a live Cloud SQL instance
// gated by the `integration` build tag. The tests in THIS file verify:
//
//  1. Every Repository method runs rls.ApplySession (SET LOCAL
//     chora.tenant_id + chora.user_gcid pair) BEFORE issuing the
//     domain query.
//  2. SQL string templates reference the expected table + columns
//     (review-via-test for the migration 0035 schema).
//  3. Sentinel-error mapping: ErrNoRows → companion.ErrChatSessionNotFound;
//     empty inputs → predictable errors.
//  4. Defence-in-depth: bare context (no tenant_id) returns
//     rls.ErrNoTenantContext.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ----------------------------------------------------------------------------
// chatStubRow + chatStubQuerier — minimal fixture that returns a row to Scan
// when the test pre-seeds expected scan values, else ErrNoRows.
// ----------------------------------------------------------------------------

type chatStubRow struct {
	values []any
	err    error
}

func (r *chatStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("chatStubRow.Scan: argc mismatch")
	}
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			if v, ok := r.values[i].(string); ok {
				*tgt = v
			}
		case *int:
			if v, ok := r.values[i].(int); ok {
				*tgt = v
			}
		case *time.Time:
			if v, ok := r.values[i].(time.Time); ok {
				*tgt = v
			}
		case **time.Time:
			if v, ok := r.values[i].(*time.Time); ok {
				*tgt = v
			}
		}
	}
	return nil
}

// chatStubQuerier records every Exec + QueryRow call so tests can verify
// the RLS pair lands first AND inspect the SQL strings emitted.
type chatStubQuerier struct {
	execCalls    []string
	queryRowSQLs []string
	queryRowArgs [][]any
	nextRow      *chatStubRow // returned by next QueryRow call
}

func (s *chatStubQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (s *chatStubQuerier) QueryRow(_ context.Context, sql string, args ...any) Row {
	s.queryRowSQLs = append(s.queryRowSQLs, sql)
	s.queryRowArgs = append(s.queryRowArgs, args)
	if s.nextRow != nil {
		return s.nextRow
	}
	// Default: return a no-rows row so Get behaves as "session not found".
	return &chatStubRow{err: ErrNoRows}
}

func (s *chatStubQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return nil, nil
}

// chatStubTxRunner runs fn against a fresh chatStubQuerier.
type chatStubTxRunner struct {
	q *chatStubQuerier
}

func (r *chatStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &chatStubQuerier{}
	}
	return fn(ctx, r.q)
}

// ----------------------------------------------------------------------------
// Get
// ----------------------------------------------------------------------------

func TestCompanionChatSessionRepo_Get_AppliesRLS(t *testing.T) {
	tx := &chatStubTxRunner{}
	repo := NewCompanionChatSessionRepo(tx)

	_, _ = repo.Get(withCtx(), pgUserGCID, "01970000-0000-7000-a000-000000000001")

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", tx.q.execCalls[1])
	}
}

func TestCompanionChatSessionRepo_Get_ReturnsNotFound_OnNoRows(t *testing.T) {
	tx := &chatStubTxRunner{}
	repo := NewCompanionChatSessionRepo(tx)

	_, err := repo.Get(withCtx(), pgUserGCID, "01970000-0000-7000-a000-000000000001")
	if !errors.Is(err, companion.ErrChatSessionNotFound) {
		t.Fatalf("err = %v; want ErrChatSessionNotFound", err)
	}
}

func TestCompanionChatSessionRepo_Get_ReturnsRow_WhenFound(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	stub := &chatStubQuerier{
		nextRow: &chatStubRow{
			values: []any{
				"01957d00-0000-7000-aaaa-000000000001", // id
				pgTenantID,                             // tenant_id
				pgUserGCID,                             // owner_gcid
				"01970000-0000-7000-a000-000000000001", // companion_id
				"vertex-session-abc",                   // engine_session_id
				int(50),                                // mana_charged_total
				int(3),                                 // turn_count
				now.Add(-time.Hour),                    // created_at
				now,                                    // last_used_at
				(*time.Time)(nil),                      // closed_at
			},
		},
	}
	tx := &chatStubTxRunner{q: stub}
	repo := NewCompanionChatSessionRepo(tx)

	got, err := repo.Get(withCtx(), pgUserGCID, "01970000-0000-7000-a000-000000000001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil session on hit")
	}
	if got.EngineSessionID != "vertex-session-abc" {
		t.Errorf("EngineSessionID = %q; want vertex-session-abc", got.EngineSessionID)
	}
	if got.TurnCount != 3 {
		t.Errorf("TurnCount = %d; want 3", got.TurnCount)
	}
	if got.ManaChargedTotal != 50 {
		t.Errorf("ManaChargedTotal = %d; want 50", got.ManaChargedTotal)
	}
	if !got.IsOpen() {
		t.Error("expected IsOpen=true for closed_at=NULL row")
	}
}

func TestCompanionChatSessionRepo_Get_FailsLoud_OnMissingTenantContext(t *testing.T) {
	tx := &chatStubTxRunner{}
	repo := NewCompanionChatSessionRepo(tx)
	_, err := repo.Get(context.Background(), pgUserGCID, "01970000-0000-7000-a000-000000000001")
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestCompanionChatSessionRepo_Get_RejectsBlankInputs(t *testing.T) {
	repo := NewCompanionChatSessionRepo(&chatStubTxRunner{})
	if _, err := repo.Get(withCtx(), "", "fam"); err == nil {
		t.Error("expected error on blank owner_gcid")
	}
	if _, err := repo.Get(withCtx(), "gcid", ""); err == nil {
		t.Error("expected error on blank companion_id")
	}
}

// ----------------------------------------------------------------------------
// Save (UPSERT)
// ----------------------------------------------------------------------------

func TestCompanionChatSessionRepo_Save_AppliesRLS(t *testing.T) {
	tx := &chatStubTxRunner{}
	repo := NewCompanionChatSessionRepo(tx)

	sess, _ := companion.NewChatSession(companion.NewChatSessionInput{
		ID:              "01957d00-0000-7000-aaaa-000000000001",
		TenantID:        pgTenantID,
		OwnerGCID:       pgUserGCID,
		CompanionID:     "01970000-0000-7000-a000-000000000001",
		EngineSessionID: "vertex-session-abc",
	})

	_ = repo.Save(withCtx(), sess)

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair before save")
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
	// The third call (after the 2 SET LOCALs) must be the UPSERT.
	if len(tx.q.execCalls) < 3 {
		t.Fatalf("expected UPSERT statement after RLS; got %d execs", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[2], "INSERT INTO companion_chat_sessions") {
		t.Errorf("call[2] = %q (missing INSERT)", tx.q.execCalls[2])
	}
	if !contains(tx.q.execCalls[2], "ON CONFLICT") {
		t.Errorf("call[2] = %q (missing ON CONFLICT — UPSERT shape required)", tx.q.execCalls[2])
	}
}

// ----------------------------------------------------------------------------
// MarkClosed
// ----------------------------------------------------------------------------

func TestCompanionChatSessionRepo_MarkClosed_AppliesRLS(t *testing.T) {
	tx := &chatStubTxRunner{}
	repo := NewCompanionChatSessionRepo(tx)

	_ = repo.MarkClosed(withCtx(), "01957d00-0000-7000-aaaa-000000000001")

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair before close")
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
	if len(tx.q.execCalls) < 3 {
		t.Fatalf("expected UPDATE statement after RLS")
	}
	if !contains(tx.q.execCalls[2], "UPDATE companion_chat_sessions") {
		t.Errorf("call[2] = %q (missing UPDATE)", tx.q.execCalls[2])
	}
	if !contains(tx.q.execCalls[2], "closed_at") {
		t.Errorf("call[2] = %q (missing closed_at)", tx.q.execCalls[2])
	}
}

func TestCompanionChatSessionRepo_MarkClosed_RejectsBlankID(t *testing.T) {
	repo := NewCompanionChatSessionRepo(&chatStubTxRunner{})
	if err := repo.MarkClosed(withCtx(), ""); err == nil {
		t.Error("expected error on blank session id")
	}
}

// ----------------------------------------------------------------------------
// SQL template review-via-test
// ----------------------------------------------------------------------------

func TestCompanionChatSession_SQLTemplates_NonEmpty(t *testing.T) {
	tmpls := []string{
		selectCompanionChatSessionOpenSQL,
		upsertCompanionChatSessionSQL,
		markClosedCompanionChatSessionSQL,
	}
	for i, s := range tmpls {
		if s == "" {
			t.Errorf("template[%d] empty", i)
		}
	}
}
