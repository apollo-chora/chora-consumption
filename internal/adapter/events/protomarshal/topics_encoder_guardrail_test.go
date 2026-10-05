// topics_encoder_guardrail_test — event-fabric Flag-1 guardrail (2026-07-01),
// cloud-neutral port.
//
// Every consumption-domain topic bound to a per-event BINARY schema MUST have
// a case in protomarshal.MarshalPayload. Otherwise the outbox JSON-falls-back
// (see encodeOutboxPayload) and the payload is not the canonical binary proto
// the consumers unmarshal. This CI test catches a missing encoder at build
// time, before it can ship.
//
// The retired source walked up to the monorepo's infra topics catalogue (a
// cloud Pub/Sub Schema Registry registry, removed with the monorepo). The
// cloud-neutral source of truth is the inlined list below: the per-event
// BINARY consumption topics from that registry (schema name
// chora-consumption-{aggregate}-{event}-v1), minus the retiring familiar.*
// rows whose companion twin is registered.
//
// CI-only (per the Flag-1 brief): it does NOT make encodeOutboxPayload
// runtime-fail-loud — some platform topics legitimately publish schemaless
// JSON, and a blanket runtime fail-loud would break them.
package protomarshal_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// perEventBinaryConsumptionTopics is the inlined per-event BINARY consumption
// topic list (see the file header for provenance).
var perEventBinaryConsumptionTopics = []string{
	"chora.consumption.kg_exploration.focal_changed.v1",
	"chora.consumption.kg_hexagon_fog.generated.v1",
	"chora.consumption.campaign.focus_assigned.v1",
	"chora.consumption.concept.atoms_bound.v1",
	"chora.consumption.campaign.rung_cleared.v1",
	"chora.consumption.campaign.node_won.v1",
	"chora.consumption.campaign.node_revealed.v1",
	"chora.consumption.campaign.goal_sealed.v1",
	"chora.consumption.weakness.analyzed.v1",
	"chora.consumption.weakness.grown.v1",
	"chora.consumption.weakness.review_pending.v1",
	"chora.consumption.companion.bonded.v1",
	"chora.consumption.companion.breed_revealed.v1",
	"chora.consumption.companion.chat_turn_completed.v1",
	"chora.consumption.companion.created.v1",
	"chora.consumption.companion.egg_purchased.v1",
	"chora.consumption.companion.exp_awarded.v1",
	"chora.consumption.companion.exposure_granted.v1",
	"chora.consumption.companion.hatched.v1",
	"chora.consumption.companion.kg_neighbor_revealed.v1",
	"chora.consumption.companion.leveled_up.v1",
	"chora.consumption.companion.loadout_changed.v1",
	"chora.consumption.companion.memory_eviction.v1",
	"chora.consumption.companion.persona_updated.v1",
	"chora.consumption.companion.retired.v1",
	"chora.consumption.companion.ritual_published.v1",
	"chora.consumption.companion.session_started.v1",
	"chora.consumption.companion.skill_granted.v1",
	"chora.consumption.companion.skill_slot_unlocked.v1",
	"chora.consumption.companion.skin_equipped.v1",
	"chora.consumption.companion.source_revelation.v1",
	"chora.consumption.companion.specialization_changed.v1",
	"chora.consumption.companion.stage_up.v1",
	"chora.consumption.companion.stirring.v1",
	"chora.consumption.companion.ritual_run_completed.v1",
}

func TestFlag1_ConsumptionPerEventTopicsHaveEncoders(t *testing.T) {
	// Any envelope satisfies the guardrail — we only care whether an encoder
	// CASE exists (ErrUnsupportedTopic), not the produced bytes.
	env := protomarshal.Envelope{
		EventID:  "01970000-0000-7000-8000-0000000000ff",
		TenantID: "01970000-0000-7000-8000-0000000000a1",
	}

	for _, topic := range perEventBinaryConsumptionTopics {
		_, mErr := protomarshal.MarshalPayload(topic, env, map[string]any{})
		if protomarshal.IsUnsupportedTopic(mErr) {
			t.Errorf("topic %q is bound to a per-event BINARY schema but has NO encoder case in "+
				"protomarshal.MarshalPayload — it will JSON-fall-back instead of marshalling the "+
				"canonical binary proto. Add a case + a round-trip test.", topic)
		} else if mErr != nil {
			t.Errorf("topic %q: unexpected MarshalPayload error with empty payload: %v", topic, mErr)
		}
	}

	t.Logf("Flag-1 guardrail asserted encoders for %d consumption per-event binary topic(s)",
		len(perEventBinaryConsumptionTopics))
}
