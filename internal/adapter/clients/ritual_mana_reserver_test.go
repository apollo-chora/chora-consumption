// ritual_mana_reserver_test.go — CHO-2016 G4: reserve→refund mana adapter for
// the Ritual runner, RED-first. Unlike proofing (units=0, server-resolved), a
// Ritual run passes EXPLICIT composed units (published_price_units, dynamic per
// ritual) — DeductMana applies the caller's quote as-is.
package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeRitualManaBackend records Deduct/Credit calls.
type fakeRitualManaBackend struct {
	deducts   []companion.DeductManaInput
	deductErr error
	credits   []CreditManaInput
	creditErr error
}

func (f *fakeRitualManaBackend) DeductMana(_ context.Context, in companion.DeductManaInput) error {
	f.deducts = append(f.deducts, in)
	return f.deductErr
}

func (f *fakeRitualManaBackend) CreditMana(_ context.Context, in CreditManaInput) error {
	f.credits = append(f.credits, in)
	return f.creditErr
}

func TestRitualReserve_DeductsComposedUnits(t *testing.T) {
	fake := &fakeRitualManaBackend{}
	r := NewRitualManaReserver(fake)
	res, err := r.Reserve(context.Background(), companion.RitualReserveInput{
		TenantID: "tenant-1", GCID: "gcid-1", RunID: "run-1", Units: 35,
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(fake.deducts) != 1 {
		t.Fatalf("deducts = %d, want 1", len(fake.deducts))
	}
	d := fake.deducts[0]
	if d.ActionCode != companion.ActionCodeRitualRun {
		t.Fatalf("action = %q, want companion_ritual_run", d.ActionCode)
	}
	if d.Units != 35 {
		t.Fatalf("units = %d, want the caller's composed 35 (dynamic per-ritual)", d.Units)
	}
	// Tenant is intentionally dropped from the key (per-GCID dedup + globally
	// unique run_id) so it fits the identity ledger's varchar(128).
	wantKey := companion.ActionCodeRitualRun + ":gcid-1:run-1"
	if d.IdempotencyKey != wantKey {
		t.Fatalf("idem key = %q, want %q", d.IdempotencyKey, wantKey)
	}
	if len(d.IdempotencyKey) > 128 {
		t.Fatalf("idem key len %d exceeds identity ledger varchar(128)", len(d.IdempotencyKey))
	}
	if d.TenantID != "tenant-1" || d.GCID != "gcid-1" {
		t.Fatalf("identity mis-passed: %+v", d)
	}
	if res.ReservationID != wantKey || res.PriceUnits != 35 {
		t.Fatalf("reservation result = %+v, want handle=%q units=35", res, wantKey)
	}
}

// The runner branches on errors.Is(err, ErrInsufficientManaSentinel) → the
// adapter MUST propagate an error that matches that sentinel (skipped_budget).
func TestRitualReserve_InsufficientManaMatchesSentinel(t *testing.T) {
	fake := &fakeRitualManaBackend{deductErr: &companion.ErrInsufficientMana{RequiredUnits: 35, CurrentBalance: 3}}
	r := NewRitualManaReserver(fake)
	_, err := r.Reserve(context.Background(), companion.RitualReserveInput{TenantID: "t", GCID: "g", RunID: "r", Units: 35})
	if !errors.Is(err, companion.ErrInsufficientManaSentinel) {
		t.Fatalf("insufficient balance must match ErrInsufficientManaSentinel, got %v", err)
	}
	var insf *companion.ErrInsufficientMana
	if !errors.As(err, &insf) || insf.RequiredUnits != 35 {
		t.Fatalf("typed upsell fields lost: %v", err)
	}
}

func TestRitualReserve_OtherErrorsSurface(t *testing.T) {
	fake := &fakeRitualManaBackend{deductErr: errors.New("identity unreachable")}
	r := NewRitualManaReserver(fake)
	if _, err := r.Reserve(context.Background(), companion.RitualReserveInput{TenantID: "t", GCID: "g", RunID: "r", Units: 20}); err == nil {
		t.Fatal("infra error must surface (fail-loud, never fail-open on a paid action)")
	}
	if len(fake.credits) != 0 {
		t.Fatal("no refund on a failed reserve")
	}
}

func TestRitualRefund_CreditsAgainstReservation(t *testing.T) {
	fake := &fakeRitualManaBackend{}
	r := NewRitualManaReserver(fake)
	err := r.Refund(context.Background(), companion.RitualRefundInput{
		TenantID: "tenant-1", GCID: "gcid-1",
		ReservationID: "companion_ritual_run:tenant-1:gcid-1:run-1", Units: 35, Reason: "step_failed",
	})
	if err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if len(fake.credits) != 1 {
		t.Fatalf("credits = %d, want 1", len(fake.credits))
	}
	c := fake.credits[0]
	if c.GCID != "gcid-1" || c.Units != 35 {
		t.Fatalf("credit mis-passed: %+v", c)
	}
	if c.IdempotencyKey != "companion_ritual_run:tenant-1:gcid-1:run-1:refund" {
		t.Fatalf("refund idem key = %q", c.IdempotencyKey)
	}
}

func TestRitualRefund_ZeroUnitsIsNoOp(t *testing.T) {
	fake := &fakeRitualManaBackend{}
	r := NewRitualManaReserver(fake)
	if err := r.Refund(context.Background(), companion.RitualRefundInput{TenantID: "t", GCID: "g", ReservationID: "res", Units: 0}); err != nil {
		t.Fatalf("zero-unit refund must be a nil no-op, got %v", err)
	}
	if len(fake.credits) != 0 {
		t.Fatal("zero-unit refund must not RPC")
	}
}

func TestRitualReserver_NilBackendFailsLoud(t *testing.T) {
	r2 := NewRitualManaReserver(nil)
	if _, err := r2.Reserve(context.Background(), companion.RitualReserveInput{TenantID: "t", GCID: "g", RunID: "r", Units: 20}); err == nil {
		t.Fatal("nil backend must error")
	}
}
