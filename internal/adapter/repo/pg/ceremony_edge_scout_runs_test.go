// ceremony_edge_scout_runs_test.go — CHO-2040 (owner ruling R8-1): RLS contract
// + SQL-shape verification for the pg first-run ledger (edgescout.RunStore).
// Mirrors learner_profile_test.go: a stub Querier captures the statements so we
// assert the SET LOCAL chora.tenant_id pair lands BEFORE the domain SQL, that
// RecordRun carries the ON CONFLICT DO NOTHING idempotency clause, and that
// HasRun probes with an EXISTS select. Parse-analysis coverage for the SQL
// consts rides prepare_smoke_integration_test.go.
package pg

import (
	"context"
	"testing"
	"time"
)

// cesrQuerier captures Exec AND QueryRow statements (the package stubQuerier
// records Exec only; HasRun goes through QueryRow).
type cesrQuerier struct {
	stubQuerier
	queryRowCalls []string
}

func (s *cesrQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	s.queryRowCalls = append(s.queryRowCalls, sql)
	return nil // nil stub row — HasRun treats it as "no row" (false)
}

type cesrTxRunner struct {
	q *cesrQuerier
}

func (r *cesrTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &cesrQuerier{}
	}
	return fn(ctx, r.q)
}

func TestPGCeremonyEdgeScoutRunRepo_HasRunAppliesRLSAndProbesExists(t *testing.T) {
	tx := &cesrTxRunner{}
	repo := NewCeremonyEdgeScoutRunRepo(tx)
	has, err := repo.HasRun(withCtx(), pgTenantID, "01970000-aaaa-7000-8000-00000000e5c0", pgUserGCID)
	if err != nil {
		t.Fatalf("HasRun: %v", err)
	}
	if has {
		t.Fatalf("HasRun = true from the nil stub row, want false")
	}
	execs := tx.q.execCalls
	if len(execs) < 2 || !contains(execs[0], "SET LOCAL chora.tenant_id") || !contains(execs[1], "SET LOCAL chora.user_gcid") {
		t.Fatalf("RLS session pair missing/misordered: %v", execs)
	}
	if len(tx.q.queryRowCalls) != 1 {
		t.Fatalf("queryRow calls = %v, want exactly the EXISTS probe", tx.q.queryRowCalls)
	}
	probe := tx.q.queryRowCalls[0]
	if !contains(probe, "SELECT EXISTS") || !contains(probe, "ceremony_edge_scout_runs") {
		t.Errorf("probe = %q; want SELECT EXISTS over ceremony_edge_scout_runs", probe)
	}
}

func TestPGCeremonyEdgeScoutRunRepo_RecordRunAppliesRLSAndIsIdempotent(t *testing.T) {
	tx := &cesrTxRunner{}
	repo := NewCeremonyEdgeScoutRunRepo(tx)
	if err := repo.RecordRun(withCtx(), pgTenantID, "01970000-aaaa-7000-8000-00000000e5c0", pgUserGCID, time.Now().UTC()); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	execs := tx.q.execCalls
	if len(execs) < 3 {
		t.Fatalf("want RLS pair + insert; got %d: %v", len(execs), execs)
	}
	if !contains(execs[0], "SET LOCAL chora.tenant_id") || !contains(execs[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS session pair missing/misordered: %v", execs[:2])
	}
	last := execs[len(execs)-1]
	if !contains(last, "INSERT INTO ceremony_edge_scout_runs") {
		t.Errorf("last = %q; want the run-ledger insert", last)
	}
	if !contains(last, "ON CONFLICT (tenant_id, goal_id, learner_gcid) DO NOTHING") {
		t.Errorf("last = %q; want the idempotent ON CONFLICT DO NOTHING clause (double-tap absorbs)", last)
	}
}
