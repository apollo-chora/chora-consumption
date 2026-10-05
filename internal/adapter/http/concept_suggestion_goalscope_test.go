// concept_suggestion_goalscope_test.go — bug #19: the pending-suggestion inbox
// (and the ceremony edge-scout fog pool) must be hard-scoped to the queried
// goal's concept subtree, so a sibling goal's fog (e.g. a Photosynthesis "NEW
// LINK") never bleeds onto another map (e.g. Scrum). The READ-side twin of the
// CHO-2117 kg_explore goal fence (companion_skill_invoke_scout_goalscope_test.go).
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	gsScrumRoot  = "01990000-0000-7000-c000-00000000a001"
	gsScrumChild = "01990000-0000-7000-c000-00000000a002"
	gsPhotoFocal = "01990000-0000-7000-c000-00000000b001"
	gsWholeSugID = "01990000-0000-7000-5000-00000000e001"
)

// goalScopedServer: a learner with a Scrum map (root→child) plus a stray
// Photosynthesis concept (a different goal), and three pending suggestions —
// a "NEW LINK" anchored in the Scrum subtree, a "NEW LINK" anchored in
// Photosynthesis, and one whole-map (focal-less) row.
func goalScopedServer() (*ExtServer, *csStubSuggestions) {
	scrumEdge := pendingEdgeSug(csSugA) // "NEW LINK" in Scrum
	scrumEdge.FocalConceptID = gsScrumChild
	photoEdge := pendingEdgeSug(csSugB) // "NEW LINK" in Photosynthesis
	photoEdge.FocalConceptID = gsPhotoFocal
	whole := pendingConceptSug(gsWholeSugID) // NULL focal (whole-map, loose)

	cs := &csStubSuggestions{pending: []*conceptgraph.Suggestion{scrumEdge, photoEdge, whole}}
	scrum := mapsGoal("g-scrum", mapsPtr(gsScrumRoot))
	s := &ExtServer{
		Suggestions:      cs,
		SuggestionAccept: &csStubAccepter{},
		Goals:            &mapsGoalStub{byID: map[string]*goal.Goal{"g-scrum": scrum}},
		Concepts: &gateConcepts{nodes: []*conceptgraph.ConceptNode{
			gateNode(gsScrumRoot, "Scrum"),
			gateNode(gsScrumChild, "Sprint Planning"),
			gateNode(gsPhotoFocal, "Chlorophyll"),
		}},
		ConceptEdges: &reStubEdges{out: []*conceptgraph.Edge{
			gateHierarchyEdge(gsScrumRoot, gsScrumChild),
		}},
	}
	return s, cs
}

func gsSuggestionIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Suggestions []map[string]any `json:"suggestions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	ids := make([]string, 0, len(resp.Suggestions))
	for _, s := range resp.Suggestions {
		ids = append(ids, s["suggestionId"].(string))
	}
	return ids
}

func TestSuggestionsList_GoalScoped_DropsSiblingGoalAndWholeMapFog(t *testing.T) {
	s, _ := goalScopedServer()
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet,
		"/v1/me/concept-graph/suggestions?goalId=g-scrum", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	ids := gsSuggestionIDs(t, w.Body.Bytes())
	if len(ids) != 1 || ids[0] != csSugA {
		t.Fatalf("got %v; want exactly [%s] — the Photosynthesis focal (%s) and the "+
			"whole-map row must be fenced out of the Scrum map", ids, csSugA, gsPhotoFocal)
	}
}

func TestSuggestionsList_NoGoalId_UnchangedWholeMapBehaviour(t *testing.T) {
	s, cs := goalScopedServer()
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet,
		"/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if !cs.listAllHit {
		t.Error("no goalId ⇒ must keep the ListPending path (back-compat)")
	}
	if got := len(gsSuggestionIDs(t, w.Body.Bytes())); got != 3 {
		t.Errorf("no goalId returned %d; want all 3 (unscoped, unchanged)", got)
	}
}

// A malformed (non-UUID) focalConceptId must NOT 500 the Suggestions tab. The
// handler routes it to the focal repo, which degrades a non-UUID focal to the
// whole-map inbox instead of binding it into a pg uuid cast (22P02). Regression
// for the 2026-07-22 live 500 (SUGGESTIONS_LIST_FAILED: invalid input syntax for
// type uuid) from a non-UUID focal sent by the map-companion panel.
func TestSuggestionsList_MalformedFocalConceptId_DegradesTo200(t *testing.T) {
	cs := &csStubSuggestions{pendingFocal: []*conceptgraph.Suggestion{pendingConceptSug(gsWholeSugID)}}
	s := &ExtServer{Suggestions: cs}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet,
		"/v1/me/concept-graph/suggestions?focalConceptId=not-a-uuid", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (a malformed focal must not 500 the tab); body=%s", w.Code, w.Body.String())
	}
	if cs.gotFocal != "not-a-uuid" {
		t.Errorf("gotFocal=%q; want the handler to hand the focal to the (degrading) focal repo, not reject it", cs.gotFocal)
	}
	if got := len(gsSuggestionIDs(t, w.Body.Bytes())); got != 1 {
		t.Errorf("returned %d suggestions; want the whole-map inbox served (1)", got)
	}
}

func TestSuggestionsList_GoalScoped_FailsClosedWhenPortsUnwired(t *testing.T) {
	s, _ := goalScopedServer()
	s.Concepts = nil // a goal-scope port is missing
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet,
		"/v1/me/concept-graph/suggestions?goalId=g-scrum", "", fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (goal-scope unwired must fail-closed, never serve unscoped)", w.Code)
	}
}

// The ceremony edge-scout is the 2nd surface fed by the flat suggestion store.
// A ROOTLESS goal falls to ListPending (every goal's rows); its virgin-fallback
// fog seeds must admit the learner's own whole-map fog but NEVER a sibling
// goal's focal-anchored fog (a Photosynthesis suggestion seeding a Scrum
// ceremony). Modeled on TestCeremonyEdgeScout_VirginFallback_FogSeeded.
func TestCeremonyEdgeScout_RootlessGoal_DropsSiblingGoalFocalFog(t *testing.T) {
	srv, id, _, _, _ := seedEdgeScoutServer(t, &fakeChatEngine{})
	// Rootless goal (no map linkage) → the handler's ListPending sweep.
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{esGoalID: {
		GoalID: esGoalID, TenantID: testTenant, LearnerGCID: testGCID,
		Kind: goal.KindCuriosity, Status: goal.StatusActive,
		RootConceptID: nil, ConceptSet: nil,
	}}}
	srv.LearnerWeakness = &esLWStub{}        // zero weaknesses  → virgin fallback
	srv.GradedComments = &esCommentsReader{} // zero comments    → virgin fallback
	srv.FogSuggestions = &esFogReader{pending: []*conceptgraph.Suggestion{
		{ // whole-map (focal-less): the learner's own un-goaled fog — ADMITTED
			SuggestionID: "sug-whole", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "Sprint retrospectives",
		},
		{ // a SIBLING goal's focal-anchored fog — must be FENCED OUT
			SuggestionID: "sug-photo", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "Photosynthesis Calvin cycle", FocalConceptID: gsPhotoFocal,
		},
	}}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title string `json:"title"`
		} `json:"candidates"`
		Fallback bool `json:"fallback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback {
		t.Fatalf("want virgin fallback (no weakness/comments); body=%s", w.Body.String())
	}
	var sawWhole, sawSibling bool
	for _, c := range resp.Candidates {
		switch c.Title {
		case "Sprint retrospectives":
			sawWhole = true
		case "Photosynthesis Calvin cycle":
			sawSibling = true
		}
	}
	if sawSibling {
		t.Errorf("a rootless goal's ceremony seeded a SIBLING goal's fog (%q) — bug #19 leak", "Photosynthesis Calvin cycle")
	}
	if !sawWhole {
		t.Errorf("the learner's own whole-map fog (%q) must still seed the ceremony", "Sprint retrospectives")
	}
}
