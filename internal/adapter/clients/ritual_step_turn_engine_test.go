// ritual_step_turn_engine_test.go — CHO-2016 G4: the engine binding's
// DETERMINISTIC parts (message build + frame→response mapping) verified against
// a fake CompanionEnginePort. The LIVE gateway turn is deploy-verified (flagged
// in ritual_step_turn_engine.go); it is NOT faked-as-success here.
package clients

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeChatEngine returns a synthetic frame stream (or a synchronous error).
type fakeChatEngine struct {
	frames  []ChatStreamFrame
	syncErr error
	gotReq  CompanionChatRequest
}

func (f *fakeChatEngine) Greet(_ context.Context, _ CompanionGreetRequest) (CompanionGreetResponse, error) {
	return CompanionGreetResponse{}, nil
}

func (f *fakeChatEngine) StreamChat(_ context.Context, req CompanionChatRequest) (<-chan ChatStreamFrame, error) {
	f.gotReq = req
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	ch := make(chan ChatStreamFrame, len(f.frames))
	for _, fr := range f.frames {
		ch <- fr
	}
	close(ch)
	return ch, nil
}

type fakeStepCtxResolver struct {
	tc  RitualStepTurnContext
	err error
}

func (f *fakeStepCtxResolver) ResolveRitualStepContext(_ context.Context, _, _, _ string) (RitualStepTurnContext, error) {
	return f.tc, f.err
}

func frame(typ string, payload any) ChatStreamFrame {
	b, _ := json.Marshal(payload)
	return ChatStreamFrame{Type: typ, Data: b}
}

func TestBuildRitualStepMessage_IsDeterministic(t *testing.T) {
	msg := BuildRitualStepMessage("explain_anew", map[string]any{"style": "analogy"}, []string{"atom.search", "atom.cite"})
	for _, want := range []string{"explain_anew", "analogy", "atom.search", "atom.cite"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// CHO-2060 leak #4: a ritual step's params can carry a cross-domain ref UUID
// (e.g. an explain_anew target). The message is fed to the companion prompt whose
// reply reaches the learner, so a raw UUID param must be stripped.
func TestBuildRitualStepMessage_SanitizesUUIDInParams(t *testing.T) {
	msg := BuildRitualStepMessage("explain_anew",
		map[string]any{"target": "01890a5d-ac96-774b-bcce-b302099a8057", "style": "analogy"},
		[]string{"atom.search"})
	if leakUUIDRe.MatchString(msg) {
		t.Errorf("ritual step message leaks a raw UUID param:\n%s", msg)
	}
	// The non-identifier content still survives for the model.
	if !strings.Contains(msg, "analogy") || !strings.Contains(msg, "explain_anew") {
		t.Errorf("message dropped legitimate content: %q", msg)
	}
}

func TestCollectStepTurnResponse_HappyPath(t *testing.T) {
	stream := chanOf(
		frame(ChatFrameSessionOpen, chatSessionOpenPayload{EngineSessionID: "s1"}),
		frame(ChatFrameToolCall, chatToolCallPayload{Tool: "atom.search"}),
		frame(ChatFrameToken, chatTokenPayload{Text: "here is "}),
		frame(ChatFrameToken, chatTokenPayload{Text: "an analogy"}),
		frame(ChatFrameTurnComplete, chatTurnCompletePayload{Model: "flash"}),
	)
	resp, err := collectStepTurnResponse(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if resp.OutputText != "here is an analogy" {
		t.Errorf("output = %q", resp.OutputText)
	}
	if len(resp.ToolsInvoked) != 1 || resp.ToolsInvoked[0] != "atom.search" {
		t.Errorf("tools = %+v", resp.ToolsInvoked)
	}
	if resp.Model != "flash" {
		t.Errorf("model = %q", resp.Model)
	}
}

func TestCollectStepTurnResponse_ErrorFrameFailsLoud(t *testing.T) {
	stream := chanOf(
		frame(ChatFrameToken, chatTokenPayload{Text: "partial"}),
		frame(ChatFrameError, chatErrorPayload{Code: "ENGINE_STREAM_ABORTED", Message: "boom"}),
	)
	if _, err := collectStepTurnResponse(stream); err == nil {
		t.Fatal("a mid-stream error frame must surface fail-loud")
	}
}

func TestCompanionEngineStepTurn_InvokeMapsThrough(t *testing.T) {
	eng := &fakeChatEngine{frames: []ChatStreamFrame{
		frame(ChatFrameToken, chatTokenPayload{Text: "done"}),
		frame(ChatFrameTurnComplete, chatTurnCompletePayload{Model: "flash"}),
	}}
	res := &fakeStepCtxResolver{tc: RitualStepTurnContext{ManaTier: "standard", CompanionConfigJSON: "{}"}}
	b := NewCompanionEngineStepTurn(eng, res)
	resp, err := b.InvokeStepTurn(context.Background(), RitualStepTurnRequest{
		TenantID: "t", CompanionID: "f", OwnerGCID: "g", RunID: "run-1", StepIndex: 0,
		SkillKey: "explain_anew", AllowedTools: []string{"atom.search"},
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.OutputText != "done" || resp.Model != "flash" {
		t.Errorf("resp = %+v", resp)
	}
	// The engine turn carried the resolved context + no per-step debit code.
	if eng.gotReq.ManaTier != "standard" || eng.gotReq.CompanionConfigJSON != "{}" {
		t.Errorf("resolved context not forwarded: %+v", eng.gotReq)
	}
	if eng.gotReq.ManaActionCode != "" {
		t.Errorf("a ritual step must NOT carry a per-step debit code (flat run charge): %q", eng.gotReq.ManaActionCode)
	}
	if !strings.Contains(eng.gotReq.Message, "explain_anew") {
		t.Errorf("message not built from the step: %q", eng.gotReq.Message)
	}
}

func TestCompanionEngineStepTurn_ResolverErrorFailsLoud(t *testing.T) {
	b := NewCompanionEngineStepTurn(&fakeChatEngine{}, &fakeStepCtxResolver{err: context.DeadlineExceeded})
	if _, err := b.InvokeStepTurn(context.Background(), RitualStepTurnRequest{TenantID: "t", CompanionID: "f", OwnerGCID: "g"}); err == nil {
		t.Fatal("a context-resolution failure must surface fail-loud")
	}
}

func TestCompanionEngineStepTurn_NilDepsFailLoud(t *testing.T) {
	b := NewCompanionEngineStepTurn(nil, nil)
	if _, err := b.InvokeStepTurn(context.Background(), RitualStepTurnRequest{}); err == nil {
		t.Fatal("nil engine/resolver must fail loud")
	}
}

func chanOf(frames ...ChatStreamFrame) <-chan ChatStreamFrame {
	ch := make(chan ChatStreamFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)
	return ch
}
