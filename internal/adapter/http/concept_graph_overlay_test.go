// concept_graph_overlay_test.go — WS-A1 (My Knowledge unification, CHO-2005):
// the sovereign concept-graph read (GET /v1/me/concept-graph) paints each
// concept node with the learner's Growth-Edge shaky/due overlay + a mastered
// (grown) flag, reusing the substrate-agnostic kg_growth_overlay engine and the
// canonical MasteredConceptKeys derivation. Read-time enrichment only — an
// overlay failure NEVER breaks the graph read (fail-soft).
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// cgServe issues GET /v1/me/concept-graph against s with the fm tenant/gcid.
func cgServe(s *ExtServer) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/concept-graph", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	s.handleMeConceptGraph(w, r)
	return w
}

// cgOverlayConcepts returns three learner concepts for the overlay tests.
func cgOverlayConcepts() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-photo", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Photosynthesis"},
		{ConceptID: "c-resp", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Cellular Respiration"},
		{ConceptID: "c-glyc", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Glycolysis"},
	}
}

// conceptDTOsByTitle decodes the concept-graph response into a title->node map.
func conceptDTOsByTitle(t *testing.T, body []byte) map[string]map[string]any {
	t.Helper()
	var resp struct {
		Concepts []map[string]any `json:"concepts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, string(body))
	}
	out := map[string]map[string]any{}
	for _, c := range resp.Concepts {
		title, _ := c["title"].(string)
		out[title] = c
	}
	return out
}

func TestConceptGraph_PaintsShakyAndMastered(t *testing.T) {
	// Photosynthesis = active shaky edge (0.8); Cellular Respiration = grown
	// (mastered, strength <= 0.1); Glycolysis = neither.
	lwStub := &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Photosynthesis", 0.8),
		kgOverlayEdge(t, "Cellular Respiration", 0.05),
	}}
	s := &ExtServer{
		Concepts:        &fmStubConcepts{out: cgOverlayConcepts()},
		ConceptEdges:    &reStubEdges{},
		LearnerWeakness: lwStub,
	}
	w := cgServe(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	byTitle := conceptDTOsByTitle(t, w.Body.Bytes())

	photo := byTitle["Photosynthesis"]
	ge, _ := photo["growthEdge"].(map[string]any)
	if ge == nil {
		t.Fatalf("Photosynthesis has no growthEdge overlay: %+v", photo)
	}
	if strength, _ := ge["strength"].(float64); strength != 0.8 {
		t.Errorf("Photosynthesis growthEdge.strength = %v; want 0.8", ge["strength"])
	}
	if photo["mastered"] == true {
		t.Error("Photosynthesis mastered = true; want not mastered (active edge)")
	}

	resp := byTitle["Cellular Respiration"]
	if resp["mastered"] != true {
		t.Errorf("Cellular Respiration mastered = %v; want true (grown edge)", resp["mastered"])
	}
	if resp["growthEdge"] != nil {
		t.Errorf("Cellular Respiration growthEdge = %v; want none (grown excluded from shaky)", resp["growthEdge"])
	}

	glyc := byTitle["Glycolysis"]
	if glyc["growthEdge"] != nil || glyc["mastered"] == true {
		t.Errorf("Glycolysis should carry no overlay: %+v", glyc)
	}
}

// WS-C: the canvas paints a provenance badge (● you / ✨ Companion) per concept,
// so the read must expose each concept's provenance.
func TestConceptGraph_ExposesProvenance(t *testing.T) {
	concepts := []*conceptgraph.ConceptNode{
		{ConceptID: "c1", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Alpha", Provenance: conceptgraph.ProvenanceLearnerAuthored},
		{ConceptID: "c2", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Beta", Provenance: conceptgraph.ProvenanceCompanionSuggestedAccepted},
	}
	s := &ExtServer{Concepts: &fmStubConcepts{out: concepts}, ConceptEdges: &reStubEdges{}}
	byTitle := conceptDTOsByTitle(t, cgServe(s).Body.Bytes())
	if byTitle["Alpha"]["provenance"] != "learner_authored" {
		t.Errorf("Alpha provenance = %v; want learner_authored", byTitle["Alpha"]["provenance"])
	}
	if byTitle["Beta"]["provenance"] != "companion_suggested_accepted" {
		t.Errorf("Beta provenance = %v; want companion_suggested_accepted", byTitle["Beta"]["provenance"])
	}
}

func TestConceptGraph_Overlay_FailSoft_NoWeaknessRepo(t *testing.T) {
	// No LearnerWeakness repo → the graph still returns 200, unpainted.
	s := &ExtServer{
		Concepts:     &fmStubConcepts{out: cgOverlayConcepts()},
		ConceptEdges: &reStubEdges{},
	}
	w := cgServe(s)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	for title, c := range conceptDTOsByTitle(t, w.Body.Bytes()) {
		if c["growthEdge"] != nil || c["mastered"] == true {
			t.Errorf("%s: expected no paint without weakness repo: %+v", title, c)
		}
	}
}
