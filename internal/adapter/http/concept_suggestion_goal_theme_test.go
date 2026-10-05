// concept_suggestion_goal_theme_test.go — CHO-2117: goal-theme attribution on
// the suggestion-generate door for FOCAL-ONLY requests.
//
// The A+ discovery graph fires POST /suggestions/generate with ONLY a
// focalConceptId (no goalId) — yet the won-gate (refuseUnwonGoalReveal)
// already resolves the CONTAINING goal server-side. RED-first: when the focal
// sits inside a WON goal-subtree node, the published
// concept_suggestion.requested.v1 payload must carry that goal's map_theme
// (root concept title) + companion attribution, so the Python fog crew can
// scope its generation to the goal theme (AC1). Loose (non-goal) territory
// keeps the unscoped sovereign behaviour — map_theme stays empty.
package http

import (
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// themeGateServer mirrors gateServer but binds a companion to the rooted goal.
func themeGateServer(progress map[string]*campaign.NodeProgress, companionID string) (*ExtServer, *events.InMemoryPublisher) {
	g := mapsGoal("g-1", mapsPtr("c-root"))
	if companionID != "" {
		g.AttachedCompanionID = &companionID
	}
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

func TestSuggestionGenerate_FocalOnly_CarriesContainingGoalTheme(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000d7"
	s, pub := themeGateServer(map[string]*campaign.NodeProgress{"c-child": gateWon("c-child")}, fid)

	// NO goalId in the body — the containing goal is resolved server-side by
	// the same subtree walk the won-gate runs.
	w := gateGenerate(t, s, `{"focalConceptId":"c-child"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	p := csGeneratePayload(t, pub)
	if p["map_theme"] != "Algebra" {
		t.Errorf("map_theme = %v; want the containing goal's root title \"Algebra\"", p["map_theme"])
	}
	if p["familiar_id"] != fid {
		t.Errorf("companion_id = %v; want the containing goal's companion %q", p["familiar_id"], fid)
	}
}

func TestSuggestionGenerate_LooseFocal_StaysUnscoped(t *testing.T) {
	s, pub := themeGateServer(nil, "01970000-0000-7000-a000-0000000000d7")

	w := gateGenerate(t, s, `{"focalConceptId":"c-loose"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	p := csGeneratePayload(t, pub)
	if p["map_theme"] != "" {
		t.Errorf("map_theme = %v; want \"\" on loose (non-goal) territory — ADR-212 sovereignty unscoped", p["map_theme"])
	}
	if p["familiar_id"] != "" {
		t.Errorf("companion_id = %v; want \"\" (no containing goal)", p["familiar_id"])
	}
}
