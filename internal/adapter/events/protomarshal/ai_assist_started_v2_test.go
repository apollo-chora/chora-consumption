// ai_assist_started_v2_test.go — CHO-2040: the consumption-side BINARY encoder
// for the CROSS-SERVICE qgen request topic chora.creation.ai_assist.started.v2.
//
// The topic is Schema-Registry BINARY-bound (terraform m10-data-plane
// compose_v2_topics) — a JSON publish is rejected with
// INVALID_BINARY_PROTO_MESSAGE and deadletters forever. Round-trip pin: encode
// with the hand-rolled protowire encoder, decode with the generated
// creationv1.AiAssistStartedV2 bindings (same tags as the flat schema).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

func TestMarshalAiAssistStartedV2_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	started := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"assist_id":       "0197a001-aaaa-7bbb-8ccc-000000000001",
		"tenant_id":       "0197a001-aaaa-7bbb-8ccc-000000000002",
		"author_gcid":     "0197a001-aaaa-7bbb-8ccc-000000000003",
		"content_type":    "mixed",
		"question_type":   "mcq", // payload-only parity key; NOT on the v2 wire
		"prompt":          "Compose a proofing test targeting the listed growth edges.",
		"requested_count": 6,
		"max_retries":     0,
		"started_at":      started,
		"metadata":        map[string]string{"category": "proofing_test", "subject": "Quadratics"},
		"target_growth_edges": []string{
			"algebra.quadratic_roots",
			"algebra.completing_square",
		},
		"type_plan": []map[string]any{
			{"question_type": "mcq", "count": 4},
			{"question_type": "oe", "count": 2},
		},
		"operation":  "compose",
		"intent":     "new_question",
		"input_kind": "prompt",
	}

	bz, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m creationv1.AiAssistStartedV2
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("gen-bindings Unmarshal: %v", err)
	}
	if m.GetAssistId() != "0197a001-aaaa-7bbb-8ccc-000000000001" {
		t.Fatalf("assist_id = %q", m.GetAssistId())
	}
	if m.GetTenantId() != "0197a001-aaaa-7bbb-8ccc-000000000002" {
		t.Fatalf("tenant_id = %q", m.GetTenantId())
	}
	if m.GetAuthorGcid() != "0197a001-aaaa-7bbb-8ccc-000000000003" {
		t.Fatalf("author_gcid = %q", m.GetAuthorGcid())
	}
	if m.GetContentType() != "mixed" {
		t.Fatalf("content_type = %q, want mixed", m.GetContentType())
	}
	if m.GetPrompt() == "" {
		t.Fatalf("prompt must ride the wire (orchestrator validate rejects empty)")
	}
	if m.GetRequestedCount() != 6 {
		t.Fatalf("requested_count = %d, want 6", m.GetRequestedCount())
	}
	// Field 19 — the R8-7 payload: concept KEYS, never titles.
	edges := m.GetTargetGrowthEdges()
	if len(edges) != 2 || edges[0] != "algebra.quadratic_roots" || edges[1] != "algebra.completing_square" {
		t.Fatalf("target_growth_edges (f19) = %v", edges)
	}
	// Field 21 — mixed MCQ+OE plan drives the set-native batch lane.
	plan := m.GetTypePlan()
	if len(plan) != 2 {
		t.Fatalf("type_plan = %d entries, want 2", len(plan))
	}
	if plan[0].GetQuestionType() != "mcq" || plan[0].GetCount() != 4 {
		t.Fatalf("plan[0] = %v", plan[0])
	}
	if plan[1].GetQuestionType() != "oe" || plan[1].GetCount() != 2 {
		t.Fatalf("plan[1] = %v", plan[1])
	}
	// Fields 23-25 — the ADR-195 compose model (v2 routes on these).
	if m.GetOperation() != "compose" || m.GetIntent() != "new_question" || m.GetInputKind() != "prompt" {
		t.Fatalf("compose trio = %q/%q/%q", m.GetOperation(), m.GetIntent(), m.GetInputKind())
	}
	if got := m.GetStartedAt().AsTime(); !got.Equal(started) {
		t.Fatalf("started_at = %v, want %v", got, started)
	}
	if md := m.GetMetadata(); md["category"] != "proofing_test" || md["subject"] != "Quadratics" {
		t.Fatalf("metadata = %v", md)
	}
	// Envelope f1 rides too (schema field 1).
	if m.GetEnvelope().GetEventId() != env.EventID {
		t.Fatalf("envelope event_id = %q, want %q", m.GetEnvelope().GetEventId(), env.EventID)
	}
}

func TestMarshalAiAssistStartedV2_TypePlanShapeFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"assist_id": "a", "tenant_id": "t", "author_gcid": "g",
		"content_type": "mixed", "prompt": "p",
		"type_plan": "not-a-slice",
	}
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err == nil {
		t.Fatalf("malformed type_plan must fail loud — a silently mis-encoded quota mis-routes the batch")
	}
}

func TestMarshalAiAssistStartedV2_GrowthEdgesTypeMismatchFailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"assist_id": "a", "tenant_id": "t", "author_gcid": "g",
		"content_type": "mixed", "prompt": "p",
		"target_growth_edges": 42,
	}
	if _, err := protomarshal.MarshalPayload("chora.creation.ai_assist.started.v2", env, payload); err == nil {
		t.Fatalf("non-slice target_growth_edges must fail loud")
	}
}
