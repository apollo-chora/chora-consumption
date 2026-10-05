// concept.go - BINARY encoder for chora.consumption.concept.atoms_bound.v1
// (ADR-244 D4, CHO-2303).
//
// Without a case here MarshalPayload JSON-falls-back and the Schema Registry
// rejects every publish with INVALID_BINARY_PROTO_MESSAGE, which deadletters
// silently. The Flag-1 guardrail test enforces that any consumption topic bound
// to a BINARY schema in chora-infra/topics/topics.yaml has an encoder.
package protomarshal

import "fmt"

// -----------------------------------------------------------------------------
// ConceptAtomsBound (chora.consumption.concept.atoms_bound.v1)
// Field layout - events-flat/consumption/concept/atoms_bound.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  concept_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  attached_atom_ids   (REPEATED)
//	6  string  detached_atom_ids   (REPEATED)
//	7  string  resulting_atom_refs (REPEATED)
//	8  string  change_source
//	9  string  provenance
//	10 bytes   Timestamp occurred_at
//
// The three repeated fields are unpacked (proto3 repeated string is always
// length-delimited per element), so an empty slice encodes as ABSENT rather than
// as one empty string. That matters: a consumer distinguishing "no atoms
// detached" from "one blank atom detached" would otherwise be reading noise.
func encodeConceptAtomsBound(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 5, payload, "attached_atom_ids"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 6, payload, "detached_atom_ids"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 7, payload, "resulting_atom_refs"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "change_source"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 9, payload, "provenance"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "occurred_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// ConceptDeleted (chora.consumption.concept.deleted.v1) — CHO-2324
// Field layout - events-flat/consumption/concept/deleted.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  concept_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  bytes   Timestamp occurred_at
//
// A concept soft-delete carries only the concept identity + when: the subscriber
// needs nothing more to cascade the incident-edge cleanup, and a leaner payload
// is a smaller wire-compat surface.
func encodeConceptDeleted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 128)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 5, payload, "occurred_at"); err != nil {
		return nil, err
	}
	return out, nil
}
