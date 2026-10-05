// companion_bindings_handler.go — the learner's per-map Companion bindings, now
// sourced from Goals (WS-A3, My Knowledge unification, CHO-2005; ADR-214 D1).
//
//	GET /v1/me/companions/bindings   list the maps that have a designated Companion
//
// A map = a Goal; the map's Companion is Goal.AttachedCompanionID (the single source
// of truth — the theme-keyed CompanionMapBinding aggregate is RETIRED per owner
// sign-off 2026-07-02). One "binding" per goal that has a Companion attached:
// bindingId = the goal/map id, mapTheme = the map's root concept title. This is a
// thin compatibility read over the map read-model (GET /v1/me/maps already carries
// attachedCompanionId); it is a candidate for removal once the FE reads the map
// list directly. Learner-scoped; empty is honest (items: []), never fabricated.
package http

import (
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
)

type companionBindingDTO struct {
	BindingID   string    `json:"bindingId"` // the map/goal id (map = Goal)
	MapTheme    string    `json:"mapTheme"`  // the map's root concept title
	CompanionID string    `json:"companionId"`
	Acquisition string    `json:"acquisition"` // retired with the binding; "" = not tracked
	CreatedAt   time.Time `json:"createdAt"`
}

// handleMeCompanionBindings — GET /v1/me/companions/bindings (Goals-sourced).
func (s *ExtServer) handleMeCompanionBindings(w http.ResponseWriter, r *http.Request) {
	if s.Goals == nil {
		extWriteError(w, http.StatusServiceUnavailable, "COMPANION_BINDINGS_UNAVAILABLE", "goal repo not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "BINDINGS_LIST_FAILED", err.Error())
		return
	}
	items := make([]companionBindingDTO, 0, len(goals))
	for _, g := range goals {
		if g == nil || g.AttachedCompanionID == nil {
			continue
		}
		items = append(items, companionBindingDTO{
			BindingID:   g.GoalID,
			MapTheme:    s.resolveMapTheme(ctx, tenantID, gcid, g),
			CompanionID: *g.AttachedCompanionID,
			CreatedAt:   g.CreatedAt,
		})
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
