// fabric_weakness_analyzed_test.go — CHO-2040 (owner ruling R8-5): canonical
// round-trip for the NEW chora.consumption.weakness.analyzed.v1 encoder. The
// ceremony confirm hook publishes remediate ticks on this BINARY
// Schema-Registry topic (until now consumption only CONSUMED it — the crew
// published it), so the topic needs a producer-side encoder or every confirm
// JSON-falls-back and deadletters with INVALID_BINARY_PROTO_MESSAGE.
//
// Same guardrail shape as fabric_canonical_test.go: representative payload
// with every encoder-read field non-zero → MarshalPayload → decode into the
// generated canonical struct (chora-contracts/gen/go — the registered schema
// shape) → per-field assertions weighted to the nested messages (edges +
// output_selection, where drift hides). output_selection.familiar_coaching is
// THE load-bearing bit: the live analyzed subscriber gates the per-Companion
// RAG write on it (weakness_companion_rag.go) — the whole point of the hook.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestFabric_WeaknessAnalyzed_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	analyzedAt := time.Date(2026, 7, 4, 21, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"upload_id":    "01970000-dddd-7000-8000-000000000001",
		"tenant_id":    "01970000-0000-7000-8000-0000000000c1",
		"learner_gcid": "01970000-0000-7000-8000-0000000000u1",
		"edges": []map[string]any{
			{
				"concept_label":   "Recursion base cases",
				"concept_key":     "recursion-base-cases",
				"category":        "computer-science",
				"tags":            []string{"recursion", "ceremony"},
				"confidence":      1.0,
				"strength":        0.6,
				"descriptor_json": `{"summary":"flagged for remediation at the binding ceremony"}`,
			},
			{
				"concept_label":   "Unit conversions",
				"concept_key":     "unit-conversions",
				"confidence":      1.0,
				"strength":        0.6,
				"descriptor_json": `{"summary":"flagged for remediation at the binding ceremony"}`,
			},
		},
		"model_used":         "learner_selection",
		"input_token_count":  7,
		"output_token_count": 11,
		"analyzed_at":        analyzedAt,
		"output_selection": map[string]any{
			"focused_dose":      false,
			"familiar_coaching": true,
			"practice_test":     false,
			"study_aids":        false,
		},
	}
	bz, err := protomarshal.MarshalPayload("chora.consumption.weakness.analyzed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m consumptionv1.WeaknessAnalyzed
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen WeaknessAnalyzed: %v", err)
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
	edges := m.GetEdges()
	if len(edges) != 2 {
		t.Fatalf("edges (5) len = %d, want 2", len(edges))
	}
	e0 := edges[0]
	if e0.GetConceptLabel() != "Recursion base cases" {
		t.Errorf("edges[0].concept_label (1) = %q", e0.GetConceptLabel())
	}
	if e0.GetConceptKey() != "recursion-base-cases" {
		t.Errorf("edges[0].concept_key (2) = %q", e0.GetConceptKey())
	}
	if e0.GetCategory() != "computer-science" {
		t.Errorf("edges[0].category (3) = %q", e0.GetCategory())
	}
	if tags := e0.GetTags(); len(tags) != 2 || tags[0] != "recursion" || tags[1] != "ceremony" {
		t.Errorf("edges[0].tags (4) = %v", tags)
	}
	if e0.GetConfidence() != 1.0 {
		t.Errorf("edges[0].confidence (5) = %v", e0.GetConfidence())
	}
	if e0.GetStrength() != 0.6 {
		t.Errorf("edges[0].strength (6) = %v", e0.GetStrength())
	}
	if e0.GetDescriptorJson() == "" {
		t.Errorf("edges[0].descriptor_json (7) empty")
	}
	if edges[1].GetConceptLabel() != "Unit conversions" {
		t.Errorf("edges[1].concept_label = %q", edges[1].GetConceptLabel())
	}
	if m.GetModelUsed() != "learner_selection" {
		t.Errorf("model_used (6) = %q", m.GetModelUsed())
	}
	if m.GetInputTokenCount() != 7 {
		t.Errorf("input_token_count (7) = %d", m.GetInputTokenCount())
	}
	if m.GetOutputTokenCount() != 11 {
		t.Errorf("output_token_count (8) = %d", m.GetOutputTokenCount())
	}
	if m.GetAnalyzedAt() == nil || !m.GetAnalyzedAt().AsTime().Equal(analyzedAt) {
		t.Errorf("analyzed_at (9) = %v want %v", m.GetAnalyzedAt(), analyzedAt)
	}
	sel := m.GetOutputSelection()
	if sel == nil {
		t.Fatalf("output_selection (10) missing")
	}
	if !sel.GetFamiliarCoaching() {
		t.Errorf("output_selection.familiar_coaching (2) = false — the RAG hook would never fire")
	}
	if sel.GetFocusedDose() || sel.GetPracticeTest() || sel.GetStudyAids() {
		t.Errorf("output_selection other flags = %v, want all false", sel)
	}
}
