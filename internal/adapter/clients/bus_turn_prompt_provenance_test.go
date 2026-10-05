package clients

// bus_turn_prompt_provenance_test.go: the synthesised turn_complete frame
// carries the prompt provenance (UX Track U, D2 / N9 step 2, last consumption
// hop).
//
// This is the hop that made the field dark. The agent stamped prompt_version,
// the kennel lane dropped it, and even once both are fixed emitTerminal has to
// put it on the frame or the ritual step still reads nothing: the frame is
// SYNTHESISED here from the stored turn (ADR-254 D8), not forwarded from the
// agent, so a field absent from this map is absent from the wire the step
// reads no matter what the upstream sends.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// frameFor stores the WIRE body and reads back the frame emitTerminal builds.
//
// The body is literal snake_case JSON on purpose. turn.Result holds the raw
// companion_turn.completed.v1 body as received, and emitTerminal re-parses it
// with ParseTurnResultJSON, so a test that stored a Go-marshalled TurnResult
// would be storing Go FIELD NAMES and proving nothing about the real path.
// That mistake is easy to make here and this comment is the guard.
//
// The body must carry turn_id. Learned the hard way: without it
// ParseTurnResultJSON refuses, and emitTerminal LOGS A WARNING AND CARRIES ON
// with a zero TurnResult, so the frame is emitted with every field empty and a
// test reads that as "the field is not wired" rather than "the fixture was
// malformed". Worth knowing beyond this test: an unreadable stored result
// degrades a completed turn to an empty one on nothing louder than a WARN.
func frameFor(t *testing.T, wireBody string) chatTurnCompletePayload {
	t.Helper()
	turn := &companion.Turn{
		TurnID: "t-1", ConversationID: "c-1", Lane: companion.TurnLaneCompanionTurn,
		Status: companion.TurnStatusCompleted, Result: json.RawMessage(wireBody),
	}
	out := make(chan ChatStreamFrame, 8)
	(&BusTurnEngine{}).emitTerminal(turn, out)
	close(out)

	for f := range out {
		if f.Type != ChatFrameTurnComplete {
			continue
		}
		var p chatTurnCompletePayload
		if err := json.Unmarshal(f.Data, &p); err != nil {
			t.Fatalf("decode turn_complete: %v", err)
		}
		return p
	}
	t.Fatal("no turn_complete frame emitted")
	return chatTurnCompletePayload{}
}

func TestEmitTerminal_CarriesPromptProvenanceOntoTheFrame(t *testing.T) {
	p := frameFor(t, `{"turn_id":"t-1","status":"OK","reply_text":"Reciprocals.",
		"generated_by_model_id":"flash","prompt_version":"v1","prompt_source":"registry"}`)
	if p.PromptVersion != "v1" {
		t.Errorf("prompt_version = %q; want v1", p.PromptVersion)
	}
	if p.PromptSource != "registry" {
		t.Errorf("prompt_source = %q; want registry", p.PromptSource)
	}
}

// A turn stored before the provenance landed carries neither, and the frame
// must say so rather than invent one.
func TestEmitTerminal_AbsentProvenanceStaysEmptyOnTheFrame(t *testing.T) {
	p := frameFor(t, `{"turn_id":"t-1","status":"OK","reply_text":"hi","generated_by_model_id":"flash"}`)
	if p.PromptVersion != "" || p.PromptSource != "" {
		t.Errorf("prompt_version = %q, prompt_source = %q; want both empty",
			p.PromptVersion, p.PromptSource)
	}
}

// The whole point of the chain: what the ritual step finally stamps.
func TestCollectStepTurnResponse_StampsTheRealPromptProvenance(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameTurnComplete, `{"turn_id":"t-1","model":"flash",
		"reply_text":"hi","prompt_version":"v1","prompt_source":"embedded_fallback"}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if out.PromptVersion != "v1" {
		t.Errorf("PromptVersion = %q; want v1", out.PromptVersion)
	}
	if out.PromptSource != "embedded_fallback" {
		t.Errorf("PromptSource = %q; want embedded_fallback", out.PromptSource)
	}
}

// The end of the chain: the O plus decision stamp. A version without its source
// cannot answer "was my override actually in effect", which is the first thing
// an operator asks when an answer surprises them.
func TestRitualStepExecutor_StampsThePromptSourceBesideTheVersion(t *testing.T) {
	exec := NewRitualStepExecutor(stubStepTurn{resp: RitualStepTurnResponse{
		OutputText: "out", Model: "flash", PromptVersion: "v1", PromptSource: "registry",
	}})
	res, err := exec.ExecuteStep(context.Background(), companion.StepExecInput{
		StepIndex: 0, SkillKey: "explain_anew", CompanionID: "fam-1",
	})
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if res.Stamp.PromptVersion != "v1" {
		t.Errorf("Stamp.PromptVersion = %q; want v1", res.Stamp.PromptVersion)
	}
	if res.Stamp.PromptSource != "registry" {
		t.Errorf("Stamp.PromptSource = %q; want registry", res.Stamp.PromptSource)
	}
}

type stubStepTurn struct{ resp RitualStepTurnResponse }

func (s stubStepTurn) InvokeStepTurn(context.Context, RitualStepTurnRequest) (RitualStepTurnResponse, error) {
	return s.resp, nil
}

// D2 tail (b): an unreadable stored result is an ERROR, never a blank success.
//
// emitTerminal used to log a WARN and carry on with a zero TurnResult, so a
// COMPLETED turn whose stored body could not be parsed reached the learner as
// a turn_complete with empty reply_text, no model, no grounding and no
// provenance. That is a fail-loud violation twice over: the learner sees a
// companion that answered nothing and looks broken, and a ritual step stamps a
// successful step that produced nothing, which is exactly the "empty result
// versus error" confusion ADR-252 D6 calls the single most likely way to build
// this wrong.
//
// The fixture is the one that fooled me while writing the N9 tests: a stored
// body with no turn_id, which ParseTurnResultJSON refuses.
func terminalFramesFor(t *testing.T, wireBody string) []ChatStreamFrame {
	t.Helper()
	turn := &companion.Turn{
		TurnID: "t-1", ConversationID: "c-1", Lane: companion.TurnLaneCompanionTurn,
		Status: companion.TurnStatusCompleted, Result: json.RawMessage(wireBody),
	}
	out := make(chan ChatStreamFrame, 8)
	(&BusTurnEngine{}).emitTerminal(turn, out)
	close(out)

	var frames []ChatStreamFrame
	for f := range out {
		frames = append(frames, f)
	}
	return frames
}

func TestEmitTerminal_UnreadableStoredResultIsAnErrorNotABlankTurn(t *testing.T) {
	// No turn_id: ParseTurnResultJSON refuses this body.
	frames := terminalFramesFor(t, `{"status":"OK","reply_text":"hi","generated_by_model_id":"m"}`)

	for _, f := range frames {
		if f.Type == ChatFrameTurnComplete {
			t.Fatalf("an unreadable stored result was served as a COMPLETED turn: %s", f.Data)
		}
	}
	if len(frames) != 1 || frames[0].Type != ChatFrameError {
		t.Fatalf("frames = %+v; want exactly one error frame", frames)
	}
	var p chatErrorPayload
	if err := json.Unmarshal(frames[0].Data, &p); err != nil {
		t.Fatalf("decode error frame: %v", err)
	}
	if p.Code != companion.TurnErrCodeFailed {
		t.Errorf("code = %q; want %q", p.Code, companion.TurnErrCodeFailed)
	}
	if strings.TrimSpace(p.Message) == "" {
		t.Error("the error frame carries no message; the learner is owed a reason")
	}
}

// A COMPLETED turn with no stored result at all is the same defect wearing a
// different costume: there is nothing to render, so rendering an empty success
// claims the companion answered and said nothing.
func TestEmitTerminal_CompletedTurnWithNoStoredResultIsAnError(t *testing.T) {
	frames := terminalFramesFor(t, "")

	for _, f := range frames {
		if f.Type == ChatFrameTurnComplete {
			t.Fatalf("a completed turn with no stored result was served as success: %s", f.Data)
		}
	}
	if len(frames) != 1 || frames[0].Type != ChatFrameError {
		t.Fatalf("frames = %+v; want exactly one error frame", frames)
	}
}

// The readable path is untouched: a well-formed result still completes.
func TestEmitTerminal_AReadableResultStillCompletes(t *testing.T) {
	frames := terminalFramesFor(t,
		`{"turn_id":"t-1","status":"OK","reply_text":"hi","generated_by_model_id":"m"}`)

	for _, f := range frames {
		if f.Type == ChatFrameError {
			t.Fatalf("a readable result was refused: %s", f.Data)
		}
	}
	if len(frames) != 1 || frames[0].Type != ChatFrameTurnComplete {
		t.Fatalf("frames = %+v; want exactly one turn_complete", frames)
	}
}

// The other end of tail (b): a ritual step fed that error frame FAILS LOUDLY.
//
// Without this the fix would only move the defect: the frame would say error
// and the step would still stamp a success. It must also NOT be mistaken for
// the ADR-252 containment denial, which is a designed pause with a refund and
// entirely different learner copy.
func TestCollectStepTurnResponse_UnreadableResultFailsTheStepLoudly(t *testing.T) {
	ch := make(chan ChatStreamFrame, 1)
	ch <- rawFrame(ChatFrameError, `{"turn_id":"t-1","code":"`+companion.TurnErrCodeFailed+
		`","message":"The Companion could not finish this turn."}`)
	close(ch)

	out, err := collectStepTurnResponse(ch)
	if err == nil {
		t.Fatal("an unreadable stored result did not fail the step; it would stamp a success")
	}
	if out.Suspended {
		t.Error("a read failure was read as a containment denial: that is a designed pause with a refund and different copy")
	}
}
