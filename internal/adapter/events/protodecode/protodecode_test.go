// protodecode_test verifies the symmetric inverse of protomarshal — given the
// canonical binary protobuf wire bytes emitted by the producer (chora-
// consumption or any peer flipping to binary), DecodePayloadMap returns a
// map[string]any with snake_case keys matching the topic's schema.
//
// JSON fallback is exercised for topics that have not yet flipped to binary
// (the chora.consumption.atom_session.completed.v1 topic still emits JSON at
// the time this package landed; see protomarshal.MarshalPayload switch).
package protodecode_test

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protodecode"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// TestDecode_DailyDoseServed_Binary verifies that the bytes produced by
// protomarshal.MarshalPayload round-trip through DecodePayloadMap and surface
// the canonical snake_case fields the existing JSON-decoded handler reads.
func TestDecode_DailyDoseServed_Binary(t *testing.T) {
	t0 := time.Date(2026, 5, 16, 9, 30, 0, 0, time.UTC)
	env := protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000abc",
		IdempotencyKey: "idemp-wire-1",
		TenantID:       "tenant-acme",
		GCID:           "gcid-phyllis",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"learner_gcid": "gcid-phyllis",
		"atom_ids":     []string{"atom-A", "atom-B"},
		"size":         2,
		"served_at":    t0,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	got, err := protodecode.DecodePayloadMap("chora.consumption.daily_dose.served.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap binary: %v", err)
	}
	if v, _ := got["learner_gcid"].(string); v != "gcid-phyllis" {
		t.Errorf("learner_gcid = %q, want gcid-phyllis", v)
	}
}

// TestDecode_AtomSessionCompleted_JSON exercises the JSON fallback path. The
// atom_session.completed.v1 topic still publishes JSON (no entry in
// protomarshal switch), so the decoder must accept JSON bytes too.
func TestDecode_AtomSessionCompleted_JSON(t *testing.T) {
	payload := map[string]any{
		"session_id":   "sess-1",
		"atom_id":      "atom-1",
		"learner_gcid": "gcid-phyllis",
		"tenant_id":    "tenant-acme",
		"is_correct":   true,
		"review_due":   false,
	}
	bz, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	got, err := protodecode.DecodePayloadMap("chora.consumption.atom_session.completed.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap json: %v", err)
	}
	if v, _ := got["session_id"].(string); v != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", v)
	}
	if v, _ := got["learner_gcid"].(string); v != "gcid-phyllis" {
		t.Errorf("learner_gcid = %q, want gcid-phyllis", v)
	}
	if v, _ := got["is_correct"].(bool); !v {
		t.Errorf("is_correct = %v, want true", v)
	}
}

// TestDecode_EmptyPayload_FailsLoud asserts the decoder fails loud on
// zero-length input rather than silently returning an empty map.
func TestDecode_EmptyPayload_FailsLoud(t *testing.T) {
	if _, err := protodecode.DecodePayloadMap("chora.consumption.daily_dose.served.v1", nil); err == nil {
		t.Fatal("DecodePayloadMap(nil) expected error, got nil")
	}
	if _, err := protodecode.DecodePayloadMap("chora.consumption.daily_dose.served.v1", []byte{}); err == nil {
		t.Fatal("DecodePayloadMap(empty) expected error, got nil")
	}
}

// TestDecode_UnknownTopic_FallsBackToJSON ensures forward-compat. A topic
// without a registered binary decoder must fall through to json.Unmarshal so
// the existing JSON-flow consumers keep working during the binary migration.
func TestDecode_UnknownTopic_FallsBackToJSON(t *testing.T) {
	payload := map[string]any{"foo": "bar"}
	bz, _ := json.Marshal(payload)

	got, err := protodecode.DecodePayloadMap("chora.consumption.unknown_topic.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap unknown_topic: %v", err)
	}
	if v, _ := got["foo"].(string); v != "bar" {
		t.Errorf("foo = %q, want bar", v)
	}
}

// TestDecode_BinaryThenJSONFallbackOnFail asserts that for a topic registered
// for binary, if the bytes are NOT valid binary (i.e. producer still emits
// JSON during transition), DecodePayloadMap falls back to JSON. The handler
// MUST NOT lose messages during the producer-side flip.
func TestDecode_BinaryRegisteredTopic_AcceptsJSONFallback(t *testing.T) {
	// daily_dose.served.v1 is registered as binary. But emit JSON to simulate
	// a producer that hasn't flipped yet — the decoder must still succeed via
	// JSON fallback (with a one-shot WARN log).
	payload := map[string]any{
		"learner_gcid": "gcid-fallback",
		"atom_ids":     []string{"atom-x"},
		"size":         1,
	}
	bz, _ := json.Marshal(payload)

	got, err := protodecode.DecodePayloadMap("chora.consumption.daily_dose.served.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap json fallback for binary topic: %v", err)
	}
	if v, _ := got["learner_gcid"].(string); v != "gcid-fallback" {
		t.Errorf("learner_gcid = %q, want gcid-fallback", v)
	}
}

// -----------------------------------------------------------------------------
// DecodePayloadMapWithAttrs — envelope-field hydration from Pub/Sub attributes.
//
// Per the gap surfaced by the #47 inbox-encoding audit: on the JSON-fallback
// path the publisher stamps event_id / tenant_id / owner_gcid into the Pub/Sub
// message attributes — NOT into the payload bytes. So when the legacy JSON
// payload lacks those fields, the decoded map returned empty for them and
// downstream handlers that read raw["tenant_id"] / raw["event_id"] /
// raw["owner_gcid"] got zero values.
//
// Fix: callers that have access to the Pub/Sub message can pass attributes in.
// The decoder hydrates missing envelope fields from attributes so handlers
// observe the same shape regardless of binary-or-JSON path.
// -----------------------------------------------------------------------------

func TestDecode_WithAttrs_JSONFallback_HydratesEventIDTenantOwner(t *testing.T) {
	// Legacy JSON-only producer: payload bytes carry domain fields only;
	// envelope fields live in msg.Attributes.
	payload := map[string]any{
		"companion_id":    "fam-1",
		"stage_from":      2,
		"stage_to":        3,
		"stage_from_name": "fledgling",
		"stage_to_name":   "awakened",
	}
	bz, _ := json.Marshal(payload)

	attrs := map[string]string{
		"event_id":   "evt-001",
		"tenant_id":  "tenant-acme",
		"gcid":       "gcid-phyllis",
		"owner_gcid": "gcid-phyllis",
	}

	got, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.consumption.companion.stage_up.v1", bz, attrs,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs: %v", err)
	}
	if v, _ := got["event_id"].(string); v != "evt-001" {
		t.Errorf("event_id = %q, want evt-001", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-acme" {
		t.Errorf("tenant_id = %q, want tenant-acme", v)
	}
	if v, _ := got["owner_gcid"].(string); v != "gcid-phyllis" {
		t.Errorf("owner_gcid = %q, want gcid-phyllis", v)
	}
	if v, _ := got["gcid"].(string); v != "gcid-phyllis" {
		t.Errorf("gcid = %q, want gcid-phyllis", v)
	}
	// Domain fields preserved.
	if v, _ := got["companion_id"].(string); v != "fam-1" {
		t.Errorf("companion_id = %q, want fam-1", v)
	}
}

func TestDecode_WithAttrs_DoesNotOverwritePayloadValues(t *testing.T) {
	// Payload-side values take precedence over attribute-side values when
	// both are present. Attribute hydration ONLY fills in zero/missing keys.
	payload := map[string]any{
		"event_id":     "from-payload",
		"tenant_id":    "from-payload",
		"owner_gcid":   "from-payload",
		"companion_id": "fam-1",
	}
	bz, _ := json.Marshal(payload)

	attrs := map[string]string{
		"event_id":   "from-attrs",
		"tenant_id":  "from-attrs",
		"owner_gcid": "from-attrs",
	}

	got, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.consumption.companion.hatched.v1", bz, attrs,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs: %v", err)
	}
	if v, _ := got["event_id"].(string); v != "from-payload" {
		t.Errorf("event_id = %q, want from-payload (payload wins)", v)
	}
	if v, _ := got["tenant_id"].(string); v != "from-payload" {
		t.Errorf("tenant_id = %q, want from-payload (payload wins)", v)
	}
	if v, _ := got["owner_gcid"].(string); v != "from-payload" {
		t.Errorf("owner_gcid = %q, want from-payload (payload wins)", v)
	}
}

func TestDecode_WithAttrs_NilAttrsBackwardCompatible(t *testing.T) {
	// Calling WithAttrs with nil attrs MUST behave identically to
	// DecodePayloadMap — no panic, no surprises.
	payload := map[string]any{"companion_id": "fam-1"}
	bz, _ := json.Marshal(payload)

	got, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.consumption.companion.hatched.v1", bz, nil,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs(nil attrs): %v", err)
	}
	if v, _ := got["companion_id"].(string); v != "fam-1" {
		t.Errorf("companion_id = %q, want fam-1", v)
	}
	// No event_id / tenant_id / owner_gcid present (attrs nil + payload silent).
	if _, ok := got["event_id"]; ok {
		t.Errorf("event_id should be absent when no attrs supplied")
	}
}

func TestDecode_WithAttrs_BinaryPathStillWorks(t *testing.T) {
	// Binary-decoded daily_dose still works through the WithAttrs entry-point —
	// the binary projector already populated tenant_id + gcid from the nested
	// envelope, so attribute hydration is a no-op in this case.
	t0 := time.Date(2026, 5, 16, 9, 30, 0, 0, time.UTC)
	env := protomarshal.Envelope{
		EventID:        "01971a90-bin-7000-8000-000000000abc",
		IdempotencyKey: "idemp-bin-1",
		TenantID:       "tenant-bin",
		GCID:           "gcid-bin",
		OccurredAt:     t0,
		PublishedAt:    t0.Add(time.Millisecond),
		Traceparent:    "00-aa-bb-01",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	payload := map[string]any{
		"learner_gcid": "gcid-bin",
		"atom_ids":     []string{"atom-A"},
		"size":         1,
		"served_at":    t0,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	attrs := map[string]string{
		"event_id":   "should-not-overwrite-projector",
		"tenant_id":  "should-not-overwrite-projector",
		"owner_gcid": "owner-from-attrs",
	}

	got, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.consumption.daily_dose.served.v1", bz, attrs,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs binary: %v", err)
	}
	// Projector already wrote tenant_id from envelope — must NOT overwrite.
	if v, _ := got["tenant_id"].(string); v != "tenant-bin" {
		t.Errorf("tenant_id = %q, want tenant-bin (binary projector wins)", v)
	}
	// owner_gcid wasn't in the binary projector — attribute hydration fills it.
	if v, _ := got["owner_gcid"].(string); v != "owner-from-attrs" {
		t.Errorf("owner_gcid = %q, want owner-from-attrs", v)
	}
	// event_id wasn't in the projector — attribute hydration fills it.
	if v, _ := got["event_id"].(string); v != "should-not-overwrite-projector" {
		t.Errorf("event_id = %q, want should-not-overwrite-projector", v)
	}
}

// ---------------------------------------------------------------------------
// KG invalidation binary decode tests — chora.creation.atom.{published,updated}.v1
// ---------------------------------------------------------------------------

// TestDecode_AtomPublished_Binary verifies the binary decoder for
// chora.creation.atom.published.v1 round-trips correctly. Schema Registry
// has this as chora-creation-atom-published-v2; the topic is v1. The
// generated type is creationv1.AtomPublished in gen/go/chora/creation/v1/.
func TestDecode_AtomPublished_Binary(t *testing.T) {
	msg := &creationv1.AtomPublished{
		Envelope: &commonv1.EventEnvelope{
			EventId:        "evt-published-01",
			IdempotencyKey: "idemp-pub-01",
			TenantId:       "tenant-acme",
			Gcid:           "gcid-author",
			Traceparent:    "00-abc-def-01",
			SourceProject:  "chora-content",
			SourceService:  "chora-creation",
			SchemaVersion:  2,
		},
		AtomId:     "atom-pub-01",
		AuthorGcid: "gcid-author",
		Title:      "Test Atom Title",
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal AtomPublished: %v", err)
	}

	got, err := protodecode.DecodePayloadMap("chora.creation.atom.published.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap atom.published.v1: %v", err)
	}
	if v, _ := got["atom_id"].(string); v != "atom-pub-01" {
		t.Errorf("atom_id = %q, want atom-pub-01", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-acme" {
		t.Errorf("tenant_id = %q, want tenant-acme", v)
	}
	if v, _ := got["author_gcid"].(string); v != "gcid-author" {
		t.Errorf("author_gcid = %q, want gcid-author", v)
	}
	if v, _ := got["traceparent"].(string); v != "00-abc-def-01" {
		t.Errorf("traceparent = %q, want 00-abc-def-01", v)
	}
}

// TestDecode_AtomCreated_Binary verifies the binary decoder for
// chora.creation.atom.created.v1 round-trips, INCLUDING course_id — the field
// that the JSON→binary flip (Task #33) dropped and that chora-consumption's
// atom_index projection + LearningPath bootstrap key on (OPEN-1, 2026-06-01).
func TestDecode_AtomCreated_Binary(t *testing.T) {
	const courseID = "019e58ea-5ae0-7b74-9f1d-73a92622ab90"
	msg := &creationv1.AtomCreated{
		Envelope: &commonv1.EventEnvelope{
			EventId:       "evt-created-01",
			TenantId:      "tenant-acme",
			Gcid:          "gcid-author",
			Traceparent:   "00-abc-def-01",
			SchemaVersion: 1,
		},
		AtomId:          "atom-created-01",
		CourseId:        courseID,
		QuestionType:    creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE,
		Title:           "VCS Basics",
		Difficulty:      3,
		CorrectOptionId: "opt-b",
		AnswerCount:     4,
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal AtomCreated: %v", err)
	}

	got, err := protodecode.DecodePayloadMap("chora.creation.atom.created.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap atom.created.v1: %v", err)
	}
	if v, _ := got["course_id"].(string); v != courseID {
		t.Errorf("course_id = %q; want %q (drives ListByCourse bootstrap)", v, courseID)
	}
	if v, _ := got["atom_id"].(string); v != "atom-created-01" {
		t.Errorf("atom_id = %q; want atom-created-01", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-acme" {
		t.Errorf("tenant_id = %q; want tenant-acme", v)
	}
	if v, _ := got["title"].(string); v != "VCS Basics" {
		t.Errorf("title = %q; want VCS Basics", v)
	}
	if v, _ := got["atom_type"].(string); v != "mcq" {
		t.Errorf("atom_type = %q; want mcq (enum ATOM_TYPE_MULTIPLE_CHOICE -> domain string)", v)
	}
	// CHO-1627: identity-based MCQ grading — the answer key flows as
	// correct_option_id (string), with answer_count as projection metadata.
	if v, _ := got["correct_option_id"].(string); v != "opt-b" {
		t.Errorf("correct_option_id = %q; want opt-b (drives identity-based MCQ grading)", v)
	}
	if v, _ := got["answer_count"].(int); v != 4 {
		t.Errorf("answer_count = %v; want 4", got["answer_count"])
	}
}

// TestDecode_CourseCreated_Binary guards the CHO-2059 course-name projection:
// chora.delivery.course.created.v1 is a BINARY (protobuf) topic, so the payload
// MUST decode via the registered binary decoder — a JSON-only handler would fail
// on every real event (the exact gap live-verify caught). course_id + title feed
// the course_directory projection.
func TestDecode_CourseCreated_Binary(t *testing.T) {
	const courseID = "05000000-0000-7000-8000-0000000c5301"
	msg := &deliveryv1.CourseCreated{
		Envelope: &commonv1.EventEnvelope{
			EventId:       "evt-course-01",
			TenantId:      "tenant-acme",
			Traceparent:   "00-abc-def-01",
			SchemaVersion: 1,
		},
		CourseId: courseID,
		Title:    "Certified ScrumMaster (CSM) Prep",
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal CourseCreated: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.course.created.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap course.created.v1 (binary MUST decode, not JSON-fallback): %v", err)
	}
	if v, _ := got["course_id"].(string); v != courseID {
		t.Errorf("course_id = %q; want %q", v, courseID)
	}
	if v, _ := got["title"].(string); v != "Certified ScrumMaster (CSM) Prep" {
		t.Errorf("title = %q; want the course title", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-acme" {
		t.Errorf("tenant_id = %q; want tenant-acme", v)
	}
}

// TestDecode_AtomUpdated_Binary verifies the binary decoder for
// chora.creation.atom.updated.v1 (canonical topic name confirmed 2026-05-26;
// schema chora-creation-atom-updated-v2; generated type creationv1.AtomUpdated).
func TestDecode_AtomUpdated_Binary(t *testing.T) {
	msg := &creationv1.AtomUpdated{
		Envelope: &commonv1.EventEnvelope{
			EventId:        "evt-updated-01",
			IdempotencyKey: "idemp-upd-01",
			TenantId:       "tenant-acme",
			Gcid:           "gcid-author",
			Traceparent:    "00-xyz-uvw-01",
			SourceProject:  "chora-content",
			SourceService:  "chora-creation",
			SchemaVersion:  2,
		},
		AtomId:        "atom-upd-01",
		AuthorGcid:    "gcid-author",
		ChangedFields: []string{"stem", "difficulty"},
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal AtomUpdated: %v", err)
	}

	got, err := protodecode.DecodePayloadMap("chora.creation.atom.updated.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap atom.updated.v1: %v", err)
	}
	if v, _ := got["atom_id"].(string); v != "atom-upd-01" {
		t.Errorf("atom_id = %q, want atom-upd-01", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-acme" {
		t.Errorf("tenant_id = %q, want tenant-acme", v)
	}
	if v, _ := got["author_gcid"].(string); v != "gcid-author" {
		t.Errorf("author_gcid = %q, want gcid-author", v)
	}
	if fields, ok := got["changed_fields"].([]string); !ok || len(fields) != 2 {
		t.Errorf("changed_fields = %v, want [stem difficulty]", got["changed_fields"])
	}
}

// TestDecode_AtomPublished_JSONFallback ensures the JSON fallback path works
// for atom.published.v1 during the transition window when producers may still
// send JSON.
func TestDecode_AtomPublished_JSONFallback(t *testing.T) {
	payload := map[string]any{
		"atom_id":     "atom-json-01",
		"tenant_id":   "tenant-acme",
		"author_gcid": "gcid-author",
	}
	bz, _ := json.Marshal(payload)

	got, err := protodecode.DecodePayloadMap("chora.creation.atom.published.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap atom.published.v1 JSON fallback: %v", err)
	}
	if v, _ := got["atom_id"].(string); v != "atom-json-01" {
		t.Errorf("atom_id = %q, want atom-json-01", v)
	}
}

// TestDecode_AtomUpdated_JSONFallback ensures the JSON fallback path works
// for atom.updated.v1.
func TestDecode_AtomUpdated_JSONFallback(t *testing.T) {
	payload := map[string]any{
		"atom_id":   "atom-json-02",
		"tenant_id": "tenant-acme",
	}
	bz, _ := json.Marshal(payload)

	got, err := protodecode.DecodePayloadMap("chora.creation.atom.updated.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap atom.updated.v1 JSON fallback: %v", err)
	}
	if v, _ := got["atom_id"].(string); v != "atom-json-02" {
		t.Errorf("atom_id = %q, want atom-json-02", v)
	}
}

func TestDecode_WithAttrs_CompanionMilestoneJSONPath(t *testing.T) {
	// Verify the JSON-fallback path through WithAttrs for each of the 4
	// companion milestone topics now registered in protomarshal — until the
	// producer side fully flips, the JSON-fallback path will hit them.
	cases := []string{
		"chora.consumption.companion.stage_up.v1",
		"chora.consumption.companion.breed_revealed.v1",
		"chora.consumption.companion.hatched.v1",
		"chora.consumption.companion.source_revelation.v1",
	}
	for _, topic := range cases {
		t.Run(topic, func(t *testing.T) {
			payload := map[string]any{"companion_id": "fam-1"}
			bz, _ := json.Marshal(payload)
			attrs := map[string]string{
				"event_id":   "evt-" + topic,
				"tenant_id":  "tenant-acme",
				"owner_gcid": "gcid-phyllis",
			}
			got, err := protodecode.DecodePayloadMapWithAttrs(topic, bz, attrs)
			if err != nil {
				t.Fatalf("DecodePayloadMapWithAttrs(%s): %v", topic, err)
			}
			if got["event_id"] != "evt-"+topic {
				t.Errorf("%s event_id = %v, want evt-%s", topic, got["event_id"], topic)
			}
			if got["tenant_id"] != "tenant-acme" {
				t.Errorf("%s tenant_id = %v", topic, got["tenant_id"])
			}
			if got["owner_gcid"] != "gcid-phyllis" {
				t.Errorf("%s owner_gcid = %v", topic, got["owner_gcid"])
			}
		})
	}
}

// -----------------------------------------------------------------------------
// CHO-2247 — chora.delivery.course.released.v1 → course_directory
// -----------------------------------------------------------------------------

// TestDecode_CourseReleased_Binary is the CHO-2247 core: course.released.v1 is
// the ONLY event the real CJ#2 authoring flow emits carrying {course_id, title}
// (course_cj2_handler.go emitCourseReleased), and it is a BINARY topic (live
// schema chora-delivery-course-released-v1). Unregistered ⇒ the payload hits the
// JSON fallback on protobuf bytes and fails on EVERY real event — the exact gap
// that left course_directory at 5 rows against 19 courses.
func TestDecode_CourseReleased_Binary(t *testing.T) {
	const courseID = "019f6966-1a0f-76a6-a824-02f7393581da"
	msg := &deliveryv1.CourseReleased{
		Envelope: &commonv1.EventEnvelope{
			EventId:       "evt-course-released-01",
			TenantId:      "tenant-mighty-mind",
			Traceparent:   "00-abc-def-01",
			SchemaVersion: 1,
		},
		CourseId:      courseID,
		Title:         "Scrum Framework Essentials",
		PriceSgdCents: 4900,
		TestSetIds:    []string{"019f6966-aaaa-7000-8000-000000000001"},
	}
	bz, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("proto.Marshal CourseReleased: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.course.released.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap course.released.v1 (binary MUST decode, not JSON-fallback): %v", err)
	}
	if v, _ := got["course_id"].(string); v != courseID {
		t.Errorf("course_id = %q; want %q", v, courseID)
	}
	if v, _ := got["title"].(string); v != "Scrum Framework Essentials" {
		t.Errorf("title = %q; want the course title", v)
	}
	if v, _ := got["tenant_id"].(string); v != "tenant-mighty-mind" {
		t.Errorf("tenant_id = %q; want tenant-mighty-mind", v)
	}
}

// TestDecode_CourseReleased_WrongDecoder_NeverErrors pins the hazard that makes
// topic-attribute routing LOAD-BEARING rather than stylistic (CHO-2247).
//
// CourseCreated and CourseReleased share field numbers 1/2/3 (envelope,
// course_id, title) and diverge at 5 (atom_path_id string vs price_sgd_cents
// int64) and 9 (capacity int32 vs test_set_ids repeated string). A wire-type
// mismatch does NOT error: protobuf's forward-compatibility rule routes such
// fields to the UNKNOWN-fields set. So the created decoder swallows ANY released
// payload silently — priced or free, test sets or not — and returns a plausible
// course_id + title purely because 2/3 happen to align today.
//
// (First cut of this test predicted the priced arm would error on a wire clash.
// It did not. The prediction was wrong and the real behaviour is worse: there is
// NO input on which the wrong decoder complains. Asserted here so the next
// reader inherits the measurement, not the guess.)
//
// Consequence: nothing downstream can detect a mis-routed message, so the topic
// attribute is the ONLY thing standing between a released event and a
// wrong-decoder read. Nobody may "simplify" course_metadata_push_handler.go's
// routing into a default-to-created fallback — and if CourseReleased is ever
// renumbered, the silent read starts returning the WRONG course's title.
func TestDecode_CourseReleased_WrongDecoder_NeverErrors(t *testing.T) {
	const courseID = "019eb059-c77f-7de5-9aae-9c72a1c37636"
	env := &commonv1.EventEnvelope{
		EventId: "evt-x", TenantId: "tenant-x", SchemaVersion: 1,
	}
	cases := []struct {
		name string
		msg  *deliveryv1.CourseReleased
	}{
		{"free_no_testsets", &deliveryv1.CourseReleased{
			Envelope: env, CourseId: courseID, Title: "Free Course",
		}},
		{"priced_with_testsets", &deliveryv1.CourseReleased{
			Envelope: env, CourseId: courseID, Title: "Priced Course",
			PriceSgdCents: 4900,
			TestSetIds:    []string{"019eb059-aaaa-7000-8000-000000000001"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bz, err := proto.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := protodecode.DecodePayloadMap("chora.delivery.course.created.v1", bz)
			if err != nil {
				t.Fatalf("wrong-decoder read ERRORED (%v) — the hazard is narrower than "+
					"documented; re-ground the comment above before relying on it", err)
			}
			// The silent read even looks CORRECT — that is precisely the danger.
			if v, _ := got["course_id"].(string); v != courseID {
				t.Errorf("course_id = %q; want %q (fields 2/3 align across both messages)", v, courseID)
			}
		})
	}
}
