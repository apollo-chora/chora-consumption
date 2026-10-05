// mana_client_credit.go — CHO-2040 (spec §3): the CreditMana (refund) surface
// of the chora-identity ManaService client.
//
// A SEPARATE file from mana_client.go on purpose (small blast radius while the
// deduct/balance surface is stable): the proofing-test composed runner needs a
// consumption-side refund lane — reserve at the door, refund on publish
// failure / crew refusal — and the existing companion.ManaQuoter port
// deliberately exposes only DeductMana + GetBalance. The refund rides the
// identity ManaService.CreditMana RPC with source=REFUND + reason=REFUND
// (matching the ai-kernel weakness-analyser crew's refund convention).
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identityv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/identity/v1"
)

// CreditManaInput is the consumption-side refund payload.
type CreditManaInput struct {
	// GCID being credited. Required.
	GCID string
	// Units to return (>= 1 — the identity contract rejects 0).
	Units int64
	// IdempotencyKey dedupes refund replays (convention:
	// "<reservation_id>:refund").
	IdempotencyKey string
	// Reason is a short human-readable cause carried into the ledger row's
	// source pointer space via logging only (the proto reason is the REFUND
	// enum; free-text reasons stay in service logs).
	Reason string
}

// CreditMana refunds a prior reservation. Fail-loud: any transport/RPC error
// surfaces so callers can NACK-and-retry (refunds are idempotent by key).
func (c *ManaClient) CreditMana(ctx context.Context, in CreditManaInput) error {
	if c == nil || c.client == nil {
		return ErrManaEmptyBaseURL
	}
	if strings.TrimSpace(in.GCID) == "" {
		return errors.New("mana_client: CreditMana: gcid required")
	}
	if in.Units < 1 {
		return fmt.Errorf("mana_client: CreditMana: units must be >= 1, got %d", in.Units)
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return errors.New("mana_client: CreditMana: idempotency key required")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	_, err := c.client.CreditMana(callCtx, &identityv1.CreditManaRequest{
		Gcid:           in.GCID,
		Source:         identityv1.ManaSource_MANA_SOURCE_REFUND,
		Units:          in.Units,
		Reason:         identityv1.ManaReasonCode_MANA_REASON_CODE_REFUND,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("mana_client: CreditMana rpc: %w", err)
	}
	return nil
}
