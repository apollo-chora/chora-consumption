package clients_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- fakes -------------------------------------------------------------------

type fakeQuoter struct {
	gotIn companion.DeductManaInput
	calls int
	err   error
}

func (f *fakeQuoter) DeductMana(_ context.Context, in companion.DeductManaInput) error {
	f.calls++
	f.gotIn = in
	return f.err
}
func (f *fakeQuoter) GetBalance(context.Context, string) (companion.BalanceSnapshot, error) {
	return companion.BalanceSnapshot{}, nil
}

type fakeLister struct {
	insts []*companion.Instance
	err   error
}

func (f *fakeLister) ListByOwner(context.Context, string, string) ([]*companion.Instance, error) {
	return f.insts, f.err
}

var rin = wu.ReserveInput{TenantID: "t-1", GCID: "g-1", UploadID: "u-7"}

// --- WeaknessManaReserver ----------------------------------------------------

func TestWeaknessManaReserver_Success_DebitsUnits0_DerivesIdempotentHandle(t *testing.T) {
	q := &fakeQuoter{}
	r := clients.NewWeaknessManaReserver(q)

	got, err := r.ReserveAnalysis(context.Background(), rin)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Server resolves the price (units=0) via the price-plan resolver, honouring
	// tenant overrides; the action_code routes to migration 0028's rows.
	if q.gotIn.ActionCode != companion.ActionWeaknessAnalysis {
		t.Errorf("action_code=%q want %q", q.gotIn.ActionCode, companion.ActionWeaknessAnalysis)
	}
	if q.gotIn.Units != 0 {
		t.Errorf("units=%d want 0 (server resolves price)", q.gotIn.Units)
	}
	if q.gotIn.GCID != rin.GCID || q.gotIn.TenantID != rin.TenantID {
		t.Errorf("identity not forwarded: %+v", q.gotIn)
	}
	if q.gotIn.IdempotencyKey == "" {
		t.Error("idempotency key must be set (idempotent reserve)")
	}
	// reservation handle == the debit idempotency key (the crew refunds against it).
	if got.ReservationID != q.gotIn.IdempotencyKey {
		t.Errorf("reservation_id=%q must equal idempotency key=%q", got.ReservationID, q.gotIn.IdempotencyKey)
	}
	// Displayed price comes from the canonical fallback (50; mirrors 0028 default).
	if got.PriceUnits != 50 {
		t.Errorf("price=%d want 50 (canonical display fallback)", got.PriceUnits)
	}
}

// Idempotency: same (tenant, gcid, upload_id) ⇒ identical idempotency key, so a
// re-published upload replays without double-reserving. Different upload ⇒
// different key.
func TestWeaknessManaReserver_IdempotencyKey_StableOnTuple(t *testing.T) {
	q1, q2, q3 := &fakeQuoter{}, &fakeQuoter{}, &fakeQuoter{}
	r := clients.NewWeaknessManaReserver(q1)
	_, _ = r.ReserveAnalysis(context.Background(), rin)

	r2 := clients.NewWeaknessManaReserver(q2)
	_, _ = r2.ReserveAnalysis(context.Background(), rin) // same tuple
	if q1.gotIn.IdempotencyKey != q2.gotIn.IdempotencyKey {
		t.Errorf("same tuple must yield same key: %q vs %q", q1.gotIn.IdempotencyKey, q2.gotIn.IdempotencyKey)
	}

	r3 := clients.NewWeaknessManaReserver(q3)
	_, _ = r3.ReserveAnalysis(context.Background(), wu.ReserveInput{TenantID: "t-1", GCID: "g-1", UploadID: "u-OTHER"})
	if q3.gotIn.IdempotencyKey == q1.gotIn.IdempotencyKey {
		t.Errorf("different upload_id must yield different key, both = %q", q1.gotIn.IdempotencyKey)
	}
}

func TestWeaknessManaReserver_InsufficientMana_Translated(t *testing.T) {
	q := &fakeQuoter{err: &companion.ErrInsufficientMana{RequiredUnits: 50, CurrentBalance: 12}}
	r := clients.NewWeaknessManaReserver(q)

	_, err := r.ReserveAnalysis(context.Background(), rin)
	var insf *wu.ErrInsufficientMana
	if !errors.As(err, &insf) {
		t.Fatalf("want *weakness_upload.ErrInsufficientMana, got %v", err)
	}
	if insf.RequiredUnits != 50 || insf.CurrentBalance != 12 {
		t.Errorf("shortfall fields lost: %+v", insf)
	}
}

func TestWeaknessManaReserver_TransportError_Wrapped(t *testing.T) {
	q := &fakeQuoter{err: errors.New("dial tcp: refused")}
	r := clients.NewWeaknessManaReserver(q)
	_, err := r.ReserveAnalysis(context.Background(), rin)
	if err == nil {
		t.Fatal("transport error must surface (never a silent free pass)")
	}
	var insf *wu.ErrInsufficientMana
	if errors.As(err, &insf) {
		t.Error("a generic transport error must NOT be classified as insufficient mana")
	}
}

// --- CompanionPresenceAdapter -------------------------------------------------

func TestCompanionPresence_HasActive_TrueWhenAny(t *testing.T) {
	p := clients.NewCompanionPresence(&fakeLister{insts: []*companion.Instance{{CompanionID: "f1"}}})
	has, err := p.HasActiveCompanion(context.Background(), "t-1", "g-1")
	if err != nil || !has {
		t.Fatalf("has=%v err=%v want true,nil", has, err)
	}
}

func TestCompanionPresence_HasActive_FalseWhenNone(t *testing.T) {
	p := clients.NewCompanionPresence(&fakeLister{insts: nil})
	has, err := p.HasActiveCompanion(context.Background(), "t-1", "g-1")
	if err != nil || has {
		t.Fatalf("has=%v err=%v want false,nil", has, err)
	}
}

func TestCompanionPresence_ErrorSurfaces(t *testing.T) {
	p := clients.NewCompanionPresence(&fakeLister{err: errors.New("rls: tenant_id missing")})
	_, err := p.HasActiveCompanion(context.Background(), "t-1", "g-1")
	if err == nil {
		t.Fatal("lister error must surface (fail-loud; never assume no Companion)")
	}
}
