// campaign.go — binary encoders for the 5 ADR-227 Companion Campaign events
// (WS-C1, CHO-2080). Field layouts mirror the FLAT protos under
// chora-contracts/proto/events-flat/consumption/campaign/*.proto (ONE
// top-level message per topic; the Schema Registry validates these exact
// bytes). Every event carries goal_id (ADR-227 Verification addendum #6 —
// chora-identity routes XP via ResolveForGoal).
//
// The `rung` field is the CampaignRung enum: LADDER positions in ADR-227 D6
// order spoken in original-Bloom vocabulary (1 knowledge … 5 evaluation,
// 6 synthesis) — deliberately NOT the creation CognitiveLevel numbering.
// Enums encode as varint, so the payload carries the ladder int (1..6) and
// writeInt32Field emits the correct wire bytes.
package protomarshal

import "fmt"

// -----------------------------------------------------------------------------
// CampaignFocusAssigned (chora.consumption.campaign.focus_assigned.v1)
// Field layout — events-flat/consumption/campaign/focus_assigned.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  concept_id
//	6  string  concept_key
//	7  string  previous_concept_id
//	8  string  familiar_id  (WIRE-FROZEN, ADR-254; the payload map key below
//	                        stays "companion_id" because it is the DOMAIN side
//	                        of this adapter, fed from CampaignFocusAssignedIn)
//	9  bytes   Timestamp assigned_at
func encodeCampaignFocusAssigned(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "goal_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "concept_key"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "previous_concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "assigned_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CampaignRungCleared (chora.consumption.campaign.rung_cleared.v1)
// Field layout — events-flat/consumption/campaign/rung_cleared.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  concept_id
//	6  string  concept_key
//	7  varint  CampaignRung rung (ladder position 1..6)
//	8  varint  bool is_refresher
//	9  varint  int32 correct_answers
//	10 bytes   Timestamp cleared_at
func encodeCampaignRungCleared(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "goal_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "concept_key"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "rung"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 8, payload, "is_refresher"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 9, payload, "correct_answers"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "cleared_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CampaignNodeWon (chora.consumption.campaign.node_won.v1)
// Field layout — events-flat/consumption/campaign/node_won.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  concept_id
//	6  string  concept_key
//	7  bytes   Timestamp won_at
func encodeCampaignNodeWon(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "goal_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "concept_key"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "won_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CampaignNodeRevealed (chora.consumption.campaign.node_revealed.v1)
// Field layout — events-flat/consumption/campaign/node_revealed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  focal_concept_id
//	6  string  focal_concept_key
//	7  varint  int32 suggestion_count
//	8  string  won_event_id
//	9  bytes   Timestamp revealed_at
func encodeCampaignNodeRevealed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "goal_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "focal_concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "focal_concept_key"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "suggestion_count"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "won_event_id"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "revealed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CampaignGoalSealed (chora.consumption.campaign.goal_sealed.v1)
// Field layout — events-flat/consumption/campaign/goal_sealed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  root_concept_id
//	6  string  root_concept_key
//	7  varint  int32 nodes_won
//	8  varint  bool is_reseal
//	9  bytes   Timestamp sealed_at
func encodeCampaignGoalSealed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "goal_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "root_concept_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "root_concept_key"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "nodes_won"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 8, payload, "is_reseal"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "sealed_at"); err != nil {
		return nil, err
	}
	return out, nil
}
