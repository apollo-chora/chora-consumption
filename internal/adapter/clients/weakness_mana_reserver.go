// weakness_mana_reserver.go — WS-4 (CHO-1956) adapters for the Growth-Edge
// upload-door economy gate (ADR-205 D6).
//
// Two driven-port adapters satisfy the weakness_upload domain ports:
//
//   - WeaknessManaReserver implements weakness_upload.ManaReserver over the
//     existing companion.ManaQuoter (the same chora-identity ManaService client
//     the daily-dose coach debit uses — mirroring ComposeDailyDoseWithMana per
//     the WS-4 directive). The reserve is a pre-flight DeductMana(units=0): the
//     server resolves the price via the ADR-178 price-plan resolver (honouring
//     tenant overrides) against the action_code registered in chora_identity
//     migration 0028. The reservation handle == the debit idempotency key, so
//     the ai-kernel crew can settle/refund against it (idempotency key =
//     gcid+tenant+upload_id, per the weakness.proto reservation_id contract).
//
//   - CompanionPresenceAdapter implements weakness_upload.CompanionPresence over
//     the narrow lister slice of companion.InstanceRepository — an "active"
//     Companion is any non-deleted instance the learner owns (ListByOwner
//     excludes soft-deleted).
//
// Hexagonal: domain (weakness_upload) owns the ports; this adapter depends on
// the domain, never the reverse.
package clients

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// WeaknessManaReserver reserves mana for the premium weakness analysis via the
// chora-identity ManaService (companion.ManaQuoter port).
type WeaknessManaReserver struct {
	quoter companion.ManaQuoter
}

// NewWeaknessManaReserver wires the reserver to a ManaQuoter (the same client
// the daily-dose coach uses). The caller supplies a non-nil quoter; the gate's
// ErrManaNotConfigured covers the unwired case upstream.
func NewWeaknessManaReserver(quoter companion.ManaQuoter) *WeaknessManaReserver {
	return &WeaknessManaReserver{quoter: quoter}
}

// Compile-time check.
var _ wu.ManaReserver = (*WeaknessManaReserver)(nil)

// ReserveAnalysis debits (reserves) the weakness-analysis price. The
// idempotency key is deterministic on (tenant, gcid, upload_id) so a
// re-published upload replays without double-reserving; the same value is the
// returned reservation handle. On a 402 from chora-identity the typed
// *companion.ErrInsufficientMana is translated to *weakness_upload.ErrInsufficientMana
// so the handler can render a 402 without the domain importing companion.
func (r *WeaknessManaReserver) ReserveAnalysis(ctx context.Context, in wu.ReserveInput) (wu.ReserveResult, error) {
	if r == nil || r.quoter == nil {
		return wu.ReserveResult{}, errors.New("weakness_mana: nil quoter")
	}
	idemKey := weaknessReservationKey(in)
	err := r.quoter.DeductMana(ctx, companion.DeductManaInput{
		GCID:           in.GCID,
		ActionCode:     companion.ActionWeaknessAnalysis,
		Units:          0, // server resolves the price (price-plan resolver, tenant-aware)
		IdempotencyKey: idemKey,
		TenantID:       in.TenantID,
	})
	if err != nil {
		var insf *companion.ErrInsufficientMana
		if errors.As(err, &insf) {
			return wu.ReserveResult{}, &wu.ErrInsufficientMana{
				RequiredUnits:  insf.RequiredUnits,
				CurrentBalance: insf.CurrentBalance,
			}
		}
		return wu.ReserveResult{}, fmt.Errorf("weakness_mana: reserve: %w", err)
	}

	// Price shown upfront. The actual debit is server-authoritative (honours a
	// tenant price-plan override); the canonical fallback is the display value
	// (the chora-identity success response does not echo the resolved cost). A
	// tenant override changes only the displayed-vs-charged number, never
	// correctness — the reserve already succeeded server-side.
	price, lerr := companion.LookupCost(companion.ActionWeaknessAnalysis)
	if lerr != nil {
		// The code is registered in the canonical map; a miss is a programming
		// error, not a runtime free pass — surface it.
		return wu.ReserveResult{}, fmt.Errorf("weakness_mana: price lookup: %w", lerr)
	}
	return wu.ReserveResult{ReservationID: idemKey, PriceUnits: price}, nil
}

// weaknessReservationKey derives the deterministic reservation handle / debit
// idempotency key from (tenant, gcid, upload_id). Per the weakness.proto
// reservation_id contract the key is gcid+tenant+upload_id; the action prefix
// namespaces it within the per-GCID ManaService dedup space so it never
// collides with another action's idempotency key.
func weaknessReservationKey(in wu.ReserveInput) string {
	return companion.ActionWeaknessAnalysis + ":" + in.TenantID + ":" + in.GCID + ":" + in.UploadID
}

// --- CompanionPresenceAdapter -------------------------------------------------

// companionLister is the narrow slice of companion.InstanceRepository the presence
// adapter needs (interface segregation — the gate only asks "any active
// Companion?"). Both the pg and inmem instance repos satisfy it.
type companionLister interface {
	ListByOwner(ctx context.Context, tenantID, ownerGCID string) ([]*companion.Instance, error)
}

// CompanionPresenceAdapter implements weakness_upload.CompanionPresence.
type CompanionPresenceAdapter struct {
	lister companionLister
}

// NewCompanionPresence wraps an instance lister (production: the pg
// CompanionInstanceRepo; the RLS session rides the request ctx).
func NewCompanionPresence(lister companionLister) *CompanionPresenceAdapter {
	return &CompanionPresenceAdapter{lister: lister}
}

// Compile-time check.
var _ wu.CompanionPresence = (*CompanionPresenceAdapter)(nil)

// HasActiveCompanion reports whether the learner owns any non-deleted Companion.
// A lister error surfaces (fail-loud) — the gate must never assume "no
// Companion" on an infrastructure failure (that would refuse a paying learner).
func (a *CompanionPresenceAdapter) HasActiveCompanion(ctx context.Context, tenantID, gcid string) (bool, error) {
	if a == nil || a.lister == nil {
		return false, errors.New("weakness_mana: nil companion lister")
	}
	insts, err := a.lister.ListByOwner(ctx, tenantID, gcid)
	if err != nil {
		return false, fmt.Errorf("weakness_mana: list companions: %w", err)
	}
	return len(insts) > 0, nil
}
