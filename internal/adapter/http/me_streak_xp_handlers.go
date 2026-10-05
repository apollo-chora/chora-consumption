// me_streak_xp_handlers.go — S5.1 Maya retention loop endpoints.
//
// Read-only learner-self endpoints surfaced to the A+ Daily Dose card
// stack + LearningPath progress sidebar. These are tablet-first reads
// (per Tier 4 D16 + chora-design-system); no write surface is exposed
// directly — the streak / XP state is mutated by:
//
//   - Server-internal SessionCompletionHandler (S4 path) on every
//     atom_session.completed event (10 XP + RecordActivity).
//   - LearningPath milestone subscriber (S5.1+) on path advance
//     (200 XP per milestone).
//   - Daily-dose handler on a successful 5-card stack served (50 XP).
//
// Auth: X-Tenant-Id + gcid headers (same as the rest of /v1/me/*). RLS
// leak guard: NO query parameter override — the response is always
// scoped to the authenticated GCID.
package http

import (
	"net/http"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// streakResp is the GET /v1/me/streak response payload.
type streakResp struct {
	LearnerGCID    string `json:"learner_gcid"`
	Count          int    `json:"count"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
}

// xpResp is the GET /v1/me/xp response payload.
type xpResp struct {
	LearnerGCID string `json:"learner_gcid"`
	XP          int    `json:"xp"`
}

func (s *Server) handleMeStreak(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	streak, serr := s.Streaks.Get(repoCtx(r, tenantID, gcid), tenantID, gcid)
	if serr != nil {
		writeError(w, http.StatusInternalServerError, "STREAK_READ_FAILED", serr.Error())
		return
	}
	resp := streakResp{
		LearnerGCID: streak.LearnerGCID,
		Count:       streak.Count,
	}
	if !streak.LastActivityAt.IsZero() {
		resp.LastActivityAt = streak.LastActivityAt.Format("2006-01-02T15:04:05Z")
	}
	// Override LearnerGCID if streak was lazily created with empty GCID
	// (defensive: NewStreak trims; surface guarantees non-empty here).
	if resp.LearnerGCID == "" {
		resp.LearnerGCID = gcid
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleMeXP(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	xp, xerr := s.XP.Get(repoCtx(r, tenantID, gcid), tenantID, gcid)
	if xerr != nil {
		writeError(w, http.StatusInternalServerError, "XP_READ_FAILED", xerr.Error())
		return
	}
	resp := xpResp{
		LearnerGCID: gcid,
		XP:          xp,
	}
	writeJSON(w, http.StatusOK, resp)
}

// Compile-time assertion: the XP cadence + activity enums are wired so
// the handler can map server-side activity-recording paths to XP awards
// without drift. (Used by SessionCompletionHandler in M12.)
var _ companion.ActivityKind = companion.ActivityAtomCompleted
