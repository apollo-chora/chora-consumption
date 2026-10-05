// proofing_mana_reserver.go — CHO-2040 (spec §3): the proofing-test runner's
// reserve→settle/refund economy adapter.
//
// Satisfies proofingtest.ManaReserver over the SAME chora-identity ManaService
// client the daily-dose / weakness flows use, mirroring
// WeaknessManaReserver's mechanics:
//
//   - Reserve = pre-flight DeductMana(units=0): the identity server resolves
//     the price via the ADR-178 price-plan resolver (tenant overrides win)
//     against `proofing_test_gen` (seeded in the matching chora_identity
//     migration). The DETERMINISTIC idempotency key —
//     action:tenant:gcid:proofing_test_id (spec §3's gcid+tenant+goal+nonce
//     shape with the aggregate id as the nonce) — doubles as the reservation
//     handle, so replays never double-reserve.
//   - Refund = CreditMana(source=REFUND) against "<handle>:refund" — a failed
//     publish or a crew refusal returns the reservation idempotently.
//
// Hexagonal: the proofingtest domain owns the port; this adapter depends on
// the domain, never the reverse.
package clients

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

// proofingManaBackend is the narrow client slice the reserver drives (both
// methods live on *ManaClient; tests inject a fake).
type proofingManaBackend interface {
	DeductMana(ctx context.Context, in companion.DeductManaInput) error
	CreditMana(ctx context.Context, in CreditManaInput) error
}

// ProofingManaReserver implements proofingtest.ManaReserver.
type ProofingManaReserver struct {
	backend proofingManaBackend
}

// NewProofingManaReserver wires the reserver. A nil backend is tolerated at
// construction (fail-soft at boot) — every call then fails loud.
func NewProofingManaReserver(backend proofingManaBackend) *ProofingManaReserver {
	return &ProofingManaReserver{backend: backend}
}

// Compile-time check.
var _ pt.ManaReserver = (*ProofingManaReserver)(nil)

// proofingReservationKey derives the deterministic reservation handle / debit
// idempotency key. The action prefix namespaces it inside the per-GCID
// ManaService dedup space (mirrors weaknessReservationKey).
func proofingReservationKey(in pt.ReserveInput) string {
	return companion.ActionProofingTestGen + ":" + in.TenantID + ":" + in.GCID + ":" + in.ProofingTestID
}

// Reserve debits (reserves) the proofing-test price. On a 402 from
// chora-identity the typed *companion.ErrInsufficientMana translates to
// *proofingtest.ErrInsufficientMana so the handler renders the canonical 402
// envelope without the domain importing companion. Any other error surfaces
// (fail-loud — a paid action never fails open).
func (r *ProofingManaReserver) Reserve(ctx context.Context, in pt.ReserveInput) (pt.ReserveResult, error) {
	if r == nil || r.backend == nil {
		return pt.ReserveResult{}, errors.New("proofing_mana: nil backend")
	}
	idemKey := proofingReservationKey(in)
	err := r.backend.DeductMana(ctx, companion.DeductManaInput{
		GCID:           in.GCID,
		ActionCode:     companion.ActionProofingTestGen,
		Units:          0, // server resolves the price (price-plan resolver, tenant-aware)
		IdempotencyKey: idemKey,
		TenantID:       in.TenantID,
	})
	if err != nil {
		var insf *companion.ErrInsufficientMana
		if errors.As(err, &insf) {
			return pt.ReserveResult{}, &pt.ErrInsufficientMana{
				RequiredUnits:  insf.RequiredUnits,
				CurrentBalance: insf.CurrentBalance,
			}
		}
		return pt.ReserveResult{}, fmt.Errorf("proofing_mana: reserve: %w", err)
	}

	// Displayed price: the canonical map value (the identity success response
	// does not echo the resolved cost; a tenant override changes only the
	// displayed-vs-charged number — the reserve already succeeded
	// server-side). A miss is a programming error, never a free pass.
	price, lerr := companion.LookupCost(companion.ActionProofingTestGen)
	if lerr != nil {
		return pt.ReserveResult{}, fmt.Errorf("proofing_mana: price lookup: %w", lerr)
	}
	return pt.ReserveResult{ReservationID: idemKey, PriceUnits: price}, nil
}

// Refund returns a reservation. Zero units is a nil no-op (nothing was
// displayed as reserved — e.g. a tenant zero-priced the action); otherwise the
// credit is idempotent on "<reservation>:refund" so terminal-event redelivery
// never double-refunds.
func (r *ProofingManaReserver) Refund(ctx context.Context, in pt.RefundInput) error {
	if r == nil || r.backend == nil {
		return errors.New("proofing_mana: nil backend")
	}
	if in.Units == 0 {
		return nil
	}
	if err := r.backend.CreditMana(ctx, CreditManaInput{
		GCID:           in.GCID,
		Units:          in.Units,
		IdempotencyKey: in.ReservationID + ":refund",
		Reason:         in.Reason,
	}); err != nil {
		return fmt.Errorf("proofing_mana: refund: %w", err)
	}
	return nil
}
