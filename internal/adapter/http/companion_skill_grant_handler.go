// companion_skill_grant_handler.go — CHO-2012 loadout endpoints (ADR-218
// D3: unlocked = owned forever; the slot cap binds only the EQUIPPED set;
// equip/unequip is free).
//
//	GET    /v1/me/companions/{id}/skills               loadout view
//	POST   /v1/me/companions/{id}/skills               grant mint (owned, unequipped, via=admin_grant)
//	PUT    /v1/me/companions/{id}/skills/{key}/equip   equip (slot-cost capped → 409 SKILL_SLOTS_FULL)
//	DELETE /v1/me/companions/{id}/skills/{key}/equip   unequip (free swap)
//
// The slot cap derives from the companion's growth stage
// (growth.SlotsForStage — ADR-218 D2); the pre-P0 grant-time cap + the
// per-user-XP tier axis are retired (D8). Species-Path minting happens in
// the growth award path (PathUnlocker) — POST here is the admin/dev mint
// (unlocked_via='admin_grant', same idempotent bridge write).
//
// Events (envelope-compliant): skill_granted on every REAL mint;
// loadout_changed on every equip/unequip. Auth + RLS: X-Tenant-Id + gcid
// headers; repoCtx stamps the ctx for the pg adapters.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// grantSkillReq is the inbound JSON body for POST .../skills.
type grantSkillReq struct {
	SkillKey string `json:"skill_key"`
}

// loadoutGrantResp is one detailed grant row in the loadout view.
// catalogue_active (CHO-2030) carries the ADR-174 release state so the FE
// renders owned-but-dark grants as named-tease dormants (R3-9) instead of
// badging skill_kind; equip/invoke stay server-gated regardless.
type loadoutGrantResp struct {
	SkillKey        string `json:"skill_key"`
	SkillKind       string `json:"skill_kind"`
	SlotCost        int    `json:"slot_cost"`
	Equipped        bool   `json:"equipped"`
	UnlockedVia     string `json:"unlocked_via"`
	UnlockedAtStage int    `json:"unlocked_at_stage"`
	CatalogueActive bool   `json:"catalogue_active"`
	// params_schema (CHO-2362) drives the Grimoire param editor. Rendered from
	// the compiled domain sheet table — NOT the catalogue row — so the editor
	// and the validator can never disagree (deploy-order-safe: live before the
	// 0106 seed applies). Omitted for skills with no editable params.
	ParamsSchema json.RawMessage `json:"params_schema,omitempty"`

	// N2 card fields (tracker row D1). Every one is a projection of a column
	// that already exists on companion.CatalogEntry; none is invented here.
	// They are what the editor's step card shows: what the Skill IS (name,
	// family), what it may do (policy class, tools), where its output lands
	// (sink) and what it costs.
	Name            string   `json:"name,omitempty"`
	Family          string   `json:"family,omitempty"`
	PolicyClass     string   `json:"policy_class,omitempty"`
	OutputSink      string   `json:"output_sink,omitempty"`
	ToolHandlerRefs []string `json:"tool_handler_refs,omitempty"`

	// PriceUnits is RESOLVED from the canonical cost map, because the catalogue
	// carries a price KEY and the card needs a number.
	//
	// A POINTER on purpose: nil means "this deployment cannot price it" (no key,
	// or a key absent from the cost map) and 0 means "genuinely free". Collapsing
	// those two would render an unknown price as free, which understates what a
	// learner is about to spend, and the composer's own price line is built by
	// summing these.
	PriceUnits *int64 `json:"price_units,omitempty"`
}

// resolveGrantPrice turns a catalogue price KEY into the number the card shows.
// Returns nil rather than 0 for an unpriceable Skill: "free" and "unknown" are
// different facts and the card must be able to say so.
func resolveGrantPrice(entry companion.CatalogEntry) *int64 {
	if strings.TrimSpace(entry.PriceKey) == "" {
		return nil
	}
	cost, err := companion.LookupCost(entry.PriceKey)
	if err != nil {
		return nil
	}
	return &cost
}

// loadoutResp is the wire shape for every loadout endpoint. skill_grants
// (owned keys) + skill_slots_unlocked + evolution_tier keep the pre-P0
// field names for compatibility; grants/equipped_skills/slots_used carry
// the CHO-2012 detail.
type loadoutResp struct {
	CompanionID        string             `json:"companion_id"`
	SkillGrants        []string           `json:"skill_grants"`
	EquippedSkills     []string           `json:"equipped_skills"`
	Grants             []loadoutGrantResp `json:"grants"`
	SkillSlotsUnlocked int                `json:"skill_slots_unlocked"`
	SlotsUsed          int                `json:"slots_used"`
	EvolutionTier      string             `json:"evolution_tier"`
	GrowthStage        int                `json:"growth_stage"`
}

func toLoadoutResp(companionID string, stage int, grants []companion.SkillGrant, catalogue map[string]companion.CatalogEntry) loadoutResp {
	l := companion.Loadout{Grants: grants}
	owned := make([]string, 0, len(grants))
	detail := make([]loadoutGrantResp, 0, len(grants))
	for _, g := range grants {
		owned = append(owned, g.SkillKey)
		detail = append(detail, loadoutGrantResp{
			SkillKey:        g.SkillKey,
			SkillKind:       string(g.SkillKind),
			SlotCost:        g.SlotCost,
			Equipped:        g.Equipped,
			UnlockedVia:     g.UnlockedVia,
			UnlockedAtStage: g.UnlockedAtStage,
			CatalogueActive: catalogue[g.SkillKey].Active,
			ParamsSchema:    companion.SkillParamsSchemaJSON(g.SkillKey),
			// N2: an absent catalogue row leaves every card field zero rather
			// than fabricating one. A retired-but-owned Skill reads as unknown.
			Name:            catalogue[g.SkillKey].Name,
			Family:          catalogue[g.SkillKey].Family,
			PolicyClass:     catalogue[g.SkillKey].PolicyClass,
			OutputSink:      catalogue[g.SkillKey].OutputSink,
			ToolHandlerRefs: catalogue[g.SkillKey].ToolHandlerRefs,
			PriceUnits:      resolveGrantPrice(catalogue[g.SkillKey]),
		})
	}
	sort.Strings(owned)
	sort.Slice(detail, func(i, j int) bool { return detail[i].SkillKey < detail[j].SkillKey })
	equipped := l.EquippedActiveSkillKeys()
	if equipped == nil {
		equipped = []string{}
	}
	return loadoutResp{
		CompanionID:        companionID,
		SkillGrants:        owned,
		EquippedSkills:     equipped,
		Grants:             detail,
		SkillSlotsUnlocked: growth.SlotsForStage(stage),
		SlotsUsed:          l.EquippedSlotCost(),
		EvolutionTier:      growth.TierForStage(stage),
		GrowthStage:        stage,
	}
}

// handleCompanionSkillsRoute dispatches the skills subtree:
// "skills" (GET list / POST mint) and "skills/{key}/equip" (PUT/DELETE).
func (s *Server) handleCompanionSkillsRoute(w http.ResponseWriter, r *http.Request, companionID, sub string) {
	if sub == "skills" {
		switch r.Method {
		case http.MethodGet:
			s.listCompanionSkills(w, r, companionID)
		case http.MethodPost:
			s.grantCompanionSkill(w, r, companionID)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		}
		return
	}
	parts := strings.Split(sub, "/")
	if len(parts) == 3 && parts[0] == "skills" && parts[2] == "equip" && parts[1] != "" {
		switch r.Method {
		case http.MethodPut:
			s.setCompanionSkillEquipped(w, r, companionID, parts[1], true)
		case http.MethodDelete:
			s.setCompanionSkillEquipped(w, r, companionID, parts[1], false)
		default:
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		}
		return
	}
	// CHO-2013 P1.B (R4-4) — the single-step Skill invoke runner.
	if len(parts) == 3 && parts[0] == "skills" && parts[2] == "invoke" && parts[1] != "" {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only")
			return
		}
		s.invokeCompanionSkill(w, r, companionID, parts[1])
		return
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "")
}

// loadoutDeps guards the CHO-2012 wiring — fail loud, never stub.
func (s *Server) loadoutDeps(w http.ResponseWriter) bool {
	if s.Loadouts == nil || s.SkillCatalog == nil || s.LoadoutEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "LOADOUT_NOT_WIRED",
			"loadout repository / skill catalogue / loadout events not wired")
		return false
	}
	return true
}

func (s *Server) listCompanionSkills(w http.ResponseWriter, r *http.Request, companionID string) {
	if !s.loadoutDeps(w) {
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := repoCtx(r, tenantID, gcid)
	if _, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID); err != nil {
		s.writeSkillError(w, err)
		return
	}
	stage, err := s.loadGrowthStage(r, tenantID, companionID, gcid)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	grants, err := s.Loadouts.ListGrants(ctx, tenantID, companionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LOADOUT_READ_FAILED", err.Error())
		return
	}
	catalogue, err := s.SkillCatalog.ListCatalogue(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CATALOGUE_READ_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toLoadoutResp(companionID, stage, grants, catalogue))
}

func (s *Server) grantCompanionSkill(w http.ResponseWriter, r *http.Request, companionID string) {
	if !s.loadoutDeps(w) {
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req grantSkillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	skillKey := strings.TrimSpace(req.SkillKey)
	if skillKey == "" {
		writeError(w, http.StatusBadRequest, "INVALID_SKILL_GRANT", "skill_key required")
		return
	}

	ctx := repoCtx(r, tenantID, gcid)
	inst, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	catalogue, err := s.SkillCatalog.ListCatalogue(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CATALOGUE_READ_FAILED", err.Error())
		return
	}
	entry, ok := catalogue[skillKey]
	if !ok {
		writeError(w, http.StatusBadRequest, "SKILL_NOT_IN_CATALOGUE",
			"skill_key not in the platform catalogue")
		return
	}
	stage, err := s.loadGrowthStage(r, tenantID, companionID, gcid)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}

	inserted, err := s.Loadouts.MintGrants(ctx, tenantID, companionID, []companion.GrantMint{{
		SkillKey: skillKey,
		// Craft Skills are always in effect from mint (owned = active).
		Equipped:        entry.SkillKind == companion.SkillKindCraft,
		UnlockedVia:     "admin_grant",
		UnlockedAtStage: stage,
	}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SKILL_GRANT_PERSIST_FAILED", err.Error())
		return
	}

	status := http.StatusOK // idempotent re-mint
	if len(inserted) > 0 {
		status = http.StatusCreated
		if perr := s.publishSkillGranted(r, tenantID, inst.OwnerGCID, companionID, entry, stage); perr != nil {
			writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", perr.Error())
			return
		}
	}
	grants, err := s.Loadouts.ListGrants(ctx, tenantID, companionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LOADOUT_READ_FAILED", err.Error())
		return
	}
	writeJSON(w, status, toLoadoutResp(companionID, stage, grants, catalogue))
}

func (s *Server) setCompanionSkillEquipped(w http.ResponseWriter, r *http.Request, companionID, skillKey string, equip bool) {
	if !s.loadoutDeps(w) {
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := repoCtx(r, tenantID, gcid)
	inst, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	stage, err := s.loadGrowthStage(r, tenantID, companionID, gcid)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	grants, err := s.Loadouts.ListGrants(ctx, tenantID, companionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LOADOUT_READ_FAILED", err.Error())
		return
	}

	catalogue, cerr := s.SkillCatalog.ListCatalogue(ctx)
	if cerr != nil {
		writeError(w, http.StatusInternalServerError, "CATALOGUE_READ_FAILED", cerr.Error())
		return
	}
	// Equip requires the catalogue row to be RELEASED (active=true —
	// ADR-174 eval gate). Ownership may accrue dark; effect may not.
	if equip {
		if entry, ok := catalogue[skillKey]; ok && !entry.Active {
			writeError(w, http.StatusConflict, "SKILL_NOT_ACTIVE",
				"skill is not released yet (ADR-174 eval gate)")
			return
		}
	}

	loadout := companion.Loadout{Grants: grants}
	if equip {
		err = loadout.Equip(skillKey, growth.SlotsForStage(stage))
	} else {
		err = loadout.Unequip(skillKey)
	}
	switch {
	case err == nil:
	case errors.Is(err, companion.ErrSkillSlotsFull):
		writeError(w, http.StatusConflict, "SKILL_SLOTS_FULL",
			"skill slots full — stage up or unequip another skill")
		return
	case errors.Is(err, companion.ErrSkillNotOwned):
		writeError(w, http.StatusConflict, "SKILL_NOT_OWNED",
			"skill not unlocked on this companion yet")
		return
	case errors.Is(err, companion.ErrCraftSkillAlwaysOn):
		writeError(w, http.StatusConflict, "CRAFT_SKILL_ALWAYS_ON",
			"craft skills are always in effect")
		return
	default:
		writeError(w, http.StatusBadRequest, "INVALID_LOADOUT_CHANGE", err.Error())
		return
	}

	if err := s.Loadouts.SetEquipped(ctx, tenantID, companionID, skillKey, loadout.IsEquipped(skillKey)); err != nil {
		if errors.Is(err, companion.ErrSkillNotOwned) {
			writeError(w, http.StatusConflict, "SKILL_NOT_OWNED", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "LOADOUT_PERSIST_FAILED", err.Error())
		return
	}

	action := "equip"
	if !equip {
		action = "unequip"
	}
	if perr := s.publishLoadoutChanged(r, tenantID, inst.OwnerGCID, companionID, skillKey, action, &loadout, stage); perr != nil {
		writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", perr.Error())
		return
	}
	writeJSON(w, http.StatusOK, toLoadoutResp(companionID, stage, loadout.Grants, catalogue))
}

// loadGrowthStage reads the companion's growth stage for slot derivation.
// Falls back to stage 0 ONLY when the growth axis is genuinely absent for
// the row (pre-0032 legacy) — a wired GrowthServer error propagates.
func (s *Server) loadGrowthStage(r *http.Request, tenantID, companionID, gcid string) (int, error) {
	if s.Growth == nil {
		// Growth axis not wired (unit-test servers) — stage-0 derivation.
		return 0, nil
	}
	state, err := s.Growth.GetCompanionGrowth(rlsCtx(r, tenantID, gcid), tenantID, companionID, gcid)
	if err != nil {
		if errors.Is(err, growth.ErrCompanionNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return state.GrowthStage, nil
}

func (s *Server) publishSkillGranted(r *http.Request, tenantID, ownerGCID, companionID string, entry companion.CatalogEntry, stage int) error {
	traceparent, tracestate := traceFromHeaders(r)
	now := time.Now().UTC()
	return s.LoadoutEvents.PublishLoadoutEvent(r.Context(), events.TopicCompanionSkillGranted, map[string]any{
		"companion_id":              companionID,
		"owner_gcid":                ownerGCID,
		"skill_key":                 entry.SkillKey,
		"skill_kind":                string(entry.SkillKind),
		"unlocked_via":              "admin_grant",
		"unlocked_at_stage":         stage,
		"equipped":                  entry.SkillKind == companion.SkillKindCraft,
		"granted_at":                now,
		"chora_companion_id":        companionID,
		"chora_companion_skill_key": entry.SkillKey,
	}, companion.LoadoutEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: "skill_granted:" + companionID + ":" + entry.SkillKey,
		TenantID:       tenantID,
		GCID:           ownerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	})
}

func (s *Server) publishLoadoutChanged(r *http.Request, tenantID, ownerGCID, companionID, skillKey, action string, loadout *companion.Loadout, stage int) error {
	traceparent, tracestate := traceFromHeaders(r)
	now := time.Now().UTC()
	return s.LoadoutEvents.PublishLoadoutEvent(r.Context(), events.TopicCompanionLoadoutChanged, map[string]any{
		"companion_id":              companionID,
		"owner_gcid":                ownerGCID,
		"skill_key":                 skillKey,
		"action":                    action,
		"equipped_skills":           loadout.EquippedActiveSkillKeys(),
		"slots_used":                loadout.EquippedSlotCost(),
		"skill_slots_unlocked":      growth.SlotsForStage(stage),
		"changed_at":                now,
		"chora_companion_id":        companionID,
		"chora_companion_skill_key": skillKey,
	}, companion.LoadoutEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: "loadout:" + companionID + ":" + skillKey + ":" + action + ":" + now.Format(time.RFC3339Nano),
		TenantID:       tenantID,
		GCID:           ownerGCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	})
}

// loadOwnedInstance fetches a Companion and enforces the (tenant, owner)
// ownership guard. Returns companion.ErrInstanceNotFound when the Companion is
// missing OR owned by another caller/tenant (no cross-owner disclosure).
func (s *Server) loadOwnedInstance(ctx context.Context, tenantID, gcid, companionID string) (*companion.Instance, error) {
	inst, err := s.CompanionInstances.Get(ctx, companionID)
	if err != nil {
		return nil, err
	}
	if inst.OwnerGCID != gcid || inst.TenantID != tenantID {
		return nil, companion.ErrInstanceNotFound
	}
	return inst, nil
}

// writeSkillError maps a repo/domain error to the HTTP envelope.
func (s *Server) writeSkillError(w http.ResponseWriter, err error) {
	if errors.Is(err, companion.ErrInstanceNotFound) {
		writeError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
		return
	}
	writeError(w, http.StatusInternalServerError, "SKILL_OP_FAILED", err.Error())
}
