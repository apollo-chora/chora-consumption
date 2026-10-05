// goals_campaign_handler_test.go — WS-C1 (CHO-2080, ADR-227 D11 + D3) RED
// tests for the goal campaign sub-resources:
//
//	POST /v1/me/goals/{id}/campaign/focus  — assign/move/clear the focus
//	POST /v1/me/goals/{id}/campaign/seal   — seal a verified frontier-clear
//
// Focus reconciles the bound Companion's resonant concept (addendum #5 — one
// affordance) and emits focus_assigned; seal walks the live subtree against
// won ladder rows, refuses while the frontier is non-empty, stamps
// CampaignSealedAt (+ the ADR-213 personal axis when open) and emits
// goal_sealed. Every emission carries goal_id (addendum #6).
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	consumptionevents "github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// --- fakes -------------------------------------------------------------------

type campConceptStub struct {
	nodes []*conceptgraph.ConceptNode
}

func (s *campConceptStub) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *campConceptStub) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	for _, n := range s.nodes {
		if n.ConceptID == id && n.DeletedAt == nil {
			return n, nil
		}
	}
	return nil, nil
}
func (s *campConceptStub) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.nodes, nil
}
func (s *campConceptStub) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

type campProgressStub struct {
	rows []*campaign.NodeProgress
}

func (s *campProgressStub) GetByConcept(_ context.Context, _, _, conceptID string) (*campaign.NodeProgress, error) {
	for _, p := range s.rows {
		if p.ConceptID == conceptID {
			return p, nil
		}
	}
	return nil, nil
}
func (s *campProgressStub) ListByLearner(context.Context, string, string) ([]*campaign.NodeProgress, error) {
	return s.rows, nil
}
func (s *campProgressStub) Save(context.Context, *campaign.NodeProgress) error { return nil }

type campResonanceStub struct {
	calls []*string // conceptID per call (nil = clear)
	fids  []string
}

func (s *campResonanceStub) SetResonantConcept(_ context.Context, _, companionID string, conceptID *string) (*growth.CompanionGrowthRow, error) {
	s.calls = append(s.calls, conceptID)
	s.fids = append(s.fids, companionID)
	return &growth.CompanionGrowthRow{CompanionID: companionID}, nil
}

type campEventsStub struct {
	focus  []consumptionevents.CampaignFocusAssignedInput
	sealed []consumptionevents.CampaignGoalSealedInput
}

func (s *campEventsStub) FocusAssigned(_ context.Context, in consumptionevents.CampaignFocusAssignedInput) error {
	s.focus = append(s.focus, in)
	return nil
}
func (s *campEventsStub) GoalSealed(_ context.Context, in consumptionevents.CampaignGoalSealedInput) error {
	s.sealed = append(s.sealed, in)
	return nil
}

// campaignGraph: root -> a, root -> b (live hierarchy).
func campaignGraph() ([]*conceptgraph.ConceptNode, []*conceptgraph.Edge) {
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: "camp-root", TenantID: goTenant, LearnerGCID: goGCID, Title: "Botany", ConceptKey: "botany"},
		{ConceptID: "camp-a", TenantID: goTenant, LearnerGCID: goGCID, Title: "Photosynthesis", ConceptKey: "photosynthesis"},
		{ConceptID: "camp-b", TenantID: goTenant, LearnerGCID: goGCID, Title: "Roots", ConceptKey: "roots"},
		{ConceptID: "camp-island", TenantID: goTenant, LearnerGCID: goGCID, Title: "Off-map", ConceptKey: "off-map"},
	}
	edges := []*conceptgraph.Edge{
		{EdgeID: "e1", TenantID: goTenant, LearnerGCID: goGCID, SourceConceptID: "camp-root", TargetConceptID: "camp-a", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e2", TenantID: goTenant, LearnerGCID: goGCID, SourceConceptID: "camp-root", TargetConceptID: "camp-b", Class: conceptgraph.EdgeClassHierarchy},
	}
	return nodes, edges
}

type campEdgeStub struct {
	out []*conceptgraph.Edge
}

func (s *campEdgeStub) Create(context.Context, *conceptgraph.Edge) error { return nil }
func (s *campEdgeStub) GetByID(context.Context, string, string, string) (*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *campEdgeStub) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.out, nil
}
func (s *campEdgeStub) ListByConcept(context.Context, string, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *campEdgeStub) Update(context.Context, *conceptgraph.Edge) error { return nil }
func (s *campEdgeStub) SoftDeleteByConcept(context.Context, string, string, string, time.Time) (int64, error) {
	return 0, nil
}

type campaignHarness struct {
	ext       *httpadapter.ExtServer
	goals     *goalRepoFake
	progress  *campProgressStub
	resonance *campResonanceStub
	events    *campEventsStub
	goal      *goal.Goal
}

func newCampaignHarness(t *testing.T, bindCompanion bool) *campaignHarness {
	t.Helper()
	nodes, edges := campaignGraph()
	goals := newGoalRepoFake()
	g := goals.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, Now: time.Now().UTC().Add(-72 * time.Hour)})
	if err := g.AnchorToConcept("camp-root", time.Now().UTC().Add(-71*time.Hour)); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if bindCompanion {
		if err := g.AttachCompanion(goCompanion, time.Now().UTC().Add(-70*time.Hour)); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}
	h := &campaignHarness{
		goals:     goals,
		progress:  &campProgressStub{},
		resonance: &campResonanceStub{},
		events:    &campEventsStub{},
		goal:      g,
	}
	ext := goalServer(goals)
	ext.Concepts = &campConceptStub{nodes: nodes}
	ext.ConceptEdges = &campEdgeStub{out: edges}
	ext.CampaignProgress = h.progress
	ext.CampaignResonance = h.resonance
	ext.CampaignEvents = h.events
	h.ext = ext
	return h
}

func wonRow(conceptID string, wonAt time.Time) *campaign.NodeProgress {
	return &campaign.NodeProgress{
		ID: "row-" + conceptID, TenantID: goTenant, LearnerGCID: goGCID,
		ConceptID: conceptID, RungsCleared: 6, WonAt: &wonAt,
	}
}

// --- focus ---------------------------------------------------------------------

func TestCampaignFocus_AssignEmitsAndRetargetsResonance(t *testing.T) {
	h := newCampaignHarness(t, true)

	w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus",
		map[string]any{"conceptId": "camp-a"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	saved := h.goals.store[h.goal.GoalID]
	if saved.FocusConceptID == nil || *saved.FocusConceptID != "camp-a" {
		t.Errorf("persisted focus = %v", saved.FocusConceptID)
	}
	if len(h.events.focus) != 1 {
		t.Fatalf("focus events = %d", len(h.events.focus))
	}
	e := h.events.focus[0]
	if e.ConceptID != "camp-a" || e.ConceptKey != "photosynthesis" || e.GoalID != h.goal.GoalID || e.CompanionID != goCompanion || e.PreviousConceptID != "" {
		t.Errorf("focus event = %+v", e)
	}
	if len(h.resonance.calls) != 1 || h.resonance.calls[0] == nil || *h.resonance.calls[0] != "camp-a" {
		t.Errorf("resonance retarget = %+v", h.resonance.calls)
	}

	// Moving focus carries the previous node.
	w = doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus",
		map[string]any{"conceptId": "camp-b"}))
	if w.Code != http.StatusOK {
		t.Fatalf("move status = %d", w.Code)
	}
	if e := h.events.focus[1]; e.PreviousConceptID != "camp-a" || e.ConceptID != "camp-b" {
		t.Errorf("move event = %+v", e)
	}
}

func TestCampaignFocus_IdempotentRepostSkipsSideEffects(t *testing.T) {
	h := newCampaignHarness(t, true)
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusOK {
		t.Fatalf("assign: %d", w.Code)
	}
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusOK {
		t.Fatalf("re-post: %d", w.Code)
	}
	if len(h.events.focus) != 1 || len(h.resonance.calls) != 1 {
		t.Errorf("idempotent re-post fired side effects: events=%d resonance=%d", len(h.events.focus), len(h.resonance.calls))
	}
}

func TestCampaignFocus_ClearClearsResonanceNoEvent(t *testing.T) {
	h := newCampaignHarness(t, true)
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusOK {
		t.Fatalf("assign: %d", w.Code)
	}
	w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": nil}))
	if w.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", w.Code, w.Body.String())
	}
	saved := h.goals.store[h.goal.GoalID]
	if saved.FocusConceptID != nil {
		t.Errorf("focus not cleared: %v", saved.FocusConceptID)
	}
	if len(h.events.focus) != 1 {
		t.Errorf("a clear is not an assignment — no second event, got %d", len(h.events.focus))
	}
	if len(h.resonance.calls) != 2 || h.resonance.calls[1] != nil {
		t.Errorf("resonance must fall back to root via clear: %+v", h.resonance.calls)
	}
}

func TestCampaignFocus_Guards(t *testing.T) {
	h := newCampaignHarness(t, false)

	// Outside the goal subtree → 422.
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-island"})); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("outside-subtree status = %d", w.Code)
	}
	// Unknown node → 404.
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "nope"})); w.Code != http.StatusNotFound {
		t.Errorf("unknown-node status = %d", w.Code)
	}
	// Unknown goal → 404.
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/ghost/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusNotFound {
		t.Errorf("unknown-goal status = %d", w.Code)
	}
	// Wrong method → 405.
	if w := doGoal(h.ext, goalReq("PATCH", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("method status = %d", w.Code)
	}
	// Unanchored goal → 422.
	bare := h.goals.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, Now: time.Now().UTC()})
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+bare.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unanchored status = %d", w.Code)
	}
	// Unwired campaign surface → 503.
	naked := goalServer(h.goals)
	if w := doGoal(naked, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/focus", map[string]any{"conceptId": "camp-a"})); w.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired status = %d", w.Code)
	}
}

// --- seal ----------------------------------------------------------------------

func TestCampaignSeal_FirstSeal(t *testing.T) {
	h := newCampaignHarness(t, true)
	now := time.Now().UTC()
	h.progress.rows = []*campaign.NodeProgress{
		wonRow("camp-root", now.Add(-3*24*time.Hour)),
		wonRow("camp-a", now.Add(-2*24*time.Hour)),
		wonRow("camp-b", now.Add(-1*24*time.Hour)),
	}

	w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/seal", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	saved := h.goals.store[h.goal.GoalID]
	if saved.CampaignSealedAt == nil || saved.PersonalCompletedAt == nil {
		t.Errorf("seal must stamp both axes: sealed=%v personal=%v", saved.CampaignSealedAt, saved.PersonalCompletedAt)
	}
	if len(h.events.sealed) != 1 {
		t.Fatalf("sealed events = %d", len(h.events.sealed))
	}
	e := h.events.sealed[0]
	if e.NodesWon != 3 || e.IsReseal || e.RootConceptID != "camp-root" || e.RootConceptKey != "botany" || e.GoalID != h.goal.GoalID {
		t.Errorf("sealed event = %+v", e)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body["isReseal"] != false || body["nodesWon"] != float64(3) {
		t.Errorf("body = %v", body)
	}
}

func TestCampaignSeal_FrontierNotEmpty409(t *testing.T) {
	h := newCampaignHarness(t, false)
	now := time.Now().UTC()
	h.progress.rows = []*campaign.NodeProgress{
		wonRow("camp-root", now), wonRow("camp-a", now), // camp-b unwon
	}
	w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/seal", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(h.events.sealed) != 0 {
		t.Error("no event on refused seal")
	}
	if h.goals.store[h.goal.GoalID].CampaignSealedAt != nil {
		t.Error("refused seal must not stamp")
	}
}

func TestCampaignSeal_ResealGates(t *testing.T) {
	h := newCampaignHarness(t, false)
	now := time.Now().UTC()

	// Sealed yesterday → weekly cap refuses.
	yesterday := now.Add(-24 * time.Hour)
	if err := h.goal.SealCampaign(yesterday); err != nil {
		t.Fatal(err)
	}
	h.goals.store[h.goal.GoalID] = h.goal
	h.progress.rows = []*campaign.NodeProgress{
		wonRow("camp-root", now.Add(-30*24*time.Hour)),
		wonRow("camp-a", now.Add(-2*time.Hour)),
		wonRow("camp-b", now.Add(-1*time.Hour)),
	}
	if w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/seal", nil)); w.Code != http.StatusConflict {
		t.Errorf("too-soon status = %d", w.Code)
	}

	// Sealed 8 days ago + 3 new wins since → re-seal allowed.
	eightDays := now.Add(-8 * 24 * time.Hour)
	g2 := h.goals.store[h.goal.GoalID]
	g2.CampaignSealedAt = &eightDays
	h.progress.rows = []*campaign.NodeProgress{
		wonRow("camp-root", now.Add(-2*24*time.Hour)),
		wonRow("camp-a", now.Add(-2*24*time.Hour)),
		wonRow("camp-b", now.Add(-1*24*time.Hour)),
	}
	w := doGoal(h.ext, goalReq("POST", "/v1/me/goals/"+h.goal.GoalID+"/campaign/seal", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("re-seal status = %d body=%s", w.Code, w.Body.String())
	}
	if len(h.events.sealed) != 1 || !h.events.sealed[0].IsReseal {
		t.Errorf("re-seal event = %+v", h.events.sealed)
	}
}
