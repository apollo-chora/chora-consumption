// helper_writers_test.go — closes the loose-typed coercion + typed-writer
// helper branches that the per-encoder suites only partially reach. These are
// the characteristically-under-covered helpers:
//
//   - asInt64 / writeInt64Field (HexagonFogGenerated's cost/token slots)
//   - writeFixed32FloatField (WeaknessGrown.final_strength)
//   - writeStringFieldAlias (HexagonFogGenerated run_id / model_id)
//   - writeStringMapField + writeTypePlanQuotas (AiAssistStartedV2)
//   - writeNeighborRelationEnumField, writeRitualRunStatusEnumField,
//     writeSpeciesEnumField unknown-value arms
//
// Values are round-tripped through the generated proto bindings so each branch
// is observably correct, not merely panic-free.
package protomarshal_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// -------------------------------------------------------------------------
// asInt64 — every numeric branch, observed through writeInt64Field on
// HexagonFogGenerated.total_cost_micros.
// -------------------------------------------------------------------------

func TestAsInt64_AllNumericTypeBranches(t *testing.T) {
	env := fixedEnvelope()
	tooBig := uint64(1<<63 - 1) // exercises uint64 → int64 narrowing without overflow to negative
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"int", int(7), 7},
		{"int32", int32(8), 8},
		{"int64", int64(9), 9},
		{"float32", float32(10.9), 10},
		{"float64", float64(11.9), 11},
		{"uint", uint(12), 12},
		{"uint32", uint32(13), 13},
		{"uint64", uint64(14), 14},
		{"uint64 top of int64", tooBig, int64(tooBig)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"total_cost_micros": tc.in}
			bz, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
			require.NoError(t, err)
			var msg consumptionv1.HexagonFogGenerated
			require.NoError(t, proto.Unmarshal(bz, &msg))
			assert.Equal(t, tc.want, msg.GetTotalCostMicros())
		})
	}
}

func TestWriteInt64Field_PresenceNilBadAndZero(t *testing.T) {
	env := fixedEnvelope()

	t.Run("absent key omitted", func(t *testing.T) {
		bz, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, map[string]any{})
		require.NoError(t, err)
		var msg consumptionv1.HexagonFogGenerated
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetTotalCostMicros())
	})

	t.Run("present-but-nil fails loud", func(t *testing.T) {
		payload := map[string]any{"total_cost_micros": nil}
		_, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "total_cost_micros")
	})

	t.Run("non-numeric fails loud", func(t *testing.T) {
		payload := map[string]any{"total_cost_micros": "100"}
		_, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "total_cost_micros")
	})

	t.Run("zero omitted", func(t *testing.T) {
		payload := map[string]any{"total_cost_micros": int64(0)}
		bz, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
		require.NoError(t, err)
		var msg consumptionv1.HexagonFogGenerated
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetTotalCostMicros())
	})
}

// counterExampleKey gives the playground a harmless no-op map (the absent-key
// subtest needs a payload that is non-nil but without the field under test).
func counterExampleKey(t *testing.T) string { return "cluster_id" }

// -------------------------------------------------------------------------
// writeFixed32FloatField — zero-omit + wrong-type via WeaknessGrown.
// -------------------------------------------------------------------------

func TestWriteFixed32FloatField_ZeroOmitAndBadType(t *testing.T) {
	env := fixedEnvelope()

	t.Run("zero omitted (proto3 default)", func(t *testing.T) {
		payload := map[string]any{"final_strength": 0.0}
		bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.grown.v1", env, payload)
		require.NoError(t, err)
		var msg consumptionv1.WeaknessGrown
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Zero(t, msg.GetFinalStrength())
	})

	t.Run("non-numeric fails loud", func(t *testing.T) {
		payload := map[string]any{"final_strength": "strong"}
		_, err := protomarshal.MarshalPayload("chora.consumption.weakness.grown.v1", env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "final_strength")
	})

	t.Run("nonzero float32 lands on wire", func(t *testing.T) {
		payload := map[string]any{"final_strength": 0.42}
		bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.grown.v1", env, payload)
		require.NoError(t, err)
		var msg consumptionv1.WeaknessGrown
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.InDelta(t, 0.42, float64(msg.GetFinalStrength()), 0.0001)
	})
}

// -------------------------------------------------------------------------
// writeStringFieldAlias — every key-presence branch via HexagonFogGenerated
// run_id (canonical "run_id", producer alias "generated_by_run_id").
// -------------------------------------------------------------------------

func TestWriteStringFieldAlias_Branches(t *testing.T) {
	env := fixedEnvelope()
	topic := "chora.consumption.kg_hexagon_fog.generated.v1"

	t.Run("alias present, canonical absent", func(t *testing.T) {
		payload := map[string]any{"generated_by_run_id": "run-alias"}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.HexagonFogGenerated
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, "run-alias", msg.GetRunId())
	})

	t.Run("canonical present beats alias", func(t *testing.T) {
		payload := map[string]any{"run_id": "run-canon", "generated_by_run_id": "run-alias"}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.HexagonFogGenerated
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, "run-canon", msg.GetRunId())
	})

	t.Run("canonical empty continues to alias", func(t *testing.T) {
		payload := map[string]any{"run_id": "", "generated_by_run_id": "run-alias"}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.HexagonFogGenerated
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, "run-alias", msg.GetRunId())
	})

	t.Run("alias wrong type fails loud", func(t *testing.T) {
		payload := map[string]any{"generated_by_run_id": 99}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "generated_by_run_id")
	})

	t.Run("canonical wrong type fails loud", func(t *testing.T) {
		payload := map[string]any{"run_id": 99}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "run_id")
	})
}

// -------------------------------------------------------------------------
// writeStringMapField — map[string]string / map[string]any / error arms via
// the AiAssistStartedV2 metadata slot.
// -------------------------------------------------------------------------

func TestWriteStringMapField_Branches(t *testing.T) {
	env := fixedEnvelope()
	topic := protomarshal.TopicAiAssistStartedV2

	t.Run("nil metadata emits nothing", func(t *testing.T) {
		payload := map[string]any{"metadata": nil}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg creationv1.AiAssistStartedV2
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetMetadata())
	})

	t.Run("map[string]string round-trips", func(t *testing.T) {
		payload := map[string]any{"metadata": map[string]string{"b": "2", "a": "1"}}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg creationv1.AiAssistStartedV2
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, map[string]string{"a": "1", "b": "2"}, msg.GetMetadata())
	})

	t.Run("map[string]any round-trips", func(t *testing.T) {
		payload := map[string]any{"metadata": map[string]any{"k": "v"}}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg creationv1.AiAssistStartedV2
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, map[string]string{"k": "v"}, msg.GetMetadata())
	})

	t.Run("map[string]any non-string value fails loud", func(t *testing.T) {
		payload := map[string]any{"metadata": map[string]any{"k": 7}}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "metadata")
	})

	t.Run("non-map fails loud", func(t *testing.T) {
		payload := map[string]any{"metadata": "plain"}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "metadata")
	})
}

// -------------------------------------------------------------------------
// writeTypePlanQuotas — []map / []any / error arms via AiAssistStartedV2.
// -------------------------------------------------------------------------

func TestWriteTypePlanQuotas_Branches(t *testing.T) {
	env := fixedEnvelope()
	topic := protomarshal.TopicAiAssistStartedV2

	t.Run("nil type_plan emits nothing", func(t *testing.T) {
		payload := map[string]any{"type_plan": nil}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg creationv1.AiAssistStartedV2
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetTypePlan())
	})

	t.Run("[]any of maps round-trips", func(t *testing.T) {
		payload := map[string]any{"type_plan": []any{
			map[string]any{"question_type": "mcq", "count": 4, "max_images": 2},
			map[string]any{"question_type": "oe", "count": 1},
		}}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg creationv1.AiAssistStartedV2
		require.NoError(t, proto.Unmarshal(bz, &msg))
		require.Len(t, msg.GetTypePlan(), 2)
		q0 := msg.GetTypePlan()[0]
		assert.Equal(t, "mcq", q0.GetQuestionType())
		assert.Equal(t, int32(4), q0.GetCount())
		assert.Equal(t, int32(2), q0.GetMaxImages())
		assert.Equal(t, "oe", msg.GetTypePlan()[1].GetQuestionType())
	})

	t.Run("[]any non-map element fails loud", func(t *testing.T) {
		payload := map[string]any{"type_plan": []any{"nope"}}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type_plan")
	})

	t.Run("per-quota question_type type error", func(t *testing.T) {
		payload := map[string]any{"type_plan": []map[string]any{{"question_type": 7}}}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "question_type")
	})

	t.Run("per-quota count type error", func(t *testing.T) {
		payload := map[string]any{"type_plan": []map[string]any{{"count": "four"}}}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "count")
	})

	t.Run("per-quota max_images type error", func(t *testing.T) {
		payload := map[string]any{"type_plan": []map[string]any{{"max_images": "two"}}}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_images")
	})
}

// -------------------------------------------------------------------------
// writeNeighborRelationEnumField — known/empty/unknown + non-string arms via
// the focal_changed encoder's top-level relation slot.
// -------------------------------------------------------------------------

func TestWriteNeighborRelationEnumField_Branches(t *testing.T) {
	env := fixedEnvelope()
	topic := "chora.consumption.kg_exploration.focal_changed.v1"
	now := env.OccurredAt
	known := map[string]consumptionv1.NeighborRelation{
		"prerequisite_of": consumptionv1.NeighborRelation_NEIGHBOR_RELATION_PREREQUISITE_OF,
		"extends":         consumptionv1.NeighborRelation_NEIGHBOR_RELATION_EXTENDS,
		"analogy_of":      consumptionv1.NeighborRelation_NEIGHBOR_RELATION_ANALOGY_OF,
		"contrasts_with":  consumptionv1.NeighborRelation_NEIGHBOR_RELATION_CONTRASTS_WITH,
		"applied_in":      consumptionv1.NeighborRelation_NEIGHBOR_RELATION_APPLIED_IN,
		"curiosity_jump":  consumptionv1.NeighborRelation_NEIGHBOR_RELATION_CURIOSITY_JUMP,
	}

	t.Run("every known relation maps", func(t *testing.T) {
		for s, enum := range known {
			payload := map[string]any{"relation": s, "traversed_at": now}
			bz, err := protomarshal.MarshalPayload(topic, env, payload)
			require.NoError(t, err, s)
			var msg consumptionv1.ExplorationFocalNodeChanged
			require.NoError(t, proto.Unmarshal(bz, &msg), s)
			assert.Equal(t, enum, msg.GetRelation(), s)
		}
	})

	t.Run("empty relation omitted", func(t *testing.T) {
		payload := map[string]any{"relation": ""}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.ExplorationFocalNodeChanged
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, consumptionv1.NeighborRelation_NEIGHBOR_RELATION_UNSPECIFIED, msg.GetRelation())
	})

	t.Run("unknown relation fails loud", func(t *testing.T) {
		payload := map[string]any{"relation": "sideways"}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "relation")
	})
}

// -------------------------------------------------------------------------
// writeRitualRunStatusEnumField — unknown / non-string / empty arms via
// ritual_run_completed (known statuses are already round-tripped upstream).
// -------------------------------------------------------------------------

func TestWriteRitualRunStatusEnumField_Branches(t *testing.T) {
	env := fixedEnvelope()
	topic := "chora.consumption.companion.ritual_run_completed.v1"

	t.Run("every canonical status maps", func(t *testing.T) {
		statuses := map[string]consumptionv1.RitualRunStatus{
			"running":        consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_RUNNING,
			"completed":      consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_COMPLETED,
			"failed":         consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_FAILED,
			"skipped_budget": consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_SKIPPED_BUDGET,
			"blocked":        consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_BLOCKED,
		}
		for s, enum := range statuses {
			payload := map[string]any{"status": s}
			bz, err := protomarshal.MarshalPayload(topic, env, payload)
			require.NoError(t, err, s)
			var msg consumptionv1.RitualRunCompleted
			require.NoError(t, proto.Unmarshal(bz, &msg), s)
			assert.Equal(t, enum, msg.GetStatus(), s)
		}
	})

	t.Run("empty status omitted", func(t *testing.T) {
		payload := map[string]any{"status": ""}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.RitualRunCompleted
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Equal(t, consumptionv1.RitualRunStatus_RITUAL_RUN_STATUS_UNSPECIFIED, msg.GetStatus())
	})

	t.Run("non-string status fails loud", func(t *testing.T) {
		payload := map[string]any{"status": 2}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status")
	})

	t.Run("unknown status fails loud", func(t *testing.T) {
		payload := map[string]any{"status": "paused"}
		_, err := protomarshal.MarshalPayload(topic, env, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status")
	})
}

// -------------------------------------------------------------------------
// writeSpeciesEnumField — unknown species error (the last reachable arm).
// -------------------------------------------------------------------------

func TestWriteSpeciesEnumField_UnknownFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{"species": "griffin"}
	_, err := protomarshal.MarshalPayload("chora.consumption.companion.hatched.v1", env, payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "species")
}

// -------------------------------------------------------------------------
// writeTimestampField convenience — a non-string, non-time value only. (The
// present-but-nil *time.Time path is asserted in the existing cover suite.)
// -------------------------------------------------------------------------

func TestEnvelopeCarriesImdaDimensionFromPayload(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"learner_gcid":         env.GCID,
		"chora_imda_dimension": "civic_literacy",
		"imda_lifecycle_stage": "assessment",
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.daily_dose.served.v1", env, payload)
	require.NoError(t, err)
	var msg consumptionv1.DailyDoseServed
	require.NoError(t, proto.Unmarshal(bz, &msg))
	gotEnv := msg.GetEnvelope()
	require.NotNil(t, gotEnv)
	assert.Equal(t, "civic_literacy", gotEnv.GetChoraImdaDimension())
	assert.Equal(t, "assessment", gotEnv.GetImdaLifecycleStage())
}
