// BE-USR-2 — chora-identity ManaService gRPC client adapter.
//
// Hexagonal port: this adapter implements companion.ManaQuoter so the Companion
// domain can pre-flight a debit (DeductMana) and read balance (GetBalance)
// before any LLM-backed action without knowing the transport.
//
// Transport: gRPC against the generated identityv1.ManaServiceClient
// (chora-contracts/proto/services/identity/v1/mana_service.proto). This REPLACES
// the earlier REST shim against /api/v1/internal/mana/{deduct,balance} — those
// endpoints were never served by chora-identity (only the gRPC ManaService +
// the learner-facing REST /api/v1/me/mana exist), so the REST client failed
// every premium debit and silently degraded the daily-dose coach to fail-open.
// The gRPC ManaService is registered in chora-identity cmd/server (port :9090,
// appProtocol grpc) and is the canonical inter-service path (Tier 2 D5). The
// consumption→identity hop is allowlisted by the mesh AuthorizationPolicy
// allow-mana-grpc (namespace `consumption`).
//
// Dial shape mirrors content_retrieval_client.go: insecure transport
// credentials (Cloud Service Mesh injects mTLS), mesh-DNS target from env
// CHORA_IDENTITY_MANA_GRPC_URL (e.g. "chora-identity.identity.svc.cluster.local:9090").
//
// Per ADR-142 §8 the caller (ComposeDailyDoseWithMana / HTTP handler /
// WeaknessManaReserver) decides fail-open vs fail-closed on adapter errors.
// DeductMana returns a typed *companion.ErrInsufficientMana when the server
// reports success=false and a generic wrapped error on any transport/RPC
// failure.
//
// Per feedback_no_inline_config, the gRPC target MUST be supplied at
// construction time (read in router.go boot wiring). An empty target fails
// loud via ErrManaEmptyBaseURL.
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ErrManaEmptyBaseURL is returned by the constructor when the gRPC target is
// empty, and by the methods when the client is unconfigured. Per
// feedback_no_inline_config, missing config MUST fail loudly rather than
// silently default to a no-op (which would hand out free LLM actions).
var ErrManaEmptyBaseURL = errors.New("mana_client: target empty (set CHORA_IDENTITY_MANA_GRPC_URL)")

// manaDefaultTimeout bounds one ManaService round-trip when the caller passes a
// non-positive timeout.
const manaDefaultTimeout = 5 * time.Second

// manaServiceClient is the minimal slice of identityv1.ManaServiceClient the
// adapter calls (DeductMana + GetBalance + CreditMana). The generated client
// satisfies it; tests inject a fake. CreditMana joined for the CHO-2040
// proofing-test reserve→refund lane (mana_client_credit.go) — it is exposed on
// the concrete *ManaClient only, NOT on the companion.ManaQuoter port (the
// invoke/dose surfaces stay debit-only per ADR-177).
type manaServiceClient interface {
	DeductMana(ctx context.Context, in *identityv1.DeductManaRequest, opts ...grpc.CallOption) (*identityv1.DeductManaResponse, error)
	GetBalance(ctx context.Context, in *identityv1.GetBalanceRequest, opts ...grpc.CallOption) (*identityv1.GetBalanceResponse, error)
	CreditMana(ctx context.Context, in *identityv1.CreditManaRequest, opts ...grpc.CallOption) (*identityv1.CreditManaResponse, error)
}

// ManaClient calls chora-identity ManaService over gRPC. The public surface
// implements companion.ManaQuoter.
type ManaClient struct {
	client  manaServiceClient
	timeout time.Duration
}

// NewManaClient dials the chora-identity ManaService at the mesh target and
// returns a ManaQuoter. An empty target fails loud (ErrManaEmptyBaseURL); a
// non-positive timeout falls back to manaDefaultTimeout. grpc.NewClient is lazy
// (no eager connection) so a healthy return does not guarantee reachability —
// the first RPC surfaces an Unavailable error, which the caller handles.
func NewManaClient(target string, timeout time.Duration) (*ManaClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrManaEmptyBaseURL
	}
	if timeout <= 0 {
		timeout = manaDefaultTimeout
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("mana_client: dial %q: %w", target, err)
	}
	return &ManaClient{client: identityv1.NewManaServiceClient(conn), timeout: timeout}, nil
}

// NewManaClientFromStub injects a fake gRPC client (tests). A non-positive
// timeout falls back to manaDefaultTimeout.
func NewManaClientFromStub(stub manaServiceClient, timeout time.Duration) *ManaClient {
	if timeout <= 0 {
		timeout = manaDefaultTimeout
	}
	return &ManaClient{client: stub, timeout: timeout}
}

// Compile-time check.
var _ companion.ManaQuoter = (*ManaClient)(nil)

// DeductMana implements companion.ManaQuoter. Returns:
//   - nil on success
//   - *companion.ErrInsufficientMana when the server reports success=false
//     (errors.Is(err, companion.ErrInsufficientManaSentinel) matches)
//   - a generic wrapped error on any transport/RPC failure
func (c *ManaClient) DeductMana(ctx context.Context, in companion.DeductManaInput) error {
	if c == nil || c.client == nil {
		return ErrManaEmptyBaseURL
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.DeductMana(callCtx, &identityv1.DeductManaRequest{
		Gcid:           in.GCID,
		ActionCode:     in.ActionCode,
		Units:          in.Units,
		IdempotencyKey: in.IdempotencyKey,
		TenantId:       in.TenantID,
		RequestId:      in.RequestID,
	})
	if err != nil {
		return fmt.Errorf("mana_client: DeductMana rpc: %w", err)
	}
	if !resp.GetSuccess() {
		return &companion.ErrInsufficientMana{
			RequiredUnits:  resp.GetRequiredUnits(),
			CurrentBalance: resp.GetCurrentBalanceUnits(),
			// SuggestedTopupID intentionally empty: the gRPC DeductManaResponse
			// contract carries no upsell topup id (the upsell hint is a price-
			// plan / marketplace concern surfaced separately by the FE).
		}
	}
	return nil
}

// GetBalance implements companion.ManaQuoter. Returns the user's current balance
// + lifetime totals + subsidy breakdown for UI cost-preview UX.
func (c *ManaClient) GetBalance(ctx context.Context, gcid string) (companion.BalanceSnapshot, error) {
	if c == nil || c.client == nil {
		return companion.BalanceSnapshot{}, ErrManaEmptyBaseURL
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.GetBalance(callCtx, &identityv1.GetBalanceRequest{Gcid: gcid})
	if err != nil {
		return companion.BalanceSnapshot{}, fmt.Errorf("mana_client: GetBalance rpc: %w", err)
	}

	out := companion.BalanceSnapshot{
		BalanceUnits:   resp.GetBalanceUnits(),
		LifetimeEarned: resp.GetLifetimeEarned(),
		LifetimeSpent:  resp.GetLifetimeSpent(),
	}
	for _, s := range resp.GetSubsidyBreakdown() {
		slice := companion.SubsidySlice{
			Source:             protoManaSourceToCompanion(s.GetSource()),
			Units:              s.GetUnits(),
			TenantID:           s.GetTenantId(),
			SourceAllocationID: s.GetSourceAllocationId(),
		}
		if ts := s.GetExpiresAt(); ts != nil {
			slice.ExpiresAtUnixNano = ts.AsTime().UnixNano()
		}
		out.SubsidyBreakdown = append(out.SubsidyBreakdown, slice)
	}
	if ts := resp.GetLastCreditedAt(); ts != nil {
		out.LastCreditedAtUnixNano = ts.AsTime().UnixNano()
	}
	return out, nil
}

// protoManaSourceToCompanion maps the proto ManaSource enum to the domain
// companion.ManaSource string (inverse of the server-side stringSourceToProto).
func protoManaSourceToCompanion(s identityv1.ManaSource) companion.ManaSource {
	switch s {
	case identityv1.ManaSource_MANA_SOURCE_SUBSCRIPTION_GRANT:
		return companion.ManaSourceSubscriptionGrant
	case identityv1.ManaSource_MANA_SOURCE_TOPUP:
		return companion.ManaSourceTopup
	case identityv1.ManaSource_MANA_SOURCE_PROMO:
		return companion.ManaSourcePromo
	case identityv1.ManaSource_MANA_SOURCE_TENANT_SUBSIDY:
		return companion.ManaSourceTenantSubsidy
	case identityv1.ManaSource_MANA_SOURCE_REFUND:
		return companion.ManaSourceRefund
	case identityv1.ManaSource_MANA_SOURCE_ROLLOVER:
		return companion.ManaSourceRollover
	case identityv1.ManaSource_MANA_SOURCE_MINT:
		return companion.ManaSourceMint
	}
	return companion.ManaSourceUnspecified
}
