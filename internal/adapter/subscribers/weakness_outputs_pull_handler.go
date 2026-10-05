// weakness_outputs_pull_handler.go - ADR-254 D4 (W4 consumption cut): binds
// the weakness.outputs_generated.v1 projector to the chora-common eventbus.
//
// The kennel's diagnosis crew publishes the event as BINARY proto (the flat
// twin of chora.consumption.v1.WeaknessOutputsGenerated, weakness_proto_encoder
// in the orchestrator) on a topic that is BINARY-schema bound live; the
// coordinator provisioned the PULL subscription
// chora-consumption.consumption-weakness-outputs-generated for this service.
// Until this adapter existed the projector had no transport at all (the
// CHO-2348 emit published into the void on both sides).
//
// Decode: binary proto first (the live lane); a body that is not a valid
// WeaknessOutputsGenerated NACKs (dead-letters loudly). The message envelope
// (decoded from the bus headers) is authoritative for event_id / idempotency /
// tenant / occurred_at; the body's nested envelope is a mirror and is not
// trusted over it.
package subscribers

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/apollo-chora/chora-common/eventbus"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
)

// WeaknessOutputsSubscription is the provisioned pull subscription id.
const WeaknessOutputsSubscription = "chora-consumption.consumption-weakness-outputs-generated"

// WeaknessOutputsPullHandler adapts the projector to an eventbus.Handler.
func WeaknessOutputsPullHandler(s *WeaknessOutputsGeneratedSubscriber) eventbus.Handler {
	return func(ctx context.Context, msg eventbus.Message) error {
		if s == nil {
			return errors.New("subscribers: weakness outputs pull handler not initialised")
		}
		var body consumptionv1.WeaknessOutputsGenerated
		if err := proto.Unmarshal(msg.Payload, &body); err != nil {
			return fmt.Errorf("subscribers: weakness.outputs_generated binary decode: %w", err)
		}
		if body.GetUploadId() == "" && body.GetTenantId() == "" && len(body.GetOutputs()) == 0 {
			// proto.Unmarshal accepts almost any bytes as an empty message; an
			// empty body is not a valid outputs_generated event.
			return errors.New("subscribers: weakness.outputs_generated body decoded to an empty message (not a WeaknessOutputsGenerated)")
		}
		p := WeaknessOutputsGeneratedPayload{
			UploadID:    body.GetUploadId(),
			TenantID:    body.GetTenantId(),
			LearnerGCID: body.GetLearnerGcid(),
		}
		if ts := body.GetGeneratedAt(); ts != nil && ts.IsValid() {
			p.GeneratedAt = ts.AsTime()
		}
		for _, o := range body.GetOutputs() {
			p.Outputs = append(p.Outputs, GeneratedOutputPayload{
				Kind: o.GetKind(), ContentJSON: o.GetContentJson(), Metered: o.GetMetered(),
			})
		}
		env := projectEnvelope(msg.Envelope)
		return s.Handle(ctx, env, p)
	}
}
