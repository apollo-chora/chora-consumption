// companion_instance_handlers.go — multi-Companion (1:N) HTTP surface for
// chora-consumption. Migrated from the legacy chora-companion service-side
// handlers per the 2026-05-12 M12.2.B Companion consolidation.
//
// Endpoint surface (mounted at /v1/me/companions per the EXT learner-self
// convention used elsewhere in this router):
//
//	POST   /v1/me/companions            create a Companion Instance
//	GET    /v1/me/companions            list the caller's Instances
//	GET    /v1/me/companions/{id}       fetch one Instance
//	DELETE /v1/me/companions/{id}       soft-delete
//
// Auth model (MVP): X-Tenant-Id + gcid headers (same as the rest of the
// surface). Real WebAuthn JWT verification lands in M13.A BFF layer.
//
// Cap enforcement: max_companions_per_user defaults to
// companion.DefaultMaxCompanionsPerUser=3. Production reads the cap from the
// chora_tenancy `tenant_entitlements.config_overrides.max_companions_per_user`
// event-sourced read model (M14.1+); the MVP hardcodes the default.
//
// Pub/Sub: emits chora.consumption.companion.created.v1 on create. The other
// 4 multi-Companion topics (skill_slot_unlocked / skill_granted /
// memory_eviction / specialization_changed) attach to future endpoints
// (skill mgmt, evolution promotion, memory eviction notifier) not in scope
// for the M12.2.B Companion slice — flagged in the consolidation report.
//
// D6 4-pillar resilience: event publish currently runs synchronously after
// the repo write. Outbox migration (atomic state-write + event-publish)
// lands in M12.3 per .claude/skills/data-consistency.
//
// Cloud Trace span attributes (per ai-observability-cloud-trace skill +
// Pillar 4 of D6): handlers populate `chora.companion_id` +
// `chora.companion.class_name` (= specialization) + `chora.companion.evolution_tier`
// via the events envelope payload (real OTel SDK span attrs land at M12).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// repoCtx attaches tenant_id + gcid to the request context so the repo
// layer's rls.ApplySession (which reads via tracing.TenantIDFromContext)
// can SET LOCAL chora.tenant_id / chora.user_gcid inside the transaction.
//
// Per multi-tenant-rls SKILL: handlers MUST populate ctx before invoking
// any RLS-scoped repo method. Without this, rls.ApplySession returns
// ErrNoTenantContext and the entire transaction aborts (HTTP 500
// LIST_FAILED with "tenant_id missing on context"). Mirror of the
// companion_chat_handler.go pattern.
func repoCtx(r *http.Request, tenantID, gcid string) context.Context {
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	ctx = tracing.WithGCID(ctx, gcid)
	return ctx
}

// ----- Request / response payloads -----

type createInstanceReq struct {
	Name            string            `json:"name"`
	Specialization  string            `json:"specialization"`
	ConfiguredRules map[string]string `json:"configured_rules,omitempty"`
}

type instanceResp struct {
	CompanionID    string `json:"companion_id"`
	TenantID       string `json:"tenant_id"`
	OwnerGCID      string `json:"owner_gcid"`
	Name           string `json:"name"`
	Specialization string `json:"specialization"`
	// Subject is the Companion's topic axis (math / history / coding / …) —
	// the SAME value as Specialization, surfaced under the FE-facing key the
	// A+ dashboard roster matches a Growth Edge's specialist Companion on
	// (case-insensitive token match against the edge's category /
	// concept_label). It is distinct from the `species` creature axis. The
	// 4th arg of companion.NewInstance(tenant, gcid, name, subject) sets it.
	// omitempty keeps pre-specialization legacy rows clean on the wire.
	Subject               string            `json:"subject,omitempty"`
	EvolutionTier         string            `json:"evolution_tier"`
	SkillSlotsUnlocked    int               `json:"skill_slots_unlocked"`
	MemoryContextCapacity int               `json:"memory_context_capacity"`
	SkillGrants           []string          `json:"skill_grants"`
	ConfiguredRules       map[string]string `json:"configured_rules"`
	MemoryBankAppName     string            `json:"memory_bank_app_name"`
	CreatedAt             string            `json:"created_at"`
	UpdatedAt             string            `json:"updated_at"`
	// debt #44 / A24 / B5 (2026-05-16) — inline enrichment so FE drops
	// the per-Companion GET /v1/me/companions/{id}/growth fetch. Populated
	// only on list responses (createCompanionInstance + getCompanionInstance
	// return the base shape — they're per-row endpoints where the FE
	// already round-trips through the growth handler when needed).
	GrowthState *growthStateResp `json:"growth_state,omitempty"`
	Cosmetic    *cosmeticResp    `json:"cosmetic,omitempty"`
}

// growthStateResp is the inline growth-axis projection per debt #44.
// Mirrors the FE `CompanionSummary` derivation in
// chora-web/src/app/core/companion/companion-growth.service.ts §wireToSummary
// so FE can drop the wireToSummary fallback chain.
//
// Always-populated fields: `stage`, `stage_name`, `exp`, `exp_to_next_stage`.
// Nullable fields: `current_breed` (empty pre-hatch), `breed_revealed_at`
// (NULL pre-hatch), `effective_llm_tier` (empty pre-hatch).
type growthStateResp struct {
	Stage                int     `json:"stage"`
	StageName            string  `json:"stage_name"`
	Exp                  int     `json:"exp"`
	ExpToNextStage       int     `json:"exp_to_next_stage"`
	CurrentBreed         string  `json:"current_breed"`
	BreedRevealedAt      *string `json:"breed_revealed_at"`
	EffectiveLLMTier     string  `json:"effective_llm_tier"`
	LastStageUpAt        *string `json:"last_stage_up_at"`
	ResonantAtomID       string  `json:"resonant_atom_id"`
	AhaMomentConsumed    bool    `json:"aha_moment_consumed"`
	AhaMomentActiveUntil *string `json:"aha_moment_active_until"`
	// NextUnlocks is the CHO-2030 (R3-9) next-stage Path preview, served on
	// the LIST read for C1a so the home's roster can render a NAMED stage-up
	// tease without N per-companion growth calls, which is the debt #44 this
	// inline projection exists to pay off.
	//
	// Same shape as the direct growth read, catalogue_active included. That
	// flag is load-bearing: it tells "unlocks next and is awake" from "unlocks
	// next, still dark", and a tease without it promises a Skill the learner
	// cannot use the moment they earn it.
	//
	// OMITTED, never a fabricated empty list, when the preview seams are
	// unwired or a read fails: the roster is the page and a preview is a
	// nicety, so a failure here never costs the learner their companions.
	NextUnlocks []nextUnlockResp `json:"next_unlocks,omitempty"`
	// GrowthExp is the CUMULATIVE EXP (distinct from per-stage `Exp`).
	// F5 surfaces it on the profile so the FE can render lifetime progress.
	// Omitted (0) on the list endpoint's per-row projection where only the
	// per-stage `Exp` is meaningful; populated on the per-instance profile.
	GrowthExp int `json:"growth_exp,omitempty"`
}

// cosmeticResp is the inline cosmetic projection per debt #44. Today
// derived from the species_rarity + shiny_variant columns on
// companion_instances; `equipped_skin_id` is reserved (NULL) for the future
// DigitalSkin grant table.
type cosmeticResp struct {
	EquippedSkinID *string `json:"equipped_skin_id"`
	Shiny          bool    `json:"shiny"`
	Rarity         string  `json:"rarity"`
}

// rosterEnvelope is the list endpoint's wire envelope. The original
// `items` array is retained (additive backwards-compat per the handoff
// §B5 contract); each item carries the enriched shape.
type rosterEnvelope struct {
	Items []instanceResp `json:"items"`
	// RosterCap is the effective maximum companions for this learner, and
	// CapSource says where it came from ("default" or, once M12.3 lands,
	// "entitlement"). C3 renders "N of RosterCap", and a wrong denominator is
	// worse than none: it either tells a learner they cannot hold a companion
	// they have paid for, or offers one the create handler will refuse.
	//
	// Both are ALWAYS present. An omitted cap would leave the client inventing
	// its own denominator, which is the fork this field exists to close.
	RosterCap int    `json:"roster_cap"`
	CapSource string `json:"cap_source"`
}

func toInstanceResp(inst *companion.Instance) instanceResp {
	grants := inst.SkillGrants
	if grants == nil {
		grants = []string{}
	}
	rules := inst.ConfiguredRules
	if rules == nil {
		rules = map[string]string{}
	}
	return instanceResp{
		CompanionID:           inst.CompanionID,
		TenantID:              inst.TenantID,
		OwnerGCID:             inst.OwnerGCID,
		Name:                  inst.Name,
		Specialization:        inst.Specialization,
		Subject:               inst.Specialization,
		EvolutionTier:         string(inst.EvolutionTier),
		SkillSlotsUnlocked:    inst.SkillSlotsUnlocked,
		MemoryContextCapacity: inst.MemoryContextCapacity,
		SkillGrants:           grants,
		ConfiguredRules:       rules,
		MemoryBankAppName:     inst.MemoryBankAppName(),
		CreatedAt:             inst.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
		UpdatedAt:             inst.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	}
}

// toRosterItemResp composes the base instanceResp with the inline
// growth_state + cosmetic projections per debt #44.
//
// When `Growth` or `Cosmetic` is nil (legacy adapter / pre-0032 row), the
// nested object is OMITTED from the wire via omitempty so clients can
// still degrade to ConfiguredRules-JSONB derivation. The in-memory +
// pg adapters always populate non-nil projections (the inmem returns
// zero-value projections); only pre-this-debt callers would observe nil.
// hatchExpThreshold resolves the incubation gate this server surfaces on a
// Stage-0 roster row (D3). It mirrors growth.NewService's own fallback exactly:
// an unset gate resolves to the domain default rather than to 0, so the roster
// read and the per-companion growth read cannot disagree even on a unit server
// that never wired the config.
func (s *Server) hatchExpThreshold() int {
	if s.HatchExpThreshold > 0 {
		return s.HatchExpThreshold
	}
	return growth.DefaultHatchExpThreshold
}

func toRosterItemResp(entry *companion.RosterEntry, hatchExpThreshold int, unlocks []nextUnlockResp) instanceResp {
	resp := toInstanceResp(entry.Instance)
	if entry.Growth != nil {
		gs := &growthStateResp{
			Stage:     entry.Growth.Stage,
			StageName: entry.Growth.StageName,
			Exp:       entry.Growth.Exp,
			// D3: a Stage-0 pod warms towards the configured HATCH gate, not
			// towards the growth curve's 0. The repos project the curve value
			// (both pg and in-memory), so the correction lands here, at the one
			// boundary both of them pass through.
			ExpToNextStage:    companion.ExpToNextStageOrHatch(entry.Growth.Stage, hatchExpThreshold),
			CurrentBreed:      entry.Growth.CurrentBreed,
			EffectiveLLMTier:  entry.Growth.EffectiveLLMTier,
			ResonantAtomID:    entry.Growth.ResonantAtomID,
			AhaMomentConsumed: entry.Growth.AhaMomentConsumed,
		}
		// stage_name defensive fallback: if the projection's stage_name
		// field is empty (in-memory zero value), derive from the int
		// stage so the wire never surfaces ""→ FE's empty fallback.
		if gs.StageName == "" {
			gs.StageName = companion.StageName(gs.Stage)
		}
		if entry.Growth.BreedRevealedAt != nil {
			s := entry.Growth.BreedRevealedAt.UTC().Format("2006-01-02T15:04:05.000000Z")
			gs.BreedRevealedAt = &s
		}
		if entry.Growth.LastStageUpAt != nil {
			s := entry.Growth.LastStageUpAt.UTC().Format("2006-01-02T15:04:05.000000Z")
			gs.LastStageUpAt = &s
		}
		if entry.Growth.AhaMomentActiveUntil != nil {
			s := entry.Growth.AhaMomentActiveUntil.UTC().Format("2006-01-02T15:04:05.000000Z")
			gs.AhaMomentActiveUntil = &s
		}
		gs.NextUnlocks = unlocks
		resp.GrowthState = gs
	}
	if entry.Cosmetic != nil {
		c := &cosmeticResp{
			Shiny:  entry.Cosmetic.Shiny,
			Rarity: entry.Cosmetic.Rarity,
		}
		if entry.Cosmetic.EquippedSkinID != "" {
			s := entry.Cosmetic.EquippedSkinID
			c.EquippedSkinID = &s
		}
		resp.Cosmetic = c
	}
	return resp
}

// ----- /v1/me/companions (collection) -----

func (s *Server) handleCompanionInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createCompanionInstance(w, r)
	case http.MethodGet:
		s.listCompanionInstances(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

func (s *Server) createCompanionInstance(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req createInstanceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	inst, err := companion.NewInstance(tenantID, gcid, req.Name, req.Specialization)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_COMPANION", err.Error())
		return
	}
	for k, v := range req.ConfiguredRules {
		inst.SetRule(k, v)
	}

	// Cap enforcement through the SINGLE definition, so the number refused
	// here and the number the roster serves as its denominator are the same
	// number and not two that happen to agree today (companion_roster_cap.go).
	maxPerUser := s.enforcedRosterCap()
	if err := s.CompanionInstances.Create(repoCtx(r, tenantID, gcid), inst, maxPerUser); err != nil {
		if errors.Is(err, companion.ErrRosterCapReached) {
			writeError(w, http.StatusConflict, "COMPANION_CAP_REACHED",
				"max companions per user reached")
			return
		}
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}

	// Emit chora.consumption.companion.created.v1.
	traceparent, tracestate := traceFromHeaders(r)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, inst.CompanionID)
	// Pillar 4 D6: emit `chora.*` attributes per agentic-resilience-d6 +
	// ai-observability-cloud-trace skill. M12.3 wires real OTel spans.
	if err := s.Publisher.Publish(events.TopicCompanionCreated, env, map[string]any{
		"companion_id":              inst.CompanionID,
		"owner_gcid":                inst.OwnerGCID,
		"name":                      inst.Name,
		"specialization":            inst.Specialization,
		"evolution_tier":            string(inst.EvolutionTier),
		"chora_companion_id":        inst.CompanionID,
		"chora_companion_class":     inst.Specialization,
		"chora_companion_evol_tier": string(inst.EvolutionTier),
	}); err != nil {
		// Non-fatal: handler is best-effort until M12.3 outbox lands.
		// Production: outbox + retry-with-DLQ via chora-go-common/outbox.
		writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toInstanceResp(inst))
}

func (s *Server) listCompanionInstances(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	// Per debt #44 / A24 / B5 (2026-05-16) the list endpoint now fetches
	// the enriched RosterEntry projection in one round-trip — eliminates
	// the FE's prior `GET /v1/me/companions` + per-Companion `GET /v1/me/
	// companions/{id}/growth` N+1.
	entries, err := s.CompanionInstances.ListRosterByOwner(repoCtx(r, tenantID, gcid), tenantID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	// C1a: the next-stage previews for every row, resolved ONCE per request.
	// A roster of eight companions must not make eight identical catalogue
	// reads, and C3 will not fall back to N growth calls.
	previews := s.rosterNextUnlocks(r.Context(), entries)
	items := make([]instanceResp, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Instance == nil {
			continue
		}
		items = append(items, toRosterItemResp(entry, s.hatchExpThreshold(),
			previews[entry.Instance.CompanionID]))
	}
	// C1a: the denominator for C3's "N of M", with its provenance. Served from
	// the same call the create handler enforces with.
	cap, capSource := s.rosterCap()
	writeJSON(w, http.StatusOK, rosterEnvelope{Items: items, RosterCap: cap, CapSource: capSource})
}

// ----- /v1/me/companions/{id} (per-instance) -----

func (s *Server) handleCompanionInstanceByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/companions/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ID", "companion_id required")
		return
	}
	// ADR-215 WS-1 (CHO-1997) — the {id}/memory subpath delegates to the
	// standalone learner-facing memory READ handler when wired. Checked BEFORE
	// the growth-route dispatch (which owns the other {id}/* subpaths). nil ⇒
	// fall through to the growth route (404) rather than a hard 503.
	if s.CompanionMemoryRead != nil {
		if id, ok := strings.CutSuffix(rest, "/memory"); ok && id != "" && !strings.Contains(id, "/") {
			s.CompanionMemoryRead.ServeHTTP(w, r)
			return
		}
	}
	// Register 6.4 R6: the {id}/memory/{memoryId} subpath delegates to the
	// standalone learner delete. Checked AFTER the read suffix above (which is
	// the shorter path and cannot collide) and BEFORE the growth-route dispatch,
	// which would otherwise swallow it as an unknown two-segment subpath and 404
	// while a perfectly correct handler sat unreachable.
	if s.CompanionMemoryDelete != nil {
		if id, sub, ok := strings.Cut(rest, "/memory/"); ok &&
			id != "" && !strings.Contains(id, "/") &&
			sub != "" && !strings.Contains(sub, "/") {
			s.CompanionMemoryDelete.ServeHTTP(w, r)
			return
		}
	}
	// ADR-149 growth subpaths: /v1/me/companions/{id}/{growth|hatch|kg-neighbors|source-revelation|growth-events}
	if strings.Contains(rest, "/") {
		s.handleCompanionGrowthRoute(w, r)
		return
	}
	id := rest
	switch r.Method {
	case http.MethodGet:
		s.getCompanionInstance(w, r, id)
	case http.MethodDelete:
		s.deleteCompanionInstance(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

// companionProfileResp is the F5 full profile projection for
// GET /v1/me/companions/{id}. It is a SUPERSET of instanceResp (every base
// identity field is preserved additively for backwards-compat), plus:
//
//   - identity extras pulled from the growth axis (display_name alias,
//     species / species_rarity / shiny_variant / growth_stage / tone /
//     specialization already on base);
//   - the full growth_state projection (growth_exp, growth_stage,
//     effective_llm_tier, last_stage_up_at, resonant_atom);
//   - cosmetic projection (shiny + rarity);
//   - roster_context (roster_size + max_companions);
//   - an OPTIONAL memory_summary (omitted unless a MemorySummaryResolver is
//     wired AND returns a non-empty summary — F4 Memory Bank is separate).
//
// The growth axis degrades gracefully: when Server.Growth is nil OR the
// growth row is not yet provisioned (ErrCompanionNotFound), a pre-hatch
// Stage-0 projection is returned — the profile NEVER 503s/500s on a missing
// growth row.
type companionProfileResp struct {
	// ----- base identity (superset of instanceResp) -----
	CompanionID           string            `json:"companion_id"`
	TenantID              string            `json:"tenant_id"`
	OwnerGCID             string            `json:"owner_gcid"`
	Name                  string            `json:"name"`
	DisplayName           string            `json:"display_name"` // alias of Name for FE profile-pane binding
	Specialization        string            `json:"specialization"`
	Tone                  string            `json:"tone"`
	EvolutionTier         string            `json:"evolution_tier"`
	SkillSlotsUnlocked    int               `json:"skill_slots_unlocked"`
	MemoryContextCapacity int               `json:"memory_context_capacity"`
	SkillGrants           []string          `json:"skill_grants"`
	ConfiguredRules       map[string]string `json:"configured_rules"`
	MemoryBankAppName     string            `json:"memory_bank_app_name"`
	CreatedAt             string            `json:"created_at"`
	UpdatedAt             string            `json:"updated_at"`

	// ----- growth-axis identity extras (F5) -----
	GrowthStage   int    `json:"growth_stage"`
	Species       string `json:"species"`
	SpeciesRarity string `json:"species_rarity"`
	ShinyVariant  bool   `json:"shiny_variant"`

	// ----- nested projections (F5) -----
	GrowthState   *growthStateResp  `json:"growth_state"`
	Cosmetic      *cosmeticResp     `json:"cosmetic"`
	RosterContext rosterContextResp `json:"roster_context"`

	// MemorySummary is OPTIONAL — omitted when Memory Bank (F4) is not wired
	// or the resolver returns empty/errors. NEVER load-bearing.
	MemorySummary string `json:"memory_summary,omitempty"`

	// CompanionStatus is the ADR-252 / ADR-254 D11 containment render. Present
	// whenever the advisory projection is wired (always in production), so the
	// FE can bind it unconditionally; omitted only in unit servers, where
	// nothing measured it and asserting "not paused" would be a fabrication.
	CompanionStatus *companionStatusResp `json:"companion_status,omitempty"`
}

// companionStatusResp is the learner-facing containment state. reason + scope
// are omitted while not paused: there is no operator decision to describe.
// (The chora-gateway CompanionBridge camelises this to companionStatus.)
type companionStatusResp struct {
	Paused bool   `json:"paused"`
	Reason string `json:"reason,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

// rosterContextResp carries the caller's roster framing so the profile pane
// can render "Companion 3 of 3" + the add-more affordance without an extra
// list round-trip.
type rosterContextResp struct {
	RosterSize    int `json:"roster_size"`
	MaxCompanions int `json:"max_companions"`
}

func (s *Server) getCompanionInstance(w http.ResponseWriter, r *http.Request, id string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := repoCtx(r, tenantID, gcid)
	inst, err := s.CompanionInstances.Get(ctx, id)
	if err != nil {
		if errors.Is(err, companion.ErrInstanceNotFound) {
			writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	// Defence-in-depth ownership/tenant guard (the pg adapter already RLS-
	// scopes, but the in-memory adapter keys only by CompanionID). A Companion
	// the caller does not own surfaces as 404, never another tenant's data.
	if inst.OwnerGCID != gcid || inst.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
		return
	}

	writeJSON(w, http.StatusOK, s.buildCompanionProfile(ctx, r, tenantID, gcid, inst))
}

// buildCompanionProfile composes the F5 full profile from the base instance +
// growth axis + roster context + optional memory summary. Every enrichment
// degrades gracefully (growth-not-wired / growth-miss / memory-not-wired /
// memory-error) so the profile always renders a complete-shape 200.
func (s *Server) buildCompanionProfile(
	ctx context.Context, r *http.Request, tenantID, gcid string, inst *companion.Instance,
) companionProfileResp {
	base := toInstanceResp(inst)
	prof := companionProfileResp{
		CompanionID:           base.CompanionID,
		TenantID:              base.TenantID,
		OwnerGCID:             base.OwnerGCID,
		Name:                  base.Name,
		DisplayName:           base.Name,
		Specialization:        base.Specialization,
		Tone:                  inst.ConfiguredRules["tone"],
		EvolutionTier:         base.EvolutionTier,
		SkillSlotsUnlocked:    base.SkillSlotsUnlocked,
		MemoryContextCapacity: base.MemoryContextCapacity,
		SkillGrants:           base.SkillGrants,
		ConfiguredRules:       base.ConfiguredRules,
		MemoryBankAppName:     base.MemoryBankAppName,
		CreatedAt:             base.CreatedAt,
		UpdatedAt:             base.UpdatedAt,
	}

	// Growth axis (nil-safe + miss-safe). Resolve via the wired GrowthServer,
	// scoped to the caller's gcid so RLS + the service-layer ownership guard
	// match. On nil reader OR ErrCompanionNotFound, fall back to a pre-hatch
	// Stage-0 projection.
	prof.GrowthState, prof.Cosmetic = s.resolveGrowthProjection(ctx, tenantID, gcid, inst.CompanionID)
	prof.GrowthStage = prof.GrowthState.Stage
	prof.Species = prof.GrowthState.CurrentBreed
	prof.SpeciesRarity = prof.Cosmetic.Rarity
	prof.ShinyVariant = prof.Cosmetic.Shiny

	// Roster context — count the caller's active Companions (in-memory + pg
	// both honour soft-delete). Best-effort: a list error yields a 0-size
	// roster_context rather than failing the whole profile.
	if entries, lerr := s.CompanionInstances.ListByOwner(ctx, tenantID, gcid); lerr == nil {
		prof.RosterContext.RosterSize = len(entries)
	}
	prof.RosterContext.MaxCompanions = s.enforcedRosterCap()

	// OPTIONAL memory_summary (F4). Omitted unless a resolver is wired AND
	// returns a non-empty summary. Resolver errors are non-fatal.
	if s.MemorySummaries != nil {
		if summary, merr := s.MemorySummaries.ResolveMemorySummary(ctx, tenantID, inst.CompanionID, gcid); merr == nil {
			prof.MemorySummary = summary
		}
	}

	// ADR-252 / ADR-254 D11 containment status. Keyed on the SAME chat-turn
	// action code the chat handler would use, so the profile's answer and the
	// turn's refusal can never disagree. Advisory: a read error renders
	// {"paused": false} and logs loud rather than 500-ing a profile over a
	// projection wobble, and never claims a pause it could not read.
	if s.CompanionSuspension != nil {
		status := &companionStatusResp{}
		actionCode := companion.ChatTurnActionCodeForTier(resolveManaTier(s, tenantID, gcid))
		if st, serr := s.CompanionSuspension.Status(ctx, tenantID, actionCode); serr != nil {
			log.Printf("consumption: companion profile suspension advisory read FAILED tenant=%s gcid=%s companion=%s action=%s: %v (rendering not-paused; chora-model-gateway remains the control)",
				tenantID, gcid, inst.CompanionID, actionCode, serr)
		} else if st.Paused {
			status.Paused = true
			status.Reason = st.Reason
			status.Scope = string(st.Scope)
		}
		prof.CompanionStatus = status
	}
	_ = r
	return prof
}

// resolveGrowthProjection returns the growth_state + cosmetic projections for
// the profile. Pre-hatch Stage-0 fallback on nil reader or growth miss.
func (s *Server) resolveGrowthProjection(
	ctx context.Context, tenantID, gcid, companionID string,
) (*growthStateResp, *cosmeticResp) {
	preHatch := func() (*growthStateResp, *cosmeticResp) {
		return &growthStateResp{
			Stage:          0,
			StageName:      companion.StageName(0),
			ExpToNextStage: companion.ExpToNextStage(0),
		}, &cosmeticResp{}
	}
	if s.Growth == nil {
		return preHatch()
	}
	st, err := s.Growth.GetCompanionGrowth(ctx, tenantID, companionID, gcid)
	if err != nil || st == nil {
		// Growth row not provisioned yet (e.g. egg not minted) — degrade to
		// pre-hatch rather than 404 the whole profile (base instance exists).
		return preHatch()
	}
	gs := &growthStateResp{
		Stage:             st.GrowthStage,
		StageName:         st.StageName,
		Exp:               st.ExpCurrent,
		ExpToNextStage:    st.ExpNextThreshold,
		CurrentBreed:      st.Species,
		EffectiveLLMTier:  st.EffectiveLLMTier,
		ResonantAtomID:    st.ResonantAtomID,
		AhaMomentConsumed: st.AhaMomentConsumed,
		// GrowthExp surfaces the cumulative EXP (distinct from per-stage Exp).
		GrowthExp: st.ExpCumulative,
	}
	if gs.StageName == "" {
		gs.StageName = companion.StageName(gs.Stage)
	}
	if st.HatchedAt != nil {
		v := st.HatchedAt.UTC().Format("2006-01-02T15:04:05.000000Z")
		gs.BreedRevealedAt = &v
	}
	if st.LastStageUpAt != nil {
		v := st.LastStageUpAt.UTC().Format("2006-01-02T15:04:05.000000Z")
		gs.LastStageUpAt = &v
	}
	if st.AhaMomentActiveUntil != nil {
		v := st.AhaMomentActiveUntil.UTC().Format("2006-01-02T15:04:05.000000Z")
		gs.AhaMomentActiveUntil = &v
	}
	cos := &cosmeticResp{
		Shiny:  st.ShinyVariant,
		Rarity: st.Rarity,
	}
	return gs, cos
}

func (s *Server) deleteCompanionInstance(w http.ResponseWriter, r *http.Request, id string) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if err := s.CompanionInstances.SoftDelete(repoCtx(r, tenantID, gcid), id); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Compile-time assertion that the repo port type is wired (defensive).
var _ companion.InstanceRepository = (*inmem.CompanionInstanceRepo)(nil)
