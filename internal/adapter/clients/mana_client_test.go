// BE-USR-2 — Tests for the chora-identity ManaService gRPC client adapter.
//
// Transport is gRPC against the generated identityv1.ManaServiceClient. Tests
// inject a fake gRPC client (no live server / bufconn needed at the unit level
// — the real wire is covered by chora-identity's own bufconn suite + the proto
// contract). Behaviours pinned:
//   - DeductMana success → nil; forwards gcid/action/units/idem/tenant/request_id.
//   - DeductMana success=false → *companion.ErrInsufficientMana with upsell units.
//   - DeductMana rpc error → generic wrapped error (NOT the insufficient sentinel).
//   - GetBalance success → BalanceSnapshot incl. subsidy breakdown + enum + ts.
//   - Empty target → constructor fails loud (ErrManaEmptyBaseURL).
package clients

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeManaService is an in-memory identityv1.ManaServiceClient slice for tests.
// It captures the last request and replays canned responses/errors.
type fakeManaService struct {
	deductReq  *identityv1.DeductManaRequest
	deductResp *identityv1.DeductManaResponse
	deductErr  error

	balanceReq  *identityv1.GetBalanceRequest
	balanceResp *identityv1.GetBalanceResponse
	balanceErr  error

	creditReq  *identityv1.CreditManaRequest
	creditResp *identityv1.CreditManaResponse
	creditErr  error
}

func (f *fakeManaService) DeductMana(_ context.Context, in *identityv1.DeductManaRequest, _ ...grpc.CallOption) (*identityv1.DeductManaResponse, error) {
	f.deductReq = in
	return f.deductResp, f.deductErr
}

func (f *fakeManaService) GetBalance(_ context.Context, in *identityv1.GetBalanceRequest, _ ...grpc.CallOption) (*identityv1.GetBalanceResponse, error) {
	f.balanceReq = in
	return f.balanceResp, f.balanceErr
}

func (f *fakeManaService) CreditMana(_ context.Context, in *identityv1.CreditManaRequest, _ ...grpc.CallOption) (*identityv1.CreditManaResponse, error) {
	f.creditReq = in
	if f.creditResp == nil && f.creditErr == nil {
		return &identityv1.CreditManaResponse{BalanceAfterUnits: in.GetUnits()}, nil
	}
	return f.creditResp, f.creditErr
}

// TestManaClient_CreditMana_RefundLane — CHO-2040: the refund forwards the
// REFUND source/reason enums + the idempotency key; unit/gcid guards fail loud.
func TestManaClient_CreditMana_RefundLane(t *testing.T) {
	fake := &fakeManaService{}
	c := NewManaClientFromStub(fake, time.Second)
	err := c.CreditMana(context.Background(), CreditManaInput{
		GCID:           "gcid-1",
		Units:          25,
		IdempotencyKey: "proofing_test_gen:t:g:x:refund",
		Reason:         "qgen refused",
	})
	if err != nil {
		t.Fatalf("CreditMana: %v", err)
	}
	if fake.creditReq.GetGcid() != "gcid-1" || fake.creditReq.GetUnits() != 25 {
		t.Fatalf("credit request mis-passed: %+v", fake.creditReq)
	}
	if fake.creditReq.GetSource() != identityv1.ManaSource_MANA_SOURCE_REFUND {
		t.Fatalf("source = %v, want MANA_SOURCE_REFUND", fake.creditReq.GetSource())
	}
	if fake.creditReq.GetReason() != identityv1.ManaReasonCode_MANA_REASON_CODE_REFUND {
		t.Fatalf("reason = %v, want MANA_REASON_CODE_REFUND", fake.creditReq.GetReason())
	}
	if fake.creditReq.GetIdempotencyKey() != "proofing_test_gen:t:g:x:refund" {
		t.Fatalf("idem key = %q", fake.creditReq.GetIdempotencyKey())
	}
	// Guards.
	if err := c.CreditMana(context.Background(), CreditManaInput{GCID: "", Units: 1, IdempotencyKey: "k"}); err == nil {
		t.Fatalf("empty gcid must fail loud")
	}
	if err := c.CreditMana(context.Background(), CreditManaInput{GCID: "g", Units: 0, IdempotencyKey: "k"}); err == nil {
		t.Fatalf("zero units must fail loud (identity rejects 0)")
	}
	if err := c.CreditMana(context.Background(), CreditManaInput{GCID: "g", Units: 1, IdempotencyKey: " "}); err == nil {
		t.Fatalf("blank idempotency key must fail loud")
	}
	fake.creditErr = errors.New("unavailable")
	if err := c.CreditMana(context.Background(), CreditManaInput{GCID: "g", Units: 1, IdempotencyKey: "k"}); err == nil {
		t.Fatalf("rpc error must surface (caller NACKs and retries)")
	}
}

// TestManaClient_DeductMana_HappyPath — success returns nil + forwards the
// canonical request fields to the gRPC call.
func TestManaClient_DeductMana_HappyPath(t *testing.T) {
	fake := &fakeManaService{deductResp: &identityv1.DeductManaResponse{Success: true, BalanceAfterUnits: 90}}
	c := NewManaClientFromStub(fake, time.Second)

	err := c.DeductMana(context.Background(), companion.DeductManaInput{
		GCID:           "018f-marcus",
		ActionCode:     companion.ActionDailyDoseCoach,
		Units:          0,
		IdempotencyKey: "idem-1",
		TenantID:       "acme",
		RequestID:      "req-1",
	})
	if err != nil {
		t.Fatalf("DeductMana err = %v", err)
	}
	if fake.deductReq.GetGcid() != "018f-marcus" {
		t.Errorf("forwarded gcid = %q", fake.deductReq.GetGcid())
	}
	if fake.deductReq.GetActionCode() != companion.ActionDailyDoseCoach {
		t.Errorf("forwarded action_code = %q", fake.deductReq.GetActionCode())
	}
	if fake.deductReq.GetIdempotencyKey() != "idem-1" {
		t.Errorf("forwarded idempotency_key = %q", fake.deductReq.GetIdempotencyKey())
	}
	if fake.deductReq.GetTenantId() != "acme" {
		t.Errorf("forwarded tenant_id = %q", fake.deductReq.GetTenantId())
	}
	if fake.deductReq.GetRequestId() != "req-1" {
		t.Errorf("forwarded request_id = %q", fake.deductReq.GetRequestId())
	}
}

// TestManaClient_DeductMana_InsufficientReturnsTypedError — success=false maps
// to *companion.ErrInsufficientMana with the required + current balance so the
// handler can render a 402.
func TestManaClient_DeductMana_InsufficientReturnsTypedError(t *testing.T) {
	fake := &fakeManaService{deductResp: &identityv1.DeductManaResponse{
		Success:             false,
		RequiredUnits:       50,
		CurrentBalanceUnits: 12,
	}}
	c := NewManaClientFromStub(fake, time.Second)
	err := c.DeductMana(context.Background(), companion.DeductManaInput{
		GCID: "g", ActionCode: companion.ActionFullExamPrepCoach, IdempotencyKey: "k",
	})
	if err == nil {
		t.Fatal("err = nil, want ErrInsufficientMana")
	}
	if !errors.Is(err, companion.ErrInsufficientManaSentinel) {
		t.Errorf("errors.Is sentinel = false, err = %v", err)
	}
	var ime *companion.ErrInsufficientMana
	if !errors.As(err, &ime) {
		t.Fatalf("errors.As failed, err = %T", err)
	}
	if ime.RequiredUnits != 50 {
		t.Errorf("RequiredUnits = %d, want 50", ime.RequiredUnits)
	}
	if ime.CurrentBalance != 12 {
		t.Errorf("CurrentBalance = %d, want 12", ime.CurrentBalance)
	}
}

// TestManaClient_DeductMana_RPCErrorWrapped — a transport/RPC error surfaces as
// a generic wrapped error so the caller decides fail-open vs fail-closed
// (ADR-142 §8); it must NOT masquerade as insufficient mana.
func TestManaClient_DeductMana_RPCErrorWrapped(t *testing.T) {
	fake := &fakeManaService{deductErr: errors.New("rpc: unavailable")}
	c := NewManaClientFromStub(fake, time.Second)
	err := c.DeductMana(context.Background(), companion.DeductManaInput{
		GCID: "g", ActionCode: companion.ActionDailyDoseCoach, IdempotencyKey: "k",
	})
	if err == nil {
		t.Fatal("err = nil, want wrapped rpc error")
	}
	if errors.Is(err, companion.ErrInsufficientManaSentinel) {
		t.Errorf("rpc err must NOT match ErrInsufficientMana sentinel; got %v", err)
	}
	if !strings.Contains(err.Error(), "mana_client") {
		t.Errorf("err = %v; want mana_client-prefixed wrap", err)
	}
}

// TestManaClient_GetBalance_HappyPath — maps the proto response into a
// BalanceSnapshot incl. the subsidy breakdown (enum + tenant + timestamps).
func TestManaClient_GetBalance_HappyPath(t *testing.T) {
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	credited := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	fake := &fakeManaService{balanceResp: &identityv1.GetBalanceResponse{
		BalanceUnits:   150,
		LifetimeEarned: 500,
		LifetimeSpent:  350,
		SubsidyBreakdown: []*identityv1.SubsidySlice{
			{Source: identityv1.ManaSource_MANA_SOURCE_TENANT_SUBSIDY, Units: 100, TenantId: "acme", ExpiresAt: timestamppb.New(expires)},
			{Source: identityv1.ManaSource_MANA_SOURCE_SUBSCRIPTION_GRANT, Units: 50},
		},
		LastCreditedAt: timestamppb.New(credited),
	}}
	c := NewManaClientFromStub(fake, time.Second)
	snap, err := c.GetBalance(context.Background(), "018f-marcus")
	if err != nil {
		t.Fatalf("GetBalance err = %v", err)
	}
	if fake.balanceReq.GetGcid() != "018f-marcus" {
		t.Errorf("forwarded gcid = %q", fake.balanceReq.GetGcid())
	}
	if snap.BalanceUnits != 150 {
		t.Errorf("BalanceUnits = %d, want 150", snap.BalanceUnits)
	}
	if snap.LifetimeEarned != 500 || snap.LifetimeSpent != 350 {
		t.Errorf("lifetime earned/spent = %d/%d, want 500/350", snap.LifetimeEarned, snap.LifetimeSpent)
	}
	if len(snap.SubsidyBreakdown) != 2 {
		t.Fatalf("SubsidyBreakdown len = %d, want 2", len(snap.SubsidyBreakdown))
	}
	if snap.SubsidyBreakdown[0].Source != companion.ManaSourceTenantSubsidy {
		t.Errorf("slice[0] source = %v, want tenant_subsidy", snap.SubsidyBreakdown[0].Source)
	}
	if snap.SubsidyBreakdown[0].TenantID != "acme" {
		t.Errorf("slice[0] tenant_id = %q", snap.SubsidyBreakdown[0].TenantID)
	}
	if snap.SubsidyBreakdown[0].ExpiresAtUnixNano != expires.UnixNano() {
		t.Errorf("slice[0] expires = %d, want %d", snap.SubsidyBreakdown[0].ExpiresAtUnixNano, expires.UnixNano())
	}
	// A nil proto timestamp maps to zero (no expiry), not an error.
	if snap.SubsidyBreakdown[1].ExpiresAtUnixNano != 0 {
		t.Errorf("slice[1] expires = %d, want 0 (nil timestamp)", snap.SubsidyBreakdown[1].ExpiresAtUnixNano)
	}
	if snap.LastCreditedAtUnixNano != credited.UnixNano() {
		t.Errorf("LastCreditedAt = %d, want %d", snap.LastCreditedAtUnixNano, credited.UnixNano())
	}
}

// TestManaClient_GetBalance_RPCErrorWrapped — an RPC error surfaces wrapped.
func TestManaClient_GetBalance_RPCErrorWrapped(t *testing.T) {
	fake := &fakeManaService{balanceErr: errors.New("rpc: deadline exceeded")}
	c := NewManaClientFromStub(fake, time.Second)
	if _, err := c.GetBalance(context.Background(), "g"); err == nil {
		t.Fatal("expected wrapped rpc error")
	} else if !strings.Contains(err.Error(), "mana_client") {
		t.Errorf("err = %v; want mana_client-prefixed wrap", err)
	}
}

// TestNewManaClient_EmptyTargetFailsLoud — per feedback_no_inline_config a
// missing gRPC target MUST fail loud at construction (never silently no-op).
func TestNewManaClient_EmptyTargetFailsLoud(t *testing.T) {
	for _, target := range []string{"", "   "} {
		c, err := NewManaClient(target, time.Second)
		if !errors.Is(err, ErrManaEmptyBaseURL) {
			t.Errorf("NewManaClient(%q) err = %v, want ErrManaEmptyBaseURL", target, err)
		}
		if c != nil {
			t.Errorf("NewManaClient(%q) client = %v, want nil", target, c)
		}
	}
}

// TestManaClient_UnconfiguredFailsLoud — a nil / zero-value client fails loud
// rather than reporting a free debit (no silent free pass per fail-loud).
func TestManaClient_UnconfiguredFailsLoud(t *testing.T) {
	var c *ManaClient
	if err := c.DeductMana(context.Background(), companion.DeductManaInput{GCID: "g"}); err == nil {
		t.Error("nil-client DeductMana err = nil, want fail-loud")
	}
	if _, err := (&ManaClient{}).GetBalance(context.Background(), "g"); err == nil {
		t.Error("unconfigured GetBalance err = nil, want fail-loud")
	}
}

// TestManaClient_ImplementsManaQuoter — compile-time assertion that
// *ManaClient satisfies companion.ManaQuoter.
func TestManaClient_ImplementsManaQuoter(t *testing.T) {
	var _ companion.ManaQuoter = (*ManaClient)(nil)
}
