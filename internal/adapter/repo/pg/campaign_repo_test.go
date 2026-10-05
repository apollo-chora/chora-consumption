// campaign_repo_test.go — WS-C1 (CHO-2080) RLS-contract + SQL-shape +
// scan-mapping gates for the campaign_node_progress pg adapter. Mirrors
// goal_test.go: stub Querier captures Exec SQL (SET LOCAL lands BEFORE the
// mutation); the generic fake row exercises scan→domain mapping.
package pg

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

func campaignProgressFixture(t *testing.T) *campaign.NodeProgress {
	t.Helper()
	p, err := campaign.NewNodeProgress(pgTenantID, pgUserGCID,
		"01971b00-bbbb-7000-8000-000000000002", time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return p
}

func TestPGCampaignRepo_SaveAppliesRLSAndUpserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewCampaignProgressRepo(tx)
	if err := repo.Save(withCtx(), campaignProgressFixture(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 2 {
		t.Fatalf("want RLS + upsert, got %d exec calls: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS must land before the write: %v", calls)
	}
	up := calls[len(calls)-1]
	for _, must := range []string{
		"INSERT INTO campaign_node_progress",
		"rung_cleared_at",
		"current_rung_correct",
		"last_advance_date",
		"ON CONFLICT (tenant_id, learner_gcid, concept_id)",
		"WHERE deleted_at IS NULL",
		"DO UPDATE",
	} {
		if !contains(up, must) {
			t.Errorf("upsert SQL missing %q:\n%s", must, up)
		}
	}
}

// A fresh ladder (first-ever graded answer on a node) has cleared NOTHING —
// the accelerated walk of 2026-07-11 caught the nil RungClearedAt reaching
// the NOT NULL rung_cleared_at TIMESTAMPTZ[] column as SQL NULL (23502 →
// FOLD_FAILED 500 on the answers door; day 1 never folded, so the hole
// stayed dark). The repo must coerce nil → empty array (the 8c4bfd887
// retrieved_atom_ids idiom).
func TestPGCampaignRepo_SaveCoercesNilRungClearedAt(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewCampaignProgressRepo(tx)
	p := campaignProgressFixture(t)
	if p.RungClearedAt != nil {
		t.Fatalf("fixture precondition: RungClearedAt must be nil, got %v", p.RungClearedAt)
	}
	if err := repo.Save(withCtx(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	got, ok := args[5].([]time.Time) // $6 = rung_cleared_at
	if !ok || got == nil {
		t.Fatalf("rung_cleared_at arg = %#v, want non-nil []time.Time (empty, not SQL NULL)", args[5])
	}
	if len(got) != 0 {
		t.Fatalf("rung_cleared_at = %v, want empty", got)
	}
}

func TestPGCampaignRepo_SaveNilIsNoop(t *testing.T) {
	tx := &stubTxRunner{}
	if err := NewCampaignProgressRepo(tx).Save(withCtx(), nil); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil save must not touch the DB: %v", tx.q.execCalls)
	}
}

func TestPGCampaignRepo_GetByConceptScansRow(t *testing.T) {
	created := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	adv := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	won := time.Date(2026, 7, 8, 9, 30, 0, 0, time.UTC)
	audit := []time.Time{created, created.Add(24 * time.Hour)}
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		"01971b00-aaaa-7000-8000-000000000001", pgTenantID, pgUserGCID,
		"01971b00-bbbb-7000-8000-000000000002",
		2, audit, 1, &won, &adv, created, created, nil,
	}}}
	repo := NewCampaignProgressRepo(&stubTxRunnerWith{q: fake})

	p, err := repo.GetByConcept(withCtx(), pgTenantID, pgUserGCID, "01971b00-bbbb-7000-8000-000000000002")
	if err != nil {
		t.Fatalf("GetByConcept: %v", err)
	}
	if p == nil {
		t.Fatal("GetByConcept returned nil for an existing row")
	}
	if p.RungsCleared != 2 || len(p.RungClearedAt) != 2 || p.CurrentRungCorrect != 1 {
		t.Errorf("ladder state = rungs=%d audit=%d counter=%d", p.RungsCleared, len(p.RungClearedAt), p.CurrentRungCorrect)
	}
	if p.WonAt == nil || !p.WonAt.Equal(won) {
		t.Errorf("WonAt = %v", p.WonAt)
	}
	if p.LastAdvanceDate == nil || !p.LastAdvanceDate.Equal(adv) {
		t.Errorf("LastAdvanceDate = %v", p.LastAdvanceDate)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM campaign_node_progress") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get SQL = %q; want live by-concept select", q)
	}
}

func TestPGCampaignRepo_GetByConceptNotFound(t *testing.T) {
	repo := NewCampaignProgressRepo(&stubTxRunner{})
	p, err := repo.GetByConcept(withCtx(), pgTenantID, pgUserGCID, "missing")
	if err != nil {
		t.Fatalf("GetByConcept: %v", err)
	}
	if p != nil {
		t.Errorf("missing row should yield nil; got %+v", p)
	}
	fake := &goalFakeQuerier{row: &goalFakeRow{err: ErrNoRows}}
	p2, err := NewCampaignProgressRepo(&stubTxRunnerWith{q: fake}).GetByConcept(withCtx(), pgTenantID, pgUserGCID, "gone")
	if err != nil {
		t.Fatalf("ErrNoRows should map to not-found: %v", err)
	}
	if p2 != nil {
		t.Errorf("ErrNoRows row should yield nil; got %+v", p2)
	}
}

func TestPGCampaignRepo_ListByLearnerScansRows(t *testing.T) {
	created := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: []any{
			"01971b00-aaaa-7000-8000-000000000001", pgTenantID, pgUserGCID,
			"01971b00-bbbb-7000-8000-000000000002",
			0, []time.Time{}, 0, nil, nil, created, created, nil,
		}},
		{cols: []any{
			"01971b00-aaaa-7000-8000-000000000003", pgTenantID, pgUserGCID,
			"01971b00-bbbb-7000-8000-000000000004",
			6, []time.Time{created, created, created, created, created, created}, 0, &created, &created, created, created, nil,
		}},
	}}}
	repo := NewCampaignProgressRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].Won() || !out[1].Won() {
		t.Errorf("won mapping broken: %v / %v", out[0].WonAt, out[1].WonAt)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM campaign_node_progress") || !contains(q, "tenant_id = $1") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("list SQL = %q", q)
	}
}
