package http

import (
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

func leMapStrPtr(s string) *string { return &s }

// CHO-2038 3a.2 (effect-b): the painted map projection surfaces the ceremony
// learning-edge intent so /a/knowledge can render "Both, labelled" (remediate vs
// explore). Orthogonal to the evidenced Growth-Edge overlay.
func TestToConceptGraphRespPainted_SurfacesIntent(t *testing.T) {
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: "c1", Title: "Adding fractions", Intent: conceptgraph.IntentRemediate},
		{ConceptID: "c2", Title: "Continued fractions", Intent: conceptgraph.IntentExplore},
		{ConceptID: "c3", Title: "Plain"},
	}
	resp := toConceptGraphRespPainted(nodes, nil, nil, nil, nil)
	if len(resp.Concepts) != 3 {
		t.Fatalf("concepts = %d; want 3", len(resp.Concepts))
	}
	if resp.Concepts[0].Intent != "remediate" {
		t.Errorf("c1 intent = %q; want remediate", resp.Concepts[0].Intent)
	}
	if resp.Concepts[1].Intent != "explore" {
		t.Errorf("c2 intent = %q; want explore", resp.Concepts[1].Intent)
	}
	if resp.Concepts[2].Intent != "" {
		t.Errorf("c3 intent = %q; want empty (plain concept)", resp.Concepts[2].Intent)
	}
}

// A ceremony remediate learning-edge counts as shaky on the Atlas card even with
// NO evidenced weakness overlay (it complements the learner_weakness paint); an
// explore edge does not.
func TestBuildMapCard_CountsRemediateIntentAsShaky(t *testing.T) {
	root := &conceptgraph.ConceptNode{ConceptID: "root", Title: "Maths"}
	remediate := &conceptgraph.ConceptNode{ConceptID: "rm", Title: "Fractions", Intent: conceptgraph.IntentRemediate}
	explore := &conceptgraph.ConceptNode{ConceptID: "ex", Title: "Continued fractions", Intent: conceptgraph.IntentExplore}
	concepts := []conceptgraph.ConceptNode{*root, *remediate, *explore}
	edges := []conceptgraph.Edge{
		{EdgeID: "e1", SourceConceptID: "root", TargetConceptID: "rm", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e2", SourceConceptID: "root", TargetConceptID: "ex", Class: conceptgraph.EdgeClassHierarchy},
	}
	byID := map[string]*conceptgraph.ConceptNode{"root": root, "rm": remediate, "ex": explore}
	g := goal.Goal{GoalID: "goal", Kind: goal.KindCuriosity, Status: goal.StatusActive, RootConceptID: leMapStrPtr("root")}

	card := buildMapCard(g, concepts, edges, byID, nil, nil, nil, nil)
	if card.ConceptCount != 3 {
		t.Fatalf("conceptCount = %d; want 3", card.ConceptCount)
	}
	if card.ShakyCount != 1 {
		t.Errorf("shakyCount = %d; want 1 (the remediate edge; explore is not shaky)", card.ShakyCount)
	}
}
