// ritual_step_turn_engine.go — CHO-2016 G4: the PRODUCTION RitualStepTurnInvoker
// binding. Routes one Ritual step through the companion engine / chora-model-
// gateway (the ADR-163/177 chokepoint) as a single agent turn, reusing the
// existing StreamChat path (now GKE model-gateway-backed).
//
// REAL wiring — not a stub. Its deterministic parts (the per-step Message build
// + the frame→response mapping) are unit-tested against a fake CompanionEnginePort.
// TWO gaps are flagged DEPLOY-TIME (never faked):
//
//	[LIVE-VERIFY] the live gateway turn itself (Armor screening, real model
//	output, token metering) can only be exercised against a running
//	model-gateway — verify at deploy, not here.
//
//	[ENGINE-CHANGE] true per-turn allowlist NARROWING requires the companion
//	agent's ToolFilter to honour a per-turn allowed-tools override. The existing
//	StreamChat request carries no allowlist field, so v1 forwards the narrowed
//	set advisorily inside the step instruction; the hard ToolFilter enforcement
//	is an engine-side change (companion_chat_adk_go). Until then the narrowing is
//	documented + logged, and the runner-level allowlist is authoritative for
//	what the step is ALLOWED to do (the engine may still see the innate∪equipped
//	union). Flagged so it is not mistaken for enforced.
//
//	[STAMP] What the ADR-197 stamp actually carries, traced end to end on
//	2026-09-02 rather than assumed. The production path is the BUS (ADR-254 D8):
//	the agent emits a completion envelope, the kennel lane republishes it as
//	companion_turn.completed.v1, and BusTurnEngine.emitTerminal SYNTHESISES the
//	turn_complete frame inside consumption from the stored turn row.
//
//	  tools    REAL. tool_call frames.
//	  model    REAL. emitTerminal carries generated_by_model_id.
//	  citations REAL as of N9 slice a. The agent has emitted `grounding` all
//	           along and emitTerminal already put it on the frame; this file
//	           simply did not read it. Per ADR-249 a ref appears only when
//	           cite_atom CONFIRMED the atom for the tenant, so these are
//	           validated sources, not tool names. Absent stamps nothing.
//	  prompt_version  NOT FED, despite the parser below reading it. The agent
//	           emits prompt_version and prompt_source on its envelope, and
//	           _completed_payload in the kennel's
//	           orchestrators/consumption_lanes.py drops both when it builds the
//	           completed event field by field; companion.ParseTurnResultJSON
//	           then has no prompt field to parse either. Restoring it needs that
//	           lane, which is a third tree. Until then this stamps "".
//	  prompt_hash     NOBODY computes it, on any side. It must be hashed by the
//	           AGENT, over the prompt it actually composed and ran. Consumption
//	           knows only the version it injected, so a hash computed here would
//	           be a true hash of the wrong thing and indistinguishable from a
//	           real one. Not approximated.
package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// RitualStepTurnContext is the per-turn state the engine needs beyond the step
// itself (resolved from the companion the same way the chat handler does).
type RitualStepTurnContext struct {
	ManaTier            string
	CompanionConfigJSON string
}

// RitualStepContextResolver resolves the per-turn engine context for a companion.
// The G5 wiring injects the existing consumption resolver (mana tier + resolved
// CompanionInstanceConfig protojson) — the same one StreamChat/daily-dose use.
type RitualStepContextResolver interface {
	ResolveRitualStepContext(ctx context.Context, tenantID, companionID, gcid string) (RitualStepTurnContext, error)
}

// CompanionEngineStepTurn binds RitualStepTurnInvoker to the companion engine.
type CompanionEngineStepTurn struct {
	engine   CompanionEnginePort
	resolver RitualStepContextResolver
}

// NewCompanionEngineStepTurn wires the binding.
func NewCompanionEngineStepTurn(engine CompanionEnginePort, resolver RitualStepContextResolver) *CompanionEngineStepTurn {
	return &CompanionEngineStepTurn{engine: engine, resolver: resolver}
}

// Compile-time check.
var _ RitualStepTurnInvoker = (*CompanionEngineStepTurn)(nil)

// InvokeStepTurn performs ONE gateway-routed step turn and collects the result.
func (b *CompanionEngineStepTurn) InvokeStepTurn(ctx context.Context, in RitualStepTurnRequest) (RitualStepTurnResponse, error) {
	if b == nil || b.engine == nil || b.resolver == nil {
		return RitualStepTurnResponse{}, fmt.Errorf("ritual_step_turn: nil engine/resolver")
	}
	tc, err := b.resolver.ResolveRitualStepContext(ctx, in.TenantID, in.CompanionID, in.OwnerGCID)
	if err != nil {
		return RitualStepTurnResponse{}, fmt.Errorf("ritual_step_turn: resolve context: %w", err)
	}

	// [LIVE] no per-step learner debit — the flat companion_ritual_run charge
	// (reserved up-front) is the single cost; leave ManaActionCode empty so the
	// gateway un-meters the debit (still OTLP-traced for the IMDA-D3 ledger).
	stream, err := b.engine.StreamChat(ctx, CompanionChatRequest{
		TenantID:            in.TenantID,
		UserGCID:            in.OwnerGCID,
		CompanionID:         in.CompanionID,
		ManaTier:            tc.ManaTier,
		Message:             BuildRitualStepMessage(in.SkillKey, in.Params, in.AllowedTools),
		TurnID:              in.RunID + ":" + itoa(in.StepIndex),
		CompanionConfigJSON: tc.CompanionConfigJSON,
	})
	if err != nil {
		// Synchronous transport failure — surface fail-loud (the runner aborts + refunds).
		return RitualStepTurnResponse{}, fmt.Errorf("ritual_step_turn: stream: %w", err)
	}
	return collectStepTurnResponse(stream)
}

// BuildRitualStepMessage renders the deterministic per-step instruction. The
// narrowed allowlist is forwarded advisorily (see [ENGINE-CHANGE] flag). Pure +
// testable.
func BuildRitualStepMessage(skillKey string, params map[string]any, allowedTools []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Perform the %q skill as one ritual step.", skillKey)
	if len(params) > 0 {
		if pj, err := json.Marshal(params); err == nil {
			// CHO-2060: params can carry a cross-domain ref UUID (e.g. an
			// explain_anew target). This message drives a learner-facing reply, so
			// strip any raw id from the marshalled params before it enters the prompt.
			fmt.Fprintf(&b, " Parameters: %s.", lp.SanitizeLearnerText(string(pj)))
		}
	}
	if len(allowedTools) > 0 {
		fmt.Fprintf(&b, " Use ONLY these tools: %s.", strings.Join(allowedTools, ", "))
	}
	return b.String()
}

// collectStepTurnResponse drains an engine chat stream into a step response.
// Deterministic mapping — tested against synthetic frames. A mid-stream error
// frame surfaces fail-loud (the runner records the step as failed + refunds);
// finer Armor-block classification (→ Blocked) is deploy-time ([STAMP]/[LIVE]).
func collectStepTurnResponse(stream <-chan ChatStreamFrame) (RitualStepTurnResponse, error) {
	var out RitualStepTurnResponse
	var text strings.Builder
	for frame := range stream {
		switch frame.Type {
		case ChatFrameToken:
			var p chatTokenPayload
			if json.Unmarshal(frame.Data, &p) == nil {
				text.WriteString(p.Text)
			}
		case ChatFrameToolCall:
			var p chatToolCallPayload
			if json.Unmarshal(frame.Data, &p) == nil && p.Tool != "" {
				out.ToolsInvoked = append(out.ToolsInvoked, p.Tool)
			}
		case ChatFrameTurnComplete:
			var p chatTurnCompletePayload
			if json.Unmarshal(frame.Data, &p) == nil {
				out.Model = p.Model
				// ADR-197 P3 (CHO-2368, G4 close): the engine client echoes
				// the resolved version it injected at session build; absent
				// stays "" (never fabricated).
				out.PromptVersion = p.PromptVersion
				out.PromptSource = p.PromptSource
				out.PromptHash = p.PromptHash
				// N9 slice a: the ADR-249 verified sources the turn grounded
				// on. Absent or empty stamps NOTHING, never a fabricated
				// citation: the agent only emits a ref cite_atom confirmed, so
				// inventing one here would make the platform vouch for a source
				// that was never consulted.
				for _, g := range p.Grounding {
					if c := g.citation(); c != "" {
						out.Citations = append(out.Citations, c)
					}
				}
				// ADR-254 D8 (bus path): the whole reply rides turn_complete.
				if p.ReplyText != "" {
					text.Reset()
					text.WriteString(p.ReplyText)
				}
			}
		case ChatFrameError:
			var p chatErrorPayload
			_ = json.Unmarshal(frame.Data, &p)
			// ADR-252 D5 / ADR-257 D7 (N13): a containment denial is a TERMINAL
			// OUTCOME, not a transport fault. It is recognised by the error CODE
			// and never by an empty result: D6 requires the two to be told apart
			// by the error and calls doing it any other way "the single most
			// likely way to build this wrong".
			//
			// Exactly one code is treated this way. Every other error frame stays
			// a fail-loud error, because widening this would dress a real outage
			// up as "your companion is paused", which is worse than the defect
			// being fixed. The two layers spell the same fact differently
			// (COMPANION_SUSPENDED on the caller-facing frame,
			// companion_suspended as the kennel's REJECTED code), so the compare
			// is case-insensitive against the one canonical token.
			if strings.EqualFold(strings.TrimSpace(p.Code), companion.KennelErrCodeCompanionSuspended) {
				return RitualStepTurnResponse{Suspended: true}, nil
			}
			return RitualStepTurnResponse{}, fmt.Errorf("ritual_step_turn: engine error %s: %s", p.Code, p.Message)
		}
	}
	out.OutputText = strings.TrimSpace(text.String())
	return out, nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
