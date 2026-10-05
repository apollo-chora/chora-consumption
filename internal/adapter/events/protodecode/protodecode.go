// Package protodecode is the symmetric inverse of protomarshal — it consumes
// inbound Pub/Sub message bytes and returns a snake_case map[string]any that
// callers can use the same way they used to use json.Unmarshal output.
//
// Why this package exists
// -----------------------
// Producer-side wave (task #33 / #38) flipped chora-consumption outbox
// payloads to binary protobuf so the BINARY schema encoding mode accepts
// them. Subscribers that previously used
// `json.Unmarshal(msg.Data, &m)` now receive non-JSON bytes for any topic
// in the protomarshal.MarshalPayload switch — `json: cannot unmarshal …` is
// the failure mode at runtime.
//
// Per the user direction 2026-05-16 ("if outbox needs fix, why not inbox?"),
// every subscriber that decodes wire bytes must accept both shapes during
// the producer-side flip. This package centralises that logic.
//
// Decode strategy
// ---------------
//  1. If the topic is registered for binary decoding (see binaryDecoders),
//     attempt proto.Unmarshal first. On success, project the proto message
//     into a snake_case map[string]any compatible with the legacy JSON shape.
//  2. On binary failure (or unregistered topic), fall back to json.Unmarshal.
//  3. On both-fail, return a wrapped error — caller fails loud, Pub/Sub
//     Nacks, broker retries + eventually deadletters.
//
// One-shot WARN logs surface (a) unknown topics + (b) binary-registered
// topics that succeed via the JSON fallback. The first is fine during the
// transition; the second is fine while the producer-side flip propagates
// but should fade to zero once every producer is fully cut over.
package protodecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
)

// ErrEmptyPayload is returned when the inbound bytes are empty. Callers MUST
// surface this to Pub/Sub via a Nack — the broker re-delivers, and persistent
// empty payloads route to DLQ.
var ErrEmptyPayload = errors.New("protodecode: empty payload")

// binaryDecoder takes raw wire bytes for a topic and returns the unmarshalled
// proto.Message. Implementations are pure: same bytes in, same message out.
type binaryDecoder func(payload []byte) (proto.Message, error)

// projector maps a successfully-unmarshalled proto.Message onto the
// snake_case map[string]any the legacy JSON-decoded handlers consume.
//
// The map keys MUST match the JSON-on-wire keys the publisher historically
// used so subscriber handlers do not change their key reads (e.g.
// "learner_gcid" not "learnerGcid", "atom_ids" not "atomIds", etc.).
type projector func(msg proto.Message, out map[string]any)

// binaryDecoders is the per-topic registry of binary-protobuf decoders +
// projectors. Add an entry when chora-consumption's protomarshal switch (or
// any peer producer) flips a topic to binary.
var binaryDecoders = map[string]struct {
	decode  binaryDecoder
	project projector
}{
	"chora.consumption.daily_dose.served.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m consumptionv1.DailyDoseServed
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectDailyDoseServed,
	},
	// LearnerProfile projection feed (ADR-200, WS1.c3). The producers pin their
	// wire bytes to the same proto/events schemas these gen structs come from,
	// so proto.Unmarshal into the canonical struct is the debt-free decode.
	"chora.delivery.certification.issued.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.CertIssued
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectCertIssued,
	},
	"chora.consumption.learning_path.completed.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m consumptionv1.PathCompleted
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectPathCompleted,
	},
	"chora.delivery.submission.graded.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.SubmissionGraded
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectSubmissionGraded,
	},
	"chora.delivery.enrollment.completed.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.EnrollmentCompleted
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectEnrollmentCompleted,
	},
	// BINARY (topic has a PROTOCOL_BUFFER schema — chora-delivery-course-created-v1).
	// Feeds the course_directory projection (CHO-2059 follow-up): course_id → title.
	// Unlike course.content_composed (JSON), this topic is proto-encoded, so it MUST
	// be registered or the payload hits the JSON fallback on protobuf bytes and fails
	// on every real event.
	//
	// LEGACY LANE (CHO-2247): this topic is emitted ONLY by the deprecated catalogue
	// handlers (/courses, /v1/courses). The CJ#2 authoring flow — the one that
	// produces every learner-facing course — emits course.released.v1 instead, which
	// is why course_directory held 5 rows against 19 courses. Kept registered because
	// the topic + subscription are still live; the sibling course.updated.v1 has NO
	// live topic and never fired once, so its dead producer was deleted.
	"chora.delivery.course.created.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.CourseCreated
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectCourseCreated,
	},
	// BINARY (topic has a PROTOCOL_BUFFER schema — chora-delivery-course-released-v1).
	// CHO-2247: the CJ#2 FSM terminus (AWAITING_REVIEW → PUBLISHED,
	// course_cj2_handler.go emitCourseReleased) and the ONLY event the real authoring
	// flow emits carrying {course_id, title}. course.proto already declared
	// "Consumers: chora-consumption" — the subscription was simply never provisioned,
	// so 9 successful publishes were discarded by a topic with zero subscribers.
	//
	// MUST stay registered: unregistered ⇒ JSON fallback on protobuf bytes ⇒ failure
	// on every real event. And note the wrong-decoder read NEVER errors (fields 1/2/3
	// align with CourseCreated; divergent fields land in unknown-fields) — see
	// TestDecode_CourseReleased_WrongDecoder_NeverErrors. The push handler's topic
	// routing is the only guard; do not weaken it to a default-to-created fallback.
	"chora.delivery.course.released.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.CourseReleased
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectCourseReleased,
	},
	"chora.consumption.companion.chat_turn_completed.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			// CompanionChatTurnCompleted is not yet in the generated bindings
			// at this package landing (BSR regen rate-limited per
			// protomarshal.go header). Returning ErrUnsupported here routes
			// callers to the JSON fallback path which is correct for the
			// transition window. When the gen lands, swap in a real
			// proto.Unmarshal call here.
			return nil, errUnsupportedYet
		},
		project: nil,
	},
	// chora.delivery.live_quiz_session.score_awarded.v1 — BINARY from day-1
	// (delivery's protomarshal encodes the events-flat schema, wire-compatible
	// with the generated deliveryv1 type). W3-derived classroom → mastery seam
	// (CR2-C3): consumed by the DerivedWeaknessProjector push path.
	"chora.delivery.live_quiz_session.score_awarded.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m deliveryv1.LiveQuizSessionScoreAwarded
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectLiveQuizScoreAwarded,
	},
	// Schema Registry note: topic is v1; schema is chora-creation-atom-published-v2
	// (PROTOCOL_BUFFER). Topic semver and schema semver evolve independently.
	// Generated type lives in gen/go/chora/creation/v1/atom.pb.go (AtomPublished).
	"chora.creation.atom.published.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m creationv1.AtomPublished
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectAtomPublished,
	},
	// Schema Registry note: topic is v1; schema is chora-creation-atom-updated-v2
	// (PROTOCOL_BUFFER). Canonical topic name confirmed 2026-05-26.
	// Generated type lives in gen/go/chora/creation/v1/atom.pb.go (AtomUpdated).
	"chora.creation.atom.updated.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m creationv1.AtomUpdated
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectAtomUpdated,
	},
	// chora.creation.atom.created.v1 — producer (chora-creation outbox) flipped
	// this to binary protobuf in Task #33 (2026-05-16) but the decoder was never
	// registered here, so events fell through to JSON and failed to decode →
	// atom_index never hydrated → course-scoped LearningPath bootstrap returned
	// PATH_NOT_BOOTSTRAPPED (OPEN-1). Registered 2026-06-01 alongside the proto
	// course_id field (field 11) that restores the atom→course association.
	// Generated type lives in gen/go/chora/creation/v1/atom.pb.go (AtomCreated).
	"chora.creation.atom.created.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m creationv1.AtomCreated
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectAtomCreated,
	},
	// chora.creation.collection.converted_to_study_list.v1 — ADR-233 / spec-001
	// US5 (WS-4). The topic is SCHEMA-BOUND (PROTOCOL_BUFFER), so chora-creation
	// puts protobuf BYTES on the wire. Without this entry the payload falls
	// through to the JSON fallback, json.Unmarshal fails on protobuf bytes for
	// EVERY real event, the push NACKs → DLQ, and no study list is ever built.
	// Generated type: gen/go/chora/creation/v1/collection.pb.go.
	"chora.creation.collection.converted_to_study_list.v1": {
		decode: func(payload []byte) (proto.Message, error) {
			var m creationv1.CollectionConvertedToStudyList
			if err := proto.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return &m, nil
		},
		project: projectCollectionConvertedToStudyList,
	},
}

// errUnsupportedYet is an internal sentinel used by entries whose generated
// proto bindings have not landed yet. The caller falls back to JSON.
var errUnsupportedYet = errors.New("protodecode: generated bindings pending")

// DecodePayloadMap decodes inbound Pub/Sub message bytes into a snake_case
// map[string]any compatible with the legacy json.Unmarshal flow.
//
// Decode precedence:
//  1. binary protobuf via the per-topic decoder + projector (when registered)
//  2. JSON fallback (json.Unmarshal directly into map[string]any)
//
// Returns ErrEmptyPayload on zero-length input. Returns a wrapped error if
// both decode paths fail.
//
// For envelope-field hydration from Pub/Sub message attributes (event_id /
// tenant_id / owner_gcid that the JSON-shape producer does NOT inline into
// the payload bytes), prefer DecodePayloadMapWithAttrs.
func DecodePayloadMap(topic string, payload []byte) (map[string]any, error) {
	return DecodePayloadMapWithAttrs(topic, payload, nil)
}

// DecodePayloadMapWithAttrs is identical to DecodePayloadMap but additionally
// hydrates envelope-derived fields (event_id, tenant_id, gcid, owner_gcid,
// traceparent, tracestate, idempotency_key) from the supplied Pub/Sub
// attribute map.
//
// Why this exists
// ---------------
// The JSON-shape publisher (legacy + in-flight topics) places envelope
// fields in Pub/Sub message Attributes — NOT in the payload bytes. The
// binary path reads them from the nested EventEnvelope. JSON fallback
// previously returned an empty payload map for those keys, so downstream
// handlers that read raw["tenant_id"] / raw["event_id"] / raw["owner_gcid"]
// observed zero values on the JSON path despite the publisher having sent
// them.
//
// Hydration is non-destructive: if the payload itself carries a non-empty
// value for a key, it wins; the attribute value only fills in
// missing-or-empty keys. This composes safely with the binary projector
// (which already writes envelope fields into the map from the nested
// EventEnvelope).
func DecodePayloadMapWithAttrs(topic string, payload []byte, attrs map[string]string) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, ErrEmptyPayload
	}

	var out map[string]any

	if entry, ok := binaryDecoders[topic]; ok {
		msg, err := entry.decode(payload)
		if err == nil && msg != nil && entry.project != nil {
			out = make(map[string]any)
			entry.project(msg, out)
		} else {
			// Binary path failed (transitioning producer or pre-gen topic) —
			// fall through to JSON. One-shot WARN per topic so the partial
			// flip is visible without spamming logs.
			warnBinaryFallback(topic, err)
		}
	} else {
		warnUnknownTopic(topic)
	}

	if out == nil {
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, fmt.Errorf("protodecode: topic %q neither binary-decodable nor JSON-decodable: %w", topic, err)
		}
		if out == nil {
			out = make(map[string]any)
		}
	}

	hydrateFromAttrs(out, attrs)
	return out, nil
}

// envelopeAttrKeys is the set of Pub/Sub message attribute keys whose values
// are envelope-derived and should be merged into the payload map when missing.
// Order matters only insofar as documentation; the merge is non-destructive
// (payload-side values win over attribute values).
//
// `owner_gcid` is an attribute-only synonym for the envelope's `gcid` —
// downstream handlers historically read raw["owner_gcid"] for ownership
// checks, so attribute publishers stamp the same value under both keys.
var envelopeAttrKeys = []string{
	"event_id",
	"idempotency_key",
	"tenant_id",
	"gcid",
	"owner_gcid",
	"traceparent",
	"tracestate",
	"source_project",
	"source_service",
	"occurred_at",
	"published_at",
	"schema_version",
}

// hydrateFromAttrs merges envelope-derived attribute values into out when
// the corresponding key is missing OR present-as-empty-string. Non-string
// values in out are NEVER overwritten (so a typed field like a parsed time
// is not clobbered by an RFC3339 string from attrs).
func hydrateFromAttrs(out map[string]any, attrs map[string]string) {
	if out == nil || len(attrs) == 0 {
		return
	}
	for _, key := range envelopeAttrKeys {
		v, ok := attrs[key]
		if !ok || v == "" {
			continue
		}
		existing, present := out[key]
		if !present {
			out[key] = v
			continue
		}
		// Only fill in when existing value is an empty string — a typed
		// non-string value or a non-empty string is authoritative.
		if s, ok := existing.(string); ok && s == "" {
			out[key] = v
		}
	}
}

// projectDailyDoseServed mirrors the chora.consumption.daily_dose.served.v1
// proto message onto the JSON-shape map subscribers consume. Keys mirror
// chora-contracts/proto/events-flat/consumption/daily_dose/served.proto field
// names (snake_case) so the legacy callers don't change.
func projectDailyDoseServed(msg proto.Message, out map[string]any) {
	m, ok := msg.(*consumptionv1.DailyDoseServed)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		// Forward the IMDA + envelope fields the subscribers tolerate
		// reading from the payload (the JSON-shape historically inlined a
		// few envelope keys into the payload map).
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
		if v := env.GetChoraImdaDimension(); v != "" {
			out["chora_imda_dimension"] = v
		}
		if v := env.GetImdaLifecycleStage(); v != "" {
			out["imda_lifecycle_stage"] = v
		}
	}
	if v := m.GetLearnerGcid(); v != "" {
		out["learner_gcid"] = v
	}
	if v := m.GetSize(); v != 0 {
		out["size"] = int(v)
	}
	if v := m.GetMessage(); v != "" {
		out["message"] = v
	}
	if v := m.GetServedAt(); v != nil {
		out["served_at"] = v.AsTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	if entries := m.GetEntries(); len(entries) > 0 {
		atomIDs := make([]string, 0, len(entries))
		entriesOut := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			if id := e.GetAtomId(); id != "" {
				atomIDs = append(atomIDs, id)
			}
			entriesOut = append(entriesOut, map[string]any{
				"atom_id":     e.GetAtomId(),
				"topic":       e.GetTopic(),
				"title":       e.GetTitle(),
				"dose_reason": int(e.GetDoseReason()),
			})
		}
		out["atom_ids"] = atomIDs
		out["entries"] = entriesOut
	}
}

// -----------------------------------------------------------------------------
// One-shot WARN logging
// -----------------------------------------------------------------------------

var (
	warnedFallbackMu sync.Mutex
	warnedFallback   = map[string]bool{}

	warnedUnknownMu sync.Mutex
	warnedUnknown   = map[string]bool{}
)

func warnBinaryFallback(topic string, err error) {
	warnedFallbackMu.Lock()
	defer warnedFallbackMu.Unlock()
	if warnedFallback[topic] {
		return
	}
	warnedFallback[topic] = true
	log.Printf("WARN protodecode: topic %q registered for binary but binary unmarshal failed (%v) — falling back to JSON. Expected during producer-side flip; investigate if persistent.", topic, err)
}

func warnUnknownTopic(topic string) {
	warnedUnknownMu.Lock()
	defer warnedUnknownMu.Unlock()
	if warnedUnknown[topic] {
		return
	}
	warnedUnknown[topic] = true
	log.Printf("WARN protodecode: topic %q has no binary decoder registered — using JSON fallback. Add an entry to internal/adapter/events/protodecode/protodecode.go when the producer flips to binary.", topic)
}

// projectLiveQuizScoreAwarded maps a deliveryv1.LiveQuizSessionScoreAwarded
// onto the snake_case map the derived-weakness push handler consumes. The
// learner is the envelope gcid (the producer emits no separate field); it is
// projected under both "gcid" and "learner_gcid" for handler symmetry with the
// atom_session.completed payload. "correct" is ALWAYS set (proto3 omits the
// zero value on the wire, but a wrong answer is load-bearing here).
func projectLiveQuizScoreAwarded(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.LiveQuizSessionScoreAwarded)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
			out["learner_gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetSessionId(); v != "" {
		out["session_id"] = v
	}
	if v := m.GetLiveQuizId(); v != "" {
		out["live_quiz_id"] = v
	}
	if v := m.GetQuestionId(); v != "" {
		out["question_id"] = v
	}
	if v := m.GetAtomId(); v != "" {
		out["atom_id"] = v
	}
	if tags := m.GetTopicTags(); len(tags) > 0 {
		out["topic_tags"] = tags
	}
	out["correct"] = m.GetCorrect()
	out["awarded_points"] = float64(m.GetAwardedPoints())
	out["cumulative_score"] = float64(m.GetCumulativeScore())
}

// projectAtomPublished maps a creationv1.AtomPublished proto message onto the
// snake_case map[string]any the KG invalidation subscriber handler consumes.
// Keys mirror the JSON-on-wire keys so the legacy-JSON path and binary path
// produce identical maps.
// projectCollectionConvertedToStudyList maps
// chora.creation.v1.CollectionConvertedToStudyList onto the snake_case map the
// study-list push handler consumes (ADR-233 / WS-4).
//
// atom_ids is projected as []string (NOT []any): these are the collection's
// ENTITLED atoms, already consent-gated by chora-creation, and the handler feeds
// them straight into the subscriber payload. The tenant/gcid/traceparent come
// from the NESTED EventEnvelope — on the binary path the Pub/Sub attributes are
// not the only source of truth.
func projectCollectionConvertedToStudyList(msg proto.Message, out map[string]any) {
	m, ok := msg.(*creationv1.CollectionConvertedToStudyList)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetEventId(); v != "" {
			out["event_id"] = v
		}
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
		if v := env.GetTracestate(); v != "" {
			out["tracestate"] = v
		}
		if v := env.GetIdempotencyKey(); v != "" {
			out["idempotency_key"] = v
		}
	}
	if v := m.GetCollectionId(); v != "" {
		out["collection_id"] = v
	}
	if v := m.GetOwnerGcid(); v != "" {
		out["owner_gcid"] = v
	}
	if v := m.GetStudyListEventId(); v != "" {
		out["study_list_event_id"] = v
	}
	// ALWAYS set atom_ids, even when empty: an empty derived list is a real
	// (if degenerate) state, and the handler must not confuse "no atoms" with
	// "field absent". Zero ENTITLED atoms is refused upstream with a 409.
	atoms := m.GetAtomIds()
	cp := make([]string, len(atoms))
	copy(cp, atoms)
	out["atom_ids"] = cp
}

func projectAtomPublished(msg proto.Message, out map[string]any) {
	m, ok := msg.(*creationv1.AtomPublished)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
		if v := env.GetChoraImdaDimension(); v != "" {
			out["chora_imda_dimension"] = v
		}
	}
	if v := m.GetAtomId(); v != "" {
		out["atom_id"] = v
	}
	if v := m.GetAuthorGcid(); v != "" {
		out["author_gcid"] = v
	}
	if v := m.GetTitle(); v != "" {
		out["title"] = v
	}
	// CHO-1968 playability projection: the atom_index flip subscriber reads
	// status + question_type + the MCQ answer key + the open-ended flag to mark
	// the atom playable + answerable. Omit-zero convention (mirrors
	// projectAtomCreated) so absent/zero fields stay absent.
	if st := atomStatusToDomain(m.GetStatus()); st != "" {
		out["status"] = st
	}
	if at := atomTypeToDomain(m.GetQuestionType()); at != "" {
		out["atom_type"] = at
	}
	if v := m.GetCorrectOptionId(); v != "" {
		out["correct_option_id"] = v
	}
	if c := m.GetAnswerCount(); c != 0 {
		out["answer_count"] = int(c)
	}
	if m.GetHasOpenEndedQuestion() {
		out["has_open_ended_question"] = true
	}
	// WS-C3 (CHO-2082): project the ORIGINAL-Bloom label for the campaign
	// question lane's level post-filter. Levels travel as LABELS, never
	// numbers — the proto enum numbering (synthesis=5, evaluation=6) differs
	// from the campaign ladder order by design. UNSPECIFIED omits the key.
	if lvl := cognitiveLevelToDomain(m.GetCognitiveLevel()); lvl != "" {
		out["cognitive_level"] = lvl
	}
}

// cognitiveLevelToDomain maps the CognitiveLevel enum to the canonical
// lowercase ORIGINAL-Bloom label (COGNITIVE_LEVEL_APPLICATION ->
// "application"). UNSPECIFIED maps to "" (omit-zero). Mirrors
// atomStatusToDomain.
func cognitiveLevelToDomain(l creationv1.CognitiveLevel) string {
	if l == creationv1.CognitiveLevel_COGNITIVE_LEVEL_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(l.String(), "COGNITIVE_LEVEL_"))
}

// atomStatusToDomain maps the AtomStatus enum to the canonical lowercase domain
// string atom_index stores (ATOM_STATUS_PUBLISHED -> "published"). UNSPECIFIED
// maps to "" so callers omit it (omit-zero). Mirrors atomTypeToDomain. CHO-1968.
func atomStatusToDomain(s creationv1.AtomStatus) string {
	switch s {
	case creationv1.AtomStatus_ATOM_STATUS_DRAFT:
		return "draft"
	case creationv1.AtomStatus_ATOM_STATUS_PUBLISHED:
		return "published"
	case creationv1.AtomStatus_ATOM_STATUS_ARCHIVED:
		return "archived"
	default:
		return ""
	}
}

// projectAtomUpdated maps a creationv1.AtomUpdated proto message onto the
// snake_case map[string]any the KG invalidation subscriber handler consumes.
func projectAtomUpdated(msg proto.Message, out map[string]any) {
	m, ok := msg.(*creationv1.AtomUpdated)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
		if v := env.GetChoraImdaDimension(); v != "" {
			out["chora_imda_dimension"] = v
		}
	}
	if v := m.GetAtomId(); v != "" {
		out["atom_id"] = v
	}
	if v := m.GetAuthorGcid(); v != "" {
		out["author_gcid"] = v
	}
	if changed := m.GetChangedFields(); len(changed) > 0 {
		out["changed_fields"] = changed
	}
}

// projectAtomCreated maps a decoded AtomCreated into the snake_case map the
// AtomCreatedSubscriber consumes. course_id is the load-bearing field — the
// atom_index projection keys the course-scoped LearningPath bootstrap
// (ListByCourse) on it. created_at is intentionally omitted: atom.created
// carries no published_at, and the dispatcher falls back to env.OccurredAt.
func projectAtomCreated(msg proto.Message, out map[string]any) {
	m, ok := msg.(*creationv1.AtomCreated)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
		if v := env.GetChoraImdaDimension(); v != "" {
			out["chora_imda_dimension"] = v
		}
	}
	if v := m.GetAtomId(); v != "" {
		out["atom_id"] = v
	}
	if v := m.GetCourseId(); v != "" {
		out["course_id"] = v
	}
	if v := m.GetTitle(); v != "" {
		out["title"] = v
	}
	if v := m.GetAuthorGcid(); v != "" {
		out["author_gcid"] = v
	}
	if d := m.GetDifficulty(); d != 0 {
		out["difficulty"] = int(d)
	}
	if at := atomTypeToDomain(m.GetQuestionType()); at != "" {
		out["atom_type"] = at
	}
	if ids := m.GetTopicNodeIds(); len(ids) > 0 {
		out["topic_tags"] = ids
	}
	// CHO-1627: identity-based MCQ grading. correct_option_id is the stable
	// answer-key option id; answer_count is retained as projection metadata.
	if v := m.GetCorrectOptionId(); v != "" {
		out["correct_option_id"] = v
	}
	if c := m.GetAnswerCount(); c != 0 {
		out["answer_count"] = int(c)
	}
}

// atomTypeToDomain maps the AtomType enum to the canonical short domain string
// (e.g. ATOM_TYPE_MULTIPLE_CHOICE -> "mcq") that atom_index stores + the MCQ
// gradability check (atom_index.Item.AtomType == "mcq") expects. Reverse of the
// producer-side protomarshal.lookupAtomType.
//
// DERIVED from the generated descriptor per the naming law in
// chora-contracts/proto/events/creation/atom.proto (CHO-2178). The hand-written
// switch this replaces knew ten values and went blind the instant the contract
// grew ATOM_TYPE_OUTLINE / FLASHCARD / VIDEO — the same rot that left
// chora-sharing's copy missing ATOM_TYPE_ESSAY and blanking the flavour on
// every essay atom it projected. Derivation makes a new contract value decode
// correctly here with no code change.
func atomTypeToDomain(t creationv1.AtomType) string {
	// UNSPECIFIED means "the producer had no opinion" — leave the key absent.
	if t == creationv1.AtomType_ATOM_TYPE_UNSPECIFIED {
		return ""
	}
	// The one label the naming law cannot produce (wire name MULTIPLE_CHOICE).
	// Load-bearing beyond display: atom_index's MCQ gradability check compares
	// against exactly this string.
	if t == creationv1.AtomType_ATOM_TYPE_MULTIPLE_CHOICE {
		return "mcq"
	}
	name, declared := creationv1.AtomType_name[int32(t)]
	if !declared {
		// Only reachable if a producer is deployed AHEAD of this consumer.
		warnUnknownAtomTypeOnce(int32(t))
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(name, "ATOM_TYPE_"))
}

// warnUnknownAtomTypeOnce logs each unrecognised AtomType wire value once —
// loud enough to page a human, quiet enough to survive a replayed backlog.
var (
	warnedAtomTypesMu sync.Mutex
	warnedAtomTypes   = map[int32]bool{}
)

func warnUnknownAtomTypeOnce(v int32) {
	warnedAtomTypesMu.Lock()
	defer warnedAtomTypesMu.Unlock()
	if warnedAtomTypes[v] {
		return
	}
	warnedAtomTypes[v] = true
	log.Printf("WARN protodecode: chora.creation.v1.AtomType value %d is not declared in this "+
		"build's contract — atom_type will be left ABSENT. A producer is running ahead of "+
		"chora-consumption; redeploy this service against current chora-contracts.", v)
}

// projectCertIssued maps deliveryv1.CertIssued (chora.delivery.certification.
// issued.v1) onto the snake_case map the LearnerProfile push handler reads
// (ADR-200, WS1.c3).
func projectCertIssued(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.CertIssued)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetCertId(); v != "" {
		out["cert_id"] = v
	}
	if v := m.GetLearnerGcid(); v != "" {
		out["learner_gcid"] = v
	}
	if v := m.GetCourseId(); v != "" {
		out["course_id"] = v
	}
	if ts := m.GetIssuedAt(); ts != nil {
		out["issued_at"] = ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
}

// projectPathCompleted maps consumptionv1.PathCompleted
// (chora.consumption.learning_path.completed.v1) onto the snake_case map the
// LearnerProfile push handler reads.
func projectPathCompleted(msg proto.Message, out map[string]any) {
	m, ok := msg.(*consumptionv1.PathCompleted)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetPathId(); v != "" {
		out["path_id"] = v
	}
	if v := m.GetLearnerGcid(); v != "" {
		out["learner_gcid"] = v
	}
	if ts := m.GetCompletedAt(); ts != nil {
		out["completed_at"] = ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
}

// projectSubmissionGraded maps deliveryv1.SubmissionGraded
// (chora.delivery.submission.graded.v1) onto the snake_case map the
// LearnerProfile push handler reads. total_points_earned/possible are surfaced
// so the handler can compute the percentage score; passed is the graded
// outcome. assessment_id is surfaced for the StudentTranscript projection
// (W6 Slice 1) — the LearnerProfile path does not read it, but the SAME
// decoded map is fanned out to both subscribers from one push endpoint.
func projectSubmissionGraded(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.SubmissionGraded)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetSubmissionId(); v != "" {
		out["submission_id"] = v
	}
	if v := m.GetAssessmentId(); v != "" {
		out["assessment_id"] = v
	}
	if v := m.GetLearnerGcid(); v != "" {
		out["learner_gcid"] = v
	}
	out["total_points_earned"] = float64(m.GetTotalPointsEarned())
	out["total_points_possible"] = int(m.GetTotalPointsPossible())
	out["passed"] = m.GetPassed()
	if v := m.GetAssessmentTitle(); v != "" {
		// WS-6 (ADR-205): the derived Growth-Edge projector keys on this title.
		out["assessment_title"] = v
	}
	if v := m.GetDeliveryType(); v != "" {
		// CHO-2224 (§10.6 criterion 1): the parent Offering's mode, snapshotted at
		// grade time. The StudentTranscript projection keys its entry's mode on
		// this — it is the ONLY mode-bearing signal on a graded submission, so a
		// field left undecoded here is a transcript that can show 2 kinds and
		// never 2 modes. Absent when unset: a freestanding assessment has no
		// Offering, and every event published before field 14 existed carries
		// nothing, so the transcript stores NULL rather than "".
		out["delivery_type"] = v
	}
	if ts := m.GetGradedAt(); ts != nil {
		out["graded_at"] = ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
}

// projectEnrollmentCompleted maps deliveryv1.EnrollmentCompleted
// (chora.delivery.enrollment.completed.v1) onto the snake_case map the
// LearnerProfile push handler reads (ADR-200, WS1.c3). course_id is the
// ref/label of the course-completed fact; passed is ALWAYS projected (proto3
// omits false on the wire, but a completion-without-passing is load-bearing for
// the LearnerProfile result framing — mirrors projectLiveQuizScoreAwarded's
// handling of "correct").
func projectEnrollmentCompleted(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.EnrollmentCompleted)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetGcid(); v != "" {
			out["gcid"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetEnrollmentId(); v != "" {
		out["enrollment_id"] = v
	}
	if v := m.GetLearnerGcid(); v != "" {
		out["learner_gcid"] = v
	}
	if v := m.GetCourseId(); v != "" {
		out["course_id"] = v
	}
	out["passed"] = m.GetPassed()
	if ts := m.GetCompletedAt(); ts != nil {
		out["completed_at"] = ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
}

// projectCourseCreated maps deliveryv1.CourseCreated
// (chora.delivery.course.created.v1) onto the snake_case map the
// course_metadata push handler reads (CHO-2059 course-name projection):
// course_id + title. tenant_id/traceparent ride the nested envelope (the
// handler also reads the envelope from message attributes).
func projectCourseCreated(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.CourseCreated)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetCourseId(); v != "" {
		out["course_id"] = v
	}
	if v := m.GetTitle(); v != "" {
		out["title"] = v
	}
}

// projectCourseReleased maps deliveryv1.CourseReleased
// (chora.delivery.course.released.v1) onto the same snake_case map the
// course_metadata push handler reads: course_id + title (CHO-2247).
//
// Deliberately projects ONLY the fields course_directory consumes. The event
// also carries price_sgd_cents / sf_eligible / instructor_gcids / test_set_ids —
// commercial + roster facts owned by chora_delivery. Projecting them here would
// silently grow a second, unowned copy of delivery's aggregate inside
// chora_consumption; the directory is a course_id → title map and nothing more.
func projectCourseReleased(msg proto.Message, out map[string]any) {
	m, ok := msg.(*deliveryv1.CourseReleased)
	if !ok || m == nil {
		return
	}
	if env := m.GetEnvelope(); env != nil {
		if v := env.GetTenantId(); v != "" {
			out["tenant_id"] = v
		}
		if v := env.GetTraceparent(); v != "" {
			out["traceparent"] = v
		}
	}
	if v := m.GetCourseId(); v != "" {
		out["course_id"] = v
	}
	if v := m.GetTitle(); v != "" {
		out["title"] = v
	}
}
