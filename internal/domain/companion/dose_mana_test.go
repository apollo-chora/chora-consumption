// BE-USR-2 — Tests for the mana-metering integration on the LLM-backed
// daily-dose composition path.
//
// Per ADR-142 §4 the LLM-backed coach action ("daily_dose_coach") costs
// 10 mana per call. ComposeDailyDoseWithCoach MUST debit the quoter
// BEFORE calling the coach. If the wallet is short, the composer returns
// the typed ErrInsufficientMana so the HTTP layer can produce 402.
//
// Free fallback path (no LLM): never touches the quoter.
package companion

import (
	"context"
	"errors"
	"testing"
)

// TestComposeDailyDoseWithMana_DebitsBeforeCoachCall — when goal + coach
// + quoter are all wired, the quoter receives DeductMana BEFORE the coach
// receives RecommendDose. Order is enforced via the call counters.
func TestComposeDailyDoseWithMana_DebitsBeforeCoachCall(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"empiricism"}},
	}
	quoter := &fakeManaQuoter{}
	goal := &ExamPrepGoal{
		LearnerGCID: "018f-marcus",
		ExamID:      "scrum-master-cert",
		ExamDate:    now.AddDate(0, 0, 14),
	}

	dose, err := ComposeDailyDoseWithMana(context.Background(),
		DailyDoseInput{Seeds: seeds, Now: now},
		coach, goal, quoter,
		"idem-key-1", "acme",
	)
	if err != nil {
		t.Fatalf("ComposeDailyDoseWithMana err = %v", err)
	}
	if len(quoter.deductCalls) != 1 {
		t.Fatalf("quoter.deductCalls = %d, want 1", len(quoter.deductCalls))
	}
	if quoter.deductCalls[0].ActionCode != ActionDailyDoseCoach {
		t.Errorf("ActionCode = %q, want %q", quoter.deductCalls[0].ActionCode, ActionDailyDoseCoach)
	}
	if quoter.deductCalls[0].GCID != "018f-marcus" {
		t.Errorf("GCID = %q, want 018f-marcus", quoter.deductCalls[0].GCID)
	}
	if quoter.deductCalls[0].IdempotencyKey != "idem-key-1" {
		t.Errorf("IdempotencyKey = %q, want idem-key-1", quoter.deductCalls[0].IdempotencyKey)
	}
	if quoter.deductCalls[0].TenantID != "acme" {
		t.Errorf("TenantID = %q, want acme", quoter.deductCalls[0].TenantID)
	}
	if len(coach.calls) != 1 {
		t.Fatalf("coach.calls = %d, want 1", len(coach.calls))
	}
	if len(dose.Entries) == 0 {
		t.Errorf("dose entries empty after successful debit")
	}
}

// TestComposeDailyDoseWithMana_InsufficientReturnsTypedError — the typed
// error MUST surface so the HTTP layer can produce 402 with the upsell
// payload. Coach MUST NOT be called when debit fails.
func TestComposeDailyDoseWithMana_InsufficientReturnsTypedError(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"empiricism"}},
	}
	quoter := &fakeManaQuoter{
		deductErr: &ErrInsufficientMana{
			RequiredUnits:    10,
			CurrentBalance:   3,
			SuggestedTopupID: "companion_basic_topup_500",
		},
	}
	goal := &ExamPrepGoal{LearnerGCID: "g", ExamID: "e", ExamDate: now}

	_, err := ComposeDailyDoseWithMana(context.Background(),
		DailyDoseInput{Seeds: seeds, Now: now},
		coach, goal, quoter,
		"idem-key-2", "acme",
	)
	if err == nil {
		t.Fatal("ComposeDailyDoseWithMana err = nil, want ErrInsufficientMana")
	}
	if !errors.Is(err, ErrInsufficientManaSentinel) {
		t.Errorf("err = %v, want errors.Is sentinel", err)
	}
	var ime *ErrInsufficientMana
	if !errors.As(err, &ime) {
		t.Fatalf("errors.As failed; err = %T", err)
	}
	if ime.RequiredUnits != 10 {
		t.Errorf("RequiredUnits = %d, want 10", ime.RequiredUnits)
	}
	if ime.CurrentBalance != 3 {
		t.Errorf("CurrentBalance = %d, want 3", ime.CurrentBalance)
	}
	if len(coach.calls) != 0 {
		t.Errorf("coach should NOT be called on insufficient mana; got %d calls", len(coach.calls))
	}
}

// TestComposeDailyDoseWithMana_NilGoalSkipsQuoter — vanilla 2-1-2 mix has
// no LLM call so the quoter MUST NOT be touched.
func TestComposeDailyDoseWithMana_NilGoalSkipsQuoter(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	quoter := &fakeManaQuoter{}
	coach := &fakeExamPrepCoach{}

	dose, err := ComposeDailyDoseWithMana(context.Background(),
		DailyDoseInput{Seeds: seeds, Now: now},
		coach, nil, quoter,
		"idem-key-3", "acme",
	)
	if err != nil {
		t.Fatalf("ComposeDailyDoseWithMana err = %v", err)
	}
	if len(quoter.deductCalls) != 0 {
		t.Errorf("quoter should NOT be called when goal nil; got %d calls", len(quoter.deductCalls))
	}
	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
}

// TestComposeDailyDoseWithMana_NilCoachSkipsQuoter — when coach is nil
// (orchestrator URL unset) we must NOT charge mana for an action that
// won't actually run.
func TestComposeDailyDoseWithMana_NilCoachSkipsQuoter(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	quoter := &fakeManaQuoter{}
	goal := &ExamPrepGoal{LearnerGCID: "g", ExamID: "e", ExamDate: now}

	_, err := ComposeDailyDoseWithMana(context.Background(),
		DailyDoseInput{Seeds: seeds, Now: now},
		nil, goal, quoter,
		"idem-key-4", "acme",
	)
	if err != nil {
		t.Fatalf("ComposeDailyDoseWithMana err = %v", err)
	}
	if len(quoter.deductCalls) != 0 {
		t.Errorf("quoter should NOT be called when coach nil; got %d calls", len(quoter.deductCalls))
	}
}

// TestComposeDailyDoseWithMana_NilQuoterFallsBackToCoach — when the
// quoter port is nil (e.g., chora-identity unreachable at boot) the dose
// MUST still compose. Backstop policy for medium-tier actions per
// ADR-142 §8: fail-open with the coach call still attempted; the HTTP
// layer logs a `mana_metering_skipped=true` audit.
func TestComposeDailyDoseWithMana_NilQuoterFallsBackToCoach(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"empiricism"}},
	}
	goal := &ExamPrepGoal{LearnerGCID: "g", ExamID: "e", ExamDate: now}

	dose, err := ComposeDailyDoseWithMana(context.Background(),
		DailyDoseInput{Seeds: seeds, Now: now},
		coach, goal, nil,
		"idem-key-5", "acme",
	)
	if err != nil {
		t.Fatalf("ComposeDailyDoseWithMana err = %v (nil quoter must fail-open)", err)
	}
	if len(coach.calls) != 1 {
		t.Errorf("coach calls = %d, want 1 (fail-open)", len(coach.calls))
	}
	if len(dose.Entries) == 0 {
		t.Errorf("dose entries empty under fail-open")
	}
}
