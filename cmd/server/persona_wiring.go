// persona_wiring.go — composition root for CHO-2015 Persona (ADR-219 D2).
// Attaches two ports to the Server:
//
//   - Persona (companion.PersonaService) — load/apply/persist over the
//     already-wired CompanionInstances repo. Always wired when the instance
//     repo is present (it always is: in-mem fallback otherwise).
//   - PersonaGuardrail (local guardrail adapter) — screens the free-text
//     guidance note at SAVE time (agent_id companion_persona → tier strict).
//     On wiring failure (e.g. the mapping YAML is unavailable in a
//     bare dev run) the guardrail stays nil: knob-only + empty-note edits
//     still work, but a PUT carrying a NON-EMPTY note fails loud (503
//     PERSONA_GUARDRAIL_NOT_WIRED) — a note bound for a kid-facing system
//     prompt is NEVER persisted unscreened.
//
// Env contract (no inline config per feedback_no_inline_config):
//
//   - CHORA_ENVIRONMENT               (default: dev)
//   - CHORA_AGENT_GUARDRAIL_MAPPING   (default: config/agent-guardrail-mapping.yaml)
//
// Mirrors services/chora-sharing/cmd/server/main.go buildGuardrail — the
// save-time screening precedent (ADR-152; chora-guardrail decommissioned).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	cgcmodelarmor "github.com/apollo-chora/chora-common/modelarmor"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionmodelarmor "github.com/apollo-chora/chora-consumption/internal/adapter/modelarmor"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// wirePersona attaches the Persona service + save-time guardrail. Returns a
// closer for the underlying screener (nil when the guardrail was not wired);
// main() defers it during graceful shutdown.
func wirePersona(ctx context.Context, srv *httpadapter.Server) func() error {
	if srv.CompanionInstances == nil {
		log.Printf("consumption: persona NOT wired (CompanionInstances nil); /v1/me/companions/{id}/persona → 503 PERSONA_NOT_WIRED")
		return nil
	}
	svc, err := companion.NewPersonaService(srv.CompanionInstances)
	if err != nil {
		log.Printf("consumption: persona service build failed: %v — persona → 503 PERSONA_NOT_WIRED", err)
		return nil
	}
	srv.Persona = svc

	guard, closer, err := buildPersonaGuardrail(ctx)
	if err != nil {
		log.Printf("consumption: persona guardrail NOT wired (%v); PUT with a NON-EMPTY note → 503 PERSONA_GUARDRAIL_NOT_WIRED (knob-only + empty-note edits still work)", err)
		return nil
	}
	srv.PersonaGuardrail = guard
	log.Printf("consumption: persona wired (CHO-2015): GET/PUT /v1/me/companions/{id}/persona; note screened via the local guardrail agent_id=%s (ADR-152)",
		consumptionmodelarmor.AgentIDCompanionPersona)
	return closer
}

// buildPersonaGuardrail constructs the local guardrail adapter for the
// persona-note save-time screen. Returns the adapter + a Close func + error.
// All config is env-derived (no inline config).
func buildPersonaGuardrail(ctx context.Context) (consumptionmodelarmor.GuardrailPort, func() error, error) {
	environment := personaEnvOr("CHORA_ENVIRONMENT", "dev")
	mappingPath := personaEnvOr("CHORA_AGENT_GUARDRAIL_MAPPING", "config/agent-guardrail-mapping.yaml")

	resolver, err := consumptionmodelarmor.LoadResolverFromFile(mappingPath, "chora-local", "local", environment)
	if err != nil {
		return nil, nil, fmt.Errorf("persona guardrail resolver: %w", err)
	}
	screener := cgcmodelarmor.NewLocalScreener()
	// Register permissive-tier templates as INSPECT_ONLY so a MATCH_FOUND on
	// an audit-only tier becomes VerdictInspectOnly rather than Block.
	// companion_persona itself is strict (the local default) — this is hygiene
	// for the shared screener, mirroring the sharing precedent.
	for _, name := range resolver.PermissiveTemplates() {
		screener.SetTemplateMode(name, cgcmodelarmor.TemplateModeInspectOnly)
	}
	adapter, err := consumptionmodelarmor.NewAdapter(screener, resolver)
	if err != nil {
		_ = screener.Close()
		return nil, nil, fmt.Errorf("persona guardrail adapter: %w", err)
	}
	return adapter, screener.Close, nil
}

// personaEnvOr returns os.Getenv(key) when non-empty (after trim) else def.
func personaEnvOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
