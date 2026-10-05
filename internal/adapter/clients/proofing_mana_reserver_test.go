// proofing_mana_reserver_test.go — CHO-2040 (spec §3): reserve→refund mana
// adapter for the proofing-test composed runner, RED-first.
//
// Mirrors the WeaknessManaReserver mechanics: reserve = pre-flight
// DeductMana(units=0) with a DETERMINISTIC idempotency key
// (action:tenant:gcid:proofing_test_id — the spec §3 gcid+tenant+goal+nonce
// shape with the aggregate id as the nonce); the key doubles as the
// reservation handle. Refund = CreditMana against the SAME handle + ":refund".
package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// fakeProofingManaBackend records Deduct/Credit calls.
type fakeProofingManaBackend struct {
	deducts   []companion.DeductManaInput
	deductErr error
	credits   []CreditManaInput
	creditErr error
}

func (f *fakeProofingManaBackend) DeductMana(_ context.Context, in companion.DeductManaInput) error {
	f.deducts = append(f.deducts, in)
	return f.deductErr
}

func (f *fakeProofingManaBackend) CreditMana(_ context.Context, in CreditManaInput) error {
	f.credits = append(f.credits, in)
	return f.creditErr
}

func TestProofingReserve_DeductsServerResolvedPrice(t *testing.T) {
	fake := &fakeProofingManaBackend{}
	r := NewProofingManaReserver(fake)
	res, err := r.Reserve(context.Background(), pt.ReserveInput{
		TenantID:       "tenant-1",
		GCID:           "gcid-1",
		ProofingTestID: "pt-1",
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(fake.deducts) != 1 {
		t.Fatalf("deducts = %d, want 1", len(fake.deducts))
	}
	d := fake.deducts[0]
	if d.ActionCode != companion.ActionProofingTestGen {
		t.Fatalf("action = %q, want proofing_test_gen", d.ActionCode)
	}
	if d.Units != 0 {
		t.Fatalf("units = %d, want 0 (server resolves the ADR-178 price)", d.Units)
	}
	wantKey := companion.ActionProofingTestGen + ":tenant-1:gcid-1:pt-1"
	if d.IdempotencyKey != wantKey {
		t.Fatalf("idem key = %q, want %q", d.IdempotencyKey, wantKey)
	}
	if d.TenantID != "tenant-1" || d.GCID != "gcid-1" {
		t.Fatalf("identity mis-passed: %+v", d)
	}
	if res.ReservationID != wantKey {
		t.Fatalf("reservation handle = %q, want the idem key", res.ReservationID)
	}
	if res.PriceUnits != 25 {
		t.Fatalf("displayed price = %d, want canonical 25", res.PriceUnits)
	}
}

func TestProofingReserve_TranslatesInsufficientMana(t *testing.T) {
	fake := &fakeProofingManaBackend{deductErr: &companion.ErrInsufficientMana{
		RequiredUnits:  25,
		CurrentBalance: 3,
	}}
	r := NewProofingManaReserver(fake)
	_, err := r.Reserve(context.Background(), pt.ReserveInput{TenantID: "t", GCID: "g", ProofingTestID: "p"})
	var insf *pt.ErrInsufficientMana
	if !errors.As(err, &insf) {
		t.Fatalf("want *proofingtest.ErrInsufficientMana, got %v", err)
	}
	if insf.RequiredUnits != 25 || insf.CurrentBalance != 3 {
		t.Fatalf("upsell numbers lost: %+v", insf)
	}
}

func TestProofingReserve_OtherErrorsSurface(t *testing.T) {
	fake := &fakeProofingManaBackend{deductErr: errors.New("identity unreachable")}
	r := NewProofingManaReserver(fake)
	if _, err := r.Reserve(context.Background(), pt.ReserveInput{TenantID: "t", GCID: "g", ProofingTestID: "p"}); err == nil {
		t.Fatalf("infra error must surface (fail-loud, never fail-open on a paid action)")
	}
	if len(fake.credits) != 0 {
		t.Fatalf("no refund on a failed reserve")
	}
}

func TestProofingRefund_CreditsAgainstReservation(t *testing.T) {
	fake := &fakeProofingManaBackend{}
	r := NewProofingManaReserver(fake)
	err := r.Refund(context.Background(), pt.RefundInput{
		TenantID:      "tenant-1",
		GCID:          "gcid-1",
		ReservationID: "proofing_test_gen:tenant-1:gcid-1:pt-1",
		Units:         25,
		Reason:        "qgen refused",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if len(fake.credits) != 1 {
		t.Fatalf("credits = %d, want 1", len(fake.credits))
	}
	c := fake.credits[0]
	if c.GCID != "gcid-1" || c.Units != 25 {
		t.Fatalf("credit mis-passed: %+v", c)
	}
	if c.IdempotencyKey != "proofing_test_gen:tenant-1:gcid-1:pt-1:refund" {
		t.Fatalf("refund idem key = %q", c.IdempotencyKey)
	}
	if c.Reason == "" {
		t.Fatalf("refund reason must be carried")
	}
}

func TestProofingRefund_ZeroUnitsIsNoOp(t *testing.T) {
	fake := &fakeProofingManaBackend{}
	r := NewProofingManaReserver(fake)
	if err := r.Refund(context.Background(), pt.RefundInput{TenantID: "t", GCID: "g", ReservationID: "res", Units: 0, Reason: "x"}); err != nil {
		t.Fatalf("zero-unit refund must be a nil no-op, got %v", err)
	}
	if len(fake.credits) != 0 {
		t.Fatalf("zero-unit refund must not RPC")
	}
}

func TestProofingReserver_NilBackendFailsLoud(t *testing.T) {
	var r *ProofingManaReserver
	if _, err := r.Reserve(context.Background(), pt.ReserveInput{TenantID: "t", GCID: "g", ProofingTestID: "p"}); err == nil {
		t.Fatalf("nil reserver must error")
	}
	r2 := NewProofingManaReserver(nil)
	if _, err := r2.Reserve(context.Background(), pt.ReserveInput{TenantID: "t", GCID: "g", ProofingTestID: "p"}); err == nil {
		t.Fatalf("nil backend must error")
	}
}
