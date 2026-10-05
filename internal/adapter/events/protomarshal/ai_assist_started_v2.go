// ai_assist_started_v2.go — CHO-2040: binary encoder for the CROSS-SERVICE
// qgen crew request topic chora.creation.ai_assist.started.v2.
//
// chora-consumption publishes this ONE creation-domain topic (the proofing-test
// composed runner's generation request; terraform m10-data-plane documents it
// as "the only CROSS-SERVICE topic"). The topic is bound to the compose-v2
// BINARY Schema Registry schema, so the payload MUST be canonical protowire
// bytes — a JSON publish is rejected with INVALID_BINARY_PROTO_MESSAGE and
// deadletters forever.
//
// Field layout mirrors chora-contracts/proto/events-flat/creation/ai_assist/
// started.v2.proto (== AiAssistStartedV2 in proto/events/creation/
// ai_assist.proto; every retained field keeps its v1 tag; 15/job_kind is
// reserved) and the producer-side encoder in services/chora-creation/internal/
// adapter/events/protomarshal/protomarshal.go::encodeAiAssistStartedV2:
//
//	 1 bytes  Envelope envelope
//	 2 string assist_id
//	 3 string tenant_id
//	 4 string author_gcid
//	 5 string atom_id
//	 6 string content_type            ("mixed" for a multi-type plan)
//	 7 string prompt
//	 8 varint int32 requested_count   (== sum(type_plan.count))
//	 9 varint int32 difficulty
//	10 bytes  Timestamp started_at
//	11 varint int32 max_retries
//	12 map<string,string> metadata
//	13 varint bool image_for_stem
//	14 varint bool image_for_answer
//	15 —      RESERVED (was job_kind)
//	16 string grounding_mode
//	17 string source_blob_uri
//	18 string source_mime_type
//	19 string repeated target_growth_edges   (R8-7: concept KEYS)
//	20 —      source_files (NOT emitted here — the proofing runner is promptless-grounded)
//	21 bytes  repeated GenerationTypeQuota type_plan {1:question_type, 2:count, 3:max_images}
//	22 —      regen (never emitted here)
//	23 string operation   ("compose")
//	24 string intent      ("new_question")
//	25 string input_kind  ("prompt")
package protomarshal

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
)

// TopicAiAssistStartedV2 is the qgen crew's request lane (creation-owned;
// consumption is an allowlisted cross-domain REQUEST publisher, CHO-2040).
const TopicAiAssistStartedV2 = "chora.creation.ai_assist.started.v2"

func encodeAiAssistStartedV2(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	for _, step := range []func() error{
		func() error { return writeStringField(&out, 2, payload, "assist_id") },
		func() error { return writeStringField(&out, 3, payload, "tenant_id") },
		func() error { return writeStringField(&out, 4, payload, "author_gcid") },
		func() error { return writeStringField(&out, 5, payload, "atom_id") },
		func() error { return writeStringField(&out, 6, payload, "content_type") },
		func() error { return writeStringField(&out, 7, payload, "prompt") },
		func() error { return writeInt32Field(&out, 8, payload, "requested_count") },
		func() error { return writeInt32Field(&out, 9, payload, "difficulty") },
		func() error { return writeTimestampField(&out, 10, payload, "started_at") },
		func() error { return writeInt32Field(&out, 11, payload, "max_retries") },
		func() error { return writeStringMapField(&out, 12, payload, "metadata") },
		func() error { return writeBoolField(&out, 13, payload, "image_for_stem") },
		func() error { return writeBoolField(&out, 14, payload, "image_for_answer") },
		// 15 RESERVED (job_kind) — v2 routes on the compose trio + plan.
		func() error { return writeStringField(&out, 16, payload, "grounding_mode") },
		func() error { return writeStringField(&out, 17, payload, "source_blob_uri") },
		func() error { return writeStringField(&out, 18, payload, "source_mime_type") },
		func() error { return writeRepeatedStringField(&out, 19, payload, "target_growth_edges") },
		func() error { return writeTypePlanQuotas(&out, 21, payload, "type_plan") },
		func() error { return writeStringField(&out, 23, payload, "operation") },
		func() error { return writeStringField(&out, 24, payload, "intent") },
		func() error { return writeStringField(&out, 25, payload, "input_kind") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeStringMapField encodes a proto map<string,string> (repeated nested
// {1: key, 2: value}) with DETERMINISTIC key order (sorted) so encodes are
// byte-stable for tests + idempotent replays. Absent/nil emits nothing;
// a non-map value fails loud.
func writeStringMapField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}
	var m map[string]string
	switch v := raw.(type) {
	case map[string]string:
		m = v
	case map[string]any:
		m = make(map[string]string, len(v))
		for k, mv := range v {
			s, ok := mv.(string)
			if !ok {
				return fmt.Errorf("field %s[%s]: expected string, got %T", key, k, mv)
			}
			m[k] = s
		}
	default:
		return fmt.Errorf("field %s: expected map[string]string, got %T", key, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		entry := make([]byte, 0, len(k)+len(m[k])+8)
		entry = appendString(entry, 1, k)
		entry = appendString(entry, 2, m[k])
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}

// writeTypePlanQuotas encodes repeated GenerationTypeQuota (nested message
// {1: question_type (string), 2: count (int32), 3: max_images (int32)}) in
// slice order. Accepts []map[string]any (the runner's shape) or []any of maps
// (a JSON-decoded bridge shape). Fails loud on any other shape — a silently
// mis-encoded quota would mis-route the mixed batch. Mirrors chora-creation's
// appendTypePlanQuotas (proto3 default-elision within each quota).
func writeTypePlanQuotas(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}
	var elements []map[string]any
	switch v := raw.(type) {
	case []map[string]any:
		elements = v
	case []any:
		elements = make([]map[string]any, 0, len(v))
		for i, e := range v {
			m, ok := e.(map[string]any)
			if !ok {
				return fmt.Errorf("field %s[%d]: expected map, got %T", key, i, e)
			}
			elements = append(elements, m)
		}
	default:
		return fmt.Errorf("field %s: expected []map (type_plan quotas), got %T", key, raw)
	}
	for i, m := range elements {
		qt := ""
		if rawQT, ok := m["question_type"]; ok {
			s, sok := rawQT.(string)
			if !sok {
				return fmt.Errorf("field %s[%d].question_type: expected string, got %T", key, i, rawQT)
			}
			qt = s
		}
		count := int32(0)
		if rawCount, ok := m["count"]; ok {
			c, cok := asInt32(rawCount)
			if !cok {
				return fmt.Errorf("field %s[%d].count: expected int32-convertible, got %T", key, i, rawCount)
			}
			count = c
		}
		maxImages := int32(0)
		if rawMax, ok := m["max_images"]; ok {
			mi, mok := asInt32(rawMax)
			if !mok {
				return fmt.Errorf("field %s[%d].max_images: expected int32-convertible, got %T", key, i, rawMax)
			}
			maxImages = mi
		}
		entry := make([]byte, 0, 16+len(qt))
		if qt != "" {
			entry = appendString(entry, 1, qt)
		}
		if count != 0 {
			entry = appendVarint(entry, 2, uint64(uint32(count)))
		}
		if maxImages != 0 {
			entry = appendVarint(entry, 3, uint64(uint32(maxImages)))
		}
		*out = appendLengthDelimited(*out, field, entry)
	}
	return nil
}
