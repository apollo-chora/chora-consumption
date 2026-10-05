// protomarshal_cover_test characterizes the uncovered branches of the
// hand-rolled wire encoder: the typed writer helpers' error + omit paths, the
// loose-typed coercion helpers (asInt32 / asFloat64 / asTime / stringSlice),
// and the per-encoder error propagation when a payload slot has the wrong Go
// type. Where a numeric/enum value lands on the wire it is round-tripped
// through the generated proto bindings so the assertion catches a real
// encoding regression, not merely the absence of a panic.
package protomarshal_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

const (
	topicStageUp          = "chora.consumption.companion.stage_up.v1"
	topicBreedRevealed    = "chora.consumption.companion.breed_revealed.v1"
	topicHatched          = "chora.consumption.companion.hatched.v1"
	topicSourceRevelation = "chora.consumption.companion.source_revelation.v1"
	topicChatTurn         = "chora.consumption.companion.chat_turn_completed.v1"
	topicDailyDose        = "chora.consumption.daily_dose.served.v1"
	topicExpAwarded       = "chora.consumption.companion.exp_awarded.v1"
	topicKgNeighbor       = "chora.consumption.companion.kg_neighbor_revealed.v1"
	topicCompanionCreated = "chora.consumption.companion.created.v1"
)

// -----------------------------------------------------------------------------
// CompanionKgNeighborRevealed + CompanionCreated — typed-writer error arms +
// nil-payload parity (same harness as CompanionExpAwarded below).
// -----------------------------------------------------------------------------

func TestKgNeighborRevealed_FieldErrorPropagation_EveryArm(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	spec := []struct {
		key  string
		good any
		bad  any
	}{
		{"companion_id", "fam-1", 999},
		{"owner_gcid", "gcid-1", 999},
		{"atom_id", "atom-1", 999},
		{"revealed_via", "user_pick", 999},
		{"graph_distance", 1, "near"},
		{"revealed_at", now, 123},
	}
	for i, tc := range spec {
		t.Run(tc.key, func(t *testing.T) {
			payload := map[string]any{}
			for j := 0; j < i; j++ {
				payload[spec[j].key] = spec[j].good
			}
			payload[tc.key] = tc.bad
			_, err := protomarshal.MarshalPayload(topicKgNeighbor, env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestCompanionCreated_FieldErrorPropagation_EveryArm(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	spec := []struct {
		key  string
		good any
		bad  any
	}{
		{"companion_id", "fam-1", 999},
		{"owner_gcid", "gcid-1", 999},
		{"name", "Ignis", 999},
		{"specialization", "structural", 999},
		{"species", "owl", 999},
		{"age_stage", "juvenile", 999},
		{"evolution_tier", "scholar", 999},
		{"roster_position", 2, "second"},
		{"created_at", now, 123},
	}
	for i, tc := range spec {
		t.Run(tc.key, func(t *testing.T) {
			payload := map[string]any{}
			for j := 0; j < i; j++ {
				payload[spec[j].key] = spec[j].good
			}
			payload[tc.key] = tc.bad
			_, err := protomarshal.MarshalPayload(topicCompanionCreated, env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestKgNeighborAndCreated_NilPayload_EnvelopeOnly(t *testing.T) {
	env := milestoneEnvelope()
	for _, topic := range []string{topicKgNeighbor, topicCompanionCreated} {
		bz, err := protomarshal.MarshalPayload(topic, env, nil)
		require.NoError(t, err, topic)
		require.NotEmpty(t, bz, topic)
	}
}

// -----------------------------------------------------------------------------
// CompanionExpAwarded — every typed-writer error arm + nil-payload parity.
// The encoder returns the FIRST field error, so each case supplies valid
// values for all earlier slots and a wrong-typed value at its own slot.
// -----------------------------------------------------------------------------

func TestExpAwarded_FieldErrorPropagation_EveryArm(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	spec := []struct {
		key  string
		good any
		bad  any
	}{
		{"companion_id", "fam-1", 999},
		{"owner_gcid", "gcid-1", 999},
		{"exp_delta", 5, "nope"},
		{"exp_total_after", 50, "nope"},
		{"source", "atom_session", 999},
		{"source_event_id", "evt-1", 999},
		{"source_topic", "chora.consumption.atom_session.completed.v1", 999},
		{"source_session_id", "sess-1", 999},
		{"source_turn_seq", 2, "nope"},
		{"daily_cap_hit", true, "yes"},
		{"triggers_stage_up", true, "yes"},
		{"awarded_at", now, 123},
	}
	for i, tc := range spec {
		t.Run(tc.key, func(t *testing.T) {
			payload := map[string]any{}
			for j := 0; j < i; j++ {
				payload[spec[j].key] = spec[j].good
			}
			payload[tc.key] = tc.bad
			_, err := protomarshal.MarshalPayload(topicExpAwarded, env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

// Nil payload → envelope-only message is still valid wire (parity with the
// other encoders' nil-payload early return).
func TestExpAwarded_NilPayload_EnvelopeOnly(t *testing.T) {
	env := milestoneEnvelope()
	bz, err := protomarshal.MarshalPayload(topicExpAwarded, env, nil)
	require.NoError(t, err)
	var msg consumptionv1.CompanionExpAwarded
	require.NoError(t, proto.Unmarshal(bz, &msg))
	require.NotNil(t, msg.GetEnvelope())
	assert.Equal(t, env.EventID, msg.GetEnvelope().GetEventId())
}

// -----------------------------------------------------------------------------
// asInt32 — every numeric type branch, round-tripped through the int32 wire
// slot of CompanionStageUp.stage_from so the coercion result is observable.
// -----------------------------------------------------------------------------

func TestAsInt32_AllNumericTypeBranches(t *testing.T) {
	env := milestoneEnvelope()
	cases := []struct {
		name string
		in   any
		want int32
	}{
		{"int", int(7), 7},
		{"int32", int32(8), 8},
		{"int64", int64(9), 9},
		{"float32", float32(10.9), 10}, // truncates toward zero
		{"float64", float64(11.9), 11}, // truncates toward zero
		{"uint", uint(12), 12},
		{"uint32", uint32(13), 13},
		{"uint64", uint64(14), 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"companion_id": "fam-1",
				"stage_from":   tc.in,
			}
			bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
			require.NoError(t, err)
			var msg consumptionv1.CompanionStageUp
			require.NoError(t, proto.Unmarshal(bz, &msg))
			assert.Equal(t, tc.want, msg.GetStageFrom())
		})
	}
}

// On an int32 slot: an ABSENT key is skipped; a key PRESENT-but-nil is a hard
// type error (writeInt32Field gates on map-key presence, then asInt32(nil)
// returns ok=false → error); a non-numeric value errors; zero is omitted per
// proto3 default.
func TestAsInt32_PresenceVsNilVsBadType(t *testing.T) {
	env := milestoneEnvelope()

	t.Run("absent key is skipped (no error)", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1"} // stage_from absent
		bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionStageUp
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetStageFrom())
	})

	t.Run("present-but-nil int32 fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "stage_from": nil}
		_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stage_from")
	})

	t.Run("string on int32 slot fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "stage_from": "3"}
		_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stage_from")
	})

	t.Run("zero int32 value is omitted (proto3 default)", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "stage_from": 0}
		bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionStageUp
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetStageFrom())
	})
}

// -----------------------------------------------------------------------------
// asFloat64 — every numeric type branch, round-tripped through the fixed64
// double slot of CompanionBreedRevealed.rolled_probability.
// -----------------------------------------------------------------------------

func TestAsFloat64_AllNumericTypeBranches(t *testing.T) {
	env := milestoneEnvelope()
	cases := []struct {
		name string
		in   any
		want float64
	}{
		{"float64", float64(1.5), 1.5},
		{"float32", float32(2.5), 2.5},
		{"int", int(3), 3},
		{"int32", int32(4), 4},
		{"int64", int64(5), 5},
		{"uint", uint(6), 6},
		{"uint32", uint32(7), 7},
		{"uint64", uint64(8), 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"companion_id":       "fam-1",
				"rolled_probability": tc.in,
			}
			bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
			require.NoError(t, err)
			var msg consumptionv1.CompanionBreedRevealed
			require.NoError(t, proto.Unmarshal(bz, &msg))
			assert.InDelta(t, tc.want, msg.GetRolledProbability(), 0.0001)
		})
	}
}

// On a double slot: absent key skipped; present-but-nil fails loud (same gate
// asymmetry as int32); non-numeric errors; zero omitted per proto3 default.
func TestAsFloat64_PresenceVsNilVsBadType_ZeroOmitted(t *testing.T) {
	env := milestoneEnvelope()

	t.Run("absent rolled_probability skipped", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1"} // absent
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetRolledProbability())
	})

	t.Run("present-but-nil rolled_probability fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "rolled_probability": nil}
		_, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rolled_probability")
	})

	t.Run("string on double slot fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "rolled_probability": "3.5"}
		_, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rolled_probability")
	})

	t.Run("zero double omitted", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "rolled_probability": 0.0}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetRolledProbability())
	})
}

// -----------------------------------------------------------------------------
// writeBoolField — true emits varint 1; false is omitted (proto3 default);
// wrong type fails loud.
// -----------------------------------------------------------------------------

func TestWriteBoolField_TrueFalseAndTypeError(t *testing.T) {
	env := milestoneEnvelope()

	t.Run("false is omitted", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "shiny_variant": false}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.False(t, msg.GetShinyVariant())
	})

	t.Run("true is encoded", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "shiny_variant": true}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.True(t, msg.GetShinyVariant())
	})

	t.Run("non-bool fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "shiny_variant": "yes"}
		_, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "shiny_variant")
	})

	t.Run("missing key is skipped", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1"}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.False(t, msg.GetShinyVariant())
	})
}

// -----------------------------------------------------------------------------
// writeRepeatedStringField — nil + wrong-type + []any-of-string branches.
// stage_up.newly_unlocked_tools is the field under test.
// -----------------------------------------------------------------------------

func TestWriteRepeatedStringField_Branches(t *testing.T) {
	env := milestoneEnvelope()

	t.Run("explicit nil value is skipped", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "newly_unlocked_tools": nil}
		bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionStageUp
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetNewlyUnlockedTools())
	})

	t.Run("[]any of strings is accepted", func(t *testing.T) {
		payload := map[string]any{
			"companion_id":         "fam-1",
			"newly_unlocked_tools": []any{"a", "b"},
		}
		bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionStageUp
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, []string{"a", "b"}, msg.GetNewlyUnlockedTools())
	})

	t.Run("non-slice fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "newly_unlocked_tools": "not-a-slice"}
		_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "newly_unlocked_tools")
	})

	t.Run("[]any with a non-string element fails loud", func(t *testing.T) {
		payload := map[string]any{
			"companion_id":         "fam-1",
			"newly_unlocked_tools": []any{"a", 42},
		}
		_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "newly_unlocked_tools")
	})
}

// -----------------------------------------------------------------------------
// writeSpeciesEnumField — non-string, empty (skip), and zero-mapped ("" enum)
// branches. Unknown-species + known-species are already covered elsewhere.
// -----------------------------------------------------------------------------

func TestWriteSpeciesEnumField_Branches(t *testing.T) {
	env := milestoneEnvelope()

	t.Run("non-string species fails loud", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "species": 4}
		_, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "species")
	})

	t.Run("empty species string is skipped", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1", "species": ""}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, consumptionv1.CompanionSpecies_COMPANION_SPECIES_UNSPECIFIED, msg.GetSpecies())
	})

	t.Run("missing species key is skipped", func(t *testing.T) {
		payload := map[string]any{"companion_id": "fam-1"}
		bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionBreedRevealed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, consumptionv1.CompanionSpecies_COMPANION_SPECIES_UNSPECIFIED, msg.GetSpecies())
	})

	t.Run("every canonical species maps to its enum value", func(t *testing.T) {
		want := map[string]consumptionv1.CompanionSpecies{
			"owl":     consumptionv1.CompanionSpecies_COMPANION_SPECIES_OWL,
			"fox":     consumptionv1.CompanionSpecies_COMPANION_SPECIES_FOX,
			"cat":     consumptionv1.CompanionSpecies_COMPANION_SPECIES_CAT,
			"dragon":  consumptionv1.CompanionSpecies_COMPANION_SPECIES_DRAGON,
			"phoenix": consumptionv1.CompanionSpecies_COMPANION_SPECIES_PHOENIX,
			"turtle":  consumptionv1.CompanionSpecies_COMPANION_SPECIES_TURTLE,
			"wolf":    consumptionv1.CompanionSpecies_COMPANION_SPECIES_WOLF,
			"raven":   consumptionv1.CompanionSpecies_COMPANION_SPECIES_RAVEN,
		}
		for s, enum := range want {
			payload := map[string]any{"companion_id": "fam-1", "species": s}
			bz, err := protomarshal.MarshalPayload(topicBreedRevealed, env, payload)
			require.NoError(t, err, s)
			var msg consumptionv1.CompanionBreedRevealed
			require.NoError(t, proto.Unmarshal(bz, &msg), s)
			assert.Equal(t, enum, msg.GetSpecies(), s)
		}
	})
}

// -----------------------------------------------------------------------------
// writeFixed64DoubleField type-error + writeTimestampField type-error are
// reached through breed_revealed / stage_up specific fields, distinct from the
// chat-turn closures already exercised.
// -----------------------------------------------------------------------------

func TestWriteTimestampField_WrongTypeFailsLoud(t *testing.T) {
	env := milestoneEnvelope()
	payload := map[string]any{"companion_id": "fam-1", "stage_up_at": "2026-05-16"}
	_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stage_up_at")
}

// asTime accepts a non-nil *time.Time, but a present nil *time.Time on a
// timestamp slot is a hard error: the map key is present, so writeTimestampField
// calls asTime, which hits the *time.Time case, sees the nil pointer, and
// returns ok=false → "expected time.Time" error. (Same present-but-nil
// asymmetry as the int32/double slots.)
func TestAsTime_NilTimePointerFailsLoud(t *testing.T) {
	env := milestoneEnvelope()
	var tp *time.Time // nil pointer
	payload := map[string]any{"companion_id": "fam-1", "stage_up_at": tp}
	_, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stage_up_at")
}

// asTime accepts a non-nil *time.Time and round-trips it onto the wire.
func TestAsTime_NonNilTimePointerAccepted(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	payload := map[string]any{"companion_id": "fam-1", "stage_up_at": &now}
	bz, err := protomarshal.MarshalPayload(topicStageUp, env, payload)
	require.NoError(t, err)
	var msg consumptionv1.CompanionStageUp
	require.NoError(t, proto.Unmarshal(bz, &msg))
	require.NotNil(t, msg.GetStageUpAt())
	assert.Equal(t, now.Unix(), msg.GetStageUpAt().AsTime().Unix())
}

// -----------------------------------------------------------------------------
// writeStringField type-error propagation through each milestone encoder.
// Each encoder returns the first field error, so a single bad string slot per
// encoder exercises its early-return error path.
// -----------------------------------------------------------------------------

func TestStringFieldTypeError_PropagatesPerEncoder(t *testing.T) {
	env := milestoneEnvelope()
	cases := []struct {
		name  string
		topic string
		key   string
	}{
		{"stage_up owner_gcid", topicStageUp, "owner_gcid"},
		{"hatched display_name", topicHatched, "display_name"},
		{"source_revelation preview_llm_tier", topicSourceRevelation, "preview_llm_tier"},
		{"breed_revealed rarity", topicBreedRevealed, "rarity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"companion_id": "fam-1", tc.key: 999}
			_, err := protomarshal.MarshalPayload(tc.topic, env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

// -----------------------------------------------------------------------------
// Full-field round trip through every milestone encoder so the int32 / repeated
// / timestamp tail fields (later cases in each encoder) are exercised, plus the
// CompanionHatched + CompanionSourceRevelation specific int32/timestamp slots.
// -----------------------------------------------------------------------------

func TestSourceRevelation_FullFieldRoundTrip(t *testing.T) {
	env := milestoneEnvelope()
	now := env.OccurredAt
	expires := now.Add(time.Hour)
	payload := map[string]any{
		"companion_id":            "fam-1",
		"owner_gcid":              env.GCID,
		"preview_llm_tier":        "pro",
		"preview_tools":           []any{"kg", "rag"},
		"revelation_at":           now,
		"window_expires_at":       expires,
		"window_duration_seconds": int64(3600),
	}
	bz, err := protomarshal.MarshalPayload(topicSourceRevelation, env, payload)
	require.NoError(t, err)
	var msg consumptionv1.CompanionSourceRevelation
	require.NoError(t, proto.Unmarshal(bz, &msg))
	assert.Equal(t, int32(3600), msg.GetWindowDurationSeconds())
	assert.Equal(t, []string{"kg", "rag"}, msg.GetPreviewTools())
	require.NotNil(t, msg.GetRevelationAt())
	assert.Equal(t, now.Unix(), msg.GetRevelationAt().AsTime().Unix())
	require.NotNil(t, msg.GetWindowExpiresAt())
	assert.Equal(t, expires.Unix(), msg.GetWindowExpiresAt().AsTime().Unix())
}

// -----------------------------------------------------------------------------
// Chat-turn: full int32 round-trips through the generated type. The existing
// suite only walks tags; here the coerced int32 values are asserted, covering
// the int32Field closure's encode branch with non-zero values.
// -----------------------------------------------------------------------------

func TestChatTurn_Int32FieldsRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	now := env.OccurredAt
	payload := map[string]any{
		"session_id":       "s-1",
		"output_tokens":    int64(120),
		"mana_charged":     int32(7),
		"tool_calls_count": float64(3),
		"started_at":       now,
		"completed_at":     now.Add(time.Second),
	}
	bz, err := protomarshal.MarshalPayload(topicChatTurn, env, payload)
	require.NoError(t, err)
	var msg consumptionv1.CompanionChatTurnCompleted
	require.NoError(t, proto.Unmarshal(bz, &msg))
	assert.Equal(t, int32(120), msg.GetOutputTokens())
	assert.Equal(t, int32(7), msg.GetManaCharged())
	assert.Equal(t, int32(3), msg.GetToolCallsCount())
	require.NotNil(t, msg.GetStartedAt())
	require.NotNil(t, msg.GetCompletedAt())
}

// Chat-turn closures: int32 closure type error + completed_at timestamp closure
// type error (distinct from started_at already covered) + missing-key skips.
func TestChatTurn_ClosureBranches(t *testing.T) {
	env := fixedEnvelope()

	t.Run("mana_charged wrong type errors", func(t *testing.T) {
		payload := map[string]any{"session_id": "s-1", "mana_charged": []int{1}}
		_, err := protomarshal.MarshalPayload(topicChatTurn, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mana_charged")
	})

	t.Run("completed_at wrong type errors", func(t *testing.T) {
		payload := map[string]any{"session_id": "s-1", "completed_at": 1234}
		_, err := protomarshal.MarshalPayload(topicChatTurn, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "completed_at")
	})
}

// -----------------------------------------------------------------------------
// DailyDose: dose_reason coercion lands on the wire enum; entries-via-atom_ids
// (legacy) vs typed entries are both round-tripped.
// -----------------------------------------------------------------------------

func TestDailyDose_TypedEntries_DoseReasonOnWire(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"learner_gcid": env.GCID,
		"entries": []map[string]any{
			{"atom_id": "a-1", "topic": "frac", "title": "Halves", "dose_reason": int64(2)},
		},
		"size": 1,
	}
	bz, err := protomarshal.MarshalPayload(topicDailyDose, env, payload)
	require.NoError(t, err)
	var msg consumptionv1.DailyDoseServed
	require.NoError(t, proto.Unmarshal(bz, &msg))
	require.Len(t, msg.GetEntries(), 1)
	e := msg.GetEntries()[0]
	assert.Equal(t, "a-1", e.GetAtomId())
	assert.Equal(t, "frac", e.GetTopic())
	assert.Equal(t, "Halves", e.GetTitle())
	// dose_reason int64(2) → DoseReason enum value 2 on the wire.
	assert.Equal(t, int32(2), int32(e.GetDoseReason()))
}

// stringSlice rejects []any with a non-string element on the atom_ids path of
// DailyDose; the encoder then falls through (atom_ids ok=false) leaving entries
// empty rather than erroring (atom_ids is the legacy fallback, not validated).
func TestDailyDose_AtomIDsNonStringElement_FallsThroughEmpty(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"learner_gcid": env.GCID,
		"atom_ids":     []any{"ok", 7}, // non-string element → stringSlice returns ok=false
		"size":         2,
	}
	bz, err := protomarshal.MarshalPayload(topicDailyDose, env, payload)
	require.NoError(t, err)
	var msg consumptionv1.DailyDoseServed
	require.NoError(t, proto.Unmarshal(bz, &msg))
	// atom_ids coercion failed silently → no entries emitted.
	assert.Empty(t, msg.GetEntries())
	assert.Equal(t, int32(2), msg.GetSize())
}

// IsUnsupportedTopic returns false for a nil and a plain error.
func TestIsUnsupportedTopic_NegativeCases(t *testing.T) {
	assert.False(t, protomarshal.IsUnsupportedTopic(nil))
	assert.False(t, protomarshal.IsUnsupportedTopic(assert.AnError))
}

// -----------------------------------------------------------------------------
// nil payload through every milestone encoder: each returns just the envelope
// bytes (the `if payload == nil { return out, nil }` guard), and that envelope
// is the well-formed leading length-delimited field 1.
// -----------------------------------------------------------------------------

func TestNilPayload_AllMilestoneEncoders_EnvelopeOnly(t *testing.T) {
	env := milestoneEnvelope()
	topics := []string{
		topicStageUp,
		topicBreedRevealed,
		topicHatched,
		topicSourceRevelation,
		topicChatTurn,
	}
	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			bz, err := protomarshal.MarshalPayload(topic, env, nil)
			require.NoError(t, err)
			require.NotEmpty(t, bz)
			num, typ, n := protowireConsumeTag(t, bz)
			assert.Equal(t, 1, num, "leading field must be envelope")
			assert.Equal(t, 2, typ, "envelope is length-delimited (BytesType=2)")
			assert.Positive(t, n)
		})
	}
}

// -----------------------------------------------------------------------------
// proto3 default-value omission via the typed-writer empty-string / zero-int32
// skip branches (writeStringField + chat-turn closures). An empty string on a
// string slot and a zero on an int32 slot must NOT appear on the wire.
// -----------------------------------------------------------------------------

func TestEmptyStringAndZeroInt_AreOmittedFromWire(t *testing.T) {
	env := fixedEnvelope()

	t.Run("chat-turn empty model + zero output_tokens omitted", func(t *testing.T) {
		payload := map[string]any{
			"session_id":    "s-1",
			"model":         "",       // string slot, empty → skip
			"output_tokens": int32(0), // int32 slot, zero → skip
		}
		bz, err := protomarshal.MarshalPayload(topicChatTurn, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionChatTurnCompleted
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetModel())
		assert.Zero(t, msg.GetOutputTokens())
		assert.Equal(t, "s-1", msg.GetSessionId())
	})

	t.Run("stage_up empty owner_gcid omitted (writeStringField skip)", func(t *testing.T) {
		menv := milestoneEnvelope()
		payload := map[string]any{"companion_id": "fam-1", "owner_gcid": ""}
		bz, err := protomarshal.MarshalPayload(topicStageUp, menv, payload)
		require.NoError(t, err)
		var msg consumptionv1.CompanionStageUp
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetOwnerGcid())
		assert.Equal(t, "fam-1", msg.GetCompanionId())
	})
}

// -----------------------------------------------------------------------------
// Per-field error propagation at LATER field positions in each encoder. Each
// encoder is a straight-line sequence of writeXField calls with `if err != nil
// { return nil, err }` after each. Forcing a type error on a tail field (with
// all earlier fields valid) exercises a distinct early-return block per slot.
// -----------------------------------------------------------------------------

func TestEncoder_TailFieldErrors_PropagatePerSlot(t *testing.T) {
	menv := milestoneEnvelope()
	now := menv.OccurredAt

	t.Run("stage_up tail slots", func(t *testing.T) {
		base := func() map[string]any {
			return map[string]any{
				"companion_id": "fam-1", "owner_gcid": "g", "stage_from": 1, "stage_to": 2,
				"stage_from_name": "a", "stage_to_name": "b",
			}
		}
		for _, bad := range []struct {
			key string
			val any
		}{
			{"newly_unlocked_tools", "not-a-slice"},
			{"newly_revealed_kg_neighbors", 5},
			{"new_llm_tier", 1},
			{"new_memory_mode", 2},
			{"stage_up_at", "not-time"},
		} {
			p := base()
			p[bad.key] = bad.val
			_, err := protomarshal.MarshalPayload(topicStageUp, menv, p)
			require.Error(t, err, bad.key)
			assert.Contains(t, err.Error(), bad.key)
		}
	})

	t.Run("breed_revealed tail slots", func(t *testing.T) {
		base := func() map[string]any {
			return map[string]any{
				"companion_id": "fam-1", "owner_gcid": "g", "species": "owl",
				"shiny_variant": true,
			}
		}
		for _, bad := range []struct {
			key string
			val any
		}{
			{"rarity", 1},
			{"egg_sku", 2},
			{"rolled_probability", "x"},
			{"revealed_at", "x"},
		} {
			p := base()
			p[bad.key] = bad.val
			_, err := protomarshal.MarshalPayload(topicBreedRevealed, menv, p)
			require.Error(t, err, bad.key)
			assert.Contains(t, err.Error(), bad.key)
		}
	})

	t.Run("hatched tail slots", func(t *testing.T) {
		base := func() map[string]any {
			return map[string]any{
				"companion_id": "fam-1", "owner_gcid": "g", "display_name": "n",
				"tone": "t", "learner_persona": "p", "resonant_atom_id": "a",
				"specialization": "math", "species": "owl",
			}
		}
		for _, bad := range []struct {
			key string
			val any
		}{
			{"tone", 1},
			{"learner_persona", 2},
			{"resonant_atom_id", 3},
			{"specialization", 4},
			{"shiny_variant", "no"},
			{"hatched_at", "x"},
		} {
			p := base()
			p[bad.key] = bad.val
			_, err := protomarshal.MarshalPayload(topicHatched, menv, p)
			require.Error(t, err, bad.key)
			assert.Contains(t, err.Error(), bad.key)
		}
	})

	t.Run("source_revelation tail slots", func(t *testing.T) {
		base := func() map[string]any {
			return map[string]any{
				"companion_id": "fam-1", "owner_gcid": "g", "preview_llm_tier": "pro",
				"preview_tools": []string{"kg"}, "revelation_at": now,
			}
		}
		for _, bad := range []struct {
			key string
			val any
		}{
			{"preview_tools", "not-a-slice"},
			{"revelation_at", "x"},
			{"window_expires_at", "x"},
			{"window_duration_seconds", "x"},
		} {
			p := base()
			p[bad.key] = bad.val
			_, err := protomarshal.MarshalPayload(topicSourceRevelation, menv, p)
			require.Error(t, err, bad.key)
			assert.Contains(t, err.Error(), bad.key)
		}
	})
}

// hatched: full happy-path round trip so the trailing display/tone/persona/
// resonant/specialization string slots + species + hatched_at all land on the
// wire and are observably correct (covers the success branch of every slot).
func TestHatched_FullFieldRoundTrip(t *testing.T) {
	menv := milestoneEnvelope()
	now := menv.OccurredAt
	payload := map[string]any{
		"companion_id":     "fam-1",
		"owner_gcid":       "g",
		"display_name":     "Sparkle",
		"tone":             "socratic",
		"learner_persona":  "explorer",
		"resonant_atom_id": "atom-x",
		"specialization":   "history",
		"species":          "phoenix",
		"shiny_variant":    true,
		"hatched_at":       now,
	}
	bz, err := protomarshal.MarshalPayload(topicHatched, menv, payload)
	require.NoError(t, err)
	var msg consumptionv1.CompanionHatched
	require.NoError(t, proto.Unmarshal(bz, &msg))
	assert.Equal(t, "Sparkle", msg.GetDisplayName())
	assert.Equal(t, "socratic", msg.GetTone())
	assert.Equal(t, "explorer", msg.GetLearnerPersona())
	assert.Equal(t, "atom-x", msg.GetResonantAtomId())
	assert.Equal(t, "history", msg.GetSpecialization())
	assert.Equal(t, consumptionv1.CompanionSpecies_COMPANION_SPECIES_PHOENIX, msg.GetSpecies())
	assert.True(t, msg.GetShinyVariant())
	require.NotNil(t, msg.GetHatchedAt())
	assert.Equal(t, now.Unix(), msg.GetHatchedAt().AsTime().Unix())
}

// Head/mid-field error propagation: bad value on the FIRST (companion_id) and
// other early slots of each encoder exercises the early `return nil, err`
// blocks that the tail-error tests (which keep early fields valid) do not.
func TestEncoder_HeadAndMidFieldErrors_PropagatePerSlot(t *testing.T) {
	menv := milestoneEnvelope()

	t.Run("companion_id bad in every milestone encoder", func(t *testing.T) {
		for _, topic := range []string{topicStageUp, topicBreedRevealed, topicHatched, topicSourceRevelation} {
			payload := map[string]any{"companion_id": 123} // string slot, int value
			_, err := protomarshal.MarshalPayload(topic, menv, payload)
			require.Error(t, err, topic)
			assert.Contains(t, err.Error(), "companion_id", topic)
		}
	})

	t.Run("owner_gcid bad in stage_up/breed/hatched/source", func(t *testing.T) {
		for _, topic := range []string{topicStageUp, topicBreedRevealed, topicHatched, topicSourceRevelation} {
			payload := map[string]any{"companion_id": "fam-1", "owner_gcid": 7}
			_, err := protomarshal.MarshalPayload(topic, menv, payload)
			require.Error(t, err, topic)
			assert.Contains(t, err.Error(), "owner_gcid", topic)
		}
	})

	t.Run("stage_up mid slots: stage_to / stage_from_name / stage_to_name", func(t *testing.T) {
		for _, bad := range []struct {
			key string
			val any
		}{
			{"stage_to", "x"},
			{"stage_from_name", 1},
			{"stage_to_name", 2},
		} {
			payload := map[string]any{
				"companion_id": "fam-1", "owner_gcid": "g", "stage_from": 1,
			}
			payload[bad.key] = bad.val
			_, err := protomarshal.MarshalPayload(topicStageUp, menv, payload)
			require.Error(t, err, bad.key)
			assert.Contains(t, err.Error(), bad.key)
		}
	})

	t.Run("hatched species bad (mid enum slot)", func(t *testing.T) {
		payload := map[string]any{
			"companion_id": "fam-1", "owner_gcid": "g", "display_name": "n",
			"tone": "t", "learner_persona": "p", "resonant_atom_id": "a",
			"specialization": "math", "species": 9, // non-string enum slot
		}
		_, err := protomarshal.MarshalPayload(topicHatched, menv, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "species")
	})
}

// protowireConsumeTag is a tiny helper mirroring the field-walk pattern the
// established suite uses, returning (fieldNum, wireType, bytesConsumed).
func protowireConsumeTag(t *testing.T, b []byte) (int, int, int) {
	t.Helper()
	num, typ, n := protowire.ConsumeTag(b)
	require.Positive(t, n, "valid leading tag")
	return int(num), int(typ), n
}
