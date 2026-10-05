// weakness_review_pending.go — binary protobuf encoder for
// chora.consumption.weakness.review_pending.v1 (the bounded HITL review
// panel; ADR-205 D4 / CHO-1973). Wire shape mirrors the flat proto in
// chora-contracts/proto/events-flat/consumption/weakness/review_pending.proto
// (WeaknessReviewPending), which the consumer unmarshals verbatim.
package protomarshal

import (
	"fmt"
)

// encodeWeaknessReviewPending marshals the review panel. Payload keys are the
// proto field names (snake_case); the consumer's proto.Unmarshal accepts them
// as-is.
func encodeWeaknessReviewPending(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "upload_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}

	if f, ok := payload["familiar"].(map[string]any); ok {
		fam := make([]byte, 0, 32)
		if err := writeStringField(&fam, 1, f, "familiar_id"); err != nil {
			return nil, err
		}
		if err := writeStringField(&fam, 2, f, "name"); err != nil {
			return nil, err
		}
		if err := writeStringField(&fam, 3, f, "species"); err != nil {
			return nil, err
		}
		out = appendLengthDelimited(out, 5, fam)
	}

	if edges := anyMaps(payload["proposed_edges"]); len(edges) > 0 {
		for _, em := range edges {
			edge := make([]byte, 0, 64)
			if err := writeStringField(&edge, 1, em, "proposed_edge_id"); err != nil {
				return nil, err
			}
			if err := writeStringField(&edge, 2, em, "concept_label"); err != nil {
				return nil, err
			}
			if err := writeStringField(&edge, 3, em, "summary"); err != nil {
				return nil, err
			}
			if err := writeRepeatedStringField(&edge, 4, em, "suggested_angles"); err != nil {
				return nil, err
			}
			if err := writeFixed32FloatField(&edge, 5, em, "strength"); err != nil {
				return nil, err
			}
			if err := writeStringField(&edge, 6, em, "suggested_difficulty"); err != nil {
				return nil, err
			}
			out = appendLengthDelimited(out, 6, edge)
		}
	}

	if struggles := anyMaps(payload["candidate_struggles"]); len(struggles) > 0 {
		for _, em := range struggles {
			c := make([]byte, 0, 32)
			if err := writeStringField(&c, 1, em, "concept_key"); err != nil {
				return nil, err
			}
			if err := writeStringField(&c, 2, em, "concept_label"); err != nil {
				return nil, err
			}
			out = appendLengthDelimited(out, 7, c)
		}
	}

	if outputs := anyMaps(payload["available_outputs"]); len(outputs) > 0 {
		for _, om := range outputs {
			o := make([]byte, 0, 32)
			if err := writeStringField(&o, 1, om, "kind"); err != nil {
				return nil, err
			}
			if err := writeInt64Field(&o, 2, om, "mana_price"); err != nil {
				return nil, err
			}
			if err := writeBoolField(&o, 3, om, "default_selected"); err != nil {
				return nil, err
			}
			out = appendLengthDelimited(out, 8, o)
		}
	}

	if t, ok := asTime(payload["pending_at"]); ok {
		out = appendLengthDelimited(out, 9, encodeTimestamp(t))
	}

	return out, nil
}

// anyMaps coerces []any or []map[string]any to a []map[string]any.
func anyMaps(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
