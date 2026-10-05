// BE-USR-2 — HTTP-layer tests for the mana-metering integration on
// /companion/daily-dose.
//
// Per ADR-142 §4 the LLM-backed coach action ("daily_dose_coach") costs
// 10 mana per call. The daily-dose handler MUST debit BEFORE calling
// the coach. On insufficient balance the response is HTTP 402 with the
// upsell payload {error, required, balance, upsell_url}.
//
// TDD strict: tests written before HTTP wiring lands.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeManaQuoter captures inputs + returns canned outcomes so HTTP tests
// run without a gRPC round-trip.
type fakeManaQuoter struct {
	deductCalls []companion.DeductManaInput
	deductErr   error
	balance     companion.BalanceSnapshot
	balanceErr  error
}

func (f *fakeManaQuoter) DeductMana(_ context.Context, in companion.DeductManaInput) error {
	f.deductCalls = append(f.deductCalls, in)
	return f.deductErr
}

func (f *fakeManaQuoter) GetBalance(_ context.Context, _ string) (companion.BalanceSnapshot, error) {
	return f.balance, f.balanceErr
}

// TestDailyDose_InsufficientMana_Returns402WithUpsell — when the wallet
// is short, the handler MUST surface 402 with the upsell payload AND
// MUST NOT call the coach.
func TestDailyDose_InsufficientMana_Returns402WithUpsell(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	coach := &fakeCoachPort{
		resp: &companion.ExamPrepCoachRecommendation{Topics: []string{"velocity_estimation"}},
	}
	srv.ExamPrepCoach = coach
	srv.ManaQuoter = &fakeManaQuoter{
		deductErr: &companion.ErrInsufficientMana{
			RequiredUnits:    10,
			CurrentBalance:   3,
			SuggestedTopupID: "companion_basic_topup_500",
		},
	}

	// Active goal triggers the LLM-backed coach path.
	seeds := srv.Atoms.Seeds()
	if len(seeds) == 0 {
		t.Fatal("seed catalogue empty")
	}
	srv.ExamPrepGoals.Save(testTenant, testGCID, &companion.ExamPrepGoal{
		LearnerGCID: testGCID,
		ExamID:      "scrum-master-cert",
		ExamDate:    time.Now().Add(14 * 24 * time.Hour),
		Retention: []companion.ExamPrepTopicRetention{
			{Topic: seeds[0].Topic, Score: 0.10},
		},
	})

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "INSUFFICIENT_MANA" {
		t.Errorf("error code = %v, want INSUFFICIENT_MANA", body["error"])
	}
	if body["required"].(float64) != 10 {
		t.Errorf("required = %v, want 10", body["required"])
	}
	if body["balance"].(float64) != 3 {
		t.Errorf("balance = %v, want 3", body["balance"])
	}
	if body["upsell_url"] != "/me/marketplace/companion-plans" {
		t.Errorf("upsell_url = %v, want /me/marketplace/companion-plans", body["upsell_url"])
	}
	if len(coach.calls) != 0 {
		t.Errorf("coach calls = %d on insufficient mana, want 0", len(coach.calls))
	}
}

// TestDailyDose_SufficientMana_DebitsBeforeCoach — happy path: quoter
// debit succeeds → coach is called → 200 returned. The deduct call MUST
// carry the canonical action_code + GCID + tenant_id.
func TestDailyDose_SufficientMana_DebitsBeforeCoach(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	seeds := srv.Atoms.Seeds()
	if len(seeds) == 0 {
		t.Fatal("seed catalogue empty")
	}
	coach := &fakeCoachPort{
		resp: &companion.ExamPrepCoachRecommendation{Topics: []string{seeds[0].Topic}},
	}
	srv.ExamPrepCoach = coach
	quoter := &fakeManaQuoter{}
	srv.ManaQuoter = quoter

	srv.ExamPrepGoals.Save(testTenant, testGCID, &companion.ExamPrepGoal{
		LearnerGCID: testGCID,
		ExamID:      "scrum-master-cert",
		ExamDate:    time.Now().Add(14 * 24 * time.Hour),
		Retention: []companion.ExamPrepTopicRetention{
			{Topic: seeds[0].Topic, Score: 0.10},
		},
	})

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if len(quoter.deductCalls) != 1 {
		t.Fatalf("deductCalls = %d, want 1", len(quoter.deductCalls))
	}
	if quoter.deductCalls[0].ActionCode != companion.ActionDailyDoseCoach {
		t.Errorf("ActionCode = %q, want %q",
			quoter.deductCalls[0].ActionCode, companion.ActionDailyDoseCoach)
	}
	if quoter.deductCalls[0].GCID != testGCID {
		t.Errorf("GCID = %q, want %q", quoter.deductCalls[0].GCID, testGCID)
	}
	if quoter.deductCalls[0].TenantID != testTenant {
		t.Errorf("TenantID = %q, want %q", quoter.deductCalls[0].TenantID, testTenant)
	}
	if quoter.deductCalls[0].IdempotencyKey == "" {
		t.Errorf("IdempotencyKey empty — must be derived from request")
	}
	if len(coach.calls) != 1 {
		t.Errorf("coach calls = %d, want 1", len(coach.calls))
	}
}

// TestDailyDose_NoGoal_QuoterUntouched — the vanilla 2-1-2 dose path
// makes no LLM call so the quoter MUST NOT be touched even when wired.
func TestDailyDose_NoGoal_QuoterUntouched(t *testing.T) {
	srv := NewServer()
	seedDailyDoseEngines(srv)
	quoter := &fakeManaQuoter{}
	srv.ManaQuoter = quoter

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(quoter.deductCalls) != 0 {
		t.Errorf("deductCalls = %d, want 0 (no goal → no LLM → no debit)",
			len(quoter.deductCalls))
	}
}
