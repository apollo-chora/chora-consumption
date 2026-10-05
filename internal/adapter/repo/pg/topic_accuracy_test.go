// topic_accuracy_test.go — verifies the Postgres adapter for the
// topic_accuracy projection: every read/write path calls
// rls.ApplySession on the supplied transaction BEFORE issuing the
// domain query (per multi-tenant-rls skill). Until pgx integration
// tests land at M12 (testcontainers), a stub Querier captures the SQL
// strings and asserts the rls helper was called.
package pg

import (
	"context"
	"testing"
)

func TestPGTopicAccuracyRepo_RecordAttemptAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTopicAccuracyRepo(tx)

	if err := repo.RecordAttempt(withCtx(), pgTenantID, pgUserGCID, "agile", true); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if tx.q == nil {
		t.Fatal("tx querier nil")
	}
	if len(tx.q.execCalls) < 2 {
		t.Fatalf("expected ≥ 2 SET LOCAL calls (tenant + gcid), got %d", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

func TestPGTopicAccuracyRepo_RecordAttemptUpsertsCounters(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTopicAccuracyRepo(tx)

	if err := repo.RecordAttempt(withCtx(), pgTenantID, pgUserGCID, "scrum", false); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	// Last exec call should contain the upsert SQL.
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO topic_accuracy") {
		t.Errorf("last exec call = %q; want INSERT INTO topic_accuracy", last)
	}
	if !contains(last, "ON CONFLICT") {
		t.Errorf("upsert missing ON CONFLICT: %q", last)
	}
}

func TestPGTopicAccuracyRepo_GetByLearnerAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewTopicAccuracyRepo(tx)

	_, err := repo.GetByLearner(withCtx(), pgTenantID, pgUserGCID)
	// Stub Query returns (nil, nil) — repo will tolerate this and
	// return an empty map.
	if err != nil {
		t.Fatalf("GetByLearner: %v", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL on read path; got %d calls", len(tx.q.execCalls))
	}
}

func TestPGTopicAccuracyRepo_MarkSessionDedupApplied(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTopicAccuracyRepo(tx)

	first, err := repo.MarkSessionSeen(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-9000-000000000abc")
	if err != nil {
		t.Fatalf("first MarkSessionSeen: %v", err)
	}
	// stub.Exec returns RowsAffected=0 — for true at-least-once dedup
	// we expect repo to gate behaviour on RowsAffected; the stub
	// always returns 0, so first=false. We only assert the SQL emit
	// happened (not the dedup outcome); real M12 integration tests
	// against testcontainers Postgres exercise the conflict path.
	_ = first
	if len(tx.q.execCalls) < 3 {
		t.Errorf("expected SET LOCAL pair + INSERT INTO; got %d calls", len(tx.q.execCalls))
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO topic_accuracy_session_dedup") {
		t.Errorf("dedup INSERT missing: %q", last)
	}
}

// stubTxRunnerWithRows returns scripted rows on Query; used to test the
// GetByLearner read path actually iterates rows.
type scriptedRows struct {
	rows   []scriptedRow
	idx    int
	err    error
	closed bool
}

type scriptedRow struct {
	topic    string
	attempts int
	correct  int
}

func (r *scriptedRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *scriptedRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return ErrNoRows
	}
	if len(dest) < 3 {
		return nil
	}
	cur := r.rows[r.idx-1]
	if p, ok := dest[0].(*string); ok {
		*p = cur.topic
	}
	if p, ok := dest[1].(*int); ok {
		*p = cur.attempts
	}
	if p, ok := dest[2].(*int); ok {
		*p = cur.correct
	}
	return nil
}

func (r *scriptedRows) Close() error { r.closed = true; return nil }
func (r *scriptedRows) Err() error   { return r.err }

type scriptedRowsQuerier struct {
	stubQuerier
	rows *scriptedRows
}

func (s *scriptedRowsQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	s.execCalls = append(s.execCalls, sql)
	return s.rows, nil
}

type scriptedRowsTxRunner struct {
	q *scriptedRowsQuerier
}

func (r *scriptedRowsTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &scriptedRowsQuerier{}
	}
	return fn(ctx, r.q)
}

func TestPGTopicAccuracyRepo_GetByLearner_IteratesRows(t *testing.T) {
	rows := &scriptedRows{
		rows: []scriptedRow{
			{topic: "agile", attempts: 4, correct: 3},
			{topic: "scrum", attempts: 2, correct: 1},
		},
	}
	tx := &scriptedRowsTxRunner{q: &scriptedRowsQuerier{rows: rows}}
	repo := NewTopicAccuracyRepo(tx)

	got, err := repo.GetByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("GetByLearner: %v", err)
	}
	if got["agile"] != 0.75 {
		t.Errorf("agile = %v; want 0.75", got["agile"])
	}
	if got["scrum"] != 0.5 {
		t.Errorf("scrum = %v; want 0.5", got["scrum"])
	}
	if !rows.closed {
		t.Error("rows should be closed after iteration")
	}
}
