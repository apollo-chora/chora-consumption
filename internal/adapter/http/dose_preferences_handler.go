// dose_preferences_handler.go — CHO-2045 / ADR-224 #3: the learner's per-KG
// (per-map) daily-dose include/exclude preferences.
//
// The store is SPARSE — the default is that every map feeds the dose, so only
// EXCLUSIONS are persisted. GET therefore returns just the excluded map ids; the
// A+ roster UI composes that with the (searchable/paginated) maps list to render
// the full include/exclude switch per map. PUT upserts one map's state.
package http

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// handleDosePreferences serves GET (excluded set) + PUT (set one map) for the
// learner's dose preferences. Tenant+gcid scoped via repoCtx (RLS). A nil
// DoseKGPrefs ⇒ 503 (fail-loud — never a silent no-op that drops a learner's
// exclusion on the floor).
func (s *Server) handleDosePreferences(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.DoseKGPrefs == nil {
		writeError(w, http.StatusServiceUnavailable, "DOSE_PREFS_NOT_WIRED", "dose preferences are not available")
		return
	}
	ctx := repoCtx(r, tenantID, gcid)

	switch r.Method {
	case http.MethodGet:
		excluded, err := s.DoseKGPrefs.ExcludedMapIDs(ctx, tenantID, gcid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DOSE_PREFS_READ_FAILED", err.Error())
			return
		}
		ids := make([]string, 0, len(excluded))
		for id := range excluded {
			ids = append(ids, id)
		}
		sort.Strings(ids) // deterministic response
		writeJSON(w, http.StatusOK, map[string]any{"excludedMapIds": ids})

	case http.MethodPut:
		var body struct {
			MapID    string `json:"mapId"`
			Included bool   `json:"included"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
			return
		}
		body.MapID = strings.TrimSpace(body.MapID)
		if body.MapID == "" {
			writeError(w, http.StatusBadRequest, "MAP_ID_REQUIRED", "mapId is required")
			return
		}
		// Ownership gate: only store a preference for a map the learner actually
		// owns (RLS-scoped read). A stale/foreign id is a 404, never a dangling row.
		if s.Goals != nil {
			g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, body.MapID)
			if gerr != nil {
				writeError(w, http.StatusInternalServerError, "GOAL_READ_FAILED", gerr.Error())
				return
			}
			if g == nil {
				writeError(w, http.StatusNotFound, "MAP_NOT_FOUND", "no such map for this learner")
				return
			}
		}
		now := time.Now().UTC()
		pref, err := s.DoseKGPrefs.Set(ctx, tenantID, gcid, body.MapID, body.Included, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DOSE_PREFS_WRITE_FAILED", err.Error())
			return
		}

		// WS1 preferences leg (ADR-200 / CHO-2049): publish the verified
		// preference change so the LearnerProfile projection mints a
		// FactPreference (the Companion can then coach with what the learner chose
		// to focus on / mute). Key = the stable dose.map.<map_id> (Q2); value =
		// included|excluded — a re-include is an "included" value, never a delete
		// (Q3). Fail-loud: a publish failure surfaces (the state write already
		// committed; the outbox/redelivery reconciles at integration).
		value := "excluded"
		if pref.Included {
			value = "included"
		}
		tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
		env := events.NewEnvelope(tenantID, gcid, tp, r.Header.Get("tracestate"), "preferences.updated."+pref.MapID)
		if perr := s.Publisher.Publish(events.TopicPreferencesUpdated, env, map[string]any{
			"tenant_id":    tenantID,
			"learner_gcid": gcid,
			"occurred_at":  now.Format(time.RFC3339Nano),
			"preferences": []map[string]any{
				{"key": "dose.map." + pref.MapID, "value": value},
			},
		}); perr != nil {
			writeError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", perr.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"mapId": pref.MapID, "included": pref.Included})

	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}
