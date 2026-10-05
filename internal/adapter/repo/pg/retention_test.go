// retention_test.go — RLS contract verification for the retention-loop
// pg adapters (migration 0048). Mirrors learning_path_test.go: a stub
// TxRunner/Querier asserts every method establishes the RLS tenant session
// (SET LOCAL pair) before touching its table.
package pg

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestPGSM2StateRepo_SaveStateAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSM2StateRepo(tx)
	state := companion.NewSM2State("01970000-0000-7000-a000-000000000001")
	if err := repo.SaveState(withCtx(), pgTenantID, pgUserGCID, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO sm2_states") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGSM2StateRepo_GetStateAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewSM2StateRepo(tx)
	_, found, err := repo.GetState(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-a000-000000000001")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if found {
		t.Error("stub Querier must read as not-found")
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGSM2StateRepo_AllStatesAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewSM2StateRepo(tx)
	states, err := repo.AllStatesForLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("AllStatesForLearner: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("stub Querier must read empty; got %d", len(states))
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGSM2StateRepo_MarkTopicSeenAppliesRLS_AndIgnoresEmpty(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSM2StateRepo(tx)
	if err := repo.MarkTopicSeen(withCtx(), pgTenantID, pgUserGCID, "  "); err != nil {
		t.Fatalf("MarkTopicSeen(blank): %v", err)
	}
	if tx.q != nil {
		t.Error("blank topic must not open a tx")
	}
	if err := repo.MarkTopicSeen(withCtx(), pgTenantID, pgUserGCID, "agile"); err != nil {
		t.Fatalf("MarkTopicSeen: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO learner_seen_topics") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGSM2StateRepo_SeenTopicsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewSM2StateRepo(tx)
	if _, err := repo.SeenTopics(withCtx(), pgTenantID, pgUserGCID); err != nil {
		t.Fatalf("SeenTopics: %v", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGSM2StateRepo_RejectsNoTenantContext(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSM2StateRepo(tx)
	// bare ctx (no tracing tenant) → rls.ApplySession must error → fail loud
	if err := repo.SaveState(context.Background(), pgTenantID, pgUserGCID, companion.NewSM2State("a")); err == nil {
		t.Error("expected RLS-context error on bare ctx; got nil")
	}
}

func TestPGLearnerStreakRepo_GetAppliesRLS_FreshStreak(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearnerStreakRepo(tx)
	s, err := repo.Get(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s == nil || s.Count != 0 {
		t.Errorf("fresh streak = %+v, want zero-count non-nil", s)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGLearnerStreakRepo_RecordActivityUpserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerStreakRepo(tx)
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	if err := repo.RecordActivity(withCtx(), pgTenantID, pgUserGCID, now); err != nil {
		t.Fatalf("RecordActivity: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO learner_streaks") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGLearnerXPRepo_GetAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearnerXPRepo(tx)
	xp, err := repo.Get(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if xp != 0 {
		t.Errorf("stub XP = %d, want 0", xp)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGLearnerXPRepo_AwardUpserts_AndIgnoresNonPositive(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerXPRepo(tx)
	if err := repo.Award(withCtx(), pgTenantID, pgUserGCID, 0); err != nil {
		t.Fatalf("Award(0): %v", err)
	}
	if tx.q != nil {
		t.Error("non-positive delta must not open a tx")
	}
	if err := repo.Award(withCtx(), pgTenantID, pgUserGCID, 50); err != nil {
		t.Fatalf("Award: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO learner_xp") {
		t.Errorf("last exec call = %q", last)
	}
}

// ----- regression: the RUNTIME no-rows sentinel maps to not-found -----
//
// Live bug 2026-06-10: the adapters checked pgx.ErrNoRows but the pgx_runtime
// Row wrapper returns the PACKAGE ErrNoRows sentinel — every unprojected atom
// surfaced as a storage error and the fail-loud dose path 500'd in prod.

type noRowsRow struct{}

func (noRowsRow) Scan(...any) error { return ErrNoRows }

type noRowsQuerier struct{ stubQuerier }

func (q *noRowsQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row {
	q.stubQuerier.QueryRow(ctx, sql, args...)
	return noRowsRow{}
}

func TestPGSM2StateRepo_RuntimeNoRowsSentinelReadsNotFound(t *testing.T) {
	repo := NewSM2StateRepo(&stubTxRunnerWith{q: &noRowsQuerier{}})
	_, found, err := repo.GetState(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-a000-000000000001")
	if err != nil {
		t.Fatalf("GetState must map runtime ErrNoRows to not-found, got err: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

func TestPGLearnerStreakRepo_RuntimeNoRowsSentinelReadsFresh(t *testing.T) {
	repo := NewLearnerStreakRepo(&stubTxRunnerWith{q: &noRowsQuerier{}})
	s, err := repo.Get(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("Get must map runtime ErrNoRows to a fresh streak, got err: %v", err)
	}
	if s == nil || s.Count != 0 {
		t.Errorf("streak = %+v, want zero-count", s)
	}
}

func TestPGLearnerXPRepo_RuntimeNoRowsSentinelReadsZero(t *testing.T) {
	repo := NewLearnerXPRepo(&stubTxRunnerWith{q: &noRowsQuerier{}})
	xp, err := repo.Get(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("Get must map runtime ErrNoRows to 0, got err: %v", err)
	}
	if xp != 0 {
		t.Errorf("xp = %d, want 0", xp)
	}
}

// stubTxRunnerWith runs the injected Querier verbatim (no auto-replacement).
type stubTxRunnerWith struct {
	q Querier
}

func (r *stubTxRunnerWith) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	return fn(ctx, r.q)
}
