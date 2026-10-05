package pg

// cover_helpers_test.go — pure-helper coverage for unexported, DB-free
// helpers in this package. Each helper is exercised directly (same package),
// covering happy paths, nil/zero arms, and error arms where reachable.
//
// No live Postgres is required: none of these helpers touch a Querier/Tx.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// marshalFailValue implements json.Marshaler and always fails — used to drive
// json.Marshal's error arms that are unreachable with the helpers' own types
// (whose fields are all trivially marshallable).
type marshalFailValue struct{}

func (marshalFailValue) MarshalJSON() ([]byte, error) { return nil, errors.New("boom") }

// ---------------------------------------------------------------------------
// learner_weakness.marshalDescriptor / unmarshalDescriptor
// ---------------------------------------------------------------------------

func TestCoverMarshalDescriptor(t *testing.T) {
	d := lw.Descriptor{
		Summary:         "blew the fraction ladder",
		Misconceptions:  []string{"common denominator"},
		SuggestedAngles: []string{"visual fraction bars"},
		SampleWrong: []lw.SampleWrong{
			{Prompt: "1/2 + 1/3", WhyWrong: "added denominators"},
		},
		PerItemCorrectness: []lw.ItemCorrectness{{Item: "q1", Correct: false}},
	}
	got := marshalDescriptor(d)
	var back lw.Descriptor
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("marshalDescriptor produced invalid JSON %q: %v", got, err)
	}
	if back.Summary != d.Summary || len(back.SampleWrong) != 1 || back.SampleWrong[0].Prompt != "1/2 + 1/3" {
		t.Errorf("marshalDescriptor round-trip mismatch: got %+v", back)
	}
}

func TestCoverUnmarshalDescriptor(t *testing.T) {
	// Empty / whitespace-only → zero value.
	if d := unmarshalDescriptor(""); d.Summary != "" || d.Misconceptions != nil {
		t.Errorf("unmarshalDescriptor(\"\") = %+v, want zero", d)
	}
	if d := unmarshalDescriptor("   \t\n"); d.Summary != "" {
		t.Errorf("unmarshalDescriptor(whitespace) = %+v, want zero", d)
	}
	// Valid JSON → populated.
	in := `{"summary":"s","misconceptions":["m1"],"sample_wrong":[{"prompt":"p","why_wrong":"w"}]}`
	d := unmarshalDescriptor(in)
	if d.Summary != "s" || len(d.Misconceptions) != 1 || d.Misconceptions[0] != "m1" {
		t.Errorf("unmarshalDescriptor(valid) = %+v, want populated", d)
	}
	if len(d.SampleWrong) != 1 || d.SampleWrong[0].Prompt != "p" || d.SampleWrong[0].WhyWrong != "w" {
		t.Errorf("unmarshalDescriptor sample_wrong = %+v", d.SampleWrong)
	}
	// Malformed JSON → error is swallowed, zero value returned.
	if d := unmarshalDescriptor("{{{not json"); d.Summary != "" {
		t.Errorf("unmarshalDescriptor(malformed) = %+v, want zero", d)
	}
}

// ---------------------------------------------------------------------------
// learner_profile.marshalDetail / unmarshalDetail
// ---------------------------------------------------------------------------

func TestCoverMarshalDetail(t *testing.T) {
	passed := true
	score := 0.9
	hc := 2
	d := lp.Detail{
		Label:     "assessment: placeholder-division",
		Score:     &score,
		Passed:    &passed,
		HintCount: &hc,
		Extra:     map[string]string{"attempt": "1"},
	}
	got := marshalDetail(d)
	var back lp.Detail
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("marshalDetail produced invalid JSON %q: %v", got, err)
	}
	if back.Label != d.Label || back.Score == nil || *back.Score != 0.9 ||
		back.Passed == nil || !*back.Passed || back.HintCount == nil || *back.HintCount != 2 {
		t.Errorf("marshalDetail round-trip mismatch: got %+v", back)
	}
}

func TestCoverUnmarshalDetail(t *testing.T) {
	if d := unmarshalDetail(""); d.Label != "" {
		t.Errorf("unmarshalDetail(\"\") = %+v, want zero", d)
	}
	if d := unmarshalDetail("  "); d.Label != "" {
		t.Errorf("unmarshalDetail(whitespace) = %+v, want zero", d)
	}
	in := `{"label":"l","score":0.5,"passed":true,"hint_count":1,"extra":{"k":"v"}}`
	d := unmarshalDetail(in)
	if d.Label != "l" || d.Score == nil || *d.Score != 0.5 || d.Passed == nil || !*d.Passed {
		t.Errorf("unmarshalDetail(valid) = %+v", d)
	}
	if d.HintCount == nil || *d.HintCount != 1 || d.Extra["k"] != "v" {
		t.Errorf("unmarshalDetail extra/hint = %+v", d)
	}
	if d := unmarshalDetail("not-json at all"); d.Label != "" {
		t.Errorf("unmarshalDetail(malformed) = %+v, want zero", d)
	}
}

// ---------------------------------------------------------------------------
// retention.derefTime
// ---------------------------------------------------------------------------

func TestCoverDerefTime(t *testing.T) {
	if got := derefTime(nil); !got.IsZero() {
		t.Errorf("derefTime(nil) = %v, want zero", got)
	}
	ts := time.Date(2026, 8, 7, 10, 30, 0, 0, time.UTC)
	if got := derefTime(&ts); !got.Equal(ts) {
		t.Errorf("derefTime(ptr) = %v, want %v", got, ts)
	}
}

// ---------------------------------------------------------------------------
// companion_ritual_run_repo.utcPtr
// ---------------------------------------------------------------------------

func TestCoverUTCPtr(t *testing.T) {
	if got := utcPtr(nil); got != nil {
		t.Errorf("utcPtr(nil) = %v, want nil", got)
	}
	local := time.Date(2026, 8, 7, 12, 0, 0, 0, time.FixedZone("+09", 9*3600))
	got := utcPtr(&local)
	if got == nil {
		t.Fatal("utcPtr(non-nil) = nil")
	}
	want := local.UTC()
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("utcPtr = %v (loc %v), want %v (UTC)", got, got.Location(), want)
	}
}

// ---------------------------------------------------------------------------
// outbox_in_tx.nilIfEmptyStr
// ---------------------------------------------------------------------------

func TestCoverNilIfEmptyStr(t *testing.T) {
	if got := nilIfEmptyStr(""); got != nil {
		t.Errorf("nilIfEmptyStr(\"\") = %v, want nil", got)
	}
	if got, ok := nilIfEmptyStr("abc").(string); !ok || got != "abc" {
		t.Errorf("nilIfEmptyStr(\"abc\") = %v, want \"abc\"", got)
	}
}

// ---------------------------------------------------------------------------
// proofingtest.nullableBytes
// ---------------------------------------------------------------------------

func TestCoverNullableBytes(t *testing.T) {
	if got := nullableBytes(nil); got != nil {
		t.Errorf("nullableBytes(nil) = %v, want nil", got)
	}
	if got := nullableBytes([]byte{}); got != nil {
		t.Errorf("nullableBytes(empty) = %v, want nil", got)
	}
	b := []byte(`{"a":1}`)
	if got := nullableBytes(b); got != nil && string(got) != string(b) {
		t.Errorf("nullableBytes(non-empty) = %q, want %q", got, b)
	}
	if got := nullableBytes(b); got == nil {
		t.Error("nullableBytes(non-empty) returned nil")
	}
}

// ---------------------------------------------------------------------------
// companion_instance.stringifyJSONValue
// ---------------------------------------------------------------------------

func TestCoverStringifyJSONValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hello", "hello"}, // string
		{true, "true"},     // bool
		{float64(3), "3"},  // float64 integer → no trailing .0
		{float64(3.14), "3.14"},
		{nil, ""},                               // nil
		{map[string]any{"k": "v"}, `{"k":"v"}`}, // default: marshal ok
		{marshalFailValue{}, "{}"},              // default: marshal fails → fmt.Sprintf
	}
	for _, c := range cases {
		if got := stringifyJSONValue(c.in); got != c.want {
			t.Errorf("stringifyJSONValue(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// concept_suggestion.uuidArr
// ---------------------------------------------------------------------------

func TestCoverUUIDArr(t *testing.T) {
	got := uuidArr(nil)
	if got == nil {
		t.Fatal("uuidArr(nil) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("uuidArr(nil) = %v, want empty", got)
	}
	in := []string{"a", "b"}
	if got := uuidArr(in); !reflect.DeepEqual(got, in) {
		t.Errorf("uuidArr(non-nil) = %v, want %v (identity)", got, in)
	}
	if got := uuidArr(in); len(got) != 2 {
		t.Errorf("uuidArr content len = %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// hexagon.reasonOrNil
// ---------------------------------------------------------------------------

func TestCoverReasonOrNil(t *testing.T) {
	if got := reasonOrNil(""); got != nil {
		t.Errorf("reasonOrNil(\"\") = %v, want nil", got)
	}
	if got := reasonOrNil(userknowledgegraph.FogInvalidationReasonNeverGenerated); got != nil {
		t.Errorf("reasonOrNil(never_generated) = %v, want nil", got)
	}
	if got, ok := reasonOrNil(userknowledgegraph.FogInvalidationReasonAtomPublished).(string); !ok ||
		got != "atom_published" {
		t.Errorf("reasonOrNil(atom_published) = %v, want \"atom_published\"", got)
	}
}

// ---------------------------------------------------------------------------
// learner_weakness.sampleWrongEvidence
// ---------------------------------------------------------------------------

func TestCoverSampleWrongEvidence(t *testing.T) {
	samples := []lw.SampleWrong{
		{Prompt: "  p1 ", WhyWrong: " w1 "}, // both → "p1: w1" (trimmed)
		{WhyWrong: "  only-why "},           // no prompt → "only-why"
		{Prompt: "only-prompt"},             // no why → "only-prompt"
		{Prompt: " ", WhyWrong: " "},        // both empty → skipped
		{},                                  // empty → skipped
	}
	got := sampleWrongEvidence(samples)
	want := []string{"p1: w1", "only-why", "only-prompt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sampleWrongEvidence = %#v, want %#v", got, want)
	}
	if got := sampleWrongEvidence(nil); len(got) != 0 {
		t.Errorf("sampleWrongEvidence(nil) = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// concept_merge_split.walkAncestorKeys
// ---------------------------------------------------------------------------

func TestCoverWalkAncestorKeys(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		m := walkAncestorKeys(nil)
		if len(m) != 0 {
			t.Errorf("walkAncestorKeys(nil) = %v, want empty map", m)
		}
	})

	t.Run("single-parent linear chain", func(t *testing.T) {
		rows := []conceptLineageRow{
			{ToConceptID: "A", FromConceptID: "B", FromConceptKey: "keyB"},
			{ToConceptID: "B", FromConceptID: "C", FromConceptKey: "keyC"},
		}
		got := walkAncestorKeys(rows)
		wantA := []string{"keyB", "keyC"}
		if !reflect.DeepEqual(got["A"], wantA) {
			t.Errorf("walkAncestorKeys[A] = %v, want %v", got["A"], wantA)
		}
		if !reflect.DeepEqual(got["B"], []string{"keyC"}) {
			t.Errorf("walkAncestorKeys[B] = %v, want [keyC]", got["B"])
		}
	})

	t.Run("dedup + empty key skip", func(t *testing.T) {
		rows := []conceptLineageRow{
			{ToConceptID: "A", FromConceptID: "B", FromConceptKey: "shared"},
			{ToConceptID: "A", FromConceptID: "C", FromConceptKey: "shared"}, // dup
			{ToConceptID: "A", FromConceptID: "D", FromConceptKey: "uniquekey"},
			{ToConceptID: "A", FromConceptID: "E", FromConceptKey: ""}, // empty → skipped, still traversed
		}
		got := walkAncestorKeys(rows)
		if !reflect.DeepEqual(got["A"], []string{"shared", "uniquekey"}) {
			t.Errorf("walkAncestorKeys[A] = %v, want [shared uniquekey]", got["A"])
		}
	})

	t.Run("cycle guarded terminates", func(t *testing.T) {
		rows := []conceptLineageRow{
			{ToConceptID: "X", FromConceptID: "Y", FromConceptKey: "kx"},
			{ToConceptID: "Y", FromConceptID: "X", FromConceptKey: "ky"},
		}
		got := walkAncestorKeys(rows)
		wantX := []string{"kx", "ky"}
		wantY := []string{"ky", "kx"}
		if !reflect.DeepEqual(got["X"], wantX) {
			t.Errorf("walkAncestorKeys[X] = %v, want %v", got["X"], wantX)
		}
		if !reflect.DeepEqual(got["Y"], wantY) {
			t.Errorf("walkAncestorKeys[Y] = %v, want %v", got["Y"], wantY)
		}
	})
}

// ---------------------------------------------------------------------------
// companion_ritual_repo.marshalTriggerConfig
// ---------------------------------------------------------------------------

func TestCoverMarshalTriggerConfig(t *testing.T) {
	t.Run("empty and nil default to {}", func(t *testing.T) {
		b, err := marshalTriggerConfig(nil)
		if err != nil {
			t.Fatalf("marshalTriggerConfig(nil): %v", err)
		}
		if string(b) != "{}" {
			t.Errorf("marshalTriggerConfig(nil) = %q, want {}", b)
		}
		b, err = marshalTriggerConfig(map[string]any{})
		if err != nil {
			t.Fatalf("marshalTriggerConfig(empty): %v", err)
		}
		if string(b) != "{}" {
			t.Errorf("marshalTriggerConfig(empty) = %q, want {}", b)
		}
	})

	t.Run("populated map", func(t *testing.T) {
		b, err := marshalTriggerConfig(map[string]any{"max_hint_count": float64(3), "mode": "timed"})
		if err != nil {
			t.Fatalf("marshalTriggerConfig(populated): %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("marshalTriggerConfig produced invalid JSON %q: %v", b, err)
		}
		if m["mode"] != "timed" {
			t.Errorf("marshalTriggerConfig mode = %v, want timed", m["mode"])
		}
	})

	t.Run("marshal error", func(t *testing.T) {
		_, err := marshalTriggerConfig(map[string]any{"bad": marshalFailValue{}})
		if err == nil {
			t.Fatal("marshalTriggerConfig(error value) expected error, got nil")
		}
		if !strings.Contains(err.Error(), "marshal ritual trigger_config") {
			t.Errorf("error = %q, want prefix", err)
		}
	})
}

// ---------------------------------------------------------------------------
// companion_memory_view.decodeMemoryProvenance
// ---------------------------------------------------------------------------

func TestCoverDecodeMemoryProvenance(t *testing.T) {
	t.Run("empty/nil → nil nil", func(t *testing.T) {
		p, err := decodeMemoryProvenance(nil)
		if err != nil || p != nil {
			t.Errorf("decodeMemoryProvenance(nil) = (%v, %v), want (nil,nil)", p, err)
		}
		p, err = decodeMemoryProvenance([]byte{})
		if err != nil || p != nil {
			t.Errorf("decodeMemoryProvenance(empty) = (%v, %v), want (nil,nil)", p, err)
		}
	})

	t.Run("malformed json → error", func(t *testing.T) {
		_, err := decodeMemoryProvenance([]byte("not json"))
		if err == nil {
			t.Fatal("decodeMemoryProvenance(malformed) expected error")
		}
		if !strings.Contains(err.Error(), "decode companion_memory_recall source_metadata") {
			t.Errorf("error = %q, want prefix", err)
		}
	})

	t.Run("empty husk {} collapses to nil", func(t *testing.T) {
		p, err := decodeMemoryProvenance([]byte(`{}`))
		if err != nil {
			t.Fatalf("decodeMemoryProvenance({}) error: %v", err)
		}
		if p != nil {
			t.Errorf("decodeMemoryProvenance({}) = %+v, want nil", p)
		}
	})

	t.Run("populated provenance", func(t *testing.T) {
		raw := []byte(`{"web_search_queries":["q1","q2"],"citations":[{"domain":"d","title":"t","snippet":"s"}]}`)
		p, err := decodeMemoryProvenance(raw)
		if err != nil {
			t.Fatalf("decodeMemoryProvenance(populated): %v", err)
		}
		var _ *companionmind.Provenance = p
		if p == nil {
			t.Fatal("decodeMemoryProvenance(populated) = nil")
		}
		if !reflect.DeepEqual(p.WebSearchQueries, []string{"q1", "q2"}) {
			t.Errorf("WebSearchQueries = %v", p.WebSearchQueries)
		}
		if len(p.Citations) != 1 || p.Citations[0].Domain != "d" ||
			p.Citations[0].Title != "t" || p.Citations[0].Snippet != "s" {
			t.Errorf("Citations = %+v", p.Citations)
		}
	})
}

// Compile-time sanity: unmarshalDescriptor is the sibling of the targeted
// marshalDescriptor and both feed the same JSON contract. A short assertion
// keeps the round-trip honest.
func TestCoverDescriptorRoundTrip(t *testing.T) {
	d := marshalDescriptor(lw.Descriptor{Summary: "rt"})
	if back := unmarshalDescriptor(d); back.Summary != "rt" {
		t.Errorf("round-trip summary = %q, want rt", back.Summary)
	}
}
