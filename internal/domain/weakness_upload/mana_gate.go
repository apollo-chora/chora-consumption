// mana_gate.go — WS-4 economy gate for the Growth-Edge upload door (ADR-205 D6
// / ADR-178 / CHO-1956).
//
// The learner-initiated upload is the PREMIUM weakness path: it requires an
// active Companion + mana, and reserves mana on accept (reserve-on-accept). The
// ai-kernel weakness-analyser crew settles the reservation on success and
// refunds it on any fail-loud / BLOCK node — keyed by the reservation handle
// (idempotency key = gcid+tenant+upload_id). The auto-derived path
// (graded-assessment / Ebbinghaus edges) is a DIFFERENT code path that stays
// FREE and never reaches this gate.
//
// DARK by default: the gate is constructed with enabled=false (the upload door
// is wired with a nil/disabled gate) until the owner cuts the live crew over to
// the graduated graph (WEAKNESS_CREW_MODE=graph). Reserving upfront while the
// live crew is still single-shot (no refund) would STRAND reservations on a
// failed analysis, so the enable flag is coupled to the crew cutover — see
// cmd/server wiring + the WEAKNESS_UPFRONT_MANA_ENABLED env flag.
//
// Hexagonal: this is pure domain. It depends only on the two driven ports below
// (CompanionPresence, ManaReserver); the adapters that satisfy them live in
// internal/adapter/clients and wrap the companion.InstanceRepository +
// companion.ManaQuoter respectively.
package weakness_upload

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors mapped to HTTP status by the upload handler.
var (
	// ErrCompanionRequired — the premium upload-analysis requires an active
	// Companion (ADR-205: premium = Companion+mana gated). The handler maps this
	// to 402 Payment Required (the learner must summon/activate a Companion). The
	// free path is the auto-derived edges, not this door.
	ErrCompanionRequired = errors.New("weakness_upload: premium analysis requires an active Companion")

	// ErrManaNotConfigured — the upfront-mana gate is enabled but a required
	// port (Companion presence / mana reserver) is not wired. A misconfiguration
	// must fail loud (the handler maps it to 503), never a silent free pass.
	ErrManaNotConfigured = errors.New("weakness_upload: upfront mana enabled but a required port is not wired")
)

// ErrInsufficientMana is the typed shortfall raised when the learner's wallet
// cannot cover the reserved weakness analysis. The adapter translates the
// chora-identity 402 into this so the handler can produce a 402 with upsell
// context without the domain importing the companion package.
type ErrInsufficientMana struct {
	RequiredUnits  int64
	CurrentBalance int64
}

// Error implements error.
func (e *ErrInsufficientMana) Error() string {
	return fmt.Sprintf("weakness_upload: insufficient mana: required=%d current=%d", e.RequiredUnits, e.CurrentBalance)
}

// ReserveInput is the per-upload reservation key. The reserver derives its
// idempotency key from (TenantID, GCID, UploadID) so a re-published upload
// replays without double-reserving.
type ReserveInput struct {
	TenantID string
	GCID     string
	UploadID string
}

// ReserveResult is the outcome of a successful reserve. ReservationID is stamped
// onto WeaknessDocUploaded.reservation_id (proto field 12); PriceUnits is the
// resolved mana cost exposed in the upload response DTO (price-shown-upfront).
type ReserveResult struct {
	ReservationID string
	PriceUnits    int64
}

// CompanionPresence reports whether a learner owns an active (non-deleted)
// Companion. The adapter wraps companion.InstanceRepository.ListByOwner.
type CompanionPresence interface {
	HasActiveCompanion(ctx context.Context, tenantID, gcid string) (bool, error)
}

// ManaReserver reserves mana for a premium weakness analysis at the upload door
// (reserve-on-accept). It MUST be idempotent on (TenantID, GCID, UploadID): a
// re-issued reserve replays without double-charging. Returns *ErrInsufficientMana
// when the wallet cannot cover the action.
type ManaReserver interface {
	ReserveAnalysis(ctx context.Context, in ReserveInput) (ReserveResult, error)
}

// ManaGate orchestrates the upload-door economy: Companion gate → price resolve →
// mana reserve, all behind the enabled flag. When disabled it is a transparent
// no-op (today's free behaviour, empty reservation).
type ManaGate struct {
	enabled    bool
	companions CompanionPresence
	reserver   ManaReserver
}

// NewManaGate constructs the gate. enabled=false (or a nil gate) makes Reserve a
// no-op; when enabled BOTH ports must be non-nil or Reserve fails loud with
// ErrManaNotConfigured.
func NewManaGate(enabled bool, companions CompanionPresence, reserver ManaReserver) *ManaGate {
	return &ManaGate{enabled: enabled, companions: companions, reserver: reserver}
}

// Enabled reports whether the upfront-mana gate is active.
func (g *ManaGate) Enabled() bool { return g != nil && g.enabled }

// Reserve runs the upload-door economy gate.
//
//   - nil gate OR disabled → no-op: empty ReserveResult, no port calls, no
//     reservation (preserves today's free behaviour; reservation_id stays empty).
//   - enabled but a port is nil → ErrManaNotConfigured (fail-loud misconfig).
//   - enabled: the learner MUST own an active Companion (else ErrCompanionRequired)
//     and afford the resolved price (else *ErrInsufficientMana from the reserver);
//     on success the mana is reserved and the handle + price returned. A reserve
//     that succeeds with an empty handle fails loud — the crew cannot
//     settle/refund an empty reservation, so we never strand mana.
func (g *ManaGate) Reserve(ctx context.Context, in ReserveInput) (ReserveResult, error) {
	if g == nil || !g.enabled {
		return ReserveResult{}, nil
	}
	if g.companions == nil || g.reserver == nil {
		return ReserveResult{}, ErrManaNotConfigured
	}

	has, err := g.companions.HasActiveCompanion(ctx, in.TenantID, in.GCID)
	if err != nil {
		return ReserveResult{}, fmt.Errorf("weakness_upload: companion presence check: %w", err)
	}
	if !has {
		return ReserveResult{}, ErrCompanionRequired
	}

	res, err := g.reserver.ReserveAnalysis(ctx, in)
	if err != nil {
		// *ErrInsufficientMana and transport errors bubble untouched — the
		// handler maps them. Never swallow into a free pass.
		return ReserveResult{}, err
	}
	if strings.TrimSpace(res.ReservationID) == "" {
		return ReserveResult{}, errors.New("weakness_upload: reserve returned an empty reservation handle (cannot settle/refund) — refusing to strand mana")
	}
	return res, nil
}
