// ritual_step_executor.go — CHO-2016 G4: the StepExecutor adapter (satisfies
// companion.StepExecutor). It orchestrates ONE Ritual step DETERMINISTICALLY —
// forwarding the runner's already-narrowed tool allowlist + bounded params to a
// single gateway-routed agent turn, then mapping the turn's result into the
// step outcome + ADR-197 decision stamp.
//
// Hexagonal seam: the actual gateway turn is the RitualStepTurnInvoker port —
// the ONLY non-deterministic surface. Its production binding
// (ritual_step_turn_engine.go) routes through the companion engine /
// chora-model-gateway (Armor + token metering central, ADR-177). Tests inject a
// fake so the deterministic mapping is verified without a live gateway.
//
// v1 metering: a Ritual step is NOT separately learner-debited — the flat
// companion_ritual_run charge (reserved up-front by RitualManaReserver) is the
// single learner-facing cost. Per-step turns are still OTLP-traced + gateway-
// metered for the IMDA-D3 token ledger (observability), but carry no per-step
// debit action code (no double-charge).
package clients

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// RitualStepTurnRequest is one gateway-routed step turn (narrowed allowlist +
// bounded params + trace). Deterministically built from a StepExecInput.
type RitualStepTurnRequest struct {
	TenantID     string
	CompanionID  string
	OwnerGCID    string
	RunID        string
	StepIndex    int
	SkillKey     string
	Params       map[string]any
	AllowedTools []string // narrowed to THIS step's Skill (forwarded verbatim)
	Traceparent  string
	Tracestate   string
}

// RitualStepTurnResponse is the turn outcome. Blocked distinguishes an Armor
// block from a transport error. Prompt version/hash + citations are the ADR-197
// stamp fields the engine emits (empty until the engine surfaces them — see
// ritual_step_turn_engine.go; the executor NEVER fabricates them).
type RitualStepTurnResponse struct {
	OutputText    string
	ToolsInvoked  []string
	Citations     []string
	Model         string
	PromptVersion string
	// PromptSource is "registry" or "embedded_fallback": WHICH prompt shaped
	// the turn. Recorded beside the version because the version alone cannot
	// say whether an operator's override was in effect.
	PromptSource string
	PromptHash   string
	Blocked      bool
	BlockReason  string
	// Suspended marks an ADR-252 containment denial: an operator paused this
	// companion and the gateway refused the turn. DISTINCT from Blocked, which
	// means an Armor content block; collapsing them tells the learner their own
	// content was unsafe when nothing of the sort happened.
	Suspended bool
}

// RitualStepTurnInvoker is the single-turn seam. Production = the model-gateway
// binding; tests = a fake.
type RitualStepTurnInvoker interface {
	InvokeStepTurn(ctx context.Context, in RitualStepTurnRequest) (RitualStepTurnResponse, error)
}

// RitualStepExecutor implements companion.StepExecutor.
type RitualStepExecutor struct {
	turn RitualStepTurnInvoker
}

// NewRitualStepExecutor wires the executor. A nil invoker is tolerated at
// construction — every call then fails loud.
func NewRitualStepExecutor(turn RitualStepTurnInvoker) *RitualStepExecutor {
	return &RitualStepExecutor{turn: turn}
}

// Compile-time check.
var _ companion.StepExecutor = (*RitualStepExecutor)(nil)

// ExecuteStep forwards the narrowed step to the gateway turn and maps the result.
func (e *RitualStepExecutor) ExecuteStep(ctx context.Context, in companion.StepExecInput) (companion.StepExecResult, error) {
	if e == nil || e.turn == nil {
		return companion.StepExecResult{}, errors.New("ritual_step_executor: nil turn invoker")
	}
	resp, err := e.turn.InvokeStepTurn(ctx, RitualStepTurnRequest{
		TenantID:     in.TenantID,
		CompanionID:  in.CompanionID,
		OwnerGCID:    in.OwnerGCID,
		RunID:        in.RunID,
		StepIndex:    in.StepIndex,
		SkillKey:     in.SkillKey,
		Params:       in.Params,
		AllowedTools: in.AllowedTools, // verbatim — the runner already narrowed it
		Traceparent:  in.Traceparent,
		Tracestate:   in.Tracestate,
	})
	if err != nil {
		// Transport/infra fault — surface fail-loud (the runner aborts + refunds).
		return companion.StepExecResult{}, fmt.Errorf("ritual_step_executor: step %d (%s): %w", in.StepIndex, in.SkillKey, err)
	}

	// Identity of the stamp is DETERMINISTIC (from the input); the rest is the
	// turn's ADR-197 decision record. Citations/prompt fields pass through
	// unfabricated (empty when the engine has not surfaced them).
	stamp := companion.StepStamp{
		StepIndex:     in.StepIndex,
		SkillKey:      in.SkillKey,
		PromptVersion: resp.PromptVersion,
		PromptSource:  resp.PromptSource,
		PromptHash:    resp.PromptHash,
		ToolsInvoked:  resp.ToolsInvoked,
		Citations:     resp.Citations,
	}
	// Checked before Blocked so the two denials can never be conflated. The
	// paused companion is THIS step's companion; when the ADR-257 peer step
	// lands, a step with an `actor` will have to name the ACTOR's id here
	// instead, because the suspended companion may then not be the one the
	// learner is looking at.
	if resp.Suspended {
		return companion.StepExecResult{
			Suspended:            true,
			SuspendedCompanionID: in.CompanionID,
			Stamp:                stamp,
		}, nil
	}
	if resp.Blocked {
		return companion.StepExecResult{Blocked: true, BlockReason: resp.BlockReason, Stamp: stamp}, nil
	}
	return companion.StepExecResult{Output: resp.OutputText, Stamp: stamp}, nil
}
