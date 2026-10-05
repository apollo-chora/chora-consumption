// goals_campaign_questions_handler.go — ADR-227 D13 (WS-C3, CHO-2082) the
// campaign question-serve door:
//
//	GET /v1/me/goals/{id}/campaign/questions[?rung=N]
//
// "Generate once, retrieve forever" (D13) — the reuse READ side (AC3). Given a
// goal with an assigned campaign focus, this resolves the focus node, picks
// the target rung (an explicit ?rung=1..6, else the deterministic grader
// serve decision — ADR-202 keeps the scheduler LLM-free), and returns the
// persisted question set for that (concept, rung) VERBATIM at zero LLM cost.
// A same-day in-flight set returns a poll status the FE re-polls on. A
// resolved GAP (no set / failed / stale-REQUESTED) fires the shared
// daily-capped gap trigger (CHO-2087 walk-found) — a practice tap on a
// non-focus node must not wait for that node to be re-elected as focus —
// then reports the live status.
//
// Nested under the already-proxied /v1/me/goals/ subtree (dispatched by
// handleMeGoalByID) so the gateway prefix proxy + Istio authz wildcard admit
// it without new plumbing. Reads run under the same tenant/gcid request ctx
// the focus/seal doors use (loadCampaignGoal), so the RLS-bound repos see the
// learner identity.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// campaignQuestionsResp is the wire shape of the question-serve door
// (snake_case — the FE campaign practice-lane contract). A servable set
// carries the payload verbatim + source; a miss / in-flight / failed set
// carries only the poll status (+ failure_reason when failed).
type campaignQuestionsResp struct {
	Status        string          `json:"status"`
	ConceptID     string          `json:"concept_id"`
	ConceptKey    string          `json:"concept_key"`
	Rung          int             `json:"rung"`
	Questions     json.RawMessage `json:"questions,omitempty"`
	Source        string          `json:"source,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
}

// handleMeGoalCampaignQuestions — GET /v1/me/goals/{id}/campaign/questions.
func (s *ExtServer) handleMeGoalCampaignQuestions(w http.ResponseWriter, r *http.Request, goalID string) {
	// Fail-loud wiring gate: the reuse read needs the goal repo, the question
	// bank, and the campaign dose's graph + grader.
	if s.Goals == nil || s.CampaignQuestionBank == nil || s.CampaignDose == nil ||
		s.CampaignDose.Concepts == nil || s.CampaignDose.Grader == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "campaign question lane not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	// ctx (tenant + gcid for RLS) + goal load + cross-learner leak guard —
	// the exact preamble the focus/seal doors share.
	ctx, tenantID, gcid, g, ok := s.loadCampaignGoal(w, r, goalID)
	if !ok {
		return
	}

	var node *conceptgraph.ConceptNode
	if raw := strings.TrimSpace(r.URL.Query().Get("concept_id")); raw != "" {
		// WS-C7 addendum #3 (hex-tap lane): a node-scoped serve. The node must
		// be a live node in the goal subtree (validated against the SAME
		// subtree the focus door uses); focus is NOT required here — the
		// param IS the target, so NO_FOCUS does not apply. Needs the graph
		// repos the subtree walk reads.
		if s.Concepts == nil || s.ConceptEdges == nil {
			extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "campaign subtree not wired")
			return
		}
		n, ok := s.resolveCampaignSubtreeNode(ctx, w, tenantID, gcid, g, raw)
		if !ok {
			return
		}
		node = n
	} else {
		// D11 param-less default (byte-identical to WS-C3): serve the assigned
		// campaign focus node.
		if g.FocusConceptID == nil || strings.TrimSpace(*g.FocusConceptID) == "" {
			extWriteError(w, http.StatusConflict, "NO_FOCUS", "assign a campaign focus first")
			return
		}
		n, err := s.CampaignDose.Concepts.GetByID(ctx, tenantID, gcid, *g.FocusConceptID)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "CONCEPT_LOAD_FAILED", err.Error())
			return
		}
		if n == nil {
			extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", "focus node not in the live graph")
			return
		}
		node = n
	}

	rung, ok := s.resolveCampaignServeRung(w, r, ctx, tenantID, gcid, node)
	if !ok {
		return
	}

	set, err := s.CampaignQuestionBank.GetByConceptRung(ctx, tenantID, gcid, node.ConceptID, rung)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "BANK_READ_FAILED", err.Error())
		return
	}
	// CHO-2087 walk-found: a resolved GAP (no set / failed / stale-REQUESTED
	// whose terminal was lost on an earlier day) fires the SAME daily-capped
	// generation the dose feeder uses — an explicit practice tap must not
	// stay bricked waiting for its node to be re-elected as focus. Ready and
	// same-day in-flight sets pass through untouched (the trigger guards);
	// at-cap the gap stays an honest miss.
	outcome := genNoRequest
	if set == nil || !set.CanServe() {
		traceparent, tracestate := extTraceFromHeaders(r)
		// An explicit hex tap draws on the OriginTap daily budget, SEPARATE from
		// the automatic dose-march, so a learner can practise a specific hex
		// even on a day the march already spent its budget (CHO tap-split).
		set, outcome = s.CampaignDose.maybeRequestCampaignGen(ctx, node, rung, campaign.Rung(rung).Label(),
			tenantID, gcid, time.Now().UTC(), traceparent, tracestate, set, derefString(g.RootConceptID), campaignquestion.OriginTap)
	}
	resp := campaignQuestionsResp{ConceptID: node.ConceptID, ConceptKey: node.ConceptKey, Rung: rung}
	switch {
	case set != nil && set.CanServe():
		// AC3 reuse read at zero LLM cost — but SANITISED (Slice F, SECURITY):
		// the stored payload carries the answer key (is_correct / explainer /
		// model_answer); serving it raw ships the answer to the browser pre-
		// submit and breaks honest ladder climbs. Strip every answer-revealing
		// field from the served copy; the stored row stays verbatim so the
		// answers door still grades from the intact server-side key. An
		// un-sanitisable payload is NEVER served raw — fail loud (honest failed).
		sanitised, serr := campaignquestion.SanitizeServedPayload(set.QuestionsPayload)
		if serr != nil {
			log.Printf("consumption: campaign question serve REFUSED — payload unsanitisable (tenant=%s gcid=%s concept=%s rung=%d): %v",
				tenantID, gcid, node.ConceptID, rung, serr)
			resp.Status = string(campaignquestion.StatusFailed)
			resp.FailureReason = "stored question payload could not be sanitised for serving"
			break
		}
		resp.Status = "ready"
		resp.Questions = json.RawMessage(sanitised)
		resp.Source = "question_bank"
	case outcome == genCapped:
		// The explicit-tap daily generation budget is spent (the march budget
		// is untouched by this). An HONEST, actionable status the FE renders as
		// a resets-tomorrow message, never a vague empty miss.
		resp.Status = "tap_capped"
	case set == nil:
		// No set even after the gap trigger (unwired lane); an honest miss;
		// the next practice tap or dose day retries.
		resp.Status = "none"
	default:
		// idle | requested | failed — the FE re-polls; failed surfaces the
		// reason (honest, never a silent empty set).
		resp.Status = string(set.GenerationStatus)
		if set.GenerationStatus == campaignquestion.StatusFailed {
			resp.FailureReason = set.FailureReason
		}
	}
	extWriteJSON(w, http.StatusOK, resp)
}

// resolveCampaignServeRung picks the ladder rung to serve: an explicit
// ?rung=1..6 wins (400 INVALID_RUNG outside the ladder); otherwise the
// deterministic grader serve decision (D8 refresh-before-advance) chooses. A
// won node has left the game (D9) → 409 NODE_WON; any other grader error is a
// 500. Writes the error itself and returns ok=false on any failure.
func (s *ExtServer) resolveCampaignServeRung(w http.ResponseWriter, r *http.Request, ctx context.Context, tenantID, gcid string, node *conceptgraph.ConceptNode) (int, bool) {
	if raw := strings.TrimSpace(r.URL.Query().Get("rung")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > campaign.TotalRungs {
			extWriteError(w, http.StatusBadRequest, "INVALID_RUNG", "rung must be an integer in 1..6")
			return 0, false
		}
		return n, true
	}
	serve, err := s.CampaignDose.Grader.ServeDecisionFor(ctx, tenantID, gcid, node.ConceptID, node.ConceptKey, time.Now().UTC())
	if err != nil {
		if errors.Is(err, campaign.ErrAlreadyWon) {
			extWriteError(w, http.StatusConflict, "NODE_WON", "this node is already won — it has left the campaign")
			return 0, false
		}
		extWriteError(w, http.StatusInternalServerError, "SERVE_DECISION_FAILED", err.Error())
		return 0, false
	}
	return int(serve.Rung), true
}
