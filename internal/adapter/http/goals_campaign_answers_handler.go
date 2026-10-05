// goals_campaign_answers_handler.go — WS-C7 (CHO-2086, ADR-227 D13 addendum
// #3) the campaign answers door:
//
//	POST /v1/me/goals/{id}/campaign/questions/answer
//
// The hex-tap practice lane submits ONE MCQ answer for a node. Grading is
// SERVER-SIDE ONLY (never trust a client verdict): the stored question set for
// (concept, rung) is graded by option IDENTITY against its verbatim
// BatchCandidatePayload answer key (campaignquestion.GradeAnswer, CHO-1627
// posture), then the graded outcome folds into the campaign ladder through the
// WS-C1 Grader (ladder → retention review → verified events). The response
// mirrors the fold outcome + the honest post-fold ladder snapshot so the FE
// paints the verdict banners without a second round-trip.
//
// No idempotency store: the fold is bounded by the rung-clear threshold + the
// D7 one-advance-per-day pacing, and the FE disables submit while a POST is in
// flight — a double-submit re-warms retention + bumps the counter at most to
// the threshold, never double-advances the ladder (the D7 gate holds the clear
// to the next calendar day). Nested under the already-proxied /v1/me/goals/
// subtree so the gateway prefix proxy + Istio authz wildcard admit it.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
)

// campaignAnswerReq is the wire shape of the answers door (snake_case — the FE
// campaign practice-lane contract).
type campaignAnswerReq struct {
	ConceptID        string `json:"concept_id"`
	Rung             int    `json:"rung"`
	QuestionIndex    int    `json:"question_index"`
	SelectedOptionID string `json:"selected_option_id"`
}

// campaignAnswerResp reports the graded verdict + the ladder fold outcome + the
// honest post-fold snapshot (rungs_cleared / current_rung_correct re-read after
// the fold) + the resolved needed_correct threshold + the node's retention.
type campaignAnswerResp struct {
	Correct            bool    `json:"correct"`
	Rung               int     `json:"rung"`
	IsRefresher        bool    `json:"is_refresher"`
	Counted            bool    `json:"counted"`
	ClearedRung        int     `json:"cleared_rung"`
	Won                bool    `json:"won"`
	PacedToday         bool    `json:"paced_today"`
	RungsCleared       int     `json:"rungs_cleared"`
	CurrentRungCorrect int     `json:"current_rung_correct"`
	NeededCorrect      int     `json:"needed_correct"`
	RetentionR         float64 `json:"retention_r"`
	// Post-grade reveal (Slice F): the serve door strips the answer key +
	// explainer pre-submit, so the FE sources them here AFTER the learner
	// commits — server-authoritative, never pre-fetched.
	CorrectOptionID string `json:"correct_option_id,omitempty"`
	Explainer       string `json:"explainer,omitempty"`
}

// handleMeGoalCampaignAnswer — POST /v1/me/goals/{id}/campaign/questions/answer.
func (s *ExtServer) handleMeGoalCampaignAnswer(w http.ResponseWriter, r *http.Request, goalID string) {
	// Fail-loud wiring gate: the fold needs the goal repo, the question bank,
	// the dose grader + ladder, and the graph (for subtree validation).
	if s.Goals == nil || s.CampaignQuestionBank == nil || s.CampaignDose == nil ||
		s.CampaignDose.Grader == nil || s.CampaignDose.Progress == nil ||
		s.Concepts == nil || s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "campaign answers lane not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	var req campaignAnswerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	conceptID := strings.TrimSpace(req.ConceptID)
	if conceptID == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONCEPT_ID", "concept_id required")
		return
	}
	if req.Rung < 1 || req.Rung > campaign.TotalRungs {
		extWriteError(w, http.StatusBadRequest, "INVALID_RUNG", "rung must be an integer in 1..6")
		return
	}
	if strings.TrimSpace(req.SelectedOptionID) == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_OPTION", "selected_option_id required")
		return
	}

	ctx, tenantID, gcid, g, ok := s.loadCampaignGoal(w, r, goalID)
	if !ok {
		return
	}
	// Subtree validation (addendum #3): the node must live inside this goal's
	// campaign scope. Also derives the node's concept_key (the retention key
	// vocabulary — never the live title).
	node, ok := s.resolveCampaignSubtreeNode(ctx, w, tenantID, gcid, g, conceptID)
	if !ok {
		return
	}

	// Load the (concept, rung) question set — it must carry a servable payload
	// (generated once, retrieved forever). A miss / in-flight / failed set has
	// no answer key to grade against.
	set, err := s.CampaignQuestionBank.GetByConceptRung(ctx, tenantID, gcid, conceptID, req.Rung)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "BANK_READ_FAILED", err.Error())
		return
	}
	if set == nil || !set.CanServe() {
		extWriteError(w, http.StatusConflict, "SET_NOT_SERVABLE", "no servable question set for this node and rung")
		return
	}

	// Server-side identity grading against the stored answer key.
	graded, err := campaignquestion.GradeAnswer(set.QuestionsPayload, req.QuestionIndex, req.SelectedOptionID)
	if err != nil {
		if errors.Is(err, campaignquestion.ErrInvalid) {
			extWriteError(w, http.StatusUnprocessableEntity, "INVALID_ANSWER", err.Error())
			return
		}
		// ErrUngradable — a served set with no usable answer key: fail loud,
		// never client-grade, never fold a fabricated verdict.
		extWriteError(w, http.StatusUnprocessableEntity, "QUESTION_NOT_GRADEABLE", err.Error())
		return
	}

	// Fold the graded answer into the ladder (persist → retention → events).
	now := time.Now().UTC()
	res, err := s.CampaignDose.Grader.RecordAnswer(ctx, campaign.RecordAnswerInput{
		TenantID: tenantID, LearnerGCID: gcid, GoalID: g.GoalID,
		ConceptID: conceptID, ConceptKey: node.ConceptKey,
		Rung: campaign.Rung(req.Rung), Correct: graded.Correct, Now: now,
	})
	if err != nil {
		switch {
		case errors.Is(err, campaign.ErrAlreadyWon):
			extWriteError(w, http.StatusConflict, "NODE_WON", "this node is already won — it has left the campaign")
		case errors.Is(err, campaign.ErrRungNotUnlocked):
			extWriteError(w, http.StatusConflict, "RUNG_NOT_UNLOCKED", err.Error())
		case errors.Is(err, campaign.ErrInvalid):
			extWriteError(w, http.StatusUnprocessableEntity, "INVALID_FOLD", err.Error())
		default:
			extWriteError(w, http.StatusInternalServerError, "FOLD_FAILED", err.Error())
		}
		return
	}

	// Re-read the ladder AFTER the fold for the honest snapshot — the same
	// repo the grader just folded into (CampaignDose.Progress == the dose
	// grader's Progress by construction). A read hiccup here leaves the
	// snapshot at zero rather than failing the (already-persisted) fold.
	rungsCleared, currentRungCorrect := 0, 0
	if p, perr := s.CampaignDose.Progress.GetByConcept(ctx, tenantID, gcid, conceptID); perr == nil && p != nil {
		rungsCleared = p.RungsCleared
		currentRungCorrect = p.CurrentRungCorrect
	}

	out := res.Outcome
	extWriteJSON(w, http.StatusOK, campaignAnswerResp{
		Correct:            graded.Correct,
		Rung:               req.Rung,
		IsRefresher:        out.IsRefresher,
		Counted:            out.Counted,
		ClearedRung:        int(out.ClearedRung),
		Won:                out.Won,
		PacedToday:         out.PacedToday,
		RungsCleared:       rungsCleared,
		CurrentRungCorrect: currentRungCorrect,
		NeededCorrect:      s.CampaignDose.Grader.RungClearCorrect(),
		RetentionR:         res.RetentionR,
		CorrectOptionID:    graded.CorrectOptionID,
		Explainer:          graded.Explainer,
	})
}
