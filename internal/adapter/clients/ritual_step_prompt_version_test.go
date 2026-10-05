package clients

// ADR-197 P3 (CHO-2368) - G4 close: the ritual step stamp carries the REAL
// resolved prompt version. The engine client injects the resolved version at
// session build and echoes it on the turn-complete frame; collectStepTurnResponse
// surfaces it so RitualStepExecutor's stamp passes it through unfabricated.
// An absent field stays "" (never fabricated) - pre-P3 frames are unchanged.

import "testing"

func TestCollectStepTurnResponse_CarriesPromptVersion(t *testing.T) {
	ch := make(chan ChatStreamFrame, 2)
	ch <- frame(ChatFrameTurnComplete, chatTurnCompletePayload{
		Model:         "flash",
		PromptVersion: "1.1.0",
	})
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if out.PromptVersion != "1.1.0" {
		t.Errorf("PromptVersion = %q; want 1.1.0", out.PromptVersion)
	}
}

func TestCollectStepTurnResponse_AbsentPromptVersionStaysEmpty(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- frame(ChatFrameTurnComplete, chatTurnCompletePayload{Model: "flash"})
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if out.PromptVersion != "" {
		t.Errorf("PromptVersion = %q; want empty (never fabricated)", out.PromptVersion)
	}
}
