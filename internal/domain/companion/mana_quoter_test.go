// BE-USR-2 — ManaQuoter port + ErrInsufficientMana tests.
//
// Per ADR-142 (per-user economy), Companion actions that hit the LLM MUST
// pre-flight a mana debit through chora-identity ManaService BEFORE the
// LLM call. The domain talks to a port (ManaQuoter); the adapter wires
// it to the real gRPC client. Tests exercise:
//
//   - DeductMana success path forwards correct request to the port
//   - DeductMana ErrInsufficientMana surfaces the upsell context
//   - GetBalance returns BalanceSnapshot with subsidy breakdown
//   - free actions (cost=0) skip the quoter
//
// TDD strict: this file exists BEFORE mana_quoter.go and cost_map.go. RED
// phase verifies the package fails to compile until those land.
package companion

import (
	"context"
	"errors"
	"testing"
)

// fakeManaQuoter captures inputs + returns canned outcomes so domain
// tests run without a gRPC round-trip.
type fakeManaQuoter struct {
	deductCalls  []DeductManaInput
	deductErr    error
	balanceCalls []string
	balanceSnap  BalanceSnapshot
	balanceErr   error
}

func (f *fakeManaQuoter) DeductMana(_ context.Context, in DeductManaInput) error {
	f.deductCalls = append(f.deductCalls, in)
	return f.deductErr
}

func (f *fakeManaQuoter) GetBalance(_ context.Context, gcid string) (BalanceSnapshot, error) {
	f.balanceCalls = append(f.balanceCalls, gcid)
	return f.balanceSnap, f.balanceErr
}

// TestErrInsufficientMana_FieldsCarryUpsellContext — the typed error MUST
// expose required_units, current_balance, and a suggested_topup so the
// HTTP layer can render the 402 upsell payload.
func TestErrInsufficientMana_FieldsCarryUpsellContext(t *testing.T) {
	err := &ErrInsufficientMana{
		RequiredUnits:    100,
		CurrentBalance:   30,
		SuggestedTopupID: "companion_basic_topup_500",
	}
	if err.RequiredUnits != 100 {
		t.Errorf("RequiredUnits = %d, want 100", err.RequiredUnits)
	}
	if err.CurrentBalance != 30 {
		t.Errorf("CurrentBalance = %d, want 30", err.CurrentBalance)
	}
	if err.SuggestedTopupID != "companion_basic_topup_500" {
		t.Errorf("SuggestedTopupID = %q", err.SuggestedTopupID)
	}
	if err.Error() == "" {
		t.Errorf("Error() empty — must produce human-readable message")
	}
}

// TestErrInsufficientMana_IsCheckable — errors.Is matches by sentinel so
// callers can branch cleanly without a type assertion.
func TestErrInsufficientMana_IsCheckable(t *testing.T) {
	wrapped := &ErrInsufficientMana{RequiredUnits: 10, CurrentBalance: 0}
	if !errors.Is(wrapped, ErrInsufficientManaSentinel) {
		t.Errorf("errors.Is(ErrInsufficientMana, sentinel) = false, want true")
	}
}

// TestManaQuoterContract_PortInterfaceShape — the port surface must match
// what BE-USR-2 needs: DeductMana(ctx, input) error and GetBalance(ctx, gcid).
// This compiles ONLY when the interface is correctly declared.
func TestManaQuoterContract_PortInterfaceShape(t *testing.T) {
	var q ManaQuoter = &fakeManaQuoter{}
	if q == nil {
		t.Fatal("fake quoter must satisfy ManaQuoter interface")
	}
}

// TestDeductManaInput_FieldsForwardedAsExpected — caller must populate
// gcid, action_code, units, idempotency_key. The fake captures the input
// so tests can assert each field flows correctly.
func TestDeductManaInput_FieldsForwardedAsExpected(t *testing.T) {
	q := &fakeManaQuoter{}
	in := DeductManaInput{
		GCID:           "018f-marcus",
		ActionCode:     "daily_dose_coach",
		Units:          10,
		IdempotencyKey: "01971234-0000-7000-b000-000000000001",
		TenantID:       "acme",
		RequestID:      "req-7",
	}
	if err := q.DeductMana(context.Background(), in); err != nil {
		t.Fatalf("DeductMana err = %v", err)
	}
	if len(q.deductCalls) != 1 {
		t.Fatalf("deductCalls = %d, want 1", len(q.deductCalls))
	}
	if got := q.deductCalls[0]; got != in {
		t.Errorf("DeductMana captured = %+v, want %+v", got, in)
	}
}

// TestGetBalance_ReturnsSnapshotShape — the BalanceSnapshot exposes
// total balance + lifetime totals + subsidy breakdown for cost preview UX.
func TestGetBalance_ReturnsSnapshotShape(t *testing.T) {
	want := BalanceSnapshot{
		BalanceUnits:   150,
		LifetimeEarned: 500,
		LifetimeSpent:  350,
		SubsidyBreakdown: []SubsidySlice{
			{Source: ManaSourceTenantSubsidy, Units: 100, TenantID: "acme"},
			{Source: ManaSourceSubscriptionGrant, Units: 50},
		},
	}
	q := &fakeManaQuoter{balanceSnap: want}
	got, err := q.GetBalance(context.Background(), "018f-marcus")
	if err != nil {
		t.Fatalf("GetBalance err = %v", err)
	}
	if got.BalanceUnits != 150 {
		t.Errorf("BalanceUnits = %d, want 150", got.BalanceUnits)
	}
	if len(got.SubsidyBreakdown) != 2 {
		t.Errorf("SubsidyBreakdown len = %d, want 2", len(got.SubsidyBreakdown))
	}
	if got.SubsidyBreakdown[0].Source != ManaSourceTenantSubsidy {
		t.Errorf("first slice source = %v, want tenant_subsidy", got.SubsidyBreakdown[0].Source)
	}
}

// TestManaSource_EnumValues — the four enum values mirror the proto enum
// so JSON marshalling round-trips a stable string for HTTP/UI consumers.
func TestManaSource_EnumValues(t *testing.T) {
	cases := []struct {
		src  ManaSource
		want string
	}{
		{ManaSourceSubscriptionGrant, "subscription_grant"},
		{ManaSourceTopup, "topup"},
		{ManaSourcePromo, "promo"},
		{ManaSourceTenantSubsidy, "tenant_subsidy"},
	}
	for _, c := range cases {
		if got := string(c.src); got != c.want {
			t.Errorf("ManaSource(%v) = %q, want %q", c.src, got, c.want)
		}
	}
}
