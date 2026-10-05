// companion_turn_completed_handler.go - ADR-254 D4/D8: the PULL consumer for
// the caller-facing result lanes consumption owns:
//
//	chora.consumption.companion_turn.completed.v1        (TopicCompanionTurnCompleted)
//	chora.consumption.dose_recommendation.completed.v1   (TopicDoseRecommendationCompleted)
//
// Wire: the kennel's generic single-agent workflow publishes the result as a
// JSON body whose keys are the proto field names (CompanionTurnCompleted /
// DoseRecommendationCompleted), with the mandatory envelope flattened onto the
// message headers (the eventbus decodes it into msg.Envelope; the same shape
// the closure subscriber already consumes from the closure orchestrator).
//
// Semantics (ack-after-processing, ADR-253 D5):
//   - well-formed result for an accepted turn -> Complete (exactly once) -> ACK
//   - redelivery of a result the store already applied -> no-op -> ACK
//   - result for an unknown turn id -> loud log -> ACK (it cannot be retried
//     into existence; dead-lettering it would only hide the anomaly)
//   - malformed body / no tenant on envelope nor body -> NACK (dead-letters)
//   - store error -> NACK (redelivery; the store's Complete is idempotent)
//
// Tenant scoping: the store runs under FORCE RLS, so the tenant comes from the
// ENVELOPE (tenant_id stamped by the kennel from the request) and is put on
// the context with tracing.WithTenantID before the store call. The result
// body's own tenant_id (if any) is a fallback, never an override.
//
// Cross-pod: any replica may receive the completion; the HTTP waiter polls the
// store, so completing the row IS the hand-off. No in-process signalling is
// required for correctness (a same-pod fast path is an optimisation the engine
// may add).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-common/tracing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// NewCompanionTurnCompletedHandler returns the eventbus.Handler for one
// result lane. lane decides which id key the body carries (turn_id vs
// dose_request_id).
func NewCompanionTurnCompletedHandler(store companion.TurnStore, lane companion.TurnLane) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		parsed, err := companion.ParseTurnResultJSON(lane, msg.Payload)
		if err != nil {
			// Malformed: NACK so it dead-letters where an operator can read it.
			return fmt.Errorf("companion_turn_completed(%s): %w", lane, err)
		}
		tenantID := strings.TrimSpace(msg.Envelope.TenantID)
		if tenantID == "" {
			var body struct {
				TenantID string `json:"tenant_id"`
			}
			_ = json.Unmarshal(msg.Payload, &body)
			tenantID = strings.TrimSpace(body.TenantID)
		}
		if tenantID == "" {
			return fmt.Errorf("companion_turn_completed(%s): result %s carries no tenant_id (envelope nor body)", lane, parsed.ID)
		}
		tctx := tracing.WithTenantID(ctx, tenantID)
		if gcid := strings.TrimSpace(msg.Envelope.GCID); gcid != "" {
			tctx = tracing.WithGCID(tctx, gcid)
		}
		applied, cerr := store.Complete(tctx, tenantID, parsed.ID, parsed.Result)
		switch {
		case cerr == nil:
			if !applied {
				log.Printf("consumption: %s result %s already applied (redelivery, ACK)", lane, parsed.ID)
			}
			return nil
		case errors.Is(cerr, companion.ErrTurnNotFound):
			// Loud, then ACK: a turn this service never began (or another
			// tenant's) cannot be completed by retrying.
			log.Printf("ERROR consumption: %s result %s for UNKNOWN turn (tenant=%s status=%s workflow=%s); acked, not retried",
				lane, parsed.ID, tenantID, parsed.Result.WireStatus, parsed.Result.WorkflowID)
			return nil
		case errors.Is(cerr, companion.ErrTurnBadResult):
			return fmt.Errorf("companion_turn_completed(%s): %w", lane, cerr)
		default:
			return fmt.Errorf("companion_turn_completed(%s): complete %s: %w", lane, parsed.ID, cerr)
		}
	}
}
