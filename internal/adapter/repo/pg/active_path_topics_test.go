// active_path_topics_test.go — RLS contract verification for the
// active_path_topics repo.
package pg

import (
	"context"
	"testing"
)

func TestPGActivePathTopicsRepo_RecordPathTopicsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewActivePathTopicsRepo(tx)

	if err := repo.RecordPathTopics(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-9000-000000000001", []string{"agile", "scrum"}); err != nil {
		t.Fatalf("RecordPathTopics: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

func TestPGActivePathTopicsRepo_RecordPathTopicsEmitsUpsertPerTopic(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewActivePathTopicsRepo(tx)

	topics := []string{"agile", "scrum", "kanban"}
	if err := repo.RecordPathTopics(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-9000-000000000abc", topics); err != nil {
		t.Fatalf("RecordPathTopics: %v", err)
	}
	// 2 SET LOCAL + 3 INSERTs = 5 minimum.
	upserts := 0
	for _, c := range tx.q.execCalls {
		if contains(c, "INSERT INTO active_path_topics") {
			upserts++
		}
	}
	if upserts != len(topics) {
		t.Errorf("expected %d INSERTs; got %d", len(topics), upserts)
	}
}

func TestPGActivePathTopicsRepo_RecordPathTopicsEmptyIsNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewActivePathTopicsRepo(tx)

	if err := repo.RecordPathTopics(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-9000-000000000abc", nil); err != nil {
		t.Fatalf("nil topics: %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("expected no exec calls for empty topics; got %v", tx.q.execCalls)
	}
}

func TestPGActivePathTopicsRepo_GetTopicsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewActivePathTopicsRepo(tx)

	got, err := repo.GetTopics(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("GetTopics: %v", err)
	}
	if got == nil {
		t.Error("expected non-nil empty map")
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair on read path; got %d calls", len(tx.q.execCalls))
	}
}

// reuse scriptedRows infra from topic_accuracy_test.go to feed real
// rows into the GetTopics read path.
type aptScriptedRows struct {
	rows   []string
	idx    int
	err    error
	closed bool
}

func (r *aptScriptedRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *aptScriptedRows) Scan(dest ...any) error {
	if r.idx == 0 || r.idx > len(r.rows) {
		return ErrNoRows
	}
	if len(dest) < 1 {
		return nil
	}
	if p, ok := dest[0].(*string); ok {
		*p = r.rows[r.idx-1]
	}
	return nil
}

func (r *aptScriptedRows) Close() error { r.closed = true; return nil }
func (r *aptScriptedRows) Err() error   { return r.err }

type aptQuerier struct {
	stubQuerier
	rows *aptScriptedRows
}

func (a *aptQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	a.execCalls = append(a.execCalls, sql)
	return a.rows, nil
}

type aptTxRunner struct {
	q *aptQuerier
}

func (r *aptTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &aptQuerier{}
	}
	return fn(ctx, r.q)
}

func TestPGActivePathTopicsRepo_MarkAdvancedAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewActivePathTopicsRepo(tx)
	if err := repo.MarkAdvanced(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-9000-000000000001", 3); err != nil {
		t.Fatalf("MarkAdvanced: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + UPDATE; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "UPDATE active_path_topics") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGActivePathTopicsRepo_GetTopicsReturnsDistinct(t *testing.T) {
	rows := &aptScriptedRows{rows: []string{"agile", "scrum", "kanban"}}
	tx := &aptTxRunner{q: &aptQuerier{rows: rows}}
	repo := NewActivePathTopicsRepo(tx)

	got, err := repo.GetTopics(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("GetTopics: %v", err)
	}
	for _, want := range []string{"agile", "scrum", "kanban"} {
		if !got[want] {
			t.Errorf("topic %q missing from result %v", want, got)
		}
	}
	if !rows.closed {
		t.Error("rows should be closed")
	}
}
