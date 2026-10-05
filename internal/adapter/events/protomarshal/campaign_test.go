// campaign_test.go — WS-C1 (CHO-2080, ADR-227) round-trip gates for the 5
// campaign.* v1 encoders. Same discipline as the goal/fabric canonical tests:
// MarshalPayload's wire bytes MUST decode into the generated consumptionv1
// schema with every field intact — the strongest guarantee the Pub/Sub Schema
// Registry (BINARY encoding) accepts the publish. RED before the encoders
// land (ErrUnsupportedTopic → JSON fallback → INVALID_BINARY_PROTO_MESSAGE,
// the exact Flag-1 failure class).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

const (
	campaignTestGoalID    = "01971b00-aaaa-7000-8000-000000000001"
	campaignTestConceptID = "01971b00-bbbb-7000-8000-000000000002"
	campaignTestKey       = "photosynthesis"
)

func TestMarshalCampaignFocusAssigned_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	assignedAt := env.OccurredAt.Add(2 * time.Second)
	payload := map[string]any{
		"goal_id":             campaignTestGoalID,
		"tenant_id":           env.TenantID,
		"learner_gcid":        env.GCID,
		"concept_id":          campaignTestConceptID,
		"concept_key":         campaignTestKey,
		"previous_concept_id": "01971b00-cccc-7000-8000-000000000003",
		"companion_id":        "01971b00-dddd-7000-8000-000000000004",
		"assigned_at":         assignedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.campaign.focus_assigned.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CampaignFocusAssigned
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CampaignFocusAssigned: %v", err)
	}
	if got.GetGoalId() != campaignTestGoalID {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetTenantId() != env.TenantID {
		t.Errorf("tenant_id = %q", got.GetTenantId())
	}
	if got.GetLearnerGcid() != env.GCID {
		t.Errorf("learner_gcid = %q", got.GetLearnerGcid())
	}
	if got.GetConceptId() != campaignTestConceptID {
		t.Errorf("concept_id = %q", got.GetConceptId())
	}
	if got.GetConceptKey() != campaignTestKey {
		t.Errorf("concept_key = %q", got.GetConceptKey())
	}
	if got.GetPreviousConceptId() != "01971b00-cccc-7000-8000-000000000003" {
		t.Errorf("previous_concept_id = %q", got.GetPreviousConceptId())
	}
	if got.GetFamiliarId() != "01971b00-dddd-7000-8000-000000000004" {
		t.Errorf("familiar_id (wire name at tag 8) = %q", got.GetFamiliarId())
	}
	if !got.GetAssignedAt().AsTime().Equal(assignedAt) {
		t.Errorf("assigned_at = %v, want %v", got.GetAssignedAt().AsTime(), assignedAt)
	}
	if gotEnv := got.GetEnvelope(); gotEnv == nil || gotEnv.GetEventId() != env.EventID {
		t.Fatalf("envelope missing or event_id mismatch: %+v", got.GetEnvelope())
	}
}

func TestMarshalCampaignRungCleared_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	clearedAt := env.OccurredAt.Add(5 * time.Second)
	payload := map[string]any{
		"goal_id":         campaignTestGoalID,
		"tenant_id":       env.TenantID,
		"learner_gcid":    env.GCID,
		"concept_id":      campaignTestConceptID,
		"concept_key":     campaignTestKey,
		"rung":            5, // ladder position 5 = evaluation (ADR-227 D6 order)
		"is_refresher":    true,
		"correct_answers": 2,
		"cleared_at":      clearedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.campaign.rung_cleared.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CampaignRungCleared
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CampaignRungCleared: %v", err)
	}
	if got.GetGoalId() != campaignTestGoalID {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetConceptId() != campaignTestConceptID {
		t.Errorf("concept_id = %q", got.GetConceptId())
	}
	if got.GetConceptKey() != campaignTestKey {
		t.Errorf("concept_key = %q", got.GetConceptKey())
	}
	if got.GetRung() != consumptionv1.CampaignRung_CAMPAIGN_RUNG_EVALUATION {
		t.Errorf("rung = %v, want CAMPAIGN_RUNG_EVALUATION(5)", got.GetRung())
	}
	if !got.GetIsRefresher() {
		t.Error("is_refresher = false, want true")
	}
	if got.GetCorrectAnswers() != 2 {
		t.Errorf("correct_answers = %d", got.GetCorrectAnswers())
	}
	if !got.GetClearedAt().AsTime().Equal(clearedAt) {
		t.Errorf("cleared_at = %v", got.GetClearedAt().AsTime())
	}
}

func TestMarshalCampaignNodeWon_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	wonAt := env.OccurredAt.Add(9 * time.Second)
	payload := map[string]any{
		"goal_id":      campaignTestGoalID,
		"tenant_id":    env.TenantID,
		"learner_gcid": env.GCID,
		"concept_id":   campaignTestConceptID,
		"concept_key":  campaignTestKey,
		"won_at":       wonAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.campaign.node_won.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CampaignNodeWon
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CampaignNodeWon: %v", err)
	}
	if got.GetGoalId() != campaignTestGoalID {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetTenantId() != env.TenantID {
		t.Errorf("tenant_id = %q", got.GetTenantId())
	}
	if got.GetLearnerGcid() != env.GCID {
		t.Errorf("learner_gcid = %q", got.GetLearnerGcid())
	}
	if got.GetConceptId() != campaignTestConceptID {
		t.Errorf("concept_id = %q", got.GetConceptId())
	}
	if got.GetConceptKey() != campaignTestKey {
		t.Errorf("concept_key = %q", got.GetConceptKey())
	}
	if !got.GetWonAt().AsTime().Equal(wonAt) {
		t.Errorf("won_at = %v", got.GetWonAt().AsTime())
	}
}

func TestMarshalCampaignNodeRevealed_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	revealedAt := env.OccurredAt.Add(11 * time.Second)
	payload := map[string]any{
		"goal_id":           campaignTestGoalID,
		"tenant_id":         env.TenantID,
		"learner_gcid":      env.GCID,
		"focal_concept_id":  campaignTestConceptID,
		"focal_concept_key": campaignTestKey,
		"suggestion_count":  6,
		"won_event_id":      env.EventID,
		"revealed_at":       revealedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.campaign.node_revealed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CampaignNodeRevealed
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CampaignNodeRevealed: %v", err)
	}
	if got.GetGoalId() != campaignTestGoalID {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetFocalConceptId() != campaignTestConceptID {
		t.Errorf("focal_concept_id = %q", got.GetFocalConceptId())
	}
	if got.GetFocalConceptKey() != campaignTestKey {
		t.Errorf("focal_concept_key = %q", got.GetFocalConceptKey())
	}
	if got.GetSuggestionCount() != 6 {
		t.Errorf("suggestion_count = %d", got.GetSuggestionCount())
	}
	if got.GetWonEventId() != env.EventID {
		t.Errorf("won_event_id = %q", got.GetWonEventId())
	}
	if !got.GetRevealedAt().AsTime().Equal(revealedAt) {
		t.Errorf("revealed_at = %v", got.GetRevealedAt().AsTime())
	}
}

func TestMarshalCampaignGoalSealed_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	sealedAt := env.OccurredAt.Add(13 * time.Second)
	payload := map[string]any{
		"goal_id":          campaignTestGoalID,
		"tenant_id":        env.TenantID,
		"learner_gcid":     env.GCID,
		"root_concept_id":  campaignTestConceptID,
		"root_concept_key": campaignTestKey,
		"nodes_won":        7,
		"is_reseal":        false, // proto3 zero — omitted from the wire, decodes false
		"sealed_at":        sealedAt,
	}

	bz, err := protomarshal.MarshalPayload("chora.consumption.campaign.goal_sealed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var got consumptionv1.CampaignGoalSealed
	if err := proto.Unmarshal(bz, &got); err != nil {
		t.Fatalf("proto.Unmarshal into CampaignGoalSealed: %v", err)
	}
	if got.GetGoalId() != campaignTestGoalID {
		t.Errorf("goal_id = %q", got.GetGoalId())
	}
	if got.GetRootConceptId() != campaignTestConceptID {
		t.Errorf("root_concept_id = %q", got.GetRootConceptId())
	}
	if got.GetRootConceptKey() != campaignTestKey {
		t.Errorf("root_concept_key = %q", got.GetRootConceptKey())
	}
	if got.GetNodesWon() != 7 {
		t.Errorf("nodes_won = %d", got.GetNodesWon())
	}
	if got.GetIsReseal() {
		t.Error("is_reseal = true, want false")
	}
	if !got.GetSealedAt().AsTime().Equal(sealedAt) {
		t.Errorf("sealed_at = %v", got.GetSealedAt().AsTime())
	}
	if gotEnv := got.GetEnvelope(); gotEnv == nil || gotEnv.GetIdempotencyKey() != env.IdempotencyKey {
		t.Fatalf("envelope missing or idempotency mismatch: %+v", got.GetEnvelope())
	}
}
