// protodecode_cover_test.go raises branch coverage on the projector +
// hydration + enum-mapping logic that the round-trip tests in
// protodecode_test.go exercise only partially. Each test here characterizes
// the CURRENT, real behavior of a specific uncovered branch so a future
// regression (wrong enum string, lossy projection, destructive hydration,
// silent decode failure) trips a red test.
//
// White-box (package protodecode) so the unexported projectors,
// hydrateFromAttrs, atomTypeToDomain, and warnBinaryFallback are reachable
// directly without round-tripping through Pub/Sub bytes.
package protodecode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
)

// -----------------------------------------------------------------------------
// atomTypeToDomain — full enum→domain-string mapping.
//
// This is the reverse of the producer-side protomarshal.lookupAtomType. The
// exact strings are load-bearing: atom_index stores them and the MCQ
// gradability check compares against the literal "mcq". A regression that
// renamed (e.g.) "fill_blank" -> "fillblank" would silently break gradability
// downstream, so each mapping is asserted.
// -----------------------------------------------------------------------------

func TestAtomTypeToDomain_AllEnumValues(t *testing.T) {
	cases := []struct {
		name string
		in   creationv1.AtomType
		want string
	}{
		{"multiple_choice", creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE, "mcq"},
		{"fill_blank", creationv1.AtomType_ATOM_TYPE_FILL_BLANK, "fill_blank"},
		{"true_false", creationv1.AtomType_ATOM_TYPE_TRUE_FALSE, "true_false"},
		{"short_answer", creationv1.AtomType_ATOM_TYPE_SHORT_ANSWER, "short_answer"},
		{"matching", creationv1.AtomType_ATOM_TYPE_MATCHING, "matching"},
		{"ordering", creationv1.AtomType_ATOM_TYPE_ORDERING, "ordering"},
		{"code", creationv1.AtomType_ATOM_TYPE_CODE, "code"},
		{"essay", creationv1.AtomType_ATOM_TYPE_ESSAY, "essay"},
		{"multimedia", creationv1.AtomType_ATOM_TYPE_MULTIMEDIA, "multimedia"},
		{"simulation", creationv1.AtomType_ATOM_TYPE_SIMULATION, "simulation"},
		// UNSPECIFIED + any unknown value falls to default "" — projector then
		// omits the atom_type key entirely.
		{"unspecified_is_empty", creationv1.AtomType_ATOM_TYPE_UNSPECIFIED, ""},
		{"out_of_range_is_empty", creationv1.AtomType(99), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, atomTypeToDomain(tc.in))
		})
	}
}

// -----------------------------------------------------------------------------
// Projector type-assertion guards.
//
// Each projector starts with `m, ok := msg.(*T); if !ok || m == nil`. When the
// wrong concrete type (or a nil) is handed in, the projector must write NOTHING
// to out and must not panic — this protects the registry from a mis-wired
// decode/project pairing.
// -----------------------------------------------------------------------------

func TestProjectors_WrongTypeOrNil_WriteNothing(t *testing.T) {
	// A message of the wrong concrete type for each projector.
	wrong := &creationv1.AtomUpdated{AtomId: "should-be-ignored"}

	t.Run("dailyDose_wrongType", func(t *testing.T) {
		out := map[string]any{}
		projectDailyDoseServed(wrong, out)
		assert.Empty(t, out)
	})
	t.Run("dailyDose_typedNil", func(t *testing.T) {
		out := map[string]any{}
		var nilMsg *consumptionv1.DailyDoseServed
		projectDailyDoseServed(nilMsg, out)
		assert.Empty(t, out)
	})
	t.Run("atomPublished_wrongType", func(t *testing.T) {
		out := map[string]any{}
		projectAtomPublished(wrong, out)
		assert.Empty(t, out)
	})
	t.Run("atomUpdated_wrongType", func(t *testing.T) {
		out := map[string]any{}
		// AtomPublished is the wrong type for the AtomUpdated projector.
		projectAtomUpdated(&creationv1.AtomPublished{AtomId: "x"}, out)
		assert.Empty(t, out)
	})
	t.Run("atomCreated_wrongType", func(t *testing.T) {
		out := map[string]any{}
		projectAtomCreated(wrong, out)
		assert.Empty(t, out)
	})
}

// -----------------------------------------------------------------------------
// projectDailyDoseServed — full field projection incl. IMDA envelope fields,
// served_at RFC3339-nano formatting, message, size, and entries (atom_ids +
// per-entry dose_reason int).
// -----------------------------------------------------------------------------

func TestProjectDailyDoseServed_AllFields(t *testing.T) {
	served := time.Date(2026, 5, 16, 9, 30, 0, 123456789, time.UTC)
	m := &consumptionv1.DailyDoseServed{
		Envelope: &commonv1.EventEnvelope{
			TenantId:           "tenant-acme",
			Gcid:               "gcid-learner",
			Traceparent:        "00-trace-span-01",
			ChoraImdaDimension: "fairness_and_human_oversight",
			ImdaLifecycleStage: "runtime",
		},
		LearnerGcid: "gcid-learner",
		Size:        2,
		Message:     "your daily dose",
		ServedAt:    timestamppb.New(served),
		Entries: []*consumptionv1.DailyDoseEntry{
			{AtomId: "atom-A", Topic: "git", Title: "Branches", DoseReason: consumptionv1.DoseReason(1)},
			// An entry with an empty atom_id must be skipped from atom_ids but
			// still appear in entries (the projector only filters atom_ids).
			{AtomId: "", Topic: "vcs", Title: "Merge", DoseReason: consumptionv1.DoseReason(2)},
		},
	}

	out := map[string]any{}
	projectDailyDoseServed(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "fairness_and_human_oversight", out["chora_imda_dimension"])
	assert.Equal(t, "runtime", out["imda_lifecycle_stage"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, 2, out["size"])
	assert.Equal(t, "your daily dose", out["message"])

	// served_at uses the RFC3339 variant that keeps sub-second precision +
	// keeps trailing-zero trimming (".999999999"). The .999999999 layout drops
	// trailing zeros; 123456789ns has none to drop here.
	assert.Equal(t, "2026-05-16T09:30:00.123456789Z", out["served_at"])

	// atom_ids excludes the empty-id entry; entries keeps both with int reason.
	require.Equal(t, []string{"atom-A"}, out["atom_ids"])
	entries, ok := out["entries"].([]map[string]any)
	require.True(t, ok, "entries should be []map[string]any")
	require.Len(t, entries, 2)
	assert.Equal(t, "atom-A", entries[0]["atom_id"])
	assert.Equal(t, "git", entries[0]["topic"])
	assert.Equal(t, "Branches", entries[0]["title"])
	assert.Equal(t, 1, entries[0]["dose_reason"])
	assert.Equal(t, "", entries[1]["atom_id"])
	assert.Equal(t, 2, entries[1]["dose_reason"])
}

// TestProjectDailyDoseServed_EmptyMessage_OmitsOptionalKeys characterizes the
// guard clauses: zero-value fields (nil envelope, empty learner_gcid, size 0,
// nil served_at, no entries) leave their keys ABSENT from the map rather than
// writing zero values.
func TestProjectDailyDoseServed_EmptyMessage_OmitsOptionalKeys(t *testing.T) {
	out := map[string]any{}
	projectDailyDoseServed(&consumptionv1.DailyDoseServed{}, out)

	assert.NotContains(t, out, "tenant_id")
	assert.NotContains(t, out, "learner_gcid")
	assert.NotContains(t, out, "size")
	assert.NotContains(t, out, "message")
	assert.NotContains(t, out, "served_at")
	assert.NotContains(t, out, "atom_ids")
	assert.NotContains(t, out, "entries")
}

// -----------------------------------------------------------------------------
// projectAtomCreated — non-MCQ atom_type, topic_tags, difficulty, and the
// guard clauses for empty/zero fields. The round-trip test only covers the MCQ
// + full-field path; this covers the essay branch + the difficulty==0 /
// atom_type=="" omission branches.
// -----------------------------------------------------------------------------

func TestProjectAtomCreated_EssayWithTopicTags(t *testing.T) {
	m := &creationv1.AtomCreated{
		Envelope: &commonv1.EventEnvelope{
			TenantId:           "tenant-x",
			Gcid:               "gcid-author",
			ChoraImdaDimension: "transparency",
		},
		AtomId:       "atom-essay-1",
		CourseId:     "course-1",
		Title:        "Reflect on VCS",
		AuthorGcid:   "gcid-author",
		Difficulty:   5,
		QuestionType: creationv1.AtomType_ATOM_TYPE_ESSAY,
		TopicNodeIds: []string{"node-1", "node-2"},
		// No correct_option_id / answer_count for an essay (zero values).
	}

	out := map[string]any{}
	projectAtomCreated(m, out)

	assert.Equal(t, "atom-essay-1", out["atom_id"])
	assert.Equal(t, "course-1", out["course_id"])
	assert.Equal(t, "transparency", out["chora_imda_dimension"])
	assert.Equal(t, "essay", out["atom_type"])
	assert.Equal(t, 5, out["difficulty"])
	assert.Equal(t, []string{"node-1", "node-2"}, out["topic_tags"])
	// Essay carries no MCQ answer key — those keys must be absent, not zero.
	assert.NotContains(t, out, "correct_option_id")
	assert.NotContains(t, out, "answer_count")
}

// TestProjectAtomCreated_EmptyMessage_OmitsOptionalKeys characterizes the
// all-zero AtomCreated: difficulty 0, UNSPECIFIED type (atom_type ""), no
// topic ids — every optional key absent.
func TestProjectAtomCreated_EmptyMessage_OmitsOptionalKeys(t *testing.T) {
	out := map[string]any{}
	projectAtomCreated(&creationv1.AtomCreated{}, out)

	assert.NotContains(t, out, "atom_id")
	assert.NotContains(t, out, "course_id")
	assert.NotContains(t, out, "difficulty") // Difficulty 0 omitted.
	assert.NotContains(t, out, "atom_type")  // UNSPECIFIED -> "" omitted.
	assert.NotContains(t, out, "topic_tags") // No ids.
	assert.NotContains(t, out, "correct_option_id")
	assert.NotContains(t, out, "answer_count")
}

// -----------------------------------------------------------------------------
// projectAtomPublished / projectAtomUpdated — guard-clause + IMDA dimension
// branches not hit by the happy-path round-trip tests.
// -----------------------------------------------------------------------------

func TestProjectAtomPublished_NilEnvelope_OmitsEnvelopeKeys(t *testing.T) {
	out := map[string]any{}
	projectAtomPublished(&creationv1.AtomPublished{AtomId: "a1"}, out)

	assert.Equal(t, "a1", out["atom_id"])
	assert.NotContains(t, out, "tenant_id")
	assert.NotContains(t, out, "gcid")
	assert.NotContains(t, out, "traceparent")
	assert.NotContains(t, out, "chora_imda_dimension")
	// author_gcid + title empty -> absent.
	assert.NotContains(t, out, "author_gcid")
	assert.NotContains(t, out, "title")
}

func TestProjectAtomPublished_ImdaDimension(t *testing.T) {
	out := map[string]any{}
	projectAtomPublished(&creationv1.AtomPublished{
		Envelope: &commonv1.EventEnvelope{ChoraImdaDimension: "safety_and_robustness"},
		AtomId:   "a2",
	}, out)
	assert.Equal(t, "safety_and_robustness", out["chora_imda_dimension"])
}

// CHO-1968 — projectAtomPublished must also project the PLAYABILITY fields
// (status / question_type / answer key / has_open_ended_question) the atom_index
// flip subscriber reads.
func TestProjectAtomPublished_PlayabilityFields_MCQ(t *testing.T) {
	out := map[string]any{}
	projectAtomPublished(&creationv1.AtomPublished{
		AtomId:          "atom-pub-mcq",
		Status:          creationv1.AtomStatus_ATOM_STATUS_PUBLISHED,
		QuestionType:    creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE,
		CorrectOptionId: "opt_1",
		AnswerCount:     2,
	}, out)

	assert.Equal(t, "published", out["status"])
	assert.Equal(t, "mcq", out["atom_type"])
	assert.Equal(t, "opt_1", out["correct_option_id"])
	assert.Equal(t, 2, out["answer_count"])
	// MCQ -> not open-ended; the false bool is omitted (omit-zero convention).
	assert.NotContains(t, out, "has_open_ended_question")
}

func TestProjectAtomPublished_PlayabilityFields_OpenEnded(t *testing.T) {
	out := map[string]any{}
	projectAtomPublished(&creationv1.AtomPublished{
		AtomId:               "atom-pub-oe",
		Status:               creationv1.AtomStatus_ATOM_STATUS_PUBLISHED,
		QuestionType:         creationv1.AtomType_ATOM_TYPE_ESSAY,
		HasOpenEndedQuestion: true,
	}, out)

	assert.Equal(t, "published", out["status"])
	assert.Equal(t, "essay", out["atom_type"])
	assert.Equal(t, true, out["has_open_ended_question"])
	assert.NotContains(t, out, "correct_option_id") // OE carries no MCQ key
}

func TestAtomStatusToDomain_AllEnumValues(t *testing.T) {
	cases := map[creationv1.AtomStatus]string{
		creationv1.AtomStatus_ATOM_STATUS_UNSPECIFIED: "",
		creationv1.AtomStatus_ATOM_STATUS_DRAFT:       "draft",
		creationv1.AtomStatus_ATOM_STATUS_PUBLISHED:   "published",
		creationv1.AtomStatus_ATOM_STATUS_ARCHIVED:    "archived",
	}
	for in, want := range cases {
		if got := atomStatusToDomain(in); got != want {
			t.Errorf("atomStatusToDomain(%v) = %q; want %q", in, got, want)
		}
	}
}

func TestProjectAtomUpdated_NoChangedFields_OmitsKey(t *testing.T) {
	out := map[string]any{}
	projectAtomUpdated(&creationv1.AtomUpdated{
		Envelope:   &commonv1.EventEnvelope{ChoraImdaDimension: "accountability"},
		AtomId:     "a3",
		AuthorGcid: "gcid-author",
		// ChangedFields nil -> changed_fields key omitted.
	}, out)
	assert.Equal(t, "a3", out["atom_id"])
	assert.Equal(t, "gcid-author", out["author_gcid"])
	assert.Equal(t, "accountability", out["chora_imda_dimension"])
	assert.NotContains(t, out, "changed_fields")
}

// -----------------------------------------------------------------------------
// hydrateFromAttrs — non-destructive merge invariants beyond the happy path.
// -----------------------------------------------------------------------------

func TestHydrateFromAttrs_EarlyReturns(t *testing.T) {
	// nil out -> no panic, nothing to do.
	hydrateFromAttrs(nil, map[string]string{"tenant_id": "t"})

	// empty attrs -> out untouched.
	out := map[string]any{"keep": "me"}
	hydrateFromAttrs(out, map[string]string{})
	assert.Equal(t, map[string]any{"keep": "me"}, out)

	// nil attrs -> out untouched.
	hydrateFromAttrs(out, nil)
	assert.Equal(t, map[string]any{"keep": "me"}, out)
}

func TestHydrateFromAttrs_SkipsEmptyAttrValue(t *testing.T) {
	out := map[string]any{}
	// Attr present but empty-string value -> NOT copied in.
	hydrateFromAttrs(out, map[string]string{"tenant_id": ""})
	assert.NotContains(t, out, "tenant_id")
}

func TestHydrateFromAttrs_DoesNotClobberTypedNonStringValue(t *testing.T) {
	// A typed (non-string) existing value is authoritative — e.g. a parsed
	// time under "occurred_at" must NOT be overwritten by the RFC3339 string
	// from attrs.
	parsed := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)
	out := map[string]any{"occurred_at": parsed}
	hydrateFromAttrs(out, map[string]string{"occurred_at": "2099-01-01T00:00:00Z"})
	assert.Equal(t, parsed, out["occurred_at"], "typed time value must survive hydration")
}

func TestHydrateFromAttrs_FillsEmptyStringValue(t *testing.T) {
	// An existing empty-string value IS filled in from attrs.
	out := map[string]any{"tenant_id": ""}
	hydrateFromAttrs(out, map[string]string{"tenant_id": "tenant-acme"})
	assert.Equal(t, "tenant-acme", out["tenant_id"])

	// Whereas a non-empty existing string wins over attrs.
	out2 := map[string]any{"gcid": "from-payload"}
	hydrateFromAttrs(out2, map[string]string{"gcid": "from-attrs"})
	assert.Equal(t, "from-payload", out2["gcid"])
}

// -----------------------------------------------------------------------------
// DecodePayloadMapWithAttrs — both-paths-fail error branch. A binary-registered
// topic handed bytes that are neither valid proto NOR valid JSON must fail loud
// with a wrapped error (the comment promises caller-Nack -> retry -> DLQ).
// -----------------------------------------------------------------------------

func TestDecodePayloadMapWithAttrs_NeitherBinaryNorJSON_FailsLoud(t *testing.T) {
	// Garbage that proto.Unmarshal rejects for AtomCreated AND json rejects.
	// A bare 0xFF byte is invalid JSON and invalid as the registered proto.
	garbage := []byte{0xff, 0xfe, 0xfd}

	_, err := DecodePayloadMapWithAttrs("chora.creation.atom.created.v1", garbage, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither binary-decodable nor JSON-decodable")
	assert.Contains(t, err.Error(), "chora.creation.atom.created.v1")
}

func TestDecodePayloadMapWithAttrs_UnknownTopicGarbage_FailsLoud(t *testing.T) {
	// Unknown topic + non-JSON bytes -> straight to JSON, which fails -> error.
	_, err := DecodePayloadMapWithAttrs("chora.consumption.totally_unknown.v1", []byte{0x00, 0x01}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither binary-decodable nor JSON-decodable")
}

// TestDecodePayloadMapWithAttrs_JSONNullThenHydrate covers the `out == nil`
// re-init branch: JSON literal `null` unmarshals into a nil map; the decoder
// must re-init it to an empty map so hydration can still write attribute keys.
func TestDecodePayloadMapWithAttrs_JSONNull_ReinitsMapAndHydrates(t *testing.T) {
	got, err := DecodePayloadMapWithAttrs(
		"chora.consumption.unknown_for_null.v1",
		[]byte("null"),
		map[string]string{"tenant_id": "tenant-acme"},
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tenant-acme", got["tenant_id"])
}

// -----------------------------------------------------------------------------
// warnBinaryFallback / warnUnknownTopic — one-shot dedup. The second call for
// the same topic must take the early-return (already-warned) branch.
// -----------------------------------------------------------------------------

func TestWarnBinaryFallback_OneShotPerTopic(t *testing.T) {
	const topic = "cover.warn.binary.v1"
	// Reset shared state for determinism under -count/-race.
	warnedFallbackMu.Lock()
	delete(warnedFallback, topic)
	warnedFallbackMu.Unlock()

	warnBinaryFallback(topic, assertErr{}) // first call -> records.
	warnBinaryFallback(topic, assertErr{}) // second call -> early return.

	warnedFallbackMu.Lock()
	recorded := warnedFallback[topic]
	warnedFallbackMu.Unlock()
	assert.True(t, recorded, "topic should be marked warned after first call")
}

func TestWarnUnknownTopic_OneShotPerTopic(t *testing.T) {
	const topic = "cover.warn.unknown.v1"
	warnedUnknownMu.Lock()
	delete(warnedUnknown, topic)
	warnedUnknownMu.Unlock()

	warnUnknownTopic(topic) // first -> records.
	warnUnknownTopic(topic) // second -> early return.

	warnedUnknownMu.Lock()
	recorded := warnedUnknown[topic]
	warnedUnknownMu.Unlock()
	assert.True(t, recorded)
}

// assertErr is a trivial error used to exercise warnBinaryFallback's %v format.
type assertErr struct{}

func (assertErr) Error() string { return "synthetic-binary-decode-failure" }

// -----------------------------------------------------------------------------
// chat_turn_completed.v1 — registered for binary but its proto bindings have
// not landed, so its decode closure returns errUnsupportedYet and project is
// nil. The decoder must therefore route to the JSON fallback path (NOT lose the
// message). This characterizes the documented transition-window behavior and
// exercises the errUnsupportedYet sentinel branch.
// -----------------------------------------------------------------------------

func TestDecode_ChatTurnCompleted_UnsupportedYet_FallsBackToJSON(t *testing.T) {
	const topic = "chora.consumption.companion.chat_turn_completed.v1"
	// JSON-shape payload (producer not yet flipped / bindings pending).
	bz := []byte(`{"companion_id":"fam-7","turn_text":"hello"}`)

	got, err := DecodePayloadMap(topic, bz)
	require.NoError(t, err, "registered-but-pending topic must fall back to JSON, not error")
	assert.Equal(t, "fam-7", got["companion_id"])
	assert.Equal(t, "hello", got["turn_text"])
}
