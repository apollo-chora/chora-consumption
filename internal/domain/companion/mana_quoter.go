// BE-USR-2 — ManaQuoter port + ErrInsufficientMana.
//
// The Companion domain consumes mana via this hexagonal port. The
// production adapter (in internal/adapter/clients/mana_client.go) speaks
// gRPC to the chora-identity ManaService per the proto contract at
// chora-contracts/proto/services/identity/v1/mana_service.proto.
//
// Per ADR-142 (per-user economy), the spend order is FIFO:
//  1. TenantManaAllocation (subsidy) drained first by `expires_at ASC NULLS LAST`
//  2. Then personal UserMana balance
//
// The domain does NOT know which slice a debit comes from — that's an
// adapter / chora-identity concern. From the consumer's perspective:
// either the debit succeeds (ledger entries written) or ErrInsufficientMana
// is returned with upsell context.
//
// Hexagonal note: NEVER import the adapter package from this file. The
// HTTP server (router.go) injects a ManaQuoter at construction time.
//
// Trace context: the caller's context.Context flows through to the gRPC
// adapter, which attaches W3C traceparent metadata. Test fakes just
// receive the ctx untouched.
package companion

import (
	"context"
	"errors"
	"fmt"
)

// ErrInsufficientManaSentinel is the sentinel returned when balance is
// short. Callers use errors.Is(err, ErrInsufficientManaSentinel) to branch
// without a type assertion. Use *ErrInsufficientMana to read the upsell
// fields.
var ErrInsufficientManaSentinel = errors.New("insufficient mana")

// ErrUnknownActionCode is returned by LookupCost when the action_code
// is not in the canonical map. See cost_map.go.
var ErrUnknownActionCode = errors.New("unknown mana action code")

// ErrInsufficientMana is the typed error returned by ManaQuoter.DeductMana
// when the user's wallet (subsidy + personal) has fewer units than the
// action requires. Carries the upsell context the HTTP layer needs to
// produce a 402 Payment Required response with a marketplace link.
type ErrInsufficientMana struct {
	// RequiredUnits is the action's mana cost (after action_code lookup).
	RequiredUnits int64
	// CurrentBalance is the wallet total at debit time (subsidy + personal).
	CurrentBalance int64
	// SuggestedTopupID is an optional pricing-config hint for the marketplace
	// upsell (e.g., "companion_basic_topup_500"). Empty when no recommendation.
	SuggestedTopupID string
}

// Error implements the error interface.
func (e *ErrInsufficientMana) Error() string {
	return fmt.Sprintf("insufficient mana: required=%d current=%d (suggested_topup=%q)",
		e.RequiredUnits, e.CurrentBalance, e.SuggestedTopupID)
}

// Is wires errors.Is(err, ErrInsufficientManaSentinel) for clean caller
// branching without a type assertion.
func (e *ErrInsufficientMana) Is(target error) bool {
	return target == ErrInsufficientManaSentinel
}

// ManaSource mirrors the proto enum on chora.services.identity.v1.ManaSource.
// Stable string values for HTTP/UI rendering.
type ManaSource string

// ManaSource enum values.
const (
	ManaSourceUnspecified       ManaSource = ""
	ManaSourceSubscriptionGrant ManaSource = "subscription_grant"
	ManaSourceTopup             ManaSource = "topup"
	ManaSourcePromo             ManaSource = "promo"
	ManaSourceTenantSubsidy     ManaSource = "tenant_subsidy"
	ManaSourceRefund            ManaSource = "refund"
	ManaSourceRollover          ManaSource = "rollover"
	ManaSourceMint              ManaSource = "mint"
)

// SubsidySlice is a per-source slice of the user's spendable balance.
// Mirrors chora.services.identity.v1.SubsidySlice; ordered by FIFO
// spend priority (per ADR-142 §6).
type SubsidySlice struct {
	Source             ManaSource
	Units              int64
	TenantID           string
	SourceAllocationID string
	// ExpiresAtUnixNano is 0 when no expiry is set. Drained ASC NULLS LAST
	// per ADR-142 (i.e., expiring slices first; 0 last).
	ExpiresAtUnixNano int64
}

// BalanceSnapshot is the read-side projection of the user's wallet at a
// point in time. Fed into UI cost-preview and CostBefore tests.
type BalanceSnapshot struct {
	BalanceUnits     int64
	LifetimeEarned   int64
	LifetimeSpent    int64
	SubsidyBreakdown []SubsidySlice
	// LastCreditedAtUnixNano is 0 when the wallet has never been credited.
	LastCreditedAtUnixNano int64
}

// DeductManaInput is the per-call payload sent to the port. Mirrors the
// proto DeductManaRequest message.
//
// IdempotencyKey is mandatory; the chora-identity ManaService dedups on
// (gcid, idempotency_key) so retries with the same key replay without
// re-debiting.
type DeductManaInput struct {
	GCID       string
	ActionCode string
	// Units is OPTIONAL. When 0, chora-identity resolves cost from the
	// action_code lookup. When > 0, the caller's quote is applied as-is
	// (used by adapters that already resolved the cost client-side).
	Units          int64
	IdempotencyKey string
	TenantID       string
	RequestID      string
}

// ManaQuoter is the hexagonal port the Companion domain calls before any
// LLM-backed action. The production adapter in internal/adapter/clients
// wraps a gRPC client to chora-identity.
//
// Two methods:
//   - DeductMana — pre-flight debit. Returns ErrInsufficientMana when
//     the wallet (subsidy + personal) cannot cover the action.
//   - GetBalance — cheap read for UI cost-preview UX and tests.
type ManaQuoter interface {
	DeductMana(ctx context.Context, in DeductManaInput) error
	GetBalance(ctx context.Context, gcid string) (BalanceSnapshot, error)
}
