// practice_budget_handler.go: GET /v1/me/practice-budget
// (UX refactor Phase B, package B6 item 3).
//
// Why this exists. The daily bound is already enforced in the domain
// (campaignquestion.DailyCapFor and CanRequestToday) and was completely
// invisible to the UI. So when a learner spent their last practice tap the
// button simply stopped working: no count, no reason, and nothing to
// distinguish "you are done for today" from "this is broken". A learner cannot
// tell those apart, and one of them becomes a bug report.
//
// The read is a PROJECTION over the same functions the enforcement path calls,
// never a parallel calculation. A budget computed twice drifts, and a card that
// promises a tap the next request refuses is worse than no card.
package http

import (
	"context"
	"net/http"
	"time"

	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

// PracticeBudgetCounter is the narrow READ port: the same per-origin daily
// count the enforcement path already asks for.
//
// Deliberately narrower than campaignquestion.Repository, matching the choice
// made for the B6 item 1 and item 2 ports: the full repository has other
// implementations and fakes, and none of them needs to grow a method for a
// read that only counts.
type PracticeBudgetCounter interface {
	CountRequestedOn(ctx context.Context, tenantID, learnerGCID string, origin cq.RequestOrigin, day time.Time) (int, error)
}

type practiceBudgetResp struct {
	Origin    string `json:"origin"`
	Cap       int    `json:"cap"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	Exhausted bool   `json:"exhausted"`
}

type practiceBudgetsResp struct {
	// ResetsAt is the next UTC midnight. Without it an exhausted lane says
	// "none left" and gives the learner nothing to wait for.
	ResetsAt string               `json:"resets_at"`
	Budgets  []practiceBudgetResp `json:"budgets"`
}

// budgetOrigins is the full set reported, in a stable order. BOTH are
// returned even though only the tap budget drives the practice lane: the two
// draw on independent allowances, and a client that could see only one would
// have no way to explain why a dose arrived on a day the taps ran out.
var budgetOrigins = []cq.RequestOrigin{cq.OriginMarch, cq.OriginTap}

// handleMePracticeBudget serves GET /v1/me/practice-budget.
func (s *ExtServer) handleMePracticeBudget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET only")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.PracticeBudget == nil {
		extWriteError(w, http.StatusServiceUnavailable, "PRACTICE_BUDGET_NOT_WIRED", "")
		return
	}

	// The cap is a per-UTC-day bound, so the day passed to the counter must be
	// a UTC instant. A local-zone day would reset the budget at the wrong hour,
	// in one direction or the other depending on where the pod runs.
	now := time.Now().UTC()
	ctx := tracingWithIdentity(r, tenantID, gcid)

	out := make([]practiceBudgetResp, 0, len(budgetOrigins))
	for _, origin := range budgetOrigins {
		used, cerr := s.PracticeBudget.CountRequestedOn(ctx, tenantID, gcid, origin, now)
		if cerr != nil {
			// Never fall back to a full budget: that would invite a tap the
			// enforcement path then refuses, which is the exact broken-button
			// experience this endpoint exists to remove.
			extWriteError(w, http.StatusInternalServerError, "PRACTICE_BUDGET_READ_FAILED", cerr.Error())
			return
		}
		b := cq.BudgetFor(origin, used)
		out = append(out, practiceBudgetResp{
			Origin:    string(b.Origin),
			Cap:       b.Cap,
			Used:      b.Used,
			Remaining: b.Remaining,
			Exhausted: b.Exhausted,
		})
	}

	extWriteJSON(w, http.StatusOK, practiceBudgetsResp{
		ResetsAt: nextUTCMidnight(now).Format(time.RFC3339),
		Budgets:  out,
	})
}

// nextUTCMidnight is when the per-UTC-day counters roll over.
func nextUTCMidnight(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
}
