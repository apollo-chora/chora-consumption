// companion_chat_growth_edges_test.go — W7 (Epic-1b): the chat handler fetches
// the learner's top Growth Edges and injects them into the engine request
// (mirrors the F4 memory recall). Soft-fail throughout — the edges are
// supplementary context, never load-bearing for the turn.
//
// Reuses fakeChatEngine / seedChatEngine / authedChatReq / fakeManaQuoter /
// companionTestID from companion_chat_handler_test.go, chatFramesWithReply from
// companion_chat_memory_test.go, and doseLWStub/doseEdge from
// dose_growth_edges_handler_test.go.
package http

import (
	"errors"
	"strings"
	"testing"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

func TestCompanionChat_GrowthEdges_InjectedIntoEngineRequest(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("Let's drill fractions!")}
	srv := newMemChatServer(t, engine)
	edge := doseEdge(t, "Fractions", 0.85)
	edge.Descriptor = lw.Descriptor{
		Summary:         "Confuses numerator and denominator roles.",
		SuggestedAngles: []string{"visual pie models"},
	}
	srv.LearnerWeakness = &doseLWStub{items: []lw.LearnerWeakness{edge}}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "what should I practice?",
	})
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got := engine.gotReq.LearnerWeaknessJSON
	if !strings.Contains(got, `"growth_edges"`) || !strings.Contains(got, "Fractions") {
		t.Fatalf("LearnerWeaknessJSON = %q; want growth_edges with the Fractions edge", got)
	}
	if !strings.Contains(got, "0.85") || !strings.Contains(got, "numerator") {
		t.Errorf("LearnerWeaknessJSON = %q; want strength + summary woven in", got)
	}
}

func TestCompanionChat_GrowthEdges_AbsentWhenRepoNil(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("hi")}
	srv := newMemChatServer(t, engine) // LearnerWeakness nil

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hello",
	})
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if engine.gotReq.LearnerWeaknessJSON != "" {
		t.Errorf("LearnerWeaknessJSON = %q; want empty when repo unwired", engine.gotReq.LearnerWeaknessJSON)
	}
}

func TestCompanionChat_GrowthEdges_ReadErrorSoftFails(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("hi")}
	srv := newMemChatServer(t, engine)
	srv.LearnerWeakness = &doseLWStub{listErr: errors.New("boom")}

	w := authedChatReq(t, srv, "/v1/me/companions/"+companionTestID+"/chat", map[string]string{
		"message": "hello",
	})
	if w.Code != 200 {
		t.Fatalf("Growth-Edge read failure must not break chat: %d body=%s", w.Code, w.Body.String())
	}
	if engine.gotReq.LearnerWeaknessJSON != "" {
		t.Errorf("LearnerWeaknessJSON = %q; want empty on read error", engine.gotReq.LearnerWeaknessJSON)
	}
}
