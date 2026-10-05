package protomarshal_test

import (
	"encoding/json"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// json_wire_test.go - ADR-254 D4: the two caller-facing REQUEST lanes
// consumption publishes are JSON-wire (schemaless); MarshalPayload must emit the
// payload as JSON (NOT ErrUnsupportedTopic, which the outbox treats as "this
// binary-schema topic has no encoder and will dead-letter").
func TestMarshalPayload_JSONWireRequestLanes(t *testing.T) {
	for _, topic := range []string{
		"chora.consumption.companion_turn.requested.v1",
		"chora.consumption.dose_recommendation.requested.v1",
	} {
		payload := map[string]any{
			"turn_id":      "01933b5e-0000-7000-8000-000000000001",
			"tenant_id":    "11111111-1111-7111-8111-111111111111",
			"growth_stage": 3,
			"message":      "hi",
		}
		bz, err := protomarshal.MarshalPayload(topic, fixedEnvelope(), payload)
		if err != nil {
			t.Fatalf("%s: MarshalPayload: %v", topic, err)
		}
		if protomarshal.IsUnsupportedTopic(err) {
			t.Fatalf("%s: must not be reported as an unsupported (binary) topic", topic)
		}
		var back map[string]any
		if err := json.Unmarshal(bz, &back); err != nil {
			t.Fatalf("%s: body is not JSON: %v (%q)", topic, err, bz)
		}
		if back["message"] != "hi" || back["growth_stage"] != float64(3) {
			t.Fatalf("%s: body keys not preserved: %v", topic, back)
		}
	}
	if _, err := protomarshal.MarshalPayload("chora.consumption.companion_turn.requested.v1", fixedEnvelope(), nil); err == nil {
		t.Fatal("a nil payload must fail loud (a request with no body is never valid)")
	}
}
