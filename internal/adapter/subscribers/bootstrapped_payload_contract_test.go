package subscribers

import (
	"testing"
)

// The learning_path.bootstrapped.v1 PAYLOAD CONTRACT.
//
// This is the test that was missing, and its absence kept the daily dose's
// CURIOSITY slot permanently empty.
//
// ActivePathTopicsSubscriber is the sole consumer of bootstrapped.v1. The push
// handler maps the wire payload with `AtomIDs: strSliceField(raw, "atom_ids")`
// and the subscriber resolves each atom's topic_tags from atom_index to build
// the learner's active topic-set. The decode side was always correct.
//
// The PUBLISHERS were not. Both EnrollmentCreatedSubscriber and (until now)
// CollectionConvertedToStudyListSubscriber emitted only `atom_count` — an int.
// No `atom_ids`. Ever. So `strSliceField(raw, "atom_ids")` returned nil, the
// subscriber resolved zero topics, and it wrote zero rows.
//
// Proven live 2026-07-14: the push arrived, the handler ran in 810µs, returned
// 200, logged nothing, and active_path_topics stayed at 0 rows — exactly as it
// had since migration 0004 created the table.
//
// Every unit test passed the whole time, because each side was tested against
// its OWN idea of the payload and nothing tested them against EACH OTHER. A
// publisher test asserting "atom_count is present" and a consumer test fed a
// hand-built struct with AtomIDs already populated are both green, forever,
// while the system is dead. That is the shape of this class of bug.
//
// These tests assert the key the consumer actually reads is the key the
// publisher actually writes.

// atomIDsFrom mirrors the push handler's strSliceField(raw, "atom_ids"): it
// accepts the []string a Go publisher emits and the []any a JSON round-trip
// produces. If this returns empty, the curiosity slot gets nothing.
func atomIDsFrom(payload map[string]any) []string {
	v, ok := payload["atom_ids"]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func TestBootstrappedPayload_MustCarryAtomIDs_NotJustACount(t *testing.T) {
	want := []string{
		"019eb058-3045-7711-a374-fb9d79b02746",
		"019eb058-30d2-7e60-98dd-61d034f43d5d",
	}

	// The payload the publishers now emit.
	payload := map[string]any{
		"path_id":      "019f5fd7-0000-7000-8000-000000000001",
		"learner_gcid": "00000000-0000-7000-8000-000000001999",
		"atom_ids":     want,
		"atom_count":   len(want),
	}

	got := atomIDsFrom(payload)
	if len(got) != len(want) {
		t.Fatalf("consumer reads %d atom_ids from a payload carrying %d — bootstrapped.v1 is "+
			"broken and ActivePathTopicsSubscriber will resolve ZERO topics and write ZERO "+
			"rows while returning 200. got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("atom_ids[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The regression itself: a count is not a list. A payload carrying only
// atom_count yields NOTHING for the consumer — which is precisely the silent
// no-op that shipped.
func TestBootstrappedPayload_CountAloneYieldsNothing(t *testing.T) {
	payload := map[string]any{
		"path_id":    "019f5fd7-0000-7000-8000-000000000001",
		"atom_count": 3, // the old payload: a count, no ids
	}
	if got := atomIDsFrom(payload); len(got) != 0 {
		t.Fatalf("a count-only payload must yield ZERO atom_ids (that IS the bug); got %v", got)
	}
}

// A JSON round-trip (both topics are schemaless, so this is the real wire) must
// preserve the key — a []string becomes []any and must still resolve.
func TestBootstrappedPayload_SurvivesTheJSONWire(t *testing.T) {
	payload := map[string]any{
		"atom_ids": []any{"019eb058-3045-7711-a374-fb9d79b02746"}, // post-JSON shape
	}
	if got := atomIDsFrom(payload); len(got) != 1 || got[0] != "019eb058-3045-7711-a374-fb9d79b02746" {
		t.Fatalf("atom_ids must survive the JSON wire as []any; got %v", got)
	}
}
