// companion_ritual_published_test — CHO-2125: ritual_published.v1 must have a
// BINARY encoder case in MarshalPayload so the outbox does NOT JSON-fall-back
// (boot WARN "no binary protobuf encoder") and dead-letter against its
// Schema-Registry-bound companion topic family (same failure class as the
// kg_hexagon_fog + loadout P0 + stirring encoder gaps; flagged in the WS-C8
// walk day-1). Payload keys mirror companion.RitualPublisher.emitPublished
// EXACTLY (ritual_publisher.go): ritual_id / companion_id / revision_no /
// trigger / sink / published_price_units — timestamps ride on the envelope.
//
// Field layout — chora-contracts/proto/events-flat/consumption/companion/ritual_published.proto
//
//	1 bytes  Envelope envelope
//	2 string ritual_id
//	3 string companion_id
//	4 varint int32 revision_no
//	5 string trigger
//	6 string sink
//	7 varint int32 published_price_units
//
// Unlike stirring, generated Go bindings EXIST (chora-contracts gen ritual.pb.go
// — CHO-2016 G6 pass), so alongside the stirring-style protowire field walk we
// round-trip through proto.Unmarshal into consumptionv1.RitualPublished — the
// load-bearing assertion that Schema Registry will accept the bytes
// (wire_compat_test.go idiom).
package protomarshal_test

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestMarshalCompanionRitualPublished_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"ritual_id":             "rit-morning-review",
		"companion_id":          "fam-ember",
		"revision_no":           3, // plain int, as emitPublished sends rev.RevisionNo
		"trigger":               "manual",
		"sink":                  "memory_note",
		"published_price_units": 35, // base 20 + one generative uplift 15
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.ritual_published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload(ritual_published): %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty ritual_published bytes")
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
			if num == 2 || num == 3 || num == 5 || num == 6 {
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
	if strs[2] != "rit-morning-review" {
		t.Errorf("ritual_id (2) = %q, want rit-morning-review", strs[2])
	}
	if strs[3] != "fam-ember" {
		t.Errorf("companion_id (3) = %q, want fam-ember", strs[3])
	}
	if varints[4] != 3 {
		t.Errorf("revision_no (4) = %d, want 3", varints[4])
	}
	if strs[5] != "manual" {
		t.Errorf("trigger (5) = %q, want manual", strs[5])
	}
	if strs[6] != "memory_note" {
		t.Errorf("sink (6) = %q, want memory_note", strs[6])
	}
	if varints[7] != 35 {
		t.Errorf("published_price_units (7) = %d, want 35", varints[7])
	}
}

// TestWireCompat_RitualPublished_DecodesIntoGeneratedType is the load-bearing
// Schema Registry assertion: the hand-rolled bytes must parse cleanly into the
// generated consumptionv1.RitualPublished and return EVERY field the publisher
// set (wire_compat_test.go idiom).
func TestWireCompat_RitualPublished_DecodesIntoGeneratedType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"ritual_id":             "0197c9a0-0000-7000-8000-0000000r1t01",
		"companion_id":          "0197c9a0-0000-7000-8000-0000000fam01",
		"revision_no":           1,
		"trigger":               "on_dose_completed",
		"sink":                  "chat",
		"published_price_units": 20, // base price, no premium steps
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.companion.ritual_published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var msg consumptionv1.RitualPublished
	if err := proto.Unmarshal(bz, &msg); err != nil {
		t.Fatalf("proto.Unmarshal into RitualPublished: %v", err)
	}

	if msg.GetEnvelope() == nil {
		t.Fatal("envelope not decoded")
	}
	if got := msg.GetEnvelope().GetEventId(); got != env.EventID {
		t.Errorf("envelope.event_id = %q, want %q", got, env.EventID)
	}
	if got := msg.GetEnvelope().GetTenantId(); got != env.TenantID {
		t.Errorf("envelope.tenant_id = %q, want %q", got, env.TenantID)
	}
	if got := msg.GetRitualId(); got != "0197c9a0-0000-7000-8000-0000000r1t01" {
		t.Errorf("ritual_id = %q", got)
	}
	if got := msg.GetCompanionId(); got != "0197c9a0-0000-7000-8000-0000000fam01" {
		t.Errorf("companion_id = %q", got)
	}
	if got := msg.GetRevisionNo(); got != 1 {
		t.Errorf("revision_no = %d, want 1", got)
	}
	if got := msg.GetTrigger(); got != "on_dose_completed" {
		t.Errorf("trigger = %q", got)
	}
	if got := msg.GetSink(); got != "chat" {
		t.Errorf("sink = %q", got)
	}
	if got := msg.GetPublishedPriceUnits(); got != 20 {
		t.Errorf("published_price_units = %d, want 20", got)
	}
}

func TestMarshalCompanionRitualPublished_TopicHasEncoder(t *testing.T) {
	// Guardrail: the ritual_published topic MUST resolve to an encoder (never
	// ErrUnsupportedTopic) — otherwise the publish JSON-falls-back and
	// dead-letters. Empty payload still encodes the envelope cleanly.
	if _, err := protomarshal.MarshalPayload("chora.consumption.companion.ritual_published.v1", fixedEnvelope(), map[string]any{}); err != nil {
		t.Fatalf("ritual_published topic must have an encoder, got %v", err)
	}
}
