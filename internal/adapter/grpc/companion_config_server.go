// companion_config_server.go — ResolveCompanionConfig gRPC method.
//
// Loads the full per-Companion session-bootstrap config the AI-Kernel Companion
// agent needs at session start (ADR-147 §7 session-time per-instance config).
// This is the PRODUCTION replacement for the agent's POC stub registry
// (skillregistry.NewStubRegistry), which only knew canned companion_ids and
// refused real Companions with "companion not found" → 0-output chat turns
// (root cause of the F2 chat 0-output defect, 2026-06-02).
//
// Per hexagonal SKILL: transport-only adapter. Validation + RLS-ctx stamping +
// proto marshaling; the base-config read is delegated to the
// companion.InstanceRepository port, the growth axis to domain growth.Service.
package grpc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// CompanionInstanceReader loads a Companion's base instance config by id.
// Satisfied by companion.InstanceRepository (pg + in-memory adapters). RLS is
// applied inside the adapter (reads tenant from ctx); ResolveCompanionConfig
// stamps the request tenant/gcid onto ctx before calling.
type CompanionInstanceReader interface {
	Get(ctx context.Context, companionID string) (*companion.Instance, error)
}

// CompanionLoadoutReader is the loadout read slice (CHO-2012, ADR-218 D3):
// the runtime AllowedSkills = the EQUIPPED active Skill keys, not the whole
// owned set. Satisfied by companion.LoadoutRepository (pg + inmem).
type CompanionLoadoutReader interface {
	ListGrants(ctx context.Context, tenantID, companionID string) ([]companion.SkillGrant, error)
}

// ResolveCompanionConfig implements consumptionv1.CompanionGrowthServer.
//
// Returns the merged base-instance + ADR-149 growth-axis config as a
// CompanionInstanceConfig. RLS + ownership are enforced via caller_gcid.
func (s *CompanionGrowthServer) ResolveCompanionConfig(
	ctx context.Context,
	req *consumptionv1.ResolveCompanionConfigRequest,
) (*consumptionv1.ResolveCompanionConfigResponse, error) {
	ctx, span := tracer().Start(ctx, "CompanionGrowth.ResolveCompanionConfig")
	defer span.End()
	span.SetAttributes(
		attribute.String("chora.tenant_id", req.GetTenantId()),
		attribute.String("chora.companion_id", req.GetCompanionId()),
	)

	if s.instances == nil {
		// Fail-loud — never synthesize a stub config (feedback_no_stubs_real_wiring).
		return nil, status.Error(codes.Unimplemented,
			"companion instance reader not wired (WithCompanionInstanceReader); ResolveCompanionConfig unavailable")
	}

	tenantID := strings.TrimSpace(req.GetTenantId())
	companionID := strings.TrimSpace(req.GetCompanionId())
	callerGCID := strings.TrimSpace(req.GetCallerGcid())
	if tenantID == "" || companionID == "" || callerGCID == "" {
		return nil, status.Error(codes.InvalidArgument,
			"tenant_id + companion_id + caller_gcid are all required")
	}

	cfg, err := ResolveCompanionInstanceConfig(ctx, s.instances, s.svc, s.loadouts, s.catalogue, tenantID, companionID, callerGCID)
	if err != nil {
		switch {
		case errors.Is(err, companion.ErrInstanceNotFound):
			return nil, status.Error(codes.NotFound, "companion not found")
		case errors.Is(err, growth.ErrCompanionNotFound):
			return nil, status.Error(codes.NotFound, "companion growth state not found")
		default:
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}

	span.SetAttributes(attribute.Int("chora.growth_stage", int(cfg.GetGrowthStage())))
	return &consumptionv1.ResolveCompanionConfigResponse{Config: cfg}, nil
}

// GrowthStateReader is the slice of growth.Service that
// ResolveCompanionInstanceConfig needs (the ADR-149 growth axis).
// *growth.Service satisfies it.
type GrowthStateReader interface {
	GetCompanionGrowth(ctx context.Context, tenantID, companionID, callerGCID string) (*growth.State, error)
}

// ResolveCompanionInstanceConfig loads + merges a Companion's base instance
// config (companion_instances) with the ADR-149 growth axis into a
// CompanionInstanceConfig.
//
// Shared by TWO callers:
//   - the gRPC ResolveCompanionConfig RPC (BFF / general consumers), and
//   - the in-process chat path (consumption resolves the config locally and
//     passes it in the ADK CreateSession state per the bug-3 mesh-sidestep —
//     the sidecar-less Companion agent reads it from session state instead of
//     calling back over consumption's STRICT-mTLS gRPC).
//
// Stamps tenant (+ gcid) onto ctx so the pg repo's rls.ApplySession reads them
// (else the read filters to 0 rows). Returns companion.ErrInstanceNotFound (also
// for ownership/tenant mismatch — defence-in-depth) or a wrapped
// growth.ErrCompanionNotFound; callers map these to their transport's shape.
// GrowthStage is LOAD-BEARING: instancedispatch refuses a pre-hatch Stage-0
// (egg) Companion, so a real Stage-2 Companion MUST carry its true stage.
//
// CHO-2012 (ADR-218 D2/D3/D8): AllowedSkills = the EQUIPPED active Skill
// keys from the loadout (a nil reader ⇒ empty allowlist — grants no longer
// ride the aggregate); SkillSlotsUnlocked + EvolutionTier DERIVE from the
// growth stage (the per-user-XP tier axis is retired).
func ResolveCompanionInstanceConfig(
	ctx context.Context,
	instances CompanionInstanceReader,
	growthReader GrowthStateReader,
	loadouts CompanionLoadoutReader,
	catalogue SkillCatalogueReader,
	tenantID, companionID, callerGCID string,
) (*consumptionv1.CompanionInstanceConfig, error) {
	ctx = tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), callerGCID)

	inst, err := instances.Get(ctx, companionID)
	if err != nil {
		if errors.Is(err, companion.ErrInstanceNotFound) {
			return nil, companion.ErrInstanceNotFound
		}
		return nil, fmt.Errorf("load companion instance: %w", err)
	}
	if inst.OwnerGCID != callerGCID || inst.TenantID != tenantID {
		return nil, companion.ErrInstanceNotFound
	}

	cfg := &consumptionv1.CompanionInstanceConfig{
		CompanionId:           inst.CompanionID,
		OwnerGcid:             inst.OwnerGCID,
		TenantId:              inst.TenantID,
		Name:                  inst.Name,
		Specialization:        inst.Specialization,
		MemoryContextCapacity: int32(inst.MemoryContextCapacity),
		PersonaSummary:        inst.PersonaSummary,
		ConfiguredRules:       mapConfiguredRules(inst.ConfiguredRules),
		AllowedSkills:         []string{},
		LearnerPersona:        learnerPersonaOrDefault(inst.ConfiguredRules),
		// CHO-2015 (ADR-219 D2) — the fenced free-text guidance note rides its
		// dedicated column (Armor-screened at save); the agent fences it into a
		// locked-frame system-prompt segment.
		GuidanceNote: inst.GuidanceNote,
	}

	state, gerr := growthReader.GetCompanionGrowth(ctx, tenantID, companionID, callerGCID)
	if gerr != nil {
		return nil, fmt.Errorf("load companion growth axis: %w", gerr)
	}
	cfg.GrowthStage = int32(state.GrowthStage)
	cfg.Species = state.Species
	cfg.ShinyVariant = state.ShinyVariant
	cfg.ResonantAtomId = state.ResonantAtomID
	// visible_kg_neighbors: RETIRED (R3-1, CHO-2013 P1) — stays empty; the
	// ring-radius centre is state.ResonantConceptID (Goal root fallback).
	if state.AhaMomentActiveUntil != nil {
		cfg.AhaMomentActiveUntil = timestamppb.New(*state.AhaMomentActiveUntil)
	}
	// Stage-derived allowances (ADR-218 D2 + L6 derived band, D8).
	cfg.SkillSlotsUnlocked = int32(growth.SlotsForStage(state.GrowthStage))
	cfg.EvolutionTier = growth.TierForStage(state.GrowthStage)

	if loadouts != nil {
		grants, lerr := loadouts.ListGrants(ctx, tenantID, companionID)
		if lerr != nil {
			return nil, fmt.Errorf("load companion loadout: %w", lerr)
		}
		loadout := companion.Loadout{Grants: grants}
		cfg.AllowedSkills = loadout.EquippedActiveSkillKeys()
	}

	// CHO-2013 P1.B (R4-2): allowed_tools = innate(stage) canonical names ∪
	// the equipped Skills' tool_handler_refs. Equipped keys expand only when
	// the catalogue reader is wired; a missing catalogue row for an equipped
	// key is grant/catalogue drift → fail-loud.
	cat := map[string]companion.CatalogEntry{}
	if catalogue != nil {
		var cerr error
		cat, cerr = catalogue.ListCatalogue(ctx)
		if cerr != nil {
			return nil, fmt.Errorf("load skill catalogue: %w", cerr)
		}
	}
	equippedForTools := cfg.AllowedSkills
	if catalogue == nil {
		equippedForTools = nil
	}
	tools, terr := mergeAllowedTools(state.UnlockedTools, equippedForTools, cat)
	if terr != nil {
		return nil, terr
	}
	cfg.AllowedTools = tools
	return cfg, nil
}

// mapConfiguredRules translates the companion_instances.configured_rules JSONB
// (a flat string map) into the typed proto rules. Keys are best-effort: the
// stored shape has drifted across migrations (tone/hint_policy vs
// max_hint_count/max_session_minutes), so probe known aliases + fall back to
// safe defaults so the agent always gets a composable rule set.
func mapConfiguredRules(m map[string]string) *consumptionv1.CompanionConfiguredRules {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	rules := &consumptionv1.CompanionConfiguredRules{
		Tone:                 firstNonEmpty(get("tone"), "encouraging"),
		MaxHintsBeforeReveal: 3,
		HintProgression:      firstNonEmpty(get("hint_progression"), "ladder"),
		DifficultyCap:        firstNonEmpty(get("difficulty_cap"), "intermediate"),
		Language:             firstNonEmpty(get("language"), "en"),
		CitationStrictness:   firstNonEmpty(get("citation_strictness"), "strict"),
		// CHO-2015 (ADR-219 D2) — persona address style + interest chips.
		AddressStyle:  firstNonEmpty(get("address_style"), "first_name"),
		InterestChips: splitInterestChips(get("interest_chips")),
	}
	if n, err := strconv.Atoi(get("max_hints_before_reveal", "max_hint_count", "hint_policy")); err == nil && n >= 0 {
		rules.MaxHintsBeforeReveal = int32(n)
	}
	return rules
}

// splitInterestChips parses the comma-joined interest_chips value (the shape
// companion.ApplyPersona writes) back into a trimmed, non-empty slice. Returns
// an empty (non-nil) slice when unset so the proto carries [] not null.
func splitInterestChips(csv string) []string {
	out := []string{}
	for _, c := range strings.Split(csv, ",") {
		if t := strings.TrimSpace(c); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func learnerPersonaOrDefault(m map[string]string) string {
	if v, ok := m["learner_persona"]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return "curious-explorer"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
