// Package modelarmor adapts the canonical Cloud Model Armor Screener
// (libs/chora-go-common/modelarmor) to a chora-consumption GuardrailPort
// contract, used to screen learner-authored free-text at save time.
//
// The sole caller today is the Companion Persona PUT handler (CHO-2015,
// ADR-219 D2): the learner's fenced guidance note — the single sanctioned
// free-text field on the Persona sheet (ADR-205 zero-free-form exception)
// — is screened via Cloud Model Armor's SanitizeUserPrompt BEFORE it is
// persisted and BEFORE it is ever injected into the Companion agent's
// system prompt. A Block verdict refuses the write (HTTP 422); the note is
// never stored.
//
// Per ADR-152 (chora-guardrail superseded by Cloud Model Armor) screening
// calls flow directly from the LLM-issuing / content-persisting service to
// Cloud Model Armor via the Vertex AI SDK — no hop through the retired
// chora-guardrail Cloud Run service.
//
// Hexagonal boundary: this is an OUTBOUND ADAPTER. It wraps the canonical
// Screener interface from chora-go-common + implements the GuardrailPort
// port consumed by the persona handler. Domain code NEVER imports this
// package — the domain (companion.PersonaService) guarantees the note's
// SHAPE; this adapter guarantees its SAFETY, screened at the HTTP edge
// before the domain applies it.
//
// This mirrors services/chora-sharing/internal/adapter/modelarmor/adapter.go
// (the save-time screening precedent for user-generated content) — same
// structure + verdict mapping, specialised for the persona agent_id.
//
// Per-agent risk tier comes from `chora-contracts/yaml/agent-guardrail-mapping.yaml`
// — loaded at boot via the TemplateResolver — and surfaced as the full
// template resource name:
//
//	projects/{CHORA_MODELARMOR_PROJECT}/locations/{CHORA_MODELARMOR_LOCATION}/templates/chora-guardrail-{tier}-{CHORA_ENVIRONMENT}
//
// The persona note's agent_id (`companion_persona`) resolves to tier
// `strict` per the YAML mapping — the note is kid-facing config that lands
// in a minor-accessible system prompt, so RAI + PI + jailbreak all block.
package modelarmor

import (
	"context"
	"errors"
	"fmt"

	cgcmodelarmor "github.com/apollo-chora/chora-common/modelarmor"
)

// AgentIDCompanionPersona is the canonical agent_id for the Companion
// Persona guidance-note screen. Resolves to tier `strict` in
// chora-contracts/yaml/agent-guardrail-mapping.yaml.
const AgentIDCompanionPersona = "companion_persona"

// GuardrailPort is the chora-consumption-facing port the persona handler
// consumes. It exposes a single Screen call over the learner's guidance
// note BEFORE the note is persisted + composed into the Companion system
// prompt. The port lives in the adapter package (not domain) because no
// other chora-consumption code consumes it — the persona handler is the
// sole caller. If a second consumer surfaces later, lift the interface to
// a domain ports file.
type GuardrailPort interface {
	// Screen runs Cloud Model Armor's SanitizeUserPrompt over the
	// supplied content. Returns a verdict the caller switches on; on
	// Block the caller MUST refuse the write + surface a 422.
	Screen(ctx context.Context, req ScreenRequest) (ScreenVerdict, error)
}

// ScreenRequest is the chora-consumption → Guardrail input. Specialised
// for the persona-note use case (the content is a learner-authored note,
// keyed to the note-owner's GCID).
type ScreenRequest struct {
	// TenantID — Chora tenant under which the persona edit is made.
	TenantID string

	// GCID — Global Chora ID of the learner authoring the note. Surfaces
	// on the emitted OTel span (+ the violation event, when B6 wires it).
	GCID string

	// AgentID — Chora agent identifier for the consuming surface. For the
	// persona note this is `companion_persona` (see AgentIDCompanionPersona).
	// The resolver maps this to the canonical risk tier.
	AgentID string

	// Content — the guidance-note text to screen.
	Content string
}

// ScreenVerdict is the guardrail's reply. The string vocabulary
// (`approved` | `blocked` | `flagged`) mirrors the chora-sharing adapter
// so any future shared caller switches on the same tokens.
type ScreenVerdict struct {
	// Verdict — `approved` (allow), `blocked` (hard block — refuse the
	// write), `flagged` (INSPECT_ONLY audit — caller MAY proceed but
	// SHOULD emit a violation event).
	Verdict string

	// Reason — short human-readable summary of which filter(s) hit.
	// Empty when Verdict == `approved`.
	Reason string

	// Violations — per-filter audit trail. Empty for approved verdicts.
	Violations []GuardrailDecision
}

// GuardrailDecision captures one filter's outcome for audit. One row per
// MATCH_FOUND filter hit.
type GuardrailDecision struct {
	// Tier — always `model_armor` for this adapter.
	Tier string

	// Result — `block` | `flag` | `pass` — canonical per-decision
	// vocabulary mirroring the sharing adapter.
	Result string

	// ViolationType — `{filter_name}[:subcategory]` (e.g. `rai:HATE_SPEECH`,
	// `pi_and_jailbreak`).
	ViolationType string
}

// Canonical verdict strings.
const (
	VerdictApproved = "approved"
	VerdictBlocked  = "blocked"
	VerdictFlagged  = "flagged"
)

// Adapter wraps cgcmodelarmor.Screener + an agent→template resolver and
// implements GuardrailPort. The persona handler calls Screen(ctx, req);
// the adapter resolves the agent's template tier, builds the full
// resource name, and dispatches to Screener.SanitizeUserPrompt.
type Adapter struct {
	screener cgcmodelarmor.Screener
	resolver *TemplateResolver
}

// Compile-time check that *Adapter implements GuardrailPort.
var _ GuardrailPort = (*Adapter)(nil)

// NewAdapter wires the canonical Screener with a template resolver. The
// caller is responsible for SDK lifecycle (defer screener.Close()).
func NewAdapter(screener cgcmodelarmor.Screener, resolver *TemplateResolver) (*Adapter, error) {
	if screener == nil {
		return nil, errors.New("modelarmor adapter: screener required")
	}
	if resolver == nil {
		return nil, errors.New("modelarmor adapter: resolver required")
	}
	return &Adapter{screener: screener, resolver: resolver}, nil
}

// Screen implements GuardrailPort. The note is screened as the LLM INPUT
// (pre-LLM) via Cloud Model Armor's SanitizeUserPrompt path — the note is
// learner-authored content destined for the Companion's system prompt. The
// verdict mapping:
//
//   - VerdictAllow       → "approved"
//   - VerdictBlock       → "blocked"
//   - VerdictInspectOnly → "flagged" (audit-only — caller MAY proceed)
//
// Violations are surfaced as []GuardrailDecision, one per MATCH_FOUND
// filter hit, so the caller's audit trail records which filter fired.
func (a *Adapter) Screen(ctx context.Context, req ScreenRequest) (ScreenVerdict, error) {
	templateName, err := a.resolver.Resolve(req.AgentID)
	if err != nil {
		return ScreenVerdict{}, fmt.Errorf("modelarmor adapter: resolve template for agent_id=%q: %w", req.AgentID, err)
	}
	result, err := a.screener.SanitizeUserPrompt(ctx, cgcmodelarmor.ScreenRequest{
		TenantID:     req.TenantID,
		AgentID:      req.AgentID,
		GCID:         req.GCID,
		TemplateName: templateName,
		Text:         req.Content,
	})
	if err != nil {
		return ScreenVerdict{}, fmt.Errorf("modelarmor adapter: SanitizeUserPrompt: %w", err)
	}
	return toScreenVerdict(result), nil
}

// toScreenVerdict maps a canonical Screener ScreenResult into the
// consumption-shaped ScreenVerdict.
func toScreenVerdict(r cgcmodelarmor.ScreenResult) ScreenVerdict {
	v := ScreenVerdict{
		Reason:     r.Reason,
		Violations: buildViolations(r),
	}
	switch r.Verdict {
	case cgcmodelarmor.VerdictBlock:
		v.Verdict = VerdictBlocked
	case cgcmodelarmor.VerdictInspectOnly:
		v.Verdict = VerdictFlagged
	default:
		v.Verdict = VerdictApproved
	}
	return v
}

// buildViolations converts canonical FilterHit rows into the consumption
// adapter's GuardrailDecision shape. Only MATCH_FOUND hits become
// decisions — allow/no-match rows are dropped to keep the audit payload
// tight.
func buildViolations(r cgcmodelarmor.ScreenResult) []GuardrailDecision {
	if len(r.Filters) == 0 {
		return nil
	}
	decisions := make([]GuardrailDecision, 0, len(r.Filters))
	for _, hit := range r.Filters {
		if hit.MatchState != cgcmodelarmor.MatchStateMatchFound {
			continue
		}
		violationType := hit.FilterName
		if hit.Subcategory != "" {
			violationType = hit.FilterName + ":" + hit.Subcategory
		}
		decisions = append(decisions, GuardrailDecision{
			Tier:          "model_armor",
			Result:        resultFor(r.Verdict),
			ViolationType: violationType,
		})
	}
	if len(decisions) == 0 {
		return nil
	}
	return decisions
}

// resultFor returns the canonical per-decision Result string for a
// given canonical Verdict.
func resultFor(v cgcmodelarmor.Verdict) string {
	switch v {
	case cgcmodelarmor.VerdictBlock:
		return "block"
	case cgcmodelarmor.VerdictInspectOnly:
		return "flag"
	default:
		return "pass"
	}
}
