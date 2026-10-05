// concept_test.go - ADR-244 D4 (CHO-2303) RED: the atoms_bound topic must have
// a BINARY encoder. Without one, MarshalPayload falls back to JSON and the
// Schema Registry rejects every publish with INVALID_BINARY_PROTO_MESSAGE, which
// deadletters silently. The guardrail test catches a MISSING case; this one
// pins the FIELD LAYOUT against the flat proto.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestMarshalPayload_ConceptAtomsBound(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	bz, err := protomarshal.MarshalPayload("chora.consumption.concept.atoms_bound.v1",
		protomarshal.Envelope{
			EventID: "e1", IdempotencyKey: "k1", TenantID: "t1", GCID: "g1",
			OccurredAt: now, PublishedAt: now, Traceparent: "tp",
			SourceProject: "chora-489812", SourceService: "chora-consumption", SchemaVersion: 1,
		},
		map[string]any{
			"concept_id":          "c1",
			"tenant_id":           "t1",
			"learner_gcid":        "g1",
			"attached_atom_ids":   []string{"a1", "a2"},
			"detached_atom_ids":   []string{"d1"},
			"resulting_atom_refs": []string{"a1", "a2", "keep"},
			"change_source":       "manual_attach",
			"provenance":          "learner_authored",
			"occurred_at":         now,
		})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty encoding")
	}
	// Walk the wire and count field numbers, so a silently-dropped repeated
	// field (the classic: only the FIRST element encoded) is caught.
	counts := map[protowire.Number]int{}
	rest := bz
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			t.Fatalf("bad tag: %v", protowire.ParseError(n))
		}
		rest = rest[n:]
		m := protowire.ConsumeFieldValue(num, typ, rest)
		if m < 0 {
			t.Fatalf("bad value for field %d: %v", num, protowire.ParseError(m))
		}
		rest = rest[m:]
		counts[num]++
	}
	// Field layout per events-flat/consumption/concept/atoms_bound.proto.
	for _, want := range []struct {
		field protowire.Number
		times int
		name  string
	}{
		{1, 1, "envelope"},
		{2, 1, "concept_id"},
		{3, 1, "tenant_id"},
		{4, 1, "learner_gcid"},
		{5, 2, "attached_atom_ids (repeated, 2 elements)"},
		{6, 1, "detached_atom_ids (repeated, 1 element)"},
		{7, 3, "resulting_atom_refs (repeated, 3 elements)"},
		{8, 1, "change_source"},
		{9, 1, "provenance"},
		{10, 1, "occurred_at"},
	} {
		if counts[want.field] != want.times {
			t.Errorf("field %d (%s): encoded %d time(s), want %d",
				want.field, want.name, counts[want.field], want.times)
		}
	}
}

// An empty repeated field must encode as ABSENT, not as one empty string.
func TestMarshalPayload_ConceptAtomsBound_EmptyRepeatedIsAbsent(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	bz, err := protomarshal.MarshalPayload("chora.consumption.concept.atoms_bound.v1",
		protomarshal.Envelope{EventID: "e1", TenantID: "t1", GCID: "g1", OccurredAt: now, PublishedAt: now},
		map[string]any{
			"concept_id": "c1", "tenant_id": "t1", "learner_gcid": "g1",
			"attached_atom_ids": []string{"a1"},
			"detached_atom_ids": []string{},
			"occurred_at":       now,
		})
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	rest := bz
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			t.Fatalf("bad tag")
		}
		rest = rest[n:]
		m := protowire.ConsumeFieldValue(num, typ, rest)
		if m < 0 {
			t.Fatalf("bad value")
		}
		rest = rest[m:]
		if num == 6 {
			t.Error("empty detached_atom_ids encoded a field; want absent")
		}
	}
}
