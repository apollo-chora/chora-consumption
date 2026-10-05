// fabric_weakness_review_pending_test.go — canonical round-trip for the
// chora.consumption.weakness.review_pending.v1 encoder (the bounded HITL
// review panel; ADR-205 D4 / CHO-1973). The consumer unmarshals the body into
// the generated canonical struct (chora-contracts/gen/go — the registered
// schema shape), so the encoder must produce exactly that wire shape.
//
// Same guardrail shape as fabric_weakness_analyzed_test.go: representative
// payload with every encoder-read field non-zero → MarshalPayload → decode
// into the generated struct → per-field assertions weighted to the nested
// messages (familiar + proposed_edges + candidate_struggles +
// available_outputs, where drift hides).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestFabric_WeaknessReviewPending_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	pendingAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"upload_id":    "01970000-dddd-7000-8000-000000000001",
		"tenant_id":    "01970000-0000-7000-8000-0000000000c1",
		"learner_gcid": "01970000-0000-7000-8000-0000000000u1",
		"familiar": map[string]any{
			"familiar_id": "01970000-0000-7000-8000-0000000000f1",
			"name":        "Ember",
			"species":     "dragon",
		},
		"proposed_edges": []map[string]any{
			{
				"proposed_edge_id":     "01970000-0000-7000-8000-0000000000e1",
				"concept_label":        "Causes of riverine flooding",
				"summary":              "One-line description of the gap",
				"suggested_angles":     []string{"map the water cycle", "trace a flood event"},
				"strength":             0.4,
				"suggested_difficulty": "standard",
			},
		},
		"candidate_struggles": []map[string]any{
			{
				"concept_key":  "riverine-flood-causes",
				"concept_label": "Causes of riverine flooding",
			},
		},
		"available_outputs": []map[string]any{
			{
				"kind":              "focused_dose",
				"mana_price":        int64(0),
				"default_selected":  true,
			},
			{
				"kind":              "companion_coaching",
				"mana_price":        int64(5),
				"default_selected":  false,
			},
		},
		"pending_at": pendingAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.review_pending.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.WeaknessReviewPending
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen WeaknessReviewPending: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
	if m.GetUploadId() != "01970000-dddd-7000-8000-000000000001" {
		t.Errorf("upload_id (2) = %q", m.GetUploadId())
	}
	if m.GetTenantId() != "01970000-0000-7000-8000-0000000000c1" {
		t.Errorf("tenant_id (3) = %q", m.GetTenantId())
	}
	if m.GetLearnerGcid() != "01970000-0000-7000-8000-0000000000u1" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	f := m.GetFamiliar()
	if f == nil {
		t.Fatal("familiar (5) missing")
	}
	if f.GetFamiliarId() != "01970000-0000-7000-8000-0000000000f1" || f.GetName() != "Ember" || f.GetSpecies() != "dragon" {
		t.Errorf("familiar = %+v", f)
	}
	if len(m.GetProposedEdges()) != 1 {
		t.Fatalf("proposed_edges (6) len = %d; want 1", len(m.GetProposedEdges()))
	}
	edge := m.GetProposedEdges()[0]
	if edge.GetProposedEdgeId() != "01970000-0000-7000-8000-0000000000e1" ||
		edge.GetConceptLabel() != "Causes of riverine flooding" ||
		edge.GetSummary() != "One-line description of the gap" ||
		edge.GetSuggestedDifficulty() != "standard" {
		t.Errorf("proposed_edge = %+v", edge)
	}
	if len(edge.GetSuggestedAngles()) != 2 || edge.GetSuggestedAngles()[0] != "map the water cycle" {
		t.Errorf("suggested_angles = %v", edge.GetSuggestedAngles())
	}
	if edge.GetStrength() < 0.39 || edge.GetStrength() > 0.41 {
		t.Errorf("strength (fixed32) = %v; want ~0.4", edge.GetStrength())
	}
	if len(m.GetCandidateStruggles()) != 1 {
		t.Fatalf("candidate_struggles (7) len = %d; want 1", len(m.GetCandidateStruggles()))
	}
	struggle := m.GetCandidateStruggles()[0]
	if struggle.GetConceptKey() != "riverine-flood-causes" || struggle.GetConceptLabel() != "Causes of riverine flooding" {
		t.Errorf("candidate_struggle = %+v", struggle)
	}
	if len(m.GetAvailableOutputs()) != 2 {
		t.Fatalf("available_outputs (8) len = %d; want 2", len(m.GetAvailableOutputs()))
	}
	if m.GetAvailableOutputs()[0].GetKind() != "focused_dose" || !m.GetAvailableOutputs()[0].GetDefaultSelected() {
		t.Errorf("available_output[0] = %+v", m.GetAvailableOutputs()[0])
	}
	if m.GetAvailableOutputs()[1].GetKind() != "companion_coaching" || m.GetAvailableOutputs()[1].GetManaPrice() != 5 {
		t.Errorf("available_output[1] = %+v", m.GetAvailableOutputs()[1])
	}
	if m.GetPendingAt() == nil || !m.GetPendingAt().AsTime().Equal(pendingAt) {
		t.Errorf("pending_at (9) = %v; want %v", m.GetPendingAt(), pendingAt)
	}
}
