// campaign_question_repo_test.go — WS-C3 (CHO-2082) RLS-contract + SQL-shape
// gates for the campaign_question_sets pg adapter. Mirrors campaign_repo_test.go:
// stub Querier captures Exec/Query SQL (SET LOCAL lands BEFORE the statement).
package pg

import (
	"testing"
	"time"

	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

func campaignQuestionFixture(t *testing.T) *cq.QuestionSet {
	t.Helper()
	s, err := cq.New(pgTenantID, pgUserGCID,
		"01971b00-cccc-7000-8000-000000000003", "addition", 3,
		time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return s
}

func TestPGCampaignQuestionRepo_SaveAppliesRLSAndUpserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewCampaignQuestionSetRepo(tx)
	if err := repo.Save(withCtx(), campaignQuestionFixture(t)); err != nil {
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
		"INSERT INTO campaign_question_sets",
		"retrieved_atom_ids",
		"generation_status",
		"questions_payload",
		"requested_on",
		"request_origin",
		"ON CONFLICT (tenant_id, learner_gcid, concept_id, rung)",
		"WHERE deleted_at IS NULL",
		"DO UPDATE",
	} {
		if !contains(up, must) {
			t.Errorf("upsert SQL missing %q:\n%s", must, up)
		}
	}
	// The origin (march-default from cq.New) must reach the write as a value.
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	foundOrigin := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == string(cq.OriginMarch) {
			foundOrigin = true
		}
	}
	if !foundOrigin {
		t.Errorf("upsert must pass the request_origin value, args=%v", args)
	}
}

// A REQUESTED set for a bare node has NO retrieved atoms — the walk of
// 2026-07-10 caught the nil slice reaching the NOT NULL retrieved_atom_ids
// column as SQL NULL (23502, "generation skipped"), so a fresh goal's bank
// could never fill. The repo must coerce nil → empty array.
func TestPGCampaignQuestionRepo_SaveCoercesNilRetrievedAtoms(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewCampaignQuestionSetRepo(tx)
	s := campaignQuestionFixture(t)
	if s.RetrievedAtomIDs != nil {
		t.Fatalf("fixture precondition: RetrievedAtomIDs must be nil, got %v", s.RetrievedAtomIDs)
	}
	if err := repo.Save(withCtx(), s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	args := tx.q.execArgs[len(tx.q.execArgs)-1]
	got, ok := args[6].([]string) // $7 = retrieved_atom_ids
	if !ok || got == nil {
		t.Fatalf("retrieved_atom_ids arg = %#v, want non-nil []string (empty, not SQL NULL)", args[6])
	}
	if len(got) != 0 {
		t.Fatalf("retrieved_atom_ids = %v, want empty", got)
	}
}

func TestPGCampaignQuestionRepo_SaveNilIsNoop(t *testing.T) {
	tx := &stubTxRunner{}
	if err := NewCampaignQuestionSetRepo(tx).Save(withCtx(), nil); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil save must not touch the DB: %v", tx.q.execCalls)
	}
}

func TestPGCampaignQuestionRepo_GetByConceptRung_MissIsNilNil(t *testing.T) {
	tx := &stubTxRunner{}
	got, err := NewCampaignQuestionSetRepo(tx).GetByConceptRung(
		withCtx(), pgTenantID, pgUserGCID, "01971b00-cccc-7000-8000-000000000003", 3)
	if err != nil || got != nil {
		t.Fatalf("miss must be (nil, nil), got %v err=%v", got, err)
	}
}

func TestPGCampaignQuestionRepo_GetByAssistID_MissIsNilNil(t *testing.T) {
	tx := &stubTxRunner{}
	got, err := NewCampaignQuestionSetRepo(tx).GetByAssistID(withCtx(), pgTenantID, "assist-x")
	if err != nil || got != nil {
		t.Fatalf("foreign assist must be (nil, nil) for ack-skip, got %v err=%v", got, err)
	}
}

func TestPGCampaignQuestionRepo_CountRequestedOn_ZeroOnEmpty(t *testing.T) {
	tx := &stubTxRunner{}
	n, err := NewCampaignQuestionSetRepo(tx).CountRequestedOn(
		withCtx(), pgTenantID, pgUserGCID, cq.OriginTap, time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 0 {
		t.Fatalf("empty count must be 0, got %d err=%v", n, err)
	}
}

// The daily-cap count is now per-origin: the SQL must filter request_origin so
// the tap budget is counted independently of the march budget.
func TestPGCampaignQuestionRepo_CountRequestedOn_FiltersByOrigin(t *testing.T) {
	if !contains(countCampaignQuestionRequestsSQL, "request_origin") {
		t.Errorf("count SQL must filter by request_origin:\n%s", countCampaignQuestionRequestsSQL)
	}
}
