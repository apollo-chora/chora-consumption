// goals_campaign_handler.go — ADR-227 goal campaign sub-resources (WS-C1,
// CHO-2080), nested under the already-proxied /v1/me/goals/ subtree so the
// gateway prefix proxy + Istio authz wildcard admit them without new
// plumbing:
//
//	POST /v1/me/goals/{id}/campaign/focus  {"conceptId": "..."|null}
//	POST /v1/me/goals/{id}/campaign/seal   (no body)
//
// Focus (D11): learner-sovereign assignment of the single campaign focus
// node; must live inside the goal's subtree; also retargets the bound
// Companion's resonant concept (addendum #5 — one affordance, two stores) and
// emits focus_assigned. A clear (null) is not an assignment: no event, and
// resonance clears to its goal-root fallback.
//
// Seal (D3): allowed only while the frontier is system-verified empty (every
// live subtree node won). Stamps goals.campaign_sealed_at (+ the ADR-213
// personal axis when still open) and emits goal_sealed — the tier-S XP rides
// that verification, never a bare declaration. Re-seals gate on >= N new
// wins + the weekly interval (campaign.EvaluateSeal).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// CampaignResonance is the narrow growth surface the focus door needs
// (addendum #5). Implemented by *pg.GrowthRepo.
type CampaignResonance interface {
	SetResonantConcept(ctx context.Context, tenantID, companionID string, conceptID *string) (*growth.CompanionGrowthRow, error)
}

// CampaignEvents is the narrow verified-stream surface the campaign doors
// emit through. Implemented by *events.CampaignEmitter.
type CampaignEvents interface {
	FocusAssigned(ctx context.Context, in events.CampaignFocusAssignedInput) error
	GoalSealed(ctx context.Context, in events.CampaignGoalSealedInput) error
}

type campaignFocusReq struct {
	ConceptID *string `json:"conceptId"`
}

// campaignDeps fail-loud gate: every campaign door needs the graph + events.
func (s *ExtServer) campaignDeps(w http.ResponseWriter) bool {
	if s.Goals == nil || s.Concepts == nil || s.ConceptEdges == nil || s.CampaignEvents == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "campaign surface not wired")
		return false
	}
	return true
}

// loadCampaignGoal shares the ctx + goal + leak-guard preamble.
func (s *ExtServer) loadCampaignGoal(w http.ResponseWriter, r *http.Request, goalID string) (context.Context, string, string, *goal.Goal, bool) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return nil, "", "", nil, false
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return nil, "", "", nil, false
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return nil, "", "", nil, false
	}
	return ctx, tenantID, gcid, g, true
}

// campaignSubtree loads the learner graph and walks the goal subtree.
func (s *ExtServer) campaignSubtree(ctx context.Context, w http.ResponseWriter, tenantID, gcid string, g *goal.Goal) ([]*conceptgraph.ConceptNode, map[string]bool, bool) {
	if g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		extWriteError(w, http.StatusUnprocessableEntity, "CAMPAIGN_NOT_ANCHORED", "goal has no root concept — anchor the map first")
		return nil, nil, false
	}
	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GRAPH_LOAD_FAILED", err.Error())
		return nil, nil, false
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GRAPH_LOAD_FAILED", err.Error())
		return nil, nil, false
	}
	nodeVals := make([]conceptgraph.ConceptNode, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			nodeVals = append(nodeVals, *n)
		}
	}
	edgeVals := make([]conceptgraph.Edge, 0, len(edges))
	for _, e := range edges {
		if e != nil {
			edgeVals = append(edgeVals, *e)
		}
	}
	subtree := conceptgraph.SubtreeConceptIDs(nodeVals, edgeVals, *g.RootConceptID)
	return nodes, subtree, true
}

// resolveCampaignSubtreeNode validates conceptID is a LIVE node inside the
// goal's campaign subtree, returning the node. 404 CONCEPT_NOT_FOUND (not in
// the live graph); 422 CONCEPT_OUTSIDE_CAMPAIGN (live but outside the subtree);
// 422 CAMPAIGN_NOT_ANCHORED (rootless goal, via campaignSubtree). Shared by the
// node-scoped question serve (addendum #3) + the answers door — the same
// subtree source of truth the focus door validates against. Writes the error
// itself and returns ok=false on any failure.
func (s *ExtServer) resolveCampaignSubtreeNode(ctx context.Context, w http.ResponseWriter, tenantID, gcid string, g *goal.Goal, conceptID string) (*conceptgraph.ConceptNode, bool) {
	nodes, subtree, ok := s.campaignSubtree(ctx, w, tenantID, gcid, g)
	if !ok {
		return nil, false
	}
	var node *conceptgraph.ConceptNode
	for _, n := range nodes {
		if n != nil && n.ConceptID == conceptID && n.DeletedAt == nil {
			node = n
			break
		}
	}
	if node == nil {
		extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", "concept is not a live node in this map")
		return nil, false
	}
	if !subtree[conceptID] {
		extWriteError(w, http.StatusUnprocessableEntity, "CONCEPT_OUTSIDE_CAMPAIGN", "concept is outside this goal's campaign subtree")
		return nil, false
	}
	return node, true
}

// handleMeGoalCampaignFocus — POST /v1/me/goals/{id}/campaign/focus.
func (s *ExtServer) handleMeGoalCampaignFocus(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.campaignDeps(w) {
		return
	}
	if s.CampaignResonance == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "resonance reconcile not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	var req campaignFocusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	ctx, tenantID, gcid, g, ok := s.loadCampaignGoal(w, r, goalID)
	if !ok {
		return
	}
	now := time.Now().UTC()

	// Clear: unassign the focus; resonance falls back to the goal root
	// (growth-side fallback). Not an assignment — no event (D11).
	if req.ConceptID == nil || strings.TrimSpace(*req.ConceptID) == "" {
		if g.FocusConceptID == nil {
			extWriteJSON(w, http.StatusOK, map[string]any{"goalId": g.GoalID, "focusConceptId": nil})
			return
		}
		if err := g.SetFocusConcept(nil, now); err != nil {
			writeGoalDomainError(w, err)
			return
		}
		if err := s.Goals.Update(ctx, g); err != nil {
			extWriteError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
			return
		}
		if g.AttachedCompanionID != nil {
			if _, err := s.CampaignResonance.SetResonantConcept(ctx, tenantID, *g.AttachedCompanionID, nil); err != nil {
				extWriteError(w, http.StatusInternalServerError, "RESONANCE_FAILED", err.Error())
				return
			}
		}
		extWriteJSON(w, http.StatusOK, map[string]any{"goalId": g.GoalID, "focusConceptId": nil})
		return
	}

	conceptID := strings.TrimSpace(*req.ConceptID)
	nodes, subtree, ok := s.campaignSubtree(ctx, w, tenantID, gcid, g)
	if !ok {
		return
	}
	var node *conceptgraph.ConceptNode
	for _, n := range nodes {
		if n != nil && n.ConceptID == conceptID && n.DeletedAt == nil {
			node = n
			break
		}
	}
	if node == nil {
		extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", "")
		return
	}
	if !subtree[conceptID] {
		extWriteError(w, http.StatusUnprocessableEntity, "FOCUS_OUTSIDE_CAMPAIGN", "focus must be a node of this goal's subtree (D11)")
		return
	}
	// Idempotent re-assignment: no mutation, no side effects.
	if g.FocusConceptID != nil && *g.FocusConceptID == conceptID {
		extWriteJSON(w, http.StatusOK, map[string]any{"goalId": g.GoalID, "focusConceptId": conceptID})
		return
	}
	previous := ""
	if g.FocusConceptID != nil {
		previous = *g.FocusConceptID
	}
	if err := g.SetFocusConcept(&conceptID, now); err != nil {
		writeGoalDomainError(w, err)
		return
	}
	if err := s.Goals.Update(ctx, g); err != nil {
		extWriteError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	companionID := ""
	if g.AttachedCompanionID != nil {
		companionID = *g.AttachedCompanionID
		// Addendum #5: setting focus retargets resonance — one affordance.
		// Persisted focus survives a failure here; the retry is idempotent.
		if _, err := s.CampaignResonance.SetResonantConcept(ctx, tenantID, companionID, &conceptID); err != nil {
			extWriteError(w, http.StatusInternalServerError, "RESONANCE_FAILED", err.Error())
			return
		}
	}
	traceparent, tracestate := traceFromHeaders(r)
	if err := s.CampaignEvents.FocusAssigned(ctx, events.CampaignFocusAssignedInput{
		TenantID: tenantID, LearnerGCID: gcid, GoalID: g.GoalID,
		ConceptID: conceptID, ConceptKey: node.ConceptKey,
		PreviousConceptID: previous, CompanionID: companionID,
		AssignedAt: now, Traceparent: traceparent, Tracestate: tracestate,
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"goalId": g.GoalID, "focusConceptId": conceptID})
}

// handleMeGoalCampaignSeal — POST /v1/me/goals/{id}/campaign/seal.
func (s *ExtServer) handleMeGoalCampaignSeal(w http.ResponseWriter, r *http.Request, goalID string) {
	if !s.campaignDeps(w) {
		return
	}
	if s.CampaignProgress == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CAMPAIGN_NOT_WIRED", "campaign progress repo not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	ctx, tenantID, gcid, g, ok := s.loadCampaignGoal(w, r, goalID)
	if !ok {
		return
	}
	nodes, subtree, ok := s.campaignSubtree(ctx, w, tenantID, gcid, g)
	if !ok {
		return
	}
	var rootNode *conceptgraph.ConceptNode
	for _, n := range nodes {
		if n != nil && g.RootConceptID != nil && n.ConceptID == *g.RootConceptID && n.DeletedAt == nil {
			rootNode = n
			break
		}
	}
	if rootNode == nil {
		extWriteError(w, http.StatusUnprocessableEntity, "CAMPAIGN_NOT_ANCHORED", "goal root concept missing from the live graph")
		return
	}

	progresses, err := s.CampaignProgress.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PROGRESS_LOAD_FAILED", err.Error())
		return
	}
	won := map[string]bool{}
	winsSince := 0
	for _, p := range progresses {
		if p == nil || !p.Won() {
			continue
		}
		won[p.ConceptID] = true
		if subtree[p.ConceptID] && g.CampaignSealedAt != nil && p.WonAt.After(*g.CampaignSealedAt) {
			winsSince++
		}
	}

	now := time.Now().UTC()
	frontier := campaign.ComputeFrontier(subtree, won)
	resealMin := s.CampaignResealMinNewWins
	if resealMin <= 0 {
		resealMin = campaign.DefaultResealMinNewWins
	}
	dec, err := campaign.EvaluateSeal(frontier, g.CampaignSealedAt, winsSince, resealMin, campaign.ResealMinInterval, now)
	if err != nil {
		switch {
		case errors.Is(err, campaign.ErrFrontierNotEmpty):
			extWriteError(w, http.StatusConflict, "FRONTIER_NOT_EMPTY", err.Error())
		case errors.Is(err, campaign.ErrSealTooSoon):
			extWriteError(w, http.StatusConflict, "SEAL_TOO_SOON", err.Error())
		case errors.Is(err, campaign.ErrSealNoNewWins):
			extWriteError(w, http.StatusConflict, "SEAL_NO_NEW_WINS", err.Error())
		case errors.Is(err, campaign.ErrInvalid):
			extWriteError(w, http.StatusUnprocessableEntity, "CAMPAIGN_EMPTY", err.Error())
		default:
			extWriteError(w, http.StatusInternalServerError, "SEAL_FAILED", err.Error())
		}
		return
	}

	if err := g.SealCampaign(now); err != nil {
		writeGoalDomainError(w, err)
		return
	}
	if err := s.Goals.Update(ctx, g); err != nil {
		extWriteError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	traceparent, tracestate := traceFromHeaders(r)
	if err := s.CampaignEvents.GoalSealed(ctx, events.CampaignGoalSealedInput{
		TenantID: tenantID, LearnerGCID: gcid, GoalID: g.GoalID,
		RootConceptID: rootNode.ConceptID, RootConceptKey: rootNode.ConceptKey,
		NodesWon: dec.NodesWon, IsReseal: dec.IsReseal,
		SealedAt: now, Traceparent: traceparent, Tracestate: tracestate,
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, map[string]any{
		"goalId":              g.GoalID,
		"sealedAt":            g.CampaignSealedAt,
		"isReseal":            dec.IsReseal,
		"nodesWon":            dec.NodesWon,
		"personalCompletedAt": g.PersonalCompletedAt,
	})
}
