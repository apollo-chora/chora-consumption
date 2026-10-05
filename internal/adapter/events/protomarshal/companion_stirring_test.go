// companion_stirring_test — F-I1.2 (CHO-2088, ADR-228): the incubation
// "stirring" event must have a BINARY encoder case in MarshalPayload so the
// outbox does NOT JSON-fall-back and dead-letter (same failure class as the
// kg_hexagon_fog + loadout P0 encoder gaps). No generated struct exists for
// this publish-only growth event, so the wire layout is asserted field-by-
// field via protowire (mirroring TestMarshalDailyDoseServed).
//
// Field layout — chora-contracts/proto/events-flat/consumption/companion/stirring.proto
//
//	1 bytes  Envelope envelope
//	2 string companion_id
//	3 string owner_gcid
//	4 varint int32 growth_exp
//	5 varint int32 hatch_threshold
//	6 bytes  Timestamp stirred_at
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestMarshalCompanionStirring_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	stirredAt := time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"companion_id":       "fam-egg",
		"owner_gcid":         "gcid-phyllis",
		"growth_exp":         30,
		"hatch_threshold":    25,
		"stirred_at":         stirredAt,
		"chora_companion_id": "fam-egg", // trace attr — NOT on the wire
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.stirring.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload(stirring): %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty stirring bytes")
	}

	strs := map[protowire.Number]string{}
	varints := map[protowire.Number]uint64{}
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				t.Fatalf("invalid bytes for field %d", num)
			}
			if num == 2 || num == 3 {
				strs[num] = string(v)
			}
			rem = rem[m:]
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				t.Fatalf("invalid varint for field %d", num)
			}
			varints[num] = v
			rem = rem[m:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}

	if !seen[1] {
		t.Error("missing envelope (field 1)")
	}
	if strs[2] != "fam-egg" {
		t.Errorf("companion_id (2) = %q, want fam-egg", strs[2])
	}
	if strs[3] != "gcid-phyllis" {
		t.Errorf("owner_gcid (3) = %q, want gcid-phyllis", strs[3])
	}
	if varints[4] != 30 {
		t.Errorf("growth_exp (4) = %d, want 30", varints[4])
	}
	if varints[5] != 25 {
		t.Errorf("hatch_threshold (5) = %d, want 25", varints[5])
	}
	if !seen[6] {
		t.Error("missing stirred_at (field 6)")
	}
}

func TestMarshalCompanionStirring_TopicHasEncoder(t *testing.T) {
	// Guardrail: the stirring topic MUST resolve to an encoder (never
	// ErrUnsupportedTopic) — otherwise the publish JSON-falls-back and
	// dead-letters. Empty payload still encodes the envelope cleanly.
	if _, err := protomarshal.MarshalPayload("chora.consumption.companion.stirring.v1", fixedEnvelope(), map[string]any{}); err != nil {
		t.Fatalf("stirring topic must have an encoder, got %v", err)
	}
}
