// encoder_error_arms_test.go — closes the per-encoder error arms that the
// happy-path round-trip suites leave uncovered. Every encoder is a straight
// line of `writeXField(...)` calls with `if err != nil { return nil, err }`
// after each. Statement coverage for those early returns requires forcing a
// type error at EVERY field position (an encoder returns at the FIRST bad
// field, so a single error case covers just that one slot). Each case here
// supplies a valid value for every earlier slot and a wrong-typed value at its
// own slot, then asserts the error cites that key.
//
// Also covers encodeConceptDeleted (0% before this file) end-to-end and the
// nested sub-encoder error arms (ExtractedGrowthEdge, OutputSelection,
// RitualStepStamp, HexagonNeighbor) that are only reachable through their
// parent encoders.
package protomarshal_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// errorSlot is one field position of an encoder: the key plus a valid value and
// a wrong-typed value that must fail loud at that slot.
type errorSlot struct {
	key  string
	good any
	bad  any
}

// envelopeOnlyTopics is every registered binary encoder topic; a nil payload
// must return just the envelope for each (exercises each `if payload == nil`
// early-exit).
var envelopeOnlyTopics = []string{
	"chora.consumption.daily_dose.served.v1",
	"chora.consumption.companion.chat_turn_completed.v1",
	"chora.consumption.companion.stage_up.v1",
	"chora.consumption.companion.breed_revealed.v1",
	"chora.consumption.companion.hatched.v1",
	"chora.consumption.companion.source_revelation.v1",
	"chora.consumption.companion.exp_awarded.v1",
	"chora.consumption.companion.kg_neighbor_revealed.v1",
	"chora.consumption.companion.created.v1",
	"chora.consumption.companion.egg_purchased.v1",
	"chora.consumption.companion.stirring.v1",
	"chora.consumption.companion.skill_slot_unlocked.v1",
	"chora.consumption.companion.skill_granted.v1",
	"chora.consumption.companion.loadout_changed.v1",
	"chora.consumption.companion.specialization_changed.v1",
	"chora.consumption.companion.persona_updated.v1",
	"chora.consumption.companion.retired.v1",
	"chora.consumption.companion.ritual_published.v1",
	"chora.consumption.companion.ritual_run_completed.v1",
	"chora.consumption.kg_hexagon_fog.generated.v1",
	"chora.consumption.kg_exploration.focal_changed.v1",
	"chora.consumption.weakness.grown.v1",
	"chora.consumption.weakness.analyzed.v1",
	"chora.consumption.goal.graduated.v1",
	"chora.consumption.goal.progress_updated.v1",
	"chora.consumption.concept.atoms_bound.v1",
	"chora.consumption.concept.deleted.v1",
	"chora.consumption.campaign.focus_assigned.v1",
	"chora.consumption.campaign.rung_cleared.v1",
	"chora.consumption.campaign.node_won.v1",
	"chora.consumption.campaign.node_revealed.v1",
	"chora.consumption.campaign.goal_sealed.v1",
	protomarshal.TopicAiAssistStartedV2,
}

// TestEnvelopeOnlyTopics_NilPayload covers the `if payload == nil { return out,
// nil }` early-exit in every encoder.
func TestEnvelopeOnlyTopics_NilPayload(t *testing.T) {
	env := fixedEnvelope()
	for _, topic := range envelopeOnlyTopics {
		t.Run(topic, func(t *testing.T) {
			bz, err := protomarshal.MarshalPayload(topic, env, nil)
			require.NoError(t, err)
			require.NotEmpty(t, bz, "nil payload must still emit the envelope")
		})
	}
}

// TestPerEncoder_EveryFieldErrorArm drives a wrong-typed value through every
// field position of each encoder that reads fields with the failing-loud typed
// writers. Skipped encoders (daily_dose.served) use silent-skip coercion for
// most slots and have no per-field error arms to exercise.
func TestPerEncoder_EveryFieldErrorArm(t *testing.T) {
	env := fixedEnvelope()
	now := env.OccurredAt

	// friction is the value "fails loud" on a string slot; nz marks a numeric
	// slot; a timestamp slot fails on a bare string.
	s := "x"
	strBad := 999

	specs := []struct {
		name   string
		topic  string
		fields []errorSlot
	}{
		{"chat_turn_completed", "chora.consumption.companion.chat_turn_completed.v1", []errorSlot{
			{"session_id", "s-1", strBad}, {"turn_id", "t-1", strBad},
			{"gcid", "g", strBad}, {"tenant_id", "t", strBad}, {"companion_id", "f", strBad},
			{"engine_session_id", "e", strBad}, {"model", "m", strBad},
			{"output_tokens", 1, s}, {"mana_charged", 1, s}, {"mana_tier", "m", strBad},
			{"tool_calls_count", 1, s}, {"finish_reason", "stop", strBad},
			{"started_at", now, s}, {"completed_at", now, s},
		}},
		{"stirring", "chora.consumption.companion.stirring.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad},
			{"growth_exp", 1, s}, {"hatch_threshold", 1, s}, {"stirred_at", now, s},
		}},
		{"egg_purchased", "chora.consumption.companion.egg_purchased.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"egg_sku", "sku", strBad},
			{"source", "purchase", strBad}, {"stripe_session_id", "cs", strBad},
			{"amount_cents", int64(100), s}, {"currency", "sgd", strBad},
			{"suggested_focal_atom_id", "a", strBad}, {"soft_expiry_at", now, s},
			{"hard_expiry_at", now, s}, {"purchased_at", now, s},
		}},
		{"weakness_grown", "chora.consumption.weakness.grown.v1", []errorSlot{
			{"growth_edge_id", "e", strBad}, {"tenant_id", "t", strBad},
			{"learner_gcid", "g", strBad}, {"concept_label", "l", strBad},
			{"concept_key", "k", strBad}, {"final_strength", 0.5, s},
			{"recovery_source", "r", strBad}, {"tags", []string{"a"}, 5}, {"grown_at", now, s},
		}},
		{"weakness_analyzed", "chora.consumption.weakness.analyzed.v1", []errorSlot{
			{"upload_id", "u", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"edges", []map[string]any{}, 5}, {"model_used", "m", strBad},
			{"input_token_count", 1, s}, {"output_token_count", 1, s}, {"analyzed_at", now, s},
			{"output_selection", map[string]any{}, "x"},
		}},
		{"goal_graduated", "chora.consumption.goal.graduated.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"kind", "k", strBad}, {"chora_target_ref", "c", strBad},
			{"mastered_concepts", 1, s}, {"total_concepts", 1, s}, {"graduated_at", now, s},
		}},
		{"goal_progress_updated", "chora.consumption.goal.progress_updated.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"mastered_concepts", 1, s}, {"total_concepts", 1, s}, {"progress_percent", 1, s},
			{"occurred_at", now, s},
		}},
		{"skill_slot_unlocked", "chora.consumption.companion.skill_slot_unlocked.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"stage_from", 1, s},
			{"stage_to", 1, s}, {"slots_before", 1, s}, {"slots_after", 1, s},
			{"unlocked_at", now, s},
		}},
		{"skill_granted", "chora.consumption.companion.skill_granted.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"skill_key", "k", strBad},
			{"skill_kind", "k", strBad}, {"unlocked_via", "u", strBad},
			{"unlocked_at_stage", 1, s}, {"equipped", true, s}, {"granted_at", now, s},
		}},
		{"loadout_changed", "chora.consumption.companion.loadout_changed.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"skill_key", "k", strBad},
			{"action", "equip", strBad}, {"equipped_skills", []string{"s"}, 5},
			{"slots_used", 1, s}, {"skill_slots_unlocked", 1, s}, {"changed_at", now, s},
		}},
		{"specialization_changed", "chora.consumption.companion.specialization_changed.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad},
			{"previous_specialization", "p", strBad}, {"new_specialization", "n", strBad},
			{"memory_handling", "m", strBad}, {"user_reason", "u", strBad}, {"changed_at", now, s},
		}},
		{"persona_updated", "chora.consumption.companion.persona_updated.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"persona_version", 1, s},
			{"tone", "t", strBad}, {"difficulty_cap", "d", strBad}, {"address_style", "a", strBad},
			{"has_guidance_note", true, s}, {"updated_at", now, s},
		}},
		{"retired", "chora.consumption.companion.retired.v1", []errorSlot{
			{"companion_id", "f", strBad}, {"owner_gcid", "g", strBad}, {"species", "owl", strBad},
			{"level", 1, s}, {"reason", "r", strBad}, {"retired_at", now, s},
		}},
		{"ritual_published", "chora.consumption.companion.ritual_published.v1", []errorSlot{
			{"ritual_id", "r", strBad}, {"companion_id", "f", strBad}, {"revision_no", 1, s},
			{"trigger", "daily", strBad}, {"sink", "s", strBad}, {"published_price_units", 1, s},
		}},
		{"ritual_run_completed", "chora.consumption.companion.ritual_run_completed.v1", []errorSlot{
			{"run_id", "r", strBad}, {"ritual_id", "r", strBad}, {"companion_id", "f", strBad},
			{"owner_gcid", "g", strBad}, {"revision_no", 1, s}, {"trigger_source", "t", strBad},
			{"status", "completed", strBad}, {"mana_charged", 1, s}, {"sink_ref", "s", strBad},
			{"error", "e", strBad}, {"stamps", []map[string]any{}, 5},
		}},
		{"hexagon_fog_generated", "chora.consumption.kg_hexagon_fog.generated.v1", []errorSlot{
			{"run_id", "run-1", strBad}, {"cluster_id", "c", strBad},
			{"exploration_id", "e", strBad}, {"user_gcid", "u", strBad},
			{"focal_atom_id", "f", strBad}, {"neighbors", []map[string]any{}, 5},
			{"model_id", "m", strBad}, {"total_cost_micros", int64(100), s},
			{"input_tokens", int64(100), s}, {"output_tokens", int64(100), s},
			{"generated_at", now, s},
		}},
		{"focal_changed", "chora.consumption.kg_exploration.focal_changed.v1", []errorSlot{
			{"exploration_id", "e", strBad}, {"cluster_id", "c", strBad}, {"user_gcid", "u", strBad},
			{"from_focal_atom_id", "f", strBad}, {"to_focal_atom_id", "t", strBad},
			{"relation", "extends", strBad}, {"step_index", 1, s}, {"traversed_at", now, s},
		}},
		{"concept_atoms_bound", "chora.consumption.concept.atoms_bound.v1", []errorSlot{
			{"concept_id", "c", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"attached_atom_ids", []string{"a"}, 5}, {"detached_atom_ids", []string{"d"}, 5},
			{"resulting_atom_refs", []string{"r"}, 5}, {"change_source", "manual", strBad},
			{"provenance", "p", strBad}, {"occurred_at", now, s},
		}},
		{"campaign_focus_assigned", "chora.consumption.campaign.focus_assigned.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"concept_id", "c", strBad}, {"concept_key", "k", strBad},
			{"previous_concept_id", "p", strBad}, {"companion_id", "f", strBad},
			{"assigned_at", now, s},
		}},
		{"campaign_rung_cleared", "chora.consumption.campaign.rung_cleared.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"concept_id", "c", strBad}, {"concept_key", "k", strBad}, {"rung", 5, s},
			{"is_refresher", true, s}, {"correct_answers", 1, s}, {"cleared_at", now, s},
		}},
		{"campaign_node_won", "chora.consumption.campaign.node_won.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"concept_id", "c", strBad}, {"concept_key", "k", strBad}, {"won_at", now, s},
		}},
		{"campaign_node_revealed", "chora.consumption.campaign.node_revealed.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"focal_concept_id", "c", strBad}, {"focal_concept_key", "k", strBad},
			{"suggestion_count", 1, s}, {"won_event_id", "w", strBad}, {"revealed_at", now, s},
		}},
		{"campaign_goal_sealed", "chora.consumption.campaign.goal_sealed.v1", []errorSlot{
			{"goal_id", "g", strBad}, {"tenant_id", "t", strBad}, {"learner_gcid", "g", strBad},
			{"root_concept_id", "c", strBad}, {"root_concept_key", "k", strBad},
			{"nodes_won", 1, s}, {"is_reseal", true, s}, {"sealed_at", now, s},
		}},
		{"ai_assist_started_v2", protomarshal.TopicAiAssistStartedV2, []errorSlot{
			{"assist_id", "a", strBad}, {"tenant_id", "t", strBad}, {"author_gcid", "g", strBad},
			{"atom_id", "a", strBad}, {"content_type", "mixed", strBad}, {"prompt", "p", strBad},
			{"requested_count", 1, s}, {"difficulty", 1, s}, {"started_at", now, s},
			{"max_retries", 1, s}, {"metadata", map[string]string{}, 123},
			{"image_for_stem", true, s}, {"image_for_answer", true, s},
			{"grounding_mode", "prompt", strBad}, {"source_blob_uri", "b", strBad},
			{"source_mime_type", "text", strBad}, {"target_growth_edges", []string{"x"}, 5},
			{"type_plan", []map[string]any{}, "x"}, {"operation", "compose", strBad},
			{"intent", "new_question", strBad}, {"input_kind", "prompt", strBad},
		}},
	}

	for _, enc := range specs {
		t.Run(enc.name, func(t *testing.T) {
			for i, fs := range enc.fields {
				t.Run(fs.key, func(t *testing.T) {
					payload := make(map[string]any, i+1)
					for j := 0; j < i; j++ {
						payload[enc.fields[j].key] = enc.fields[j].good
					}
					payload[fs.key] = fs.bad
					_, err := protomarshal.MarshalPayload(enc.topic, env, payload)
					require.Error(t, err, "encoder %s field %s must fail loud", enc.name, fs.key)
					assert.Contains(t, err.Error(), fs.key)
				})
			}
		})
	}
}

// -------------------------------------------------------------------------
// ConceptDeleted (0% before this file) — full round-trip + error arms.
// -------------------------------------------------------------------------

func TestConceptDeleted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	occurredAt := env.OccurredAt.Add(3 * time.Second)
	payload := map[string]any{
		"concept_id":   "01971b00-1111-7000-8000-0000000000c1",
		"tenant_id":    env.TenantID,
		"learner_gcid": env.GCID,
		"occurred_at":  occurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.concept.deleted.v1", env, payload)
	require.NoError(t, err)

	var got consumptionv1.ConceptDeleted
	require.NoError(t, proto.Unmarshal(bz, &got))
	assert.Equal(t, "01971b00-1111-7000-8000-0000000000c1", got.GetConceptId())
	assert.Equal(t, env.TenantID, got.GetTenantId())
	assert.Equal(t, env.GCID, got.GetLearnerGcid())
	require.NotNil(t, got.GetOccurredAt())
	assert.True(t, got.GetOccurredAt().AsTime().Equal(occurredAt))
	require.NotNil(t, got.GetEnvelope())
	assert.Equal(t, env.EventID, got.GetEnvelope().GetEventId())
}

func TestConceptDeleted_EveryFieldErrorArm(t *testing.T) {
	env := fixedEnvelope()
	now := env.OccurredAt
	spec := []errorSlot{
		{"concept_id", "c", 999},
		{"tenant_id", "t", 999},
		{"learner_gcid", "g", 999},
		{"occurred_at", now, "x"},
	}
	for i, fs := range spec {
		t.Run(fs.key, func(t *testing.T) {
			payload := make(map[string]any, i+1)
			for j := 0; j < i; j++ {
				payload[spec[j].key] = spec[j].good
			}
			payload[fs.key] = fs.bad
			_, err := protomarshal.MarshalPayload("chora.consumption.concept.deleted.v1", env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fs.key)
		})
	}
}

// -------------------------------------------------------------------------
// companion_display_name (CHO-2266) is the LAST field of stage_up (13) and
// source_revelation (9). Its error arm is not exercised by the generic runner
// because those encoders were otherwise fully covered; pin it explicitly.
// -------------------------------------------------------------------------

func TestDisplayName_ErrorArm_StageUpAndSourceRevelation(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
	}{
		{"chora.consumption.companion.stage_up.v1"},
		{"chora.consumption.companion.source_revelation.v1"},
	}
	for _, tc := range cases {
		t.Run(tc.topic, func(t *testing.T) {
			payload := map[string]any{"companion_display_name": 999}
			_, err := protomarshal.MarshalPayload(tc.topic, env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "companion_display_name")
		})
	}
}

// -------------------------------------------------------------------------
// encodeDailyDoseServed — the ONLY reachable error arms are the typed `entries`
// path. `encodeDailyDoseEntry` never returns an error, so the `entries: %w`
// wraps are unreachable; here we cover the entry rejection of a raw (non-slice)
// `entries` value and the atom_ids legacy fallback with mismatched entries.
// -------------------------------------------------------------------------

func TestDailyDose_EntriesShapeRejection(t *testing.T) {
	env := fixedEnvelope()
	topic := "chora.consumption.daily_dose.served.v1"

	t.Run("entries wrong shape falls back to atom_ids", func(t *testing.T) {
		// `entries` present but not []map[string]any → the encoder silently
		// falls through to the atom_ids path (both coercion points are
		// loose-typed; no error), exercising that branch when atom_ids is valid.
		payload := map[string]any{
			"entries":  "junk",
			"atom_ids": []string{"a1", "a2"},
			"size":     2,
		}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.DailyDoseServed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Len(t, msg.GetEntries(), 2)
	})

	t.Run("atom_ids non-string slice emits nothing", func(t *testing.T) {
		payload := map[string]any{"atom_ids": []any{1, 2}}
		bz, err := protomarshal.MarshalPayload(topic, env, payload)
		require.NoError(t, err)
		var msg consumptionv1.DailyDoseServed
		require.NoError(t, proto.Unmarshal(bz, &msg))
		assert.Empty(t, msg.GetEntries())
	})
}

// -------------------------------------------------------------------------
// Nested sub-encoder error arms — reachable only through their parent encoder.
// Each forces a type error at every nested field position so the sub-encoder's
// own `return nil, err` statements are exercised and the parent's
// `field X[i]: %w` wrap is reached.
// -------------------------------------------------------------------------

func TestExtractedGrowthEdge_EveryNestedFieldError(t *testing.T) {
	env := fixedEnvelope()
	spec := []errorSlot{
		{"concept_label", "l", 999},
		{"concept_key", "k", 999},
		{"category", "c", 999},
		{"tags", []string{"a"}, 5},
		{"confidence", 0.5, "x"},
		{"strength", 0.5, "x"},
		{"descriptor_json", `{}`, 999},
	}
	for i, fs := range spec {
		t.Run(fs.key, func(t *testing.T) {
			edge := make(map[string]any, i+1)
			for j := 0; j < i; j++ {
				edge[spec[j].key] = spec[j].good
			}
			edge[fs.key] = fs.bad
			payload := map[string]any{"edges": []map[string]any{edge}}
			_, err := protomarshal.MarshalPayload("chora.consumption.weakness.analyzed.v1", env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fs.key)
		})
	}
}

func TestOutputSelection_EveryNestedFieldError(t *testing.T) {
	env := fixedEnvelope()
	for _, fs := range []errorSlot{
		{"focused_dose", true, "x"},
		{"familiar_coaching", true, "x"},
		{"practice_test", true, "x"},
		{"study_aids", true, "x"},
	} {
		t.Run(fs.key, func(t *testing.T) {
			payload := map[string]any{"output_selection": map[string]any{fs.key: fs.bad}}
			_, err := protomarshal.MarshalPayload("chora.consumption.weakness.analyzed.v1", env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fs.key)
		})
	}
}

func TestRitualStepStamp_EveryNestedFieldError(t *testing.T) {
	env := fixedEnvelope()
	spec := []errorSlot{
		{"step_index", 1, "x"},
		{"skill_key", "k", 999},
		{"prompt_version", "v", 999},
		{"prompt_hash", "h", 999},
		{"tools_invoked", []string{"t"}, 5},
		{"citations", []string{"c"}, 5},
	}
	for i, fs := range spec {
		t.Run(fs.key, func(t *testing.T) {
			stamp := make(map[string]any, i+1)
			for j := 0; j < i; j++ {
				stamp[spec[j].key] = spec[j].good
			}
			stamp[fs.key] = fs.bad
			payload := map[string]any{"stamps": []map[string]any{stamp}}
			_, err := protomarshal.MarshalPayload("chora.consumption.companion.ritual_run_completed.v1", env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fs.key)
		})
	}
}

func TestHexagonNeighbor_EveryNestedFieldError(t *testing.T) {
	env := fixedEnvelope()
	spec := []errorSlot{
		{"atom_id", "a", 999},
		{"relation", "extends", 999},
		{"confidence", 0.5, "x"},
		{"fog_label", "f", 999},
		{"is_junction", true, "x"},
	}
	for i, fs := range spec {
		t.Run(fs.key, func(t *testing.T) {
			n := make(map[string]any, i+1)
			for j := 0; j < i; j++ {
				n[spec[j].key] = spec[j].good
			}
			n[fs.key] = fs.bad
			payload := map[string]any{"neighbors": []map[string]any{n}}
			_, err := protomarshal.MarshalPayload("chora.consumption.kg_hexagon_fog.generated.v1", env, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), fs.key)
		})
	}
}
