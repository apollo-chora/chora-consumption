// atom_refresh_ledger_test.go: ADR-244 D5 claim-then-mark + per-day cap gates
// for the atom_refresh_ledger pg adapter (migration 0108). Mirrors
// campaign_reveal_repo_test.go: a fake Querier captures the SQL (SET LOCAL
// lands BEFORE the statement) and drives Claim / MarkPublished /
// CountClaimedSince.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
)

func atomRefreshFixture(t *testing.T) atomrefresh.RefreshClaim {
	t.Helper()
	c, err := atomrefresh.NewRefreshClaim(pgTenantID, pgUserGCID,
		"01990b00-aaaa-7000-8000-000000000010", // atom_id
		"01990b00-bbbb-7000-8000-000000000020", // focal_concept_id
		"01990b00-cccc-7000-8000-000000000030", // trigger_event_id
		time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return c
}

type refreshFakeRow struct {
	cols []any
	err  error
}

func (r *refreshFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		if i >= len(r.cols) {
			break
		}
		switch p := d.(type) {
		case *string:
			if s, ok := r.cols[i].(string); ok {
				*p = s
			}
		case **string:
			if s, ok := r.cols[i].(string); ok {
				*p = &s
			} else {
				*p = nil
			}
		case *time.Time:
			if ts, ok := r.cols[i].(time.Time); ok {
				*p = ts
			}
		case **time.Time:
			if ts, ok := r.cols[i].(time.Time); ok {
				*p = &ts
			} else {
				*p = nil
			}
		case *int:
			if n, ok := r.cols[i].(int); ok {
				*p = n
			}
		}
	}
	return nil
}

type refreshFakeQuerier struct {
	sqls     []string
	rowQueue []*refreshFakeRow
	rowsAff  int64
}

func (q *refreshFakeQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.sqls = append(q.sqls, sql)
	if contains(sql, "SET LOCAL") {
		return rls.CommandTag{}, nil
	}
	return rls.CommandTag{RowsAffected: q.rowsAff}, nil
}

func (q *refreshFakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.sqls = append(q.sqls, sql)
	if len(q.rowQueue) == 0 {
		return &refreshFakeRow{err: ErrNoRows}
	}
	head := q.rowQueue[0]
	q.rowQueue = q.rowQueue[1:]
	return head
}

func (q *refreshFakeQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.sqls = append(q.sqls, sql)
	return nil, errors.New("refreshFakeQuerier: Query unused")
}

func TestPGAtomRefreshLedger_ClaimInsertsFreshRow(t *testing.T) {
	claim := atomRefreshFixture(t)
	fake := &refreshFakeQuerier{rowQueue: []*refreshFakeRow{{cols: []any{
		claim.ID, claim.TenantID, claim.LearnerGCID, claim.AtomID, claim.FocalConceptID,
		claim.TriggerEventID, nil, nil, claim.CreatedAt,
	}}}}
	repo := NewAtomRefreshLedgerRepo(&stubTxRunnerWith{q: fake})

	claimed, existing, err := repo.Claim(withCtx(), claim)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed {
		t.Error("fresh Claim must report claimed=true")
	}
	if existing == nil || existing.ID != claim.ID {
		t.Fatalf("fresh Claim must return the inserted row, got %+v", existing)
	}
	if existing.RequestID != nil || existing.PublishedAt != nil {
		t.Errorf("fresh claim carries no request_id/published_at yet: %+v", existing)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS must land before the claim: %v", fake.sqls)
	}
	ins := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{
		"INSERT INTO atom_refresh_ledger",
		"ON CONFLICT (tenant_id, learner_gcid, atom_id, focal_concept_id)",
		"DO NOTHING",
		"RETURNING",
	} {
		if !contains(ins, must) {
			t.Errorf("claim SQL missing %q:\n%s", must, ins)
		}
	}
}

func TestPGAtomRefreshLedger_ClaimConflictReturnsExistingLiveRow(t *testing.T) {
	claim := atomRefreshFixture(t)
	published := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	fake := &refreshFakeQuerier{rowQueue: []*refreshFakeRow{
		{err: ErrNoRows}, // INSERT ... RETURNING found a conflict
		{cols: []any{
			"existing-id", claim.TenantID, claim.LearnerGCID, claim.AtomID, claim.FocalConceptID,
			"older-event", "req-1", published, claim.CreatedAt,
		}},
	}}
	repo := NewAtomRefreshLedgerRepo(&stubTxRunnerWith{q: fake})

	claimed, existing, err := repo.Claim(withCtx(), claim)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed {
		t.Error("conflicting Claim must report claimed=false")
	}
	if existing == nil || existing.ID != "existing-id" {
		t.Fatalf("conflicting Claim must return the existing live row, got %+v", existing)
	}
	if existing.PublishedAt == nil || !existing.PublishedAt.Equal(published) {
		t.Errorf("existing row must carry its published_at: %+v", existing)
	}
}

func TestPGAtomRefreshLedger_MarkPublishedZeroRowsFailsLoud(t *testing.T) {
	fake := &refreshFakeQuerier{rowsAff: 0}
	repo := NewAtomRefreshLedgerRepo(&stubTxRunnerWith{q: fake})

	err := repo.MarkPublished(withCtx(), pgTenantID, pgUserGCID, "atom", "focal", "req", time.Now().UTC())
	if !errors.Is(err, atomrefresh.ErrRefreshClaimNotFound) {
		t.Fatalf("RowsAffected==0 must fail loud with ErrRefreshClaimNotFound, got %v", err)
	}
}

func TestPGAtomRefreshLedger_MarkPublishedStampsLiveRow(t *testing.T) {
	fake := &refreshFakeQuerier{rowsAff: 1}
	repo := NewAtomRefreshLedgerRepo(&stubTxRunnerWith{q: fake})

	if err := repo.MarkPublished(withCtx(), pgTenantID, pgUserGCID, "atom", "focal", "req", time.Now().UTC()); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	upd := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{"UPDATE atom_refresh_ledger", "request_id", "published_at", "deleted_at IS NULL"} {
		if !contains(upd, must) {
			t.Errorf("mark SQL missing %q:\n%s", must, upd)
		}
	}
}

func TestPGAtomRefreshLedger_CountClaimedSince(t *testing.T) {
	fake := &refreshFakeQuerier{rowQueue: []*refreshFakeRow{{cols: []any{4}}}}
	repo := NewAtomRefreshLedgerRepo(&stubTxRunnerWith{q: fake})

	n, err := repo.CountClaimedSince(withCtx(), pgTenantID, pgUserGCID, time.Now().UTC().Truncate(24*time.Hour))
	if err != nil {
		t.Fatalf("CountClaimedSince: %v", err)
	}
	if n != 4 {
		t.Fatalf("count = %d, want 4", n)
	}
	q := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{"SELECT count(*)", "atom_refresh_ledger", "created_at >=", "deleted_at IS NULL"} {
		if !contains(q, must) {
			t.Errorf("count SQL missing %q:\n%s", must, q)
		}
	}
}
