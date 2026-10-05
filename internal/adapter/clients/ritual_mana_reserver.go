// ritual_mana_reserver.go — CHO-2016 G4: the Ritual runner's reserve→refund
// economy adapter (satisfies companion.RitualManaReserver over the SAME
// chora-identity ManaService client the dose / proofing / weakness flows use).
//
// Mirrors ProofingManaReserver, with ONE deliberate difference: a Ritual run's
// price is COMPOSED-FLAT per ritual (published_price_units), NOT a fixed
// action-code price — so Reserve passes the caller's EXPLICIT units (DeductMana
// applies the quote as-is when units>0). The companion_ritual_run action-code
// row still exists in mana_action_pricing (chora-identity seed migration) so
// the debit validates.
//
//   - Reserve = DeductMana(units=published_price_units) with the deterministic
//     idempotency key companion_ritual_run:{tenant}:{gcid}:{run_id} — the handle
//     doubles as the reservation id (replays never double-reserve). A 402
//     surfaces as the typed *companion.ErrInsufficientMana, which errors.Is-
//     matches ErrInsufficientManaSentinel so the runner maps it to
//     skipped_budget.
//   - Refund = CreditMana(source=REFUND) against "<handle>:refund".
package clients

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ritualManaBackend is the narrow client slice the reserver drives (both
// methods live on *ManaClient; tests inject a fake).
type ritualManaBackend interface {
	DeductMana(ctx context.Context, in companion.DeductManaInput) error
	CreditMana(ctx context.Context, in CreditManaInput) error
}

// RitualManaReserver implements companion.RitualManaReserver.
type RitualManaReserver struct {
	backend ritualManaBackend
}

// NewRitualManaReserver wires the reserver. A nil backend is tolerated at
// construction (fail-soft at boot) — every call then fails loud.
func NewRitualManaReserver(backend ritualManaBackend) *RitualManaReserver {
	return &RitualManaReserver{backend: backend}
}

// Compile-time check.
var _ companion.RitualManaReserver = (*RitualManaReserver)(nil)

// ritualReservationKey derives the deterministic reservation handle / debit
// idempotency key, namespaced inside the per-GCID ManaService dedup space.
// Shape: {action_code}:{gcid}:{run_id}. The tenant is intentionally omitted —
// dedup is already per-GCID and run_id is a globally-unique UUIDv7, so tenant
// was redundant AND pushed the key past the identity ledger's varchar(128)
// (owner+tenant+gcid+run_id = 130 chars → SQLSTATE 22001 on DeductMana).
func ritualReservationKey(in companion.RitualReserveInput) string {
	return companion.ActionCodeRitualRun + ":" + in.GCID + ":" + in.RunID
}

// Reserve debits the composed-flat ritual price up-front. On a 402 the typed
// *companion.ErrInsufficientMana propagates unchanged (it errors.Is-matches
// ErrInsufficientManaSentinel → the runner records skipped_budget). Any other
// error surfaces (fail-loud — a paid action never fails open).
func (r *RitualManaReserver) Reserve(ctx context.Context, in companion.RitualReserveInput) (companion.RitualReserveResult, error) {
	if r == nil || r.backend == nil {
		return companion.RitualReserveResult{}, errors.New("ritual_mana: nil backend")
	}
	idemKey := ritualReservationKey(in)
	err := r.backend.DeductMana(ctx, companion.DeductManaInput{
		GCID:           in.GCID,
		ActionCode:     companion.ActionCodeRitualRun,
		Units:          int64(in.Units), // caller's composed quote (dynamic per-ritual)
		IdempotencyKey: idemKey,
		TenantID:       in.TenantID,
	})
	if err != nil {
		var insf *companion.ErrInsufficientMana
		if errors.As(err, &insf) {
			// Propagate the typed error unchanged — errors.Is(ErrInsufficientManaSentinel)
			// matches, and errors.As recovers the upsell numbers.
			return companion.RitualReserveResult{}, insf
		}
		return companion.RitualReserveResult{}, fmt.Errorf("ritual_mana: reserve: %w", err)
	}
	return companion.RitualReserveResult{ReservationID: idemKey, PriceUnits: in.Units}, nil
}

// Refund returns a reservation on a failed/blocked/swept run. Zero units is a
// nil no-op; otherwise the credit is idempotent on "<reservation>:refund" so
// terminal-event redelivery never double-refunds.
func (r *RitualManaReserver) Refund(ctx context.Context, in companion.RitualRefundInput) error {
	if r == nil || r.backend == nil {
		return errors.New("ritual_mana: nil backend")
	}
	if in.Units <= 0 {
		return nil
	}
	if err := r.backend.CreditMana(ctx, CreditManaInput{
		GCID:           in.GCID,
		Units:          int64(in.Units),
		IdempotencyKey: in.ReservationID + ":refund",
		Reason:         in.Reason,
	}); err != nil {
		return fmt.Errorf("ritual_mana: refund: %w", err)
	}
	return nil
}
