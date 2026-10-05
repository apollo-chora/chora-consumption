// concept_suggestion_capb_test.go - ADR-247 Capability B (CHO-2328): the learner
// door (POST /v1/me/concept-graph/suggestions/generate) enriches the fog request
// with the focal node's sub-goal (F1), its diagnosed weakness (F2), and its goal +
// ancestor lineage (F3). Mirrors the concept_suggestion_gate_test fixture (a WON
// focal inside a rooted goal subtree) so the enrichment runs on the post-gate
// happy path. The wire keys themselves are pinned by the events wire-contract test;
// here we prove the DOOR resolves + carries them, and degrades without failing.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// capbWeaknessLoader is a narrow WeaknessContextLoader double: it returns a fixed
// context for a matching concept_key and records the key it was asked for (so a
// test proves the FOCAL key travels, not some other node's). A nil out ⇒ (nil,nil),
// the "no diagnosis" path.
type capbWeaknessLoader struct {
	wantKey string
	out     *lw.WeaknessContext
	gotKey  string
	calls   int
	err     error
}

func (l *capbWeaknessLoader) LoadWeaknessContextByConceptKey(_ context.Context, _, _, conceptKey string) (*lw.WeaknessContext, error) {
	l.calls++
	l.gotKey = conceptKey
	if l.err != nil {
		return nil, l.err
	}
	if l.wantKey != "" && conceptKey != l.wantKey {
		return nil, nil
	}
	return l.out, nil
}

// capbFocalNode is the focal concept with a sub-goal + a stable concept_key (the
// weakness join axis).
func capbFocalNode() *conceptgraph.ConceptNode {
	n := gateNode("c-focal", "Comparing fractions")
	n.SubGoal = "Compare two fractions by finding a common denominator"
	n.ConceptKey = "comparing-fractions"
	return n
}

// capbServer builds a generate-ready ExtServer whose focal (c-focal) sits under
// c-root -> c-mid -> c-focal and is WON, so refuseUnwonGoalReveal passes and the
// enrichment runs. weakness/edges are injected by the caller.
func capbServer(pub *events.InMemoryPublisher, loader lw.WeaknessContextLoader) *ExtServer {
	fid := "01970000-0000-7000-a000-0000000000d1"
	g := mapsGoal("g-1", mapsPtr("c-root"))
	g.AttachedCompanionID = &fid
	nodes := []*conceptgraph.ConceptNode{
		gateNode("c-root", "Fractions"),
		gateNode("c-mid", "Fraction basics"),
		capbFocalNode(),
	}
	edges := &reStubEdges{out: []*conceptgraph.Edge{
		gateHierarchyEdge("c-root", "c-mid"),
		gateHierarchyEdge("c-mid", "c-focal"),
	}}
	return &ExtServer{
		Concepts:         &gateConcepts{nodes: nodes},
		ConceptEdges:     edges,
		CampaignProgress: &gateProgress{rows: map[string]*campaign.NodeProgress{"c-focal": gateWon("c-focal")}},
		Goals:            &mapsGoalStub{byID: map[string]*goal.Goal{"g-1": g}},
		WeaknessLoader:   loader,
		Publisher:        pub,
	}
}

func capbGenerate(t *testing.T, s *ExtServer, pub *events.InMemoryPublisher) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleMeSuggestionGenerate(w, caReq(http.MethodPost,
		"/v1/me/concept-graph/suggestions/generate",
		`{"goalId":"g-1","focalConceptId":"c-focal"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	return csGeneratePayload(t, pub)
}

// Happy path: the door resolves sub_goal (F1), weakness (F2), and goal_title +
// ancestors (F3) and carries them on the requested.v1 payload.
func TestSuggestionGenerate_CapB_CarriesEnrichment(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	loader := &capbWeaknessLoader{
		wantKey: "comparing-fractions",
		out: &lw.WeaknessContext{
			Descriptor:     "Compares fractions by numerator alone",
			Misconceptions: []string{"bigger numerator means bigger fraction"},
			Evidence:       []string{"picked 3/8 over 1/2"},
		},
	}
	s := capbServer(pub, loader)
	p := capbGenerate(t, s, pub)

	if got := p["sub_goal"]; got != "Compare two fractions by finding a common denominator" {
		t.Errorf("sub_goal = %v", got)
	}
	if got := p["goal_title"]; got != "Fractions" {
		t.Errorf("goal_title = %v; want Fractions (root concept title)", got)
	}
	anc, ok := p["ancestors"].([]string)
	if !ok || len(anc) != 1 || anc[0] != "Fraction basics" {
		t.Errorf("ancestors = %#v; want [Fraction basics] (c-mid, root excluded)", p["ancestors"])
	}
	// The weakness join must use the FOCAL node's key, not a whole-map default.
	if loader.gotKey != "comparing-fractions" {
		t.Errorf("weakness loaded for key %q; want the focal concept_key", loader.gotKey)
	}
	w, ok := p["weakness"].(map[string]any)
	if !ok {
		t.Fatalf("weakness is not an object: %#v", p["weakness"])
	}
	if w["descriptor"] != "Compares fractions by numerator alone" {
		t.Errorf("weakness.descriptor = %v", w["descriptor"])
	}
	if misc, _ := w["misconceptions"].([]string); len(misc) != 1 || misc[0] != "bigger numerator means bigger fraction" {
		t.Errorf("weakness.misconceptions = %#v", w["misconceptions"])
	}
}

// Degrade: a nil WeaknessLoader (and, below, a no-match) must leave weakness null
// while the request still publishes with the other keys present - the enrichment
// is additive, never a gate.
func TestSuggestionGenerate_CapB_NilWeaknessLoaderDegradesToNull(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	s := capbServer(pub, nil) // no weakness loader wired
	p := capbGenerate(t, s, pub)

	// sub_goal + lineage still resolve (they do not depend on the weakness loader).
	if p["sub_goal"] != "Compare two fractions by finding a common denominator" {
		t.Errorf("sub_goal = %v; a nil weakness loader must not drop the sub-goal", p["sub_goal"])
	}
	if p["goal_title"] != "Fractions" {
		t.Errorf("goal_title = %v", p["goal_title"])
	}
	// weakness marshals to JSON null (the fog's no-diagnosis signal). The in-memory
	// value is a nil map held in an interface, so assert the WIRE, not identity.
	if wb, err := json.Marshal(p["weakness"]); err != nil {
		t.Fatalf("marshal weakness: %v", err)
	} else if string(wb) != "null" {
		t.Errorf("weakness marshalled to %s; want null with no loader", wb)
	}
	// ancestors is still a JSON array (never null).
	if _, ok := p["ancestors"].([]string); !ok {
		t.Errorf("ancestors is not a []string: %#v", p["ancestors"])
	}
}
