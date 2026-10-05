// campaign_reveal_repo_test.go — WS-C4 (CHO-2083, ADR-227 D2) RLS-contract +
// claim-then-mark protocol gates for the campaign_reveal_ledger pg adapter.
//
// Mirrors campaign_repo_test.go/campaign_question_repo_test.go: a fake Querier
// captures the SQL (SET LOCAL lands BEFORE the statement) and drives the two
// halves of the redelivery-safe protocol — Claim (INSERT ... ON CONFLICT DO
// NOTHING RETURNING, falling back to a SELECT of the live row) and
// MarkPublished (UPDATE whose RowsAffected==0 fails loud).
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

func campaignRevealFixture(t *testing.T) campaign.RevealClaim {
	t.Helper()
	c, err := campaign.NewRevealClaim(pgTenantID, pgUserGCID,
		"01971b00-eeee-7000-8000-000000000020", // goal_id
		"01971b00-ffff-7000-8000-000000000030", // concept_id
		"01971b00-1111-7000-8000-000000000040", // node_won_event_id
		time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return c
}

func TestPGCampaignRevealRepo_ClaimInsertsFreshRow(t *testing.T) {
	claim := campaignRevealFixture(t)
	fake := &revealFakeQuerier{rowQueue: []*goalFakeRow{{cols: []any{
		claim.ID, claim.TenantID, claim.LearnerGCID, claim.GoalID, claim.ConceptID,
		claim.NodeWonEventID, nil, nil, claim.CreatedAt,
	}}}}
	repo := NewCampaignRevealLedgerRepo(&stubTxRunnerWith{q: fake})

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
		"INSERT INTO campaign_reveal_ledger",
		"node_won_event_id",
		"ON CONFLICT (tenant_id, learner_gcid, goal_id, concept_id)",
		"WHERE deleted_at IS NULL",
		"DO NOTHING",
		"RETURNING",
	} {
		if !contains(ins, must) {
			t.Errorf("claim SQL missing %q:\n%s", must, ins)
		}
	}
}

func TestPGCampaignRevealRepo_ClaimDuplicateReturnsExisting(t *testing.T) {
	claim := campaignRevealFixture(t)
	reqID := "01971b00-2222-7000-8000-000000000050"
	pub := claim.CreatedAt.Add(time.Minute)
	fake := &revealFakeQuerier{rowQueue: []*goalFakeRow{
		{err: ErrNoRows}, // INSERT ... ON CONFLICT DO NOTHING RETURNING → empty
		{cols: []any{ // fallback SELECT → the live row already claimed + published
			claim.ID, claim.TenantID, claim.LearnerGCID, claim.GoalID, claim.ConceptID,
			claim.NodeWonEventID, &reqID, &pub, claim.CreatedAt,
		}},
	}}
	repo := NewCampaignRevealLedgerRepo(&stubTxRunnerWith{q: fake})

	claimed, existing, err := repo.Claim(withCtx(), claim)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed {
		t.Error("duplicate Claim must report claimed=false")
	}
	if existing == nil {
		t.Fatal("duplicate Claim must return the existing live row")
	}
	if existing.PublishedAt == nil || !existing.PublishedAt.Equal(pub) {
		t.Errorf("existing.PublishedAt = %v; a duplicate of a published reveal must expose it so the caller acks", existing.PublishedAt)
	}
	sel := fake.sqls[len(fake.sqls)-1]
	if !contains(sel, "FROM campaign_reveal_ledger") || !contains(sel, "tenant_id = $1") || !contains(sel, "deleted_at IS NULL") {
		t.Errorf("fallback select SQL = %q", sel)
	}
}

func TestPGCampaignRevealRepo_MarkPublishedSetsFields(t *testing.T) {
	claim := campaignRevealFixture(t)
	fake := &revealFakeQuerier{rowsAff: 1}
	repo := NewCampaignRevealLedgerRepo(&stubTxRunnerWith{q: fake})

	at := time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)
	if err := repo.MarkPublished(withCtx(), claim.TenantID, claim.LearnerGCID,
		claim.GoalID, claim.ConceptID, "01971b00-2222-7000-8000-000000000050", at); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS must land before the update: %v", fake.sqls)
	}
	up := fake.sqls[len(fake.sqls)-1]
	for _, must := range []string{
		"UPDATE campaign_reveal_ledger",
		"request_id",
		"published_at",
		"updated_at",
		"WHERE tenant_id = $1",
		"deleted_at IS NULL",
	} {
		if !contains(up, must) {
			t.Errorf("mark-published SQL missing %q:\n%s", must, up)
		}
	}
}

func TestPGCampaignRevealRepo_MarkPublishedMissingRowErrors(t *testing.T) {
	claim := campaignRevealFixture(t)
	fake := &revealFakeQuerier{rowsAff: 0} // no live row matched the identity
	repo := NewCampaignRevealLedgerRepo(&stubTxRunnerWith{q: fake})

	err := repo.MarkPublished(withCtx(), claim.TenantID, claim.LearnerGCID,
		claim.GoalID, claim.ConceptID, "01971b00-2222-7000-8000-000000000050",
		time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("MarkPublished on a missing live row must fail loud")
	}
	if !errors.Is(err, campaign.ErrRevealClaimNotFound) {
		t.Errorf("want ErrRevealClaimNotFound, got %v", err)
	}
}

// revealFakeQuerier records every SQL string and drives Claim's two-step
// QueryRow protocol (INSERT ... RETURNING, then the fallback SELECT) plus
// MarkPublished's RowsAffected fail-loud. rowQueue pops front-to-back so a
// test can make the INSERT RETURNING miss (conflict) then the SELECT hit; the
// two rls.ApplySession SET LOCALs report RowsAffected 0 (not the statement
// under test), the domain mutation reports rowsAff.
type revealFakeQuerier struct {
	sqls     []string
	rowQueue []*goalFakeRow
	rowsAff  int64
}

func (q *revealFakeQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.sqls = append(q.sqls, sql)
	if contains(sql, "SET LOCAL") {
		return rls.CommandTag{}, nil
	}
	return rls.CommandTag{RowsAffected: q.rowsAff}, nil
}

func (q *revealFakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.sqls = append(q.sqls, sql)
	if len(q.rowQueue) == 0 {
		return &goalFakeRow{err: ErrNoRows}
	}
	r := q.rowQueue[0]
	q.rowQueue = q.rowQueue[1:]
	return r
}

func (q *revealFakeQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.sqls = append(q.sqls, sql)
	return nil, nil
}
