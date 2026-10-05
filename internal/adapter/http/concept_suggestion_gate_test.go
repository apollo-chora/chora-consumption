// concept_suggestion_gate_test.go — WS-C4 (CHO-2083, ADR-227 D2) hard
// fog-of-war gate on the suggestion-generate door:
//
//   - a focal concept inside ANY of the learner's goal subtrees may only be
//     LLM-revealed (children proposed) once that node is WON (won_at set) —
//     resolved server-side from the learner's goals, never trusted from the
//     request's goalId;
//   - a whole-map request that names a goalId is refused (a goal map's fog
//     lifts only through wins — CAMPAIGN_MAP_REQUIRES_FOCAL);
//   - non-goal territory (loose clusters, no goalId) keeps both request forms
//     free (ADR-212 sovereignty unchanged);
//   - the gate is fail-CLOSED: a focal request with the campaign wiring
//     absent is refused 503 rather than silently fail-open.
//
// Manual node authoring is untouched by design — the gate governs only this
// door (the LLM's pen), never the learner's own minting endpoints.
package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---- fakes -----------------------------------------------------------------

// gateConcepts is a full-roster concept stub: GetByID + ListByLearner both
// serve from the same node slice (the gate's subtree walk needs the roster;
// acqConcepts.ListByLearner returns nil so it cannot back a subtree).
type gateConcepts struct{ nodes []*conceptgraph.ConceptNode }

func (s *gateConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *gateConcepts) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	for _, n := range s.nodes {
		if n.ConceptID == id && n.DeletedAt == nil {
			return n, nil
		}
	}
	return nil, nil
}
func (s *gateConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.nodes, nil
}
func (s *gateConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

// gateProgress serves won-state by concept id.
type gateProgress struct {
	rows map[string]*campaign.NodeProgress
}

func (s *gateProgress) GetByConcept(_ context.Context, _, _, conceptID string) (*campaign.NodeProgress, error) {
	return s.rows[conceptID], nil
}
func (s *gateProgress) ListByLearner(context.Context, string, string) ([]*campaign.NodeProgress, error) {
	out := make([]*campaign.NodeProgress, 0, len(s.rows))
	for _, p := range s.rows {
		out = append(out, p)
	}
	return out, nil
}
func (s *gateProgress) Save(context.Context, *campaign.NodeProgress) error { return nil }

// ---- fixture ----------------------------------------------------------------

func gateNode(id, title string) *conceptgraph.ConceptNode {
	return &conceptgraph.ConceptNode{
		ConceptID: id, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: title,
	}
}

func gateHierarchyEdge(parent, child string) *conceptgraph.Edge {
	return &conceptgraph.Edge{
		EdgeID: "e-" + parent + "-" + child, TenantID: fmTenantID, LearnerGCID: fmGCID,
		SourceConceptID: parent, TargetConceptID: child, Class: conceptgraph.EdgeClassHierarchy,
	}
}

func gateWon(conceptID string) *campaign.NodeProgress {
	won := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	return &campaign.NodeProgress{
		TenantID: fmTenantID, LearnerGCID: fmGCID, ConceptID: conceptID, WonAt: &won,
	}
}

// gateServer wires the full campaign gate: goal g-1 rooted at c-root with
// child c-child, plus a loose non-goal concept c-loose.
func gateServer(progress map[string]*campaign.NodeProgress) (*ExtServer, *events.InMemoryPublisher) {
	g := mapsGoal("g-1", mapsPtr("c-root"))
	pub := events.NewInMemoryPublisher()
	s := &ExtServer{
		Concepts: &gateConcepts{nodes: []*conceptgraph.ConceptNode{
			gateNode("c-root", "Algebra"),
			gateNode("c-child", "Factorisation"),
			gateNode("c-loose", "Baking"),
		}},
		ConceptEdges: &reStubEdges{out: []*conceptgraph.Edge{
			gateHierarchyEdge("c-root", "c-child"),
		}},
		Goals:            &mapsGoalStub{list: []*goal.Goal{g}, byID: map[string]*goal.Goal{"g-1": g}},
		CampaignProgress: &gateProgress{rows: progress},
		Publisher:        pub,
	}
	return s, pub
}

func gateGenerate(t *testing.T, s *ExtServer, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleMeSuggestionGenerate(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/generate", body, fmTenantID, fmGCID))
	return w
}

// ---- the gate (D2) -----------------------------------------------------------

// An unwon node inside a goal subtree refuses the LLM reveal of its children —
// even when the request omits goalId (server-side resolution, no bypass).
func TestSuggestionGate_UnwonGoalNode_Refused(t *testing.T) {
	s, pub := gateServer(nil)
	w := gateGenerate(t, s, `{"focalConceptId":"c-child"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CAMPAIGN_NODE_NOT_WON") {
		t.Errorf("body = %s; want CAMPAIGN_NODE_NOT_WON", w.Body.String())
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events; want 0 (gate must block the request)", n)
	}
}

// The goal ROOT is itself gated — its children reveal only once the root is won
// (the learner can battle the root through the campaign dose without a reveal).
func TestSuggestionGate_UnwonGoalRoot_Refused(t *testing.T) {
	s, pub := gateServer(nil)
	w := gateGenerate(t, s, `{"focalConceptId":"c-root"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (%s)", w.Code, w.Body.String())
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events; want 0", n)
	}
}

// A WON node's reveal goes through and publishes the requested.v1 event with
// the won node as focal.
func TestSuggestionGate_WonGoalNode_Publishes(t *testing.T) {
	s, pub := gateServer(map[string]*campaign.NodeProgress{"c-child": gateWon("c-child")})
	w := gateGenerate(t, s, `{"focalConceptId":"c-child","goalId":"g-1"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	p := csGeneratePayload(t, pub)
	if p["focal_concept_id"] != "c-child" {
		t.Errorf("focal_concept_id = %v; want c-child", p["focal_concept_id"])
	}
}

// A concept OUTSIDE every goal subtree stays free (ADR-212 sovereignty — the
// campaign gate never reaches loose clusters).
func TestSuggestionGate_NonGoalConcept_StaysFree(t *testing.T) {
	s, pub := gateServer(nil)
	w := gateGenerate(t, s, `{"focalConceptId":"c-loose"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	if n := len(pub.Events()); n != 1 {
		t.Errorf("published %d events; want 1", n)
	}
}

// A whole-map request that names a goalId is refused: on a goal map the fog
// lifts only through wins, so an unanchored LLM sweep would bypass D2.
func TestSuggestionGate_WholeMapOnGoalMap_RequiresFocal(t *testing.T) {
	s, pub := gateServer(nil)
	w := gateGenerate(t, s, `{"goalId":"g-1"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CAMPAIGN_MAP_REQUIRES_FOCAL") {
		t.Errorf("body = %s; want CAMPAIGN_MAP_REQUIRES_FOCAL", w.Body.String())
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events; want 0", n)
	}
}

// A whole-map request WITHOUT a goalId stays free even when the learner owns
// goals (the sovereign whole-graph suggestion is not campaign territory).
func TestSuggestionGate_WholeMapNoGoal_StaysFree(t *testing.T) {
	s, pub := gateServer(nil)
	w := gateGenerate(t, s, `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	if n := len(pub.Events()); n != 1 {
		t.Errorf("published %d events; want 1", n)
	}
}

// The gate is fail-CLOSED: a focal request with the campaign wiring missing
// must refuse loudly (503), never silently skip the won-check.
func TestSuggestionGate_FocalWithoutCampaignWiring_Unavailable(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	s := &ExtServer{
		Concepts: &gateConcepts{nodes: []*conceptgraph.ConceptNode{
			gateNode("c-child", "Factorisation"),
		}},
		Publisher: pub, // Goals / ConceptEdges / CampaignProgress all nil
	}
	w := gateGenerate(t, s, `{"focalConceptId":"c-child"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 fail-closed (%s)", w.Code, w.Body.String())
	}
	if n := len(pub.Events()); n != 0 {
		t.Errorf("published %d events; want 0", n)
	}
}
