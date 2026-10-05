// companion_acquire_handler.go — Companion acquisition for a MAP (WS-A3, My
// Knowledge unification, CHO-2005; owner full-consolidation sign-off 2026-07-02,
// ADR-214 D1).
//
//	POST /v1/me/companions/acquire   body: {goalId, companionName?, mode?}
//
// A map = a Goal (ADR-214 D1). Acquiring a Companion for a map mints a PERSISTED
// Instance specialising in the map's theme (its root concept's title) and
// attaches it to the map's Goal (Goal.AttachCompanion) — the Goal is the SINGLE
// source of truth for "which Companion curates this map". The theme-keyed
// CompanionMapBinding is RETIRED (owner sign-off): no parallel binding row is
// written. mode = "dev_hatched" acquires without the live Stripe ceremony (honest
// free/dev path); "hatched" (default) is the paid ceremony.
//
// Learner-scoped: the GCID is the validated session subject (headers via
// extRequireContext), NEVER a body param; the goal is re-checked against the
// caller (cross-learner → 404). Consistency: the Instance persist + the Goal
// attach are not one transaction (separate aggregate repos), so a Goal-update
// failure COMPENSATES the just-created orphan Instance (best-effort soft-delete)
// — a partial write never leaks a mapless Companion.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// companionHeroSpecies mirrors companion.CanonicalSpecies as a set for the
// acquire-door pre-validation (R3-7).
var companionHeroSpecies = func() map[string]bool {
	out := make(map[string]bool, len(companion.CanonicalSpecies))
	for _, sp := range companion.CanonicalSpecies {
		out[sp] = true
	}
	return out
}()

type acquireReq struct {
	GoalID        string `json:"goalId"`
	CompanionName string `json:"companionName,omitempty"`
	Mode          string `json:"mode,omitempty"` // "hatched" (default) | "dev_hatched"
	// Species is the optional learner pick (CHO-2013 P1, R3-7): one of the
	// 5 heroes; omitted ⇒ the 0046 random-hero default stands.
	Species string `json:"species,omitempty"`
}

type acquireResp struct {
	CompanionID string `json:"companionId"`
	Name        string `json:"name"`
	GoalID      string `json:"goalId"`
	Acquisition string `json:"acquisition"`
	// Species echoes the EFFECTIVE species (the pick, or the rolled 0046
	// default when omitted) — R3-7. GrowthStage is 1: born hatched.
	Species     string `json:"species"`
	GrowthStage int    `json:"growthStage"`
}

// handleMeCompanionAcquire — POST /v1/me/companions/acquire.
func (s *ExtServer) handleMeCompanionAcquire(w http.ResponseWriter, r *http.Request) {
	if s.CompanionInstances == nil || s.Goals == nil || s.GrowthInit == nil {
		extWriteError(w, http.StatusServiceUnavailable, "COMPANION_ACQUIRE_UNAVAILABLE", "companion acquisition not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	var req acquireReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	goalID := strings.TrimSpace(req.GoalID)
	if goalID == "" {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_ACQUIRE", "goalId is required (a Companion is acquired for a map)")
		return
	}
	mode := companion.AcquisitionMode(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = companion.AcquisitionHatched
	}
	if !mode.Valid() {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_ACQUIRE", "unknown acquisition mode")
		return
	}
	// CHO-2013 P1 (R3-7): optional learner species pick, validated against
	// the 5-hero roster BEFORE anything mints (growth re-validates).
	speciesPick := strings.TrimSpace(req.Species)
	if speciesPick != "" && !companionHeroSpecies[speciesPick] {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_ACQUIRE", "unknown species (canonical: owl|fox|dragon|phoenix|penguin)")
		return
	}

	// Load the map's Goal + guard cross-learner/tenant (never leak another's map).
	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOAL_LOOKUP_FAILED", err.Error())
		return
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "MAP_NOT_FOUND", "")
		return
	}
	// Pre-check the 1:1 invariant here so the common already-summoned case never
	// mints an orphan Instance (Goal.AttachCompanion re-checks as defence-in-depth).
	if g.AttachedCompanionID != nil {
		extWriteError(w, http.StatusConflict, "MAP_ALREADY_HAS_COMPANION", "this map already has a designated Companion (dismiss it first)")
		return
	}

	// The Instance specialises in the map's theme = its root concept's title,
	// falling back to the goal's north-star note, then a generic label (never blank).
	theme := s.resolveMapTheme(ctx, tenantID, gcid, g)
	name := strings.TrimSpace(req.CompanionName)
	if name == "" {
		name = theme
	}
	inst, err := companion.NewInstance(tenantID, gcid, name, theme)
	if err != nil {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_ACQUIRE", err.Error())
		return
	}

	// Persist the Instance (roster-cap enforced in the repo).
	if err := s.CompanionInstances.Create(ctx, inst, companion.DefaultMaxCompanionsPerUser); err != nil {
		if errors.Is(err, companion.ErrRosterCapReached) {
			extWriteError(w, http.StatusConflict, "COMPANION_CAP_REACHED", "max companions per user reached")
			return
		}
		extWriteError(w, http.StatusInternalServerError, "INSTANCE_CREATE_FAILED", err.Error())
		return
	}

	// CHO-2013 P1: births the sovereign companion HATCHED (stage 1 + optional
	// species pick) — the honest fix for the silent stage-0 egg. On failure
	// COMPENSATE the orphan Instance before surfacing the error.
	traceparent, tracestate := traceFromHeaders(r)
	born, err := s.GrowthInit.InitBornHatched(ctx, growth.InitBornHatchedInput{
		TenantID:    tenantID,
		CompanionID: inst.CompanionID,
		OwnerGCID:   gcid,
		Species:     speciesPick,
		DisplayName: inst.Name,
		Traceparent: traceparent,
		Tracestate:  tracestate,
	})
	if err != nil {
		s.compensateOrphanCompanion(ctx, inst.CompanionID, err)
		// No repeats while an unseen species remains (owner ruling 2026-08-07,
		// inverting the 2026-07-08 one-species directive): picking a companion
		// type the learner already owns is a 409, not a 500. The retired
		// SPECIES_LOCKED code meant the OPPOSITE condition - a pick that
		// DIFFERED from the locked species - so reusing it would leave every
		// client branching on a code whose meaning silently flipped.
		if errors.Is(err, growth.ErrSpeciesAlreadyOwned) {
			extWriteError(w, http.StatusConflict, "SPECIES_ALREADY_OWNED", err.Error())
			return
		}
		extWriteError(w, http.StatusInternalServerError, "BORN_HATCH_FAILED", err.Error())
		return
	}

	// Attach to the map's Goal (the source of truth) + persist. On failure
	// COMPENSATE the orphan Instance (best-effort soft-delete) so a partial write
	// never leaks a mapless Companion.
	now := time.Now().UTC()
	if err := g.AttachCompanion(inst.CompanionID, now); err != nil {
		s.compensateOrphanCompanion(ctx, inst.CompanionID, err)
		if errors.Is(err, goal.ErrAlreadyAttached) {
			extWriteError(w, http.StatusConflict, "MAP_ALREADY_HAS_COMPANION", err.Error())
			return
		}
		extWriteError(w, http.StatusInternalServerError, "GOAL_ATTACH_FAILED", err.Error())
		return
	}
	if err := s.Goals.Update(ctx, g); err != nil {
		s.compensateOrphanCompanion(ctx, inst.CompanionID, err)
		extWriteError(w, http.StatusInternalServerError, "GOAL_ATTACH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusCreated, acquireResp{
		CompanionID: inst.CompanionID,
		Name:        inst.Name,
		GoalID:      g.GoalID,
		Acquisition: string(mode),
		Species:     born.Species,
		GrowthStage: born.State.GrowthStage,
	})
}

// resolveMapTheme resolves a non-blank theme label for the map: the root
// concept's title (best), else the goal's north-star note, else a generic label.
// Concept lookup is best-effort (nil repo / missing root / read error → fallback).
func (s *ExtServer) resolveMapTheme(ctx context.Context, tenantID, gcid string, g *goal.Goal) string {
	if s.Concepts != nil && g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
		if c, err := s.Concepts.GetByID(ctx, tenantID, gcid, *g.RootConceptID); err == nil && c != nil {
			if t := strings.TrimSpace(c.Title); t != "" {
				return t
			}
		}
	}
	if n := strings.TrimSpace(g.NorthStarNote); n != "" {
		return n
	}
	return "discovery"
}

// compensateOrphanCompanion best-effort soft-deletes a just-created Instance whose
// Goal attach failed, so a partial write never leaks a mapless Companion. A
// compensation failure is logged (fail-loud) but does not mask the original error.
func (s *ExtServer) compensateOrphanCompanion(ctx context.Context, companionID string, cause error) {
	if derr := s.CompanionInstances.SoftDelete(ctx, companionID); derr != nil {
		log.Printf("consumption: acquire compensation FAILED for orphan companion_id=%s: %v (original error: %v)", companionID, derr, cause)
	}
}
