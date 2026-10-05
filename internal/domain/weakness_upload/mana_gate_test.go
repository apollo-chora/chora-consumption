package weakness_upload_test

import (
	"context"
	"errors"
	"testing"

	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

type fakePresence struct {
	has   bool
	err   error
	calls int
}

func (f *fakePresence) HasActiveCompanion(_ context.Context, _, _ string) (bool, error) {
	f.calls++
	return f.has, f.err
}

type fakeReserver struct {
	res   wu.ReserveResult
	err   error
	got   wu.ReserveInput
	calls int
}

func (f *fakeReserver) ReserveAnalysis(_ context.Context, in wu.ReserveInput) (wu.ReserveResult, error) {
	f.calls++
	f.got = in
	return f.res, f.err
}

var sampleIn = wu.ReserveInput{TenantID: "t-1", GCID: "g-1", UploadID: "u-1"}

// Disabled gate (default / dark): no-op, no port calls, empty reservation —
// preserves today's free behaviour.
func TestManaGate_Disabled_IsNoOp(t *testing.T) {
	fam := &fakePresence{has: true}
	res := &fakeReserver{res: wu.ReserveResult{ReservationID: "r", PriceUnits: 50}}
	g := wu.NewManaGate(false, fam, res)

	got, err := g.Reserve(context.Background(), sampleIn)
	if err != nil {
		t.Fatalf("disabled gate must not error: %v", err)
	}
	if got.ReservationID != "" || got.PriceUnits != 0 {
		t.Errorf("disabled gate must return empty result, got %+v", got)
	}
	if fam.calls != 0 || res.calls != 0 {
		t.Errorf("disabled gate must not call ports (fam=%d res=%d)", fam.calls, res.calls)
	}
}

// Nil gate is a safe no-op (ExtServer field unwired ⇒ free path).
func TestManaGate_Nil_IsNoOp(t *testing.T) {
	var g *wu.ManaGate
	got, err := g.Reserve(context.Background(), sampleIn)
	if err != nil || got.ReservationID != "" {
		t.Fatalf("nil gate must be a no-op, got %+v err=%v", got, err)
	}
}

// Enabled + active Companion + affordable ⇒ reserves and returns the handle +
// price; the reserver receives the exact (tenant, gcid, upload_id) so its
// idempotency key is derivable.
func TestManaGate_Enabled_ReservesAndReturnsHandle(t *testing.T) {
	fam := &fakePresence{has: true}
	res := &fakeReserver{res: wu.ReserveResult{ReservationID: "rsv-1", PriceUnits: 50}}
	g := wu.NewManaGate(true, fam, res)

	got, err := g.Reserve(context.Background(), sampleIn)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.ReservationID != "rsv-1" || got.PriceUnits != 50 {
		t.Errorf("got %+v want {rsv-1 50}", got)
	}
	if fam.calls != 1 || res.calls != 1 {
		t.Errorf("ports calls: fam=%d res=%d want 1,1", fam.calls, res.calls)
	}
	if res.got != sampleIn {
		t.Errorf("reserver got %+v want %+v (idempotency key derives from these)", res.got, sampleIn)
	}
}

// Enabled + no active Companion ⇒ fail-loud ErrCompanionRequired, NO reserve
// (premium = Companion-gated; the free path is the auto-derived edges, not here).
func TestManaGate_Enabled_NoCompanion_Refuses(t *testing.T) {
	fam := &fakePresence{has: false}
	res := &fakeReserver{}
	g := wu.NewManaGate(true, fam, res)

	_, err := g.Reserve(context.Background(), sampleIn)
	if !errors.Is(err, wu.ErrCompanionRequired) {
		t.Fatalf("want ErrCompanionRequired, got %v", err)
	}
	if res.calls != 0 {
		t.Errorf("must NOT reserve when no Companion (reserve calls=%d)", res.calls)
	}
}

// Enabled + insufficient mana ⇒ the typed shortfall propagates (handler → 402);
// no reservation handle leaks.
func TestManaGate_Enabled_InsufficientMana_Propagates(t *testing.T) {
	fam := &fakePresence{has: true}
	res := &fakeReserver{err: &wu.ErrInsufficientMana{RequiredUnits: 50, CurrentBalance: 10}}
	g := wu.NewManaGate(true, fam, res)

	got, err := g.Reserve(context.Background(), sampleIn)
	var insf *wu.ErrInsufficientMana
	if !errors.As(err, &insf) {
		t.Fatalf("want *ErrInsufficientMana, got %v", err)
	}
	if insf.RequiredUnits != 50 || insf.CurrentBalance != 10 {
		t.Errorf("shortfall fields lost: %+v", insf)
	}
	if got.ReservationID != "" {
		t.Errorf("no reservation must leak on shortfall, got %q", got.ReservationID)
	}
}

// Enabled + companion-check transport error ⇒ wrapped, reserve NOT attempted.
func TestManaGate_Enabled_CompanionCheckError_Wraps(t *testing.T) {
	fam := &fakePresence{err: errors.New("boom")}
	res := &fakeReserver{}
	g := wu.NewManaGate(true, fam, res)

	_, err := g.Reserve(context.Background(), sampleIn)
	if err == nil || res.calls != 0 {
		t.Fatalf("companion-check error must abort before reserve (err=%v reserve=%d)", err, res.calls)
	}
}

// Enabled but ports unwired ⇒ fail-loud ErrManaNotConfigured (never a silent
// free pass).
func TestManaGate_Enabled_UnwiredPorts_FailLoud(t *testing.T) {
	g := wu.NewManaGate(true, nil, nil)
	_, err := g.Reserve(context.Background(), sampleIn)
	if !errors.Is(err, wu.ErrManaNotConfigured) {
		t.Fatalf("want ErrManaNotConfigured, got %v", err)
	}
}

// Enabled + reserve succeeds but yields an empty handle ⇒ fail-loud (the crew
// cannot settle/refund an empty reservation → never strand mana).
func TestManaGate_Enabled_EmptyHandle_FailLoud(t *testing.T) {
	fam := &fakePresence{has: true}
	res := &fakeReserver{res: wu.ReserveResult{ReservationID: "", PriceUnits: 50}}
	g := wu.NewManaGate(true, fam, res)

	_, err := g.Reserve(context.Background(), sampleIn)
	if err == nil {
		t.Fatal("empty reservation handle must fail loud")
	}
	if errors.Is(err, wu.ErrCompanionRequired) || errors.Is(err, wu.ErrManaNotConfigured) {
		t.Errorf("wrong sentinel for empty-handle: %v", err)
	}
}
