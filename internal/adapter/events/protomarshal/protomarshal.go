// Package protomarshal encodes chora-consumption outbox event payloads to
// canonical binary protobuf wire format so the BINARY schema validation
// passes at publish time.
//
// Why hand-rolled
// ---------------
// Generated Go bindings exist for some (DailyDoseServed, AtomSessionCompleted)
// but not all (CompanionChatTurnCompleted at the time of writing — chora-contracts
// regen is BSR-rate-limited). We use google.golang.org/protobuf/encoding/protowire
// to emit canonical wire bytes for the exact subset of fields each Schema
// Registry schema expects, with zero dependency on the generated bindings.
//
// Field numbers + wire types are pinned to chora-contracts/proto/events-flat/
// consumption/* — those flat protos ARE the Schema Registry schemas.
//
// Invariants per the Schema Registry binary-encoded protos:
//
//   - Field 1 = envelope (length-delimited nested message)
//   - Envelope nested fields 1..15 follow chora.common.v1.EventEnvelope layout
//   - Timestamps are nested messages: int64 seconds (field 1) + int32 nanos
//     (field 2)
//   - Unknown topics fail loud (ErrUnsupportedTopic) so the dispatcher
//     dead-letters rather than retrying forever against a schema mismatch
//
// Per CLAUDE.md §6 — wire format MUST be binary protobuf for Pub/Sub-attached
// topics. JSON encoding is rejected at publish time with "Invalid binary proto
// message".
package protomarshal

import (
	"errors"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Envelope is the producer-side flat shape of chora.common.v1.EventEnvelope
// that the encoder needs. Mirrors services/chora-consumption/internal/adapter/
// events.Envelope; defined locally to keep this package import-cycle-free
// (both events.outbox.Publisher and events.CloudPublisher depend on
// protomarshal).
type Envelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
}

// ErrUnsupportedTopic is returned by MarshalPayload when the topic has no
// registered binary encoder. The dispatcher should dead-letter rows that
// surface this error rather than retrying forever — the row is structurally
// incompatible with its destination schema.
var ErrUnsupportedTopic = errors.New("protomarshal: topic has no binary encoder; outbox row will dead-letter")

// IsUnsupportedTopic reports whether err is (or wraps) ErrUnsupportedTopic.
func IsUnsupportedTopic(err error) bool { return errors.Is(err, ErrUnsupportedTopic) }

// MarshalPayload converts a topic + envelope + loose payload map into the
// canonical binary protobuf wire bytes for that topic's Schema Registry
// schema. Returns ErrUnsupportedTopic if no encoder is registered for the
// supplied topic.
//
// Topics with registered encoders:
//
//   - chora.consumption.daily_dose.served.v1
//   - chora.consumption.companion.chat_turn_completed.v1
//   - chora.consumption.companion.stage_up.v1
//   - chora.consumption.companion.breed_revealed.v1
//   - chora.consumption.companion.hatched.v1
//   - chora.consumption.companion.source_revelation.v1
//   - chora.consumption.companion.exp_awarded.v1
//   - chora.consumption.companion.kg_neighbor_revealed.v1
//   - chora.consumption.companion.created.v1
//   - chora.consumption.companion.egg_purchased.v1
//   - chora.consumption.companion.skill_slot_unlocked.v1
//   - chora.consumption.companion.skill_granted.v1
//   - chora.consumption.companion.loadout_changed.v1
//   - chora.consumption.companion.specialization_changed.v1
//   - chora.consumption.companion.ritual_published.v1
//   - chora.consumption.companion.ritual_run_completed.v1
//   - chora.consumption.kg_hexagon_fog.generated.v1
//   - chora.consumption.kg_exploration.focal_changed.v1
//   - chora.consumption.weakness.grown.v1
//   - chora.consumption.weakness.analyzed.v1
//
// (Non-exhaustive — the switch below is the authoritative registry; stirring /
// persona_updated / retired / goal.* / campaign.* / ai_assist_started_v2 have
// cases too.)
//
// Adding more topics: append a case to the switch + implement
// encode{X}(env, payload) returning the wire bytes.
func MarshalPayload(topic string, env Envelope, payload map[string]any) ([]byte, error) {
	switch topic {
	case "chora.consumption.daily_dose.served.v1":
		return encodeDailyDoseServed(env, payload)
	case "chora.consumption.companion.chat_turn_completed.v1":
		return encodeCompanionChatTurnCompleted(env, payload)
	case "chora.consumption.companion.stage_up.v1":
		return encodeCompanionStageUp(env, payload)
	case "chora.consumption.companion.breed_revealed.v1":
		return encodeCompanionBreedRevealed(env, payload)
	case "chora.consumption.companion.hatched.v1":
		return encodeCompanionHatched(env, payload)
	case "chora.consumption.companion.source_revelation.v1":
		return encodeCompanionSourceRevelation(env, payload)
	case "chora.consumption.companion.exp_awarded.v1":
		return encodeCompanionExpAwarded(env, payload)
	case "chora.consumption.companion.kg_neighbor_revealed.v1":
		return encodeCompanionKgNeighborRevealed(env, payload)
	case "chora.consumption.companion.created.v1":
		return encodeCompanionCreated(env, payload)
	case "chora.consumption.companion.egg_purchased.v1":
		return encodeCompanionEggPurchased(env, payload)
	case "chora.consumption.companion.stirring.v1":
		return encodeCompanionStirring(env, payload)
	case "chora.consumption.companion.skill_slot_unlocked.v1":
		return encodeCompanionSkillSlotUnlocked(env, payload)
	case "chora.consumption.companion.skill_granted.v1":
		return encodeCompanionSkillGranted(env, payload)
	case "chora.consumption.companion.loadout_changed.v1":
		return encodeCompanionLoadoutChanged(env, payload)
	case "chora.consumption.companion.specialization_changed.v1":
		return encodeCompanionSpecializationChanged(env, payload)
	case "chora.consumption.companion.persona_updated.v1":
		return encodeCompanionPersonaUpdated(env, payload)
	case "chora.consumption.companion.retired.v1":
		return encodeCompanionRetired(env, payload)
	case "chora.consumption.companion.ritual_published.v1":
		return encodeCompanionRitualPublished(env, payload)
	case "chora.consumption.companion.ritual_run_completed.v1":
		return encodeCompanionRitualRunCompleted(env, payload)
	case "chora.consumption.companion.bonded.v1":
		return encodeCompanionBonded(env, payload)
	case "chora.consumption.companion.exposure_granted.v1":
		return encodeCompanionExposureGranted(env, payload)
	case "chora.consumption.companion.leveled_up.v1":
		return encodeCompanionLeveledUp(env, payload)
	case "chora.consumption.companion.memory_eviction.v1":
		return encodeCompanionMemoryEviction(env, payload)
	case "chora.consumption.companion.session_started.v1":
		return encodeCompanionSessionStarted(env, payload)
	case "chora.consumption.companion.skin_equipped.v1":
		return encodeCompanionSkinEquipped(env, payload)
	case "chora.consumption.kg_hexagon_fog.generated.v1":
		return encodeHexagonFogGenerated(env, payload)
	case "chora.consumption.kg_exploration.focal_changed.v1":
		return encodeExplorationFocalChanged(env, payload)
	case "chora.consumption.weakness.grown.v1":
		return encodeWeaknessGrown(env, payload)
	case "chora.consumption.weakness.analyzed.v1":
		return encodeWeaknessAnalyzed(env, payload)
	case "chora.consumption.weakness.review_pending.v1":
		// ADR-205 D4 (CHO-1973): the bounded HITL review panel.
		return encodeWeaknessReviewPending(env, payload)
	case "chora.consumption.goal.graduated.v1":
		return encodeGoalGraduated(env, payload)
	case "chora.consumption.goal.progress_updated.v1":
		return encodeGoalProgressUpdated(env, payload)
	case "chora.consumption.concept.atoms_bound.v1":
		// ADR-244 D4 (CHO-2303): the atom-to-concept binding.
		return encodeConceptAtomsBound(env, payload)
	case "chora.consumption.concept.deleted.v1":
		// CHO-2324: the concept soft-delete cascade trigger.
		return encodeConceptDeleted(env, payload)
	case "chora.consumption.campaign.focus_assigned.v1":
		return encodeCampaignFocusAssigned(env, payload)
	case "chora.consumption.campaign.rung_cleared.v1":
		return encodeCampaignRungCleared(env, payload)
	case "chora.consumption.campaign.node_won.v1":
		return encodeCampaignNodeWon(env, payload)
	case "chora.consumption.campaign.node_revealed.v1":
		return encodeCampaignNodeRevealed(env, payload)
	case "chora.consumption.campaign.goal_sealed.v1":
		return encodeCampaignGoalSealed(env, payload)
	case TopicAiAssistStartedV2:
		// CHO-2040 — the ONE cross-domain REQUEST topic consumption publishes
		// (the proofing-test runner's qgen batch request; compose-v2 BINARY
		// schema). See ai_assist_started_v2.go.
		return encodeAiAssistStartedV2(env, payload)
	case "chora.consumption.companion_turn.requested.v1",
		"chora.consumption.dose_recommendation.requested.v1":
		// ADR-254 D4 caller-facing workflow REQUEST lanes: JSON-wire, schemaless
		// on the bus (topics.yaml `schema: null`); the kennel decodes the body by
		// the proto field names (CompanionTurnRequested / DoseRecommendationRequested
		// in chora-contracts). Explicit here so the outbox never treats them as a
		// missing binary encoder (the WARN + JSON fallback path is for topics that
		// DO carry a BINARY schema and would dead-letter).
		return encodeJSONWire(payload)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTopic, topic)
	}
}

// -----------------------------------------------------------------------------
// DailyDoseServed (chora.consumption.daily_dose.served.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/daily_dose/served.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string learner_gcid
//	3  bytes  repeated DailyDoseEntry entries
//	4  varint int32 size
//	5  string message
//	6  bytes  Timestamp served_at
func encodeDailyDoseServed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	// field 1: envelope
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	// field 2: learner_gcid
	if v, ok := payload["learner_gcid"].(string); ok && v != "" {
		out = appendString(out, 2, v)
	}

	// field 3: entries (repeated DailyDoseEntry) — sourced from atom_ids when
	// only the IDs are known (legacy call sites).
	if entries, ok := payload["entries"].([]map[string]any); ok {
		for _, e := range entries {
			entryBz, err := encodeDailyDoseEntry(e)
			if err != nil {
				return nil, fmt.Errorf("entries: %w", err)
			}
			out = appendLengthDelimited(out, 3, entryBz)
		}
	} else if ids, ok := stringSlice(payload["atom_ids"]); ok {
		for _, id := range ids {
			entry := map[string]any{"atom_id": id}
			entryBz, err := encodeDailyDoseEntry(entry)
			if err != nil {
				return nil, fmt.Errorf("entries from atom_ids: %w", err)
			}
			out = appendLengthDelimited(out, 3, entryBz)
		}
	}

	// field 4: size (int32)
	if v, ok := asInt32(payload["size"]); ok {
		out = appendVarint(out, 4, uint64(uint32(v)))
	}

	// field 5: message
	if v, ok := payload["message"].(string); ok && v != "" {
		out = appendString(out, 5, v)
	}

	// field 6: served_at
	if t, ok := asTime(payload["served_at"]); ok {
		tsBz := encodeTimestamp(t)
		out = appendLengthDelimited(out, 6, tsBz)
	}

	return out, nil
}

// encodeDailyDoseEntry: chora-contracts events-flat DailyDoseEntry
//
//	1  string atom_id
//	2  string topic
//	3  string title
//	4  varint DoseReason dose_reason
func encodeDailyDoseEntry(e map[string]any) ([]byte, error) {
	out := make([]byte, 0, 64)
	if v, ok := e["atom_id"].(string); ok && v != "" {
		out = appendString(out, 1, v)
	}
	if v, ok := e["topic"].(string); ok && v != "" {
		out = appendString(out, 2, v)
	}
	if v, ok := e["title"].(string); ok && v != "" {
		out = appendString(out, 3, v)
	}
	if v, ok := asInt32(e["dose_reason"]); ok && v != 0 {
		out = appendVarint(out, 4, uint64(uint32(v)))
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionChatTurnCompleted (chora.consumption.companion.chat_turn_completed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/chat_turn_completed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string session_id
//	3  string turn_id
//	4  string gcid
//	5  string tenant_id
//	6  string companion_id
//	7  string engine_session_id
//	8  string model
//	9  varint int32 output_tokens
//	10 varint int32 mana_charged
//	11 string mana_tier
//	12 varint int32 tool_calls_count
//	13 string finish_reason
//	14 bytes  Timestamp started_at
//	15 bytes  Timestamp completed_at
func encodeCompanionChatTurnCompleted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	stringField := func(field protowire.Number, key string) error {
		raw, present := payload[key]
		if !present {
			return nil
		}
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("field %s: expected string, got %T", key, raw)
		}
		if s == "" {
			return nil
		}
		out = appendString(out, field, s)
		return nil
	}
	int32Field := func(field protowire.Number, key string) error {
		raw, present := payload[key]
		if !present {
			return nil
		}
		v, ok := asInt32(raw)
		if !ok {
			return fmt.Errorf("field %s: expected int32-convertible, got %T", key, raw)
		}
		if v == 0 {
			return nil
		}
		out = appendVarint(out, field, uint64(uint32(v)))
		return nil
	}
	timestampField := func(field protowire.Number, key string) error {
		raw, present := payload[key]
		if !present {
			return nil
		}
		t, ok := asTime(raw)
		if !ok {
			return fmt.Errorf("field %s: expected time.Time, got %T", key, raw)
		}
		out = appendLengthDelimited(out, field, encodeTimestamp(t))
		return nil
	}

	for _, step := range []func() error{
		func() error { return stringField(2, "session_id") },
		func() error { return stringField(3, "turn_id") },
		func() error { return stringField(4, "gcid") },
		func() error { return stringField(5, "tenant_id") },
		func() error { return stringField(6, "companion_id") },
		func() error { return stringField(7, "engine_session_id") },
		func() error { return stringField(8, "model") },
		func() error { return int32Field(9, "output_tokens") },
		func() error { return int32Field(10, "mana_charged") },
		func() error { return stringField(11, "mana_tier") },
		func() error { return int32Field(12, "tool_calls_count") },
		func() error { return stringField(13, "finish_reason") },
		func() error { return timestampField(14, "started_at") },
		func() error { return timestampField(15, "completed_at") },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionStageUp (chora.consumption.companion.stage_up.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/stage_up.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint int32 stage_from
//	5  varint int32 stage_to
//	6  string stage_from_name
//	7  string stage_to_name
//	8  string repeated newly_unlocked_tools
//	9  string repeated newly_revealed_kg_neighbors
//	10 string new_llm_tier
//	11 string new_memory_mode
//	12 bytes  Timestamp stage_up_at
//	13 string companion_display_name (CHO-2266 — denormalised name for C+ copy)
func encodeCompanionStageUp(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "stage_from"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "stage_to"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "stage_from_name"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "stage_to_name"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 8, payload, "newly_unlocked_tools"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 9, payload, "newly_revealed_kg_neighbors"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 10, payload, "new_llm_tier"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 11, payload, "new_memory_mode"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 12, payload, "stage_up_at"); err != nil {
		return nil, err
	}
	// field 13: companion_display_name (CHO-2266) — omit-zero safe, so an absent
	// name writes no bytes and the consumer branches on presence.
	if err := writeStringField(&out, 13, payload, "companion_display_name"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionStirring (chora.consumption.companion.stirring.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/stirring.proto
// -----------------------------------------------------------------------------
//
//	1 bytes  Envelope envelope
//	2 string companion_id
//	3 string owner_gcid
//	4 varint int32 growth_exp
//	5 varint int32 hatch_threshold
//	6 bytes  Timestamp stirred_at
func encodeCompanionStirring(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 128)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "growth_exp"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "hatch_threshold"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 6, payload, "stirred_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionBreedRevealed (chora.consumption.companion.breed_revealed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/breed_revealed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint CompanionSpecies species (enum)
//	5  varint bool shiny_variant
//	6  string rarity
//	7  string egg_sku
//	8  fixed64 double rolled_probability
//	9  bytes   Timestamp revealed_at
func encodeCompanionBreedRevealed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 4, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 5, payload, "shiny_variant"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "rarity"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "egg_sku"); err != nil {
		return nil, err
	}
	if err := writeFixed64DoubleField(&out, 8, payload, "rolled_probability"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "revealed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionHatched (chora.consumption.companion.hatched.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/hatched.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string display_name
//	5  string tone
//	6  string learner_persona
//	7  string resonant_atom_id
//	8  string specialization
//	9  varint CompanionSpecies species (enum)
//	10 varint bool shiny_variant
//	11 bytes  Timestamp hatched_at
func encodeCompanionHatched(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "display_name"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "tone"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "learner_persona"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "resonant_atom_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "specialization"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 9, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 10, payload, "shiny_variant"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 11, payload, "hatched_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSourceRevelation (chora.consumption.companion.source_revelation.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/source_revelation.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string preview_llm_tier
//	5  string repeated preview_tools
//	6  bytes  Timestamp revelation_at
//	7  bytes  Timestamp window_expires_at
//	8  varint int32 window_duration_seconds
//	9  string companion_display_name (CHO-2266 — denormalised name for C+ copy)
func encodeCompanionSourceRevelation(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "preview_llm_tier"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 5, payload, "preview_tools"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 6, payload, "revelation_at"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "window_expires_at"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "window_duration_seconds"); err != nil {
		return nil, err
	}
	// field 9: companion_display_name (CHO-2266) — omit-zero safe.
	if err := writeStringField(&out, 9, payload, "companion_display_name"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionExpAwarded (chora.consumption.companion.exp_awarded.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/exp_awarded.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint int32 exp_delta
//	5  varint int32 exp_total_after
//	6  string source
//	7  string source_event_id
//	8  string source_topic
//	9  string source_session_id
//	10 varint int32 source_turn_seq
//	11 varint bool daily_cap_hit
//	12 varint bool triggers_stage_up
//	13 bytes  Timestamp awarded_at
func encodeCompanionExpAwarded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "exp_delta"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "exp_total_after"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "source"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "source_event_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "source_topic"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 9, payload, "source_session_id"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 10, payload, "source_turn_seq"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 11, payload, "daily_cap_hit"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 12, payload, "triggers_stage_up"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 13, payload, "awarded_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionKgNeighborRevealed (chora.consumption.companion.kg_neighbor_revealed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/kg_neighbor_revealed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string atom_id
//	5  string revealed_via
//	6  varint int32 graph_distance
//	7  bytes  Timestamp revealed_at
func encodeCompanionKgNeighborRevealed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "atom_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "revealed_via"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 6, payload, "graph_distance"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "revealed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionCreated (chora.consumption.companion.created.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/created.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string name
//	5  string specialization
//	6  varint CompanionSpecies species (enum)
//	7  string age_stage
//	8  string evolution_tier
//	9  varint int32 roster_position
//	10 bytes  Timestamp created_at
func encodeCompanionCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "name"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "specialization"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 6, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "age_stage"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "evolution_tier"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 9, payload, "roster_position"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "created_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionEggPurchased (chora.consumption.companion.egg_purchased.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/egg_purchased.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string egg_sku
//	5  string source
//	6  string stripe_session_id
//	7  varint int64 amount_cents
//	8  string currency
//	9  string suggested_focal_atom_id
//	10 bytes  Timestamp soft_expiry_at
//	11 bytes  Timestamp hard_expiry_at
//	12 bytes  Timestamp purchased_at
//
// The producer payload also carries egg_purchase_id + chora_companion_id
// (producer-internal correlation keys) — the schema does not define them, so
// they are intentionally not encoded.
func encodeCompanionEggPurchased(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "egg_sku"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "source"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "stripe_session_id"); err != nil {
		return nil, err
	}
	if err := writeInt64Field(&out, 7, payload, "amount_cents"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "currency"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 9, payload, "suggested_focal_atom_id"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "soft_expiry_at"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 11, payload, "hard_expiry_at"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 12, payload, "purchased_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// WeaknessGrown (chora.consumption.weakness.grown.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/weakness/grown.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  growth_edge_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  concept_label
//	6  string  concept_key
//	7  fixed32 float final_strength
//	8  string  recovery_source
//	9  string  repeated tags
//	10 bytes   Timestamp grown_at
func encodeWeaknessGrown(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "growth_edge_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "tenant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "concept_label"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "concept_key"); err != nil {
		return nil, err
	}
	if err := writeFixed32FloatField(&out, 7, payload, "final_strength"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 8, payload, "recovery_source"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 9, payload, "tags"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "grown_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// WeaknessAnalyzed (chora.consumption.weakness.analyzed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/weakness/analyzed.proto
// -----------------------------------------------------------------------------
//
// CHO-2040 (owner ruling R8-5): until now this topic was PRODUCED only by the
// ai-kernel weakness-analyser crew (Python) and consumption only consumed it.
// The ceremony confirm hook publishes the learner's remediate ticks on the
// SAME topic through the outbox, so the producer-side binary encoder lands
// here (a JSON fallback would be rejected by the BINARY Schema Registry and
// deadletter forever).
//
//	1  bytes   Envelope envelope
//	2  string  upload_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  bytes   repeated ExtractedGrowthEdge edges
//	6  string  model_used
//	7  varint  int32 input_token_count
//	8  varint  int32 output_token_count
//	9  bytes   Timestamp analyzed_at
//	10 bytes   OutputSelection output_selection
func encodeWeaknessAnalyzed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)
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
	// field 5 edges (repeated ExtractedGrowthEdge).
	if raw, present := payload["edges"]; present && raw != nil {
		items, ok := raw.([]map[string]any)
		if !ok {
			return nil, fmt.Errorf("field edges: expected []map[string]any, got %T", raw)
		}
		for i, e := range items {
			eBz, eErr := encodeExtractedGrowthEdge(e)
			if eErr != nil {
				return nil, fmt.Errorf("field edges[%d]: %w", i, eErr)
			}
			out = appendLengthDelimited(out, 5, eBz)
		}
	}
	if err := writeStringField(&out, 6, payload, "model_used"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "input_token_count"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "output_token_count"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "analyzed_at"); err != nil {
		return nil, err
	}
	// field 10 output_selection (OutputSelection). familiar_coaching=true is
	// the load-bearing bit: the analyzed subscriber gates the per-Companion
	// RAG coaching write on it (weakness_companion_rag.go).
	if raw, present := payload["output_selection"]; present && raw != nil {
		sel, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field output_selection: expected map[string]any, got %T", raw)
		}
		selBz, sErr := encodeOutputSelection(sel)
		if sErr != nil {
			return nil, fmt.Errorf("field output_selection: %w", sErr)
		}
		out = appendLengthDelimited(out, 10, selBz)
	}
	return out, nil
}

// encodeExtractedGrowthEdge: chora-contracts events-flat ExtractedGrowthEdge
//
//	1  string  concept_label
//	2  string  concept_key
//	3  string  category
//	4  string  repeated tags
//	5  fixed32 float confidence
//	6  fixed32 float strength
//	7  string  descriptor_json
func encodeExtractedGrowthEdge(e map[string]any) ([]byte, error) {
	out := make([]byte, 0, 128)
	if err := writeStringField(&out, 1, e, "concept_label"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 2, e, "concept_key"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, e, "category"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 4, e, "tags"); err != nil {
		return nil, err
	}
	if err := writeFixed32FloatField(&out, 5, e, "confidence"); err != nil {
		return nil, err
	}
	if err := writeFixed32FloatField(&out, 6, e, "strength"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, e, "descriptor_json"); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeOutputSelection: chora-contracts events-flat OutputSelection
//
//	1  varint bool focused_dose
//	2  varint bool familiar_coaching
//	3  varint bool practice_test
//	4  varint bool study_aids
func encodeOutputSelection(sel map[string]any) ([]byte, error) {
	out := make([]byte, 0, 16)
	if err := writeBoolField(&out, 1, sel, "focused_dose"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 2, sel, "familiar_coaching"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 3, sel, "practice_test"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 4, sel, "study_aids"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// GoalGraduated (chora.consumption.goal.graduated.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/goal/graduated.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  string  kind
//	6  string  chora_target_ref
//	7  varint  int32 mastered_concepts
//	8  varint  int32 total_concepts
//	9  bytes   Timestamp graduated_at
func encodeGoalGraduated(env Envelope, payload map[string]any) ([]byte, error) {
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
	if err := writeStringField(&out, 5, payload, "kind"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "chora_target_ref"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "mastered_concepts"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "total_concepts"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "graduated_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// GoalProgressUpdated (chora.consumption.goal.progress_updated.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/goal/progress_updated.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  goal_id
//	3  string  tenant_id
//	4  string  learner_gcid
//	5  varint  int32 mastered_concepts
//	6  varint  int32 total_concepts
//	7  varint  int32 progress_percent
//	8  bytes   Timestamp occurred_at
func encodeGoalProgressUpdated(env Envelope, payload map[string]any) ([]byte, error) {
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
	if err := writeInt32Field(&out, 5, payload, "mastered_concepts"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 6, payload, "total_concepts"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "progress_percent"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 8, payload, "occurred_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSkillSlotUnlocked (chora.consumption.companion.skill_slot_unlocked.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/skill_slot_unlocked.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint int32 stage_from
//	5  varint int32 stage_to
//	6  varint int32 slots_before
//	7  varint int32 slots_after
//	8  bytes  Timestamp unlocked_at
//
// CHO-2013 P1: the P0 growth-service emitter (publishSlotUnlocked) shipped
// without this case, so every stage transition deadlettered against the
// BINARY schema. Producer payload keys match the field names 1:1.
func encodeCompanionSkillSlotUnlocked(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "stage_from"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "stage_to"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 6, payload, "slots_before"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "slots_after"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 8, payload, "unlocked_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSkillGranted (chora.consumption.companion.skill_granted.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/skill_granted.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string skill_key
//	5  string skill_kind
//	6  string unlocked_via
//	7  varint int32 unlocked_at_stage
//	8  varint bool equipped
//	9  bytes  Timestamp granted_at
//
// CHO-2013 P1: producer = companion.PathUnlocker grant mints (payload keys
// match 1:1). Same missing-encoder deadletter class as slot_unlocked.
func encodeCompanionSkillGranted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 224)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "skill_key"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "skill_kind"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "unlocked_via"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "unlocked_at_stage"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 8, payload, "equipped"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "granted_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionLoadoutChanged (chora.consumption.companion.loadout_changed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/loadout_changed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string skill_key
//	5  string action
//	6  string repeated equipped_skills
//	7  varint int32 slots_used
//	8  varint int32 skill_slots_unlocked
//	9  bytes  Timestamp changed_at
//
// CHO-2013 P1: producer = the equip/unequip handler (publishLoadoutChanged,
// payload keys match 1:1). Topic + BINARY schema were provisioned with P0.
func encodeCompanionLoadoutChanged(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "skill_key"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "action"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 6, payload, "equipped_skills"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "slots_used"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "skill_slots_unlocked"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "changed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSpecializationChanged (chora.consumption.companion.specialization_changed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/specialization_changed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string previous_specialization
//	5  string new_specialization
//	6  string memory_handling
//	7  string user_reason
//	8  bytes  Timestamp changed_at
//
// CHO-2013 P1: first real emitter is the Summon-onto-Goal specialization
// derive (goal attach). The registered schema already matches this shape.
func encodeCompanionSpecializationChanged(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 224)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "previous_specialization"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "new_specialization"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "memory_handling"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "user_reason"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 8, payload, "changed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionPersonaUpdated (chora.consumption.companion.persona_updated.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/persona_updated.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  companion_id
//	3  string  owner_gcid
//	4  int32   persona_version
//	5  string  tone
//	6  string  difficulty_cap
//	7  string  address_style
//	8  bool    has_guidance_note
//	9  bytes   Timestamp updated_at
//
// CHO-2015 (ADR-219 D2): the topic is registered BINARY — a JSON fallback
// dead-letters forever. PRIVACY: the free-text guidance note is NEVER emitted;
// only has_guidance_note. The absent-key-safe writers omit proto3-default
// fields, so a partial payload stays wire-valid.
func encodeCompanionPersonaUpdated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "persona_version"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "tone"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "difficulty_cap"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "address_style"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 8, payload, "has_guidance_note"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "updated_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionRetired (chora.consumption.companion.retired.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/retired.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  companion_id
//	3  string  owner_gcid
//	4  enum    CompanionSpecies species
//	5  int32   level
//	6  string  reason
//	7  bytes   Timestamp retired_at
//
// CHO-2033 SP2: the topic is registered BINARY, so a JSON fallback dead-letters
// forever. The consumption Instance carries no species enum / int level, so the
// live retire handler emits only companion_id + owner_gcid + reason + retired_at;
// species (4) + level (5) stay proto3-default (the absent-key-safe writers omit
// them). The realtime SSE consumer needs only id + gcid — species/level are
// enriched here only if a downstream consumer (Sharing memorial post) needs them.
func encodeCompanionRetired(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 4, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "level"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "reason"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "retired_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionRitualPublished (chora.consumption.companion.ritual_published.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/ritual_published.proto
// (message RitualPublished; source events/consumption/ritual.proto)
// -----------------------------------------------------------------------------
//
//	1 bytes  Envelope envelope
//	2 string ritual_id
//	3 string companion_id
//	4 varint int32 revision_no
//	5 string trigger
//	6 string sink
//	7 varint int32 published_price_units
//
// CHO-2125: producer = companion.RitualPublisher.emitPublished (CHO-2016 G5),
// payload keys match the schema 1:1 — no event-body timestamp (occurred_at /
// published_at ride on the envelope). The topic family is Schema-Registry-bound
// BINARY, so without this case the outbox JSON-fell-back (boot WARN "no binary
// protobuf encoder") and every ritual publish deadlettered (WS-C8 walk day-1
// flag). Same missing-encoder class as stirring / skill_slot_unlocked.
func encodeCompanionRitualPublished(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "ritual_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 4, payload, "revision_no"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "trigger"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "sink"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "published_price_units"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionRitualRunCompleted (chora.consumption.companion.ritual_run_completed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/ritual_run_completed.proto
// (message RitualRunCompleted; source events/consumption/ritual.proto)
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string run_id
//	3  string ritual_id
//	4  string companion_id
//	5  string owner_gcid
//	6  varint int32 revision_no
//	7  string trigger_source
//	8  varint RitualRunStatus status (enum)
//	9  varint int32 mana_charged
//	10 string sink_ref
//	11 string error
//	12 bytes  repeated RitualStepStamp stamps
//
// CHO-2136 (CHO-2125's flagged sibling): producer =
// companion.RitualRunner.publishCompleted, payload keys match the schema 1:1;
// stamps arrive as wire-ready snake_case maps (stampsWirePayload). Timestamps
// ride on the envelope. This topic has a LIVE consumer — chora-observability's
// ritual_run_audit projection — whose pull handler proto-first dual-decodes,
// so the binary flip here is deploy-safe consumer-first.
func encodeCompanionRitualRunCompleted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "run_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "ritual_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 6, payload, "revision_no"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 7, payload, "trigger_source"); err != nil {
		return nil, err
	}
	if err := writeRitualRunStatusEnumField(&out, 8, payload, "status"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 9, payload, "mana_charged"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 10, payload, "sink_ref"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 11, payload, "error"); err != nil {
		return nil, err
	}
	// field 12 stamps (repeated RitualStepStamp).
	if raw, present := payload["stamps"]; present && raw != nil {
		items, ok := raw.([]map[string]any)
		if !ok {
			return nil, fmt.Errorf("field stamps: expected []map[string]any, got %T", raw)
		}
		for i, s := range items {
			sBz, sErr := encodeRitualStepStamp(s)
			if sErr != nil {
				return nil, fmt.Errorf("field stamps[%d]: %w", i, sErr)
			}
			out = appendLengthDelimited(out, 12, sBz)
		}
	}
	return out, nil
}

// encodeRitualStepStamp: chora-contracts events-flat RitualStepStamp
//
//	1 varint int32 step_index
//	2 string skill_key
//	3 string prompt_version
//	4 string prompt_hash
//	5 string repeated tools_invoked
//	6 string repeated citations
func encodeRitualStepStamp(s map[string]any) ([]byte, error) {
	out := make([]byte, 0, 96)
	if err := writeInt32Field(&out, 1, s, "step_index"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 2, s, "skill_key"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, s, "prompt_version"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, s, "prompt_hash"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 5, s, "tools_invoked"); err != nil {
		return nil, err
	}
	if err := writeRepeatedStringField(&out, 6, s, "citations"); err != nil {
		return nil, err
	}
	return out, nil
}

// ritualRunStatusEnum maps the domain companion.RunStatus strings onto the
// flat-proto RitualRunStatus enum values.
var ritualRunStatusEnum = map[string]int32{
	"running":        1,
	"completed":      2,
	"failed":         3,
	"skipped_budget": 4,
	"blocked":        5,
}

func writeRitualRunStatusEnumField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("field %s: expected status string, got %T", key, raw)
	}
	if s == "" {
		return nil
	}
	v, known := ritualRunStatusEnum[s]
	if !known {
		return fmt.Errorf("field %s: unknown RitualRunStatus %q (canonical: running|completed|failed|skipped_budget|blocked)", key, s)
	}
	*out = appendVarint(*out, field, uint64(uint32(v)))
	return nil
}

// -----------------------------------------------------------------------------
// HexagonFogGenerated (chora.consumption.kg_hexagon_fog.generated.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/kg_hexagon_fog/generated.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  run_id
//	3  string  cluster_id
//	4  string  exploration_id
//	5  string  user_gcid
//	6  string  focal_atom_id
//	7  bytes   repeated HexagonNeighbor neighbors
//	8  string  model_id
//	9  varint  int64 total_cost_micros
//	10 varint  int64 input_tokens
//	11 varint  int64 output_tokens
//	12 bytes   Timestamp generated_at
//
// 2026-07-01 event-fabric repair: this topic had NO case in MarshalPayload, so
// the outbox JSON-fell-back and the BINARY schema validation rejected the bytes
// with INVALID_BINARY_PROTO_MESSAGE (7 deadlettered rows in the audit). Adding
// the encoder closes the gap. The sole producer (events.KGPublisher
// .PublishHexagonFogGenerated) emits run_id / model_id under the payload keys
// `generated_by_run_id` / `generated_by_model_id`; we read those aliases (and
// the canonical names) so the real event round-trips. It does NOT yet populate
// user_gcid / neighbors / generated_at — those are encoded here for schema
// completeness and flow the moment the producer sets them.
// -----------------------------------------------------------------------------
// CompanionBonded (chora.consumption.companion.bonded.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/bonded.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint CompanionSpecies species (enum)
//	5  varint int32 level
//	6  bytes  Timestamp bonded_at
func encodeCompanionBonded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 4, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "level"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 6, payload, "bonded_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionExposureGranted (chora.consumption.companion.exposure_granted.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/exposure_granted.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string exposure_grant_id
//	4  string learner_gcid
//	5  string content_atom_id
//	6  string exposure_scope
//	7  bytes  Timestamp granted_at
//	8  bytes  Timestamp expires_at
//	9  string grant_reason
func encodeCompanionExposureGranted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "exposure_grant_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "content_atom_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "exposure_scope"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "granted_at"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 8, payload, "expires_at"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 9, payload, "grant_reason"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionLeveledUp (chora.consumption.companion.leveled_up.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/leveled_up.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  varint CompanionSpecies species (enum)
//	5  varint int32 previous_level
//	6  varint int32 level
//	7  bytes  Timestamp leveled_up_at
func encodeCompanionLeveledUp(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeSpeciesEnumField(&out, 4, payload, "species"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "previous_level"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 6, payload, "level"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "leveled_up_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionMemoryEviction (chora.consumption.companion.memory_eviction.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/memory_eviction.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string memory_bank_app_name
//	5  varint int32 batch_count
//	6  string reason
//	7  varint int32 capacity
//	8  varint int32 retained_count
//	9  bytes  Timestamp oldest_retained_at
//	10 bytes  Timestamp evicted_at
func encodeCompanionMemoryEviction(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "memory_bank_app_name"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 5, payload, "batch_count"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "reason"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 7, payload, "capacity"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "retained_count"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "oldest_retained_at"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 10, payload, "evicted_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSessionStarted (chora.consumption.companion.session_started.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/session_started.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string session_id
//	4  string learner_gcid
//	5  string session_type
//	6  string context_atom_id
//	7  bytes  Timestamp started_at
func encodeCompanionSessionStarted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "session_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "learner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "session_type"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "context_atom_id"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 7, payload, "started_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// CompanionSkinEquipped (chora.consumption.companion.skin_equipped.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/companion/skin_equipped.proto
// -----------------------------------------------------------------------------
//
//	1  bytes  Envelope envelope
//	2  string companion_id
//	3  string owner_gcid
//	4  string digital_skin_id
//	5  string previous_skin_id
//	6  bytes  Timestamp equipped_at
func encodeCompanionSkinEquipped(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 192)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "companion_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "owner_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "digital_skin_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "previous_skin_id"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 6, payload, "equipped_at"); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeHexagonFogGenerated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	// field 2 run_id — producer emits it as `generated_by_run_id`.
	if err := writeStringFieldAlias(&out, 2, payload, "run_id", "generated_by_run_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "cluster_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "exploration_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "user_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "focal_atom_id"); err != nil {
		return nil, err
	}
	// field 7 neighbors (repeated HexagonNeighbor).
	if raw, present := payload["neighbors"]; present && raw != nil {
		items, ok := raw.([]map[string]any)
		if !ok {
			return nil, fmt.Errorf("field neighbors: expected []map[string]any, got %T", raw)
		}
		for i, n := range items {
			nBz, nErr := encodeHexagonNeighbor(n)
			if nErr != nil {
				return nil, fmt.Errorf("field neighbors[%d]: %w", i, nErr)
			}
			out = appendLengthDelimited(out, 7, nBz)
		}
	}
	// field 8 model_id — producer emits it as `generated_by_model_id`.
	if err := writeStringFieldAlias(&out, 8, payload, "model_id", "generated_by_model_id"); err != nil {
		return nil, err
	}
	if err := writeInt64Field(&out, 9, payload, "total_cost_micros"); err != nil {
		return nil, err
	}
	if err := writeInt64Field(&out, 10, payload, "input_tokens"); err != nil {
		return nil, err
	}
	if err := writeInt64Field(&out, 11, payload, "output_tokens"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 12, payload, "generated_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeHexagonNeighbor: chora-contracts events-flat HexagonNeighbor
//
//	1  string  atom_id
//	2  varint  NeighborRelation relation (enum)
//	3  fixed32 float confidence
//	4  string  fog_label
//	5  varint  bool is_junction
func encodeHexagonNeighbor(n map[string]any) ([]byte, error) {
	out := make([]byte, 0, 64)
	if err := writeStringField(&out, 1, n, "atom_id"); err != nil {
		return nil, err
	}
	if err := writeNeighborRelationEnumField(&out, 2, n, "relation"); err != nil {
		return nil, err
	}
	if err := writeFixed32FloatField(&out, 3, n, "confidence"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, n, "fog_label"); err != nil {
		return nil, err
	}
	if err := writeBoolField(&out, 5, n, "is_junction"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// ExplorationFocalNodeChanged (chora.consumption.kg_exploration.focal_changed.v1)
// Field layout — chora-contracts/proto/events-flat/consumption/kg_exploration/focal_changed.proto
// -----------------------------------------------------------------------------
//
//	1  bytes   Envelope envelope
//	2  string  exploration_id
//	3  string  cluster_id
//	4  string  user_gcid
//	5  string  from_focal_atom_id
//	6  string  to_focal_atom_id
//	7  varint  NeighborRelation relation (enum)
//	8  varint  int32 step_index
//	9  bytes   Timestamp traversed_at
//
// 2026-07-01 event-fabric repair (Class D): this topic was emitted by
// kg_publisher.PublishExplorationFocalChanged but had NO case in MarshalPayload
// AND its Pub/Sub topic was never provisioned → deadlettered NotFound. Adding
// the encoder closes the wire-format half; the topics.yaml + m10-data-plane
// entry provisions the schema-bound topic + DLQ. The producer emits the
// non-schema key `trail_id`, which is silently dropped (proto3 has no field for
// it). user_gcid / traversed_at are encoded for schema completeness and flow
// the moment the producer sets them (mirrors encodeHexagonFogGenerated).
func encodeExplorationFocalChanged(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	if err := writeStringField(&out, 2, payload, "exploration_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 3, payload, "cluster_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 4, payload, "user_gcid"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 5, payload, "from_focal_atom_id"); err != nil {
		return nil, err
	}
	if err := writeStringField(&out, 6, payload, "to_focal_atom_id"); err != nil {
		return nil, err
	}
	if err := writeNeighborRelationEnumField(&out, 7, payload, "relation"); err != nil {
		return nil, err
	}
	if err := writeInt32Field(&out, 8, payload, "step_index"); err != nil {
		return nil, err
	}
	if err := writeTimestampField(&out, 9, payload, "traversed_at"); err != nil {
		return nil, err
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Envelope (nested in every event message; field layout matches
// chora.common.v1.EventEnvelope as flattened by chora-contracts/internal/
// protoflatten and embedded as a NESTED type in every events-flat schema).
// -----------------------------------------------------------------------------
//
//	1  string event_id
//	2  string idempotency_key
//	3  string tenant_id
//	4  string gcid
//	5  bytes  Timestamp occurred_at
//	6  bytes  Timestamp published_at
//	7  string traceparent
//	8  string tracestate
//	9  string source_project
//	10 string source_service
//	11 varint int32 schema_version
//	12 string correlation_id
//	13 string causation_id
//	14 string chora_imda_dimension
//	15 string imda_lifecycle_stage
func encodeEnvelope(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	if env.EventID != "" {
		out = appendString(out, 1, env.EventID)
	}
	if env.IdempotencyKey != "" {
		out = appendString(out, 2, env.IdempotencyKey)
	}
	if env.TenantID != "" {
		out = appendString(out, 3, env.TenantID)
	}
	if env.GCID != "" {
		out = appendString(out, 4, env.GCID)
	}
	if !env.OccurredAt.IsZero() {
		out = appendLengthDelimited(out, 5, encodeTimestamp(env.OccurredAt))
	}
	if !env.PublishedAt.IsZero() {
		out = appendLengthDelimited(out, 6, encodeTimestamp(env.PublishedAt))
	}
	if env.Traceparent != "" {
		out = appendString(out, 7, env.Traceparent)
	}
	if env.Tracestate != "" {
		out = appendString(out, 8, env.Tracestate)
	}
	if env.SourceProject != "" {
		out = appendString(out, 9, env.SourceProject)
	}
	if env.SourceService != "" {
		out = appendString(out, 10, env.SourceService)
	}
	if env.SchemaVersion > 0 {
		out = appendVarint(out, 11, uint64(uint32(env.SchemaVersion)))
	}

	// Optional IMDA evidence fields sourced from the payload (used by
	// chora.consumption.daily_dose.served.v1 per companion_handlers.go).
	if payload != nil {
		if v, ok := payload["chora_imda_dimension"].(string); ok && v != "" {
			out = appendString(out, 14, v)
		}
		if v, ok := payload["imda_lifecycle_stage"].(string); ok && v != "" {
			out = appendString(out, 15, v)
		}
	}

	return out, nil
}

// encodeTimestamp emits the nested google.protobuf.Timestamp wire shape:
//
//	1 varint int64  seconds
//	2 varint int32  nanos
func encodeTimestamp(t time.Time) []byte {
	out := make([]byte, 0, 16)
	t = t.UTC()
	secs := t.Unix()
	nanos := int32(t.Nanosecond())
	if secs != 0 {
		out = appendVarint(out, 1, uint64(secs))
	}
	if nanos != 0 {
		out = appendVarint(out, 2, uint64(uint32(nanos)))
	}
	return out
}

// -----------------------------------------------------------------------------
// Wire-format helpers (thin protowire wrappers; reuse keeps callers tidy).
// -----------------------------------------------------------------------------

func appendString(b []byte, field protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendString(b, v)
	return b
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	b = protowire.AppendVarint(b, v)
	return b
}

func appendLengthDelimited(b []byte, field protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

func appendFixed64(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, v)
	return b
}

func appendFixed32(b []byte, field protowire.Number, v uint32) []byte {
	b = protowire.AppendTag(b, field, protowire.Fixed32Type)
	b = protowire.AppendFixed32(b, v)
	return b
}

// -----------------------------------------------------------------------------
// Typed writer helpers — keep the encode{X} functions tidy + uniform.
// Each writer fails loud (returns error) when the payload has the wrong Go
// type for the schema slot. Missing keys are skipped silently per proto3
// default-value semantics.
// -----------------------------------------------------------------------------

func writeStringField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("field %s: expected string, got %T", key, raw)
	}
	if s == "" {
		return nil
	}
	*out = appendString(*out, field, s)
	return nil
}

// writeStringFieldAlias writes the first present, non-empty string among keys
// (in order) to field. Lets an encoder accept both the canonical schema key and
// a producer-side alias — e.g. HexagonFogGenerated.run_id, which the KGPublisher
// emits as `generated_by_run_id`.
func writeStringFieldAlias(out *[]byte, field protowire.Number, payload map[string]any, keys ...string) error {
	for _, key := range keys {
		raw, present := payload[key]
		if !present {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("field %s: expected string, got %T", key, raw)
		}
		if s == "" {
			continue
		}
		*out = appendString(*out, field, s)
		return nil
	}
	return nil
}

func writeInt32Field(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	v, ok := asInt32(raw)
	if !ok {
		return fmt.Errorf("field %s: expected int32-convertible, got %T", key, raw)
	}
	if v == 0 {
		return nil
	}
	*out = appendVarint(*out, field, uint64(uint32(v)))
	return nil
}

// writeInt64Field encodes a proto `int64` (varint, wire type 0). Used by
// HexagonFogGenerated.total_cost_micros / input_tokens / output_tokens, whose
// producer FogResult fields are int64 (int32 truncation would corrupt cost).
func writeInt64Field(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	v, ok := asInt64(raw)
	if !ok {
		return fmt.Errorf("field %s: expected int64-convertible, got %T", key, raw)
	}
	if v == 0 {
		return nil
	}
	*out = appendVarint(*out, field, uint64(v))
	return nil
}

func writeTimestampField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	t, ok := asTime(raw)
	if !ok {
		return fmt.Errorf("field %s: expected time.Time, got %T", key, raw)
	}
	*out = appendLengthDelimited(*out, field, encodeTimestamp(t))
	return nil
}

func writeBoolField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	b, ok := raw.(bool)
	if !ok {
		return fmt.Errorf("field %s: expected bool, got %T", key, raw)
	}
	if !b {
		// proto3 default — omit zero values from the wire.
		return nil
	}
	*out = appendVarint(*out, field, 1)
	return nil
}

func writeRepeatedStringField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present || raw == nil {
		return nil
	}
	items, ok := stringSlice(raw)
	if !ok {
		return fmt.Errorf("field %s: expected []string, got %T", key, raw)
	}
	for _, s := range items {
		*out = appendString(*out, field, s)
	}
	return nil
}

func writeFixed64DoubleField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	f, ok := asFloat64(raw)
	if !ok {
		return fmt.Errorf("field %s: expected float64-convertible, got %T", key, raw)
	}
	if f == 0 {
		return nil
	}
	*out = appendFixed64(*out, field, math.Float64bits(f))
	return nil
}

// writeFixed32FloatField encodes a proto `float` (4-byte IEEE-754, wire type
// I32). Mirrors writeFixed64DoubleField but narrows to float32 — used by
// WeaknessGrown.final_strength.
func writeFixed32FloatField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	f, ok := asFloat64(raw)
	if !ok {
		return fmt.Errorf("field %s: expected float-convertible, got %T", key, raw)
	}
	if f == 0 {
		return nil // proto3 default — omit zero
	}
	*out = appendFixed32(*out, field, math.Float32bits(float32(f)))
	return nil
}

// CompanionSpecies enum mapping — mirrors the gen'd CompanionSpecies enum in
// chora-contracts/gen/go/chora/consumption/v1 + the canonicalSpecies set in
// internal/domain/growth/breed.go. Lowercase strings are the producer-side
// representation; the proto wire form is varint enum.
var companionSpeciesEnum = map[string]int32{
	"":        0, // UNSPECIFIED
	"owl":     1,
	"fox":     2,
	"cat":     3, // legacy gacha breed — RETIRED from the roll (CHO-2032);
	"dragon":  4,
	"phoenix": 5,
	"turtle":  6, // kept here only to encode/decode already-emitted events.
	"wolf":    7,
	"raven":   8,
	"penguin": 9, // 5th hero — COMPANION_SPECIES_PENGUIN (additive, v1-safe).
}

func writeSpeciesEnumField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("field %s: expected species string, got %T", key, raw)
	}
	if s == "" {
		return nil
	}
	v, known := companionSpeciesEnum[s]
	if !known {
		return fmt.Errorf("field %s: unknown CompanionSpecies %q (heroes: owl|fox|dragon|phoenix|penguin)", key, s)
	}
	if v == 0 {
		return nil
	}
	*out = appendVarint(*out, field, uint64(uint32(v)))
	return nil
}

// NeighborRelation enum mapping — mirrors the gen'd NeighborRelation enum in
// chora-contracts/gen/go/chora/consumption/v1 + the NeighborRelation string
// constants in internal/domain/user_knowledge_graph/hexagon.go. Lowercase
// strings are the producer-side representation; the proto wire form is varint
// enum. Used by HexagonFogGenerated.neighbors[].relation.
var neighborRelationEnum = map[string]int32{
	"":                0, // UNSPECIFIED
	"prerequisite_of": 1,
	"extends":         2,
	"analogy_of":      3,
	"contrasts_with":  4,
	"applied_in":      5,
	"curiosity_jump":  6,
}

func writeNeighborRelationEnumField(out *[]byte, field protowire.Number, payload map[string]any, key string) error {
	raw, present := payload[key]
	if !present {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("field %s: expected relation string, got %T", key, raw)
	}
	if s == "" {
		return nil
	}
	v, known := neighborRelationEnum[s]
	if !known {
		return fmt.Errorf("field %s: unknown NeighborRelation %q (canonical: prerequisite_of|extends|analogy_of|contrasts_with|applied_in|curiosity_jump)", key, s)
	}
	if v == 0 {
		return nil
	}
	*out = appendVarint(*out, field, uint64(uint32(v)))
	return nil
}

// -----------------------------------------------------------------------------
// Loose-typed payload coercion (in/out: map[string]any).
// -----------------------------------------------------------------------------

// asInt32 converts the value to int32. Returns false (not 0) if v is nil OR
// is a non-numeric type — caller decides whether to fail or skip.
func asInt32(v any) (int32, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int32(n), true
	case int32:
		return n, true
	case int64:
		return int32(n), true
	case float32:
		return int32(n), true
	case float64:
		return int32(n), true
	case uint:
		return int32(n), true
	case uint32:
		return int32(n), true
	case uint64:
		return int32(n), true
	default:
		return 0, false
	}
}

// asInt64 converts the value to int64 without the narrowing that asInt32 applies.
// Returns false (not 0) if v is nil OR a non-numeric type — caller decides
// whether to fail or skip. Used for the int64 cost/token fields on
// HexagonFogGenerated.
func asInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	case uint:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	default:
		return 0, false
	}
}

// asFloat64 coerces a numeric value to float64. Returns false on nil/non-numeric
// (caller decides whether to fail or skip).
func asFloat64(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// asTime coerces a value to time.Time. Accepts time.Time directly + nil.
func asTime(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	default:
		return time.Time{}, false
	}
}

// stringSlice coerces []string or []any-of-string to a string slice.
func stringSlice(v any) ([]string, bool) {
	if v == nil {
		return nil, false
	}
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}
