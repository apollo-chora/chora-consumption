// ritual_step_executor_test.go — CHO-2016 G4: the StepExecutor adapter's
// DETERMINISTIC orchestration, RED-first. The single non-deterministic seam
// (the gateway-routed agent turn) is a fake here; the LIVE turn is
// deploy-verified (per owner ruling: unit-test the deterministic logic, fail-
// loud-flag the live-verify — never stub a fake success).
package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeStepTurn records the request it received and returns a canned response.
type fakeStepTurn struct {
	got  RitualStepTurnRequest
	resp RitualStepTurnResponse
	err  error
}

func (f *fakeStepTurn) InvokeStepTurn(_ context.Context, in RitualStepTurnRequest) (RitualStepTurnResponse, error) {
	f.got = in
	return f.resp, f.err
}

func stepInput() companion.StepExecInput {
	return companion.StepExecInput{
		TenantID: "t-1", CompanionID: "fam-1", OwnerGCID: "gcid-1", RunID: "run-1",
		StepIndex: 2, SkillKey: "explain_anew", Params: map[string]any{"style": "analogy"},
		AllowedTools: []string{"atom.search", "atom.cite"},
		Traceparent:  "tp", Tracestate: "ts",
	}
}

func TestStepExecutor_HappyPathMapsOutputAndStamp(t *testing.T) {
	turn := &fakeStepTurn{resp: RitualStepTurnResponse{
		OutputText: "here is an analogy...", ToolsInvoked: []string{"atom.search"},
		Citations: []string{"Fractions basics"}, Model: "flash", PromptVersion: "explain_anew@v3", PromptHash: "sha256:abc",
	}}
	e := NewRitualStepExecutor(turn)
	res, err := e.ExecuteStep(context.Background(), stepInput())
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if res.Blocked {
		t.Error("happy path must not be Blocked")
	}
	if res.Output != "here is an analogy..." {
		t.Errorf("output = %v", res.Output)
	}
	// Stamp: index+key from the INPUT (deterministic), the rest from the turn.
	if res.Stamp.StepIndex != 2 || res.Stamp.SkillKey != "explain_anew" {
		t.Errorf("stamp identity = %d/%q, want 2/explain_anew", res.Stamp.StepIndex, res.Stamp.SkillKey)
	}
	if res.Stamp.PromptVersion != "explain_anew@v3" || res.Stamp.PromptHash != "sha256:abc" {
		t.Errorf("stamp prompt fields not carried: %+v", res.Stamp)
	}
	if len(res.Stamp.ToolsInvoked) != 1 || res.Stamp.ToolsInvoked[0] != "atom.search" {
		t.Errorf("tools invoked not carried: %+v", res.Stamp.ToolsInvoked)
	}
	if len(res.Stamp.Citations) != 1 || res.Stamp.Citations[0] != "Fractions basics" {
		t.Errorf("citations not carried: %+v", res.Stamp.Citations)
	}
}

// The allowlist passed to the turn MUST be exactly the step's narrowed set — the
// runner already narrowed it; the executor forwards it verbatim (no widening).
func TestStepExecutor_ForwardsNarrowedAllowlistAndParams(t *testing.T) {
	turn := &fakeStepTurn{resp: RitualStepTurnResponse{OutputText: "ok"}}
	e := NewRitualStepExecutor(turn)
	if _, err := e.ExecuteStep(context.Background(), stepInput()); err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	g := turn.got
	if g.SkillKey != "explain_anew" || g.StepIndex != 2 || g.RunID != "run-1" {
		t.Errorf("turn request identity mis-forwarded: %+v", g)
	}
	if len(g.AllowedTools) != 2 || g.AllowedTools[0] != "atom.search" || g.AllowedTools[1] != "atom.cite" {
		t.Errorf("allowlist not forwarded verbatim: %+v", g.AllowedTools)
	}
	if g.Params["style"] != "analogy" {
		t.Errorf("params not forwarded: %+v", g.Params)
	}
	if g.TenantID != "t-1" || g.OwnerGCID != "gcid-1" || g.Traceparent != "tp" {
		t.Errorf("tenant/gcid/trace mis-forwarded: %+v", g)
	}
}

func TestStepExecutor_BlockedIsTerminalNotError(t *testing.T) {
	turn := &fakeStepTurn{resp: RitualStepTurnResponse{Blocked: true, BlockReason: "armor: unsafe"}}
	e := NewRitualStepExecutor(turn)
	res, err := e.ExecuteStep(context.Background(), stepInput())
	if err != nil {
		t.Fatalf("an Armor block is a terminal StepExecResult, not a Go error: %v", err)
	}
	if !res.Blocked || res.BlockReason != "armor: unsafe" {
		t.Errorf("blocked mapping lost: %+v", res)
	}
	// Even a blocked step stamps its identity (for the run record).
	if res.Stamp.StepIndex != 2 || res.Stamp.SkillKey != "explain_anew" {
		t.Errorf("blocked step must still stamp identity: %+v", res.Stamp)
	}
}

func TestStepExecutor_TurnErrorSurfacesFailLoud(t *testing.T) {
	turn := &fakeStepTurn{err: errors.New("gateway unreachable")}
	e := NewRitualStepExecutor(turn)
	if _, err := e.ExecuteStep(context.Background(), stepInput()); err == nil {
		t.Fatal("an infra/transport error from the turn must surface (fail-loud)")
	}
}

func TestStepExecutor_NilInvokerFailsLoud(t *testing.T) {
	e := NewRitualStepExecutor(nil)
	if _, err := e.ExecuteStep(context.Background(), stepInput()); err == nil {
		t.Fatal("nil turn invoker must fail loud")
	}
}
