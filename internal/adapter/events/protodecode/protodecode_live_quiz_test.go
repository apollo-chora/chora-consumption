// protodecode_live_quiz_test.go — W3-derived: binary decode of
// chora.delivery.live_quiz_session.score_awarded.v1 (the CR2-C3 classroom →
// mastery seam). The producer (chora-delivery protomarshal) emits the flat
// schema wire-compatible with the generated deliveryv1 type.
package protodecode

import (
	"testing"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
)

func TestDecodeLiveQuizScoreAwarded_Binary(t *testing.T) {
	msg := &deliveryv1.LiveQuizSessionScoreAwarded{
		Envelope: &commonv1.EventEnvelope{
			EventId:     "ev-1",
			TenantId:    "11111111-1111-7111-8111-111111111111",
			Gcid:        "00000000-0000-7000-8000-000000001999",
			Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		},
		SessionId:       "lqs-1",
		LiveQuizId:      "lq-1",
		QuestionId:      "q-1",
		AwardedPoints:   800,
		CumulativeScore: 1800,
		Correct:         true,
		AnswerMillis:    1234,
		AtomId:          "atom-1",
		TopicTags:       []string{"fractions", "arithmetic"},
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := DecodePayloadMap("chora.delivery.live_quiz_session.score_awarded.v1", payload)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}

	for k, want := range map[string]any{
		"tenant_id":    "11111111-1111-7111-8111-111111111111",
		"gcid":         "00000000-0000-7000-8000-000000001999",
		"learner_gcid": "00000000-0000-7000-8000-000000001999",
		"session_id":   "lqs-1",
		"live_quiz_id": "lq-1",
		"question_id":  "q-1",
		"atom_id":      "atom-1",
		"correct":      true,
	} {
		if got := out[k]; got != want {
			t.Errorf("out[%q] = %v, want %v", k, got, want)
		}
	}
	tags, ok := out["topic_tags"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "fractions" {
		t.Fatalf("topic_tags = %v", out["topic_tags"])
	}
}

func TestDecodeLiveQuizScoreAwarded_FalseCorrectOmitted(t *testing.T) {
	// proto3 zero-value: a wrong answer encodes no `correct` field at all —
	// the projector must still emit correct=false so boolField reads it.
	msg := &deliveryv1.LiveQuizSessionScoreAwarded{
		Envelope:  &commonv1.EventEnvelope{EventId: "ev-2", TenantId: "t-1", Gcid: "g-1"},
		SessionId: "lqs-1",
		AtomId:    "atom-1",
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := DecodePayloadMap("chora.delivery.live_quiz_session.score_awarded.v1", payload)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got, ok := out["correct"].(bool); !ok || got {
		t.Fatalf("correct = %v (present=%v), want explicit false", out["correct"], ok)
	}
}
