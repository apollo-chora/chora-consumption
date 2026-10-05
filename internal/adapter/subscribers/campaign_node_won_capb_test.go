// campaign_node_won_capb_test.go - ADR-247 Capability B (CHO-2328): the free-on-win
// reveal enriches its concept_suggestion.requested.v1 with the WON node's sub-goal
// (F1), diagnosed weakness (F2), and goal + ancestor lineage (F3), matching the
// learner door. The optional deps (weakness loader + edge repo) attach via
// WithWeakness / WithEdges; absent, the reveal must STILL publish (backward-compat),
// with weakness null + ancestors []. This is the producer the Suggestions tab
// actually rides (it is gated behind winning the hex), so its enrichment matters.
package subscribers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// nwWeakness is a narrow WeaknessContextLoader double: returns a fixed context for
// the matching concept_key (recording the key asked for), (nil,nil) otherwise.
type nwWeakness struct {
	wantKey string
	out     *lw.WeaknessContext
	gotKey  string
	calls   int
	err     error
}

func (l *nwWeakness) LoadWeaknessContextByConceptKey(_ context.Context, _, _, conceptKey string) (*lw.WeaknessContext, error) {
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

// nwEdges is a full EdgeRepository double serving a fixed edge slice from
// ListByLearner (the ancestor walk's only read).
type nwEdges struct {
	out []*conceptgraph.Edge
	err error
}

func (s *nwEdges) Create(context.Context, *conceptgraph.Edge) error { return nil }
func (s *nwEdges) GetByID(context.Context, string, string, string) (*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *nwEdges) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.out, s.err
}
func (s *nwEdges) ListByConcept(context.Context, string, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *nwEdges) Update(context.Context, *conceptgraph.Edge) error { return nil }
func (s *nwEdges) SoftDeleteByConcept(context.Context, string, string, string, time.Time) (int64, error) {
	return 0, nil
}

func nwHierEdge(parent, child string) *conceptgraph.Edge {
	return &conceptgraph.Edge{
		EdgeID: "e-" + parent + "-" + child, TenantID: nwTenant, LearnerGCID: nwGCID,
		SourceConceptID: parent, TargetConceptID: child, Class: conceptgraph.EdgeClassHierarchy,
	}
}

// nwFocalNode is the won concept with a sub-goal + concept_key (the weakness join
// axis). The payload's ConceptKey mirrors it (nwPayload uses "factorisation").
func nwFocalNode() *conceptgraph.ConceptNode {
	n := nwNode("c-child", "Factorisation")
	n.SubGoal = "Factor a quadratic into two binomials"
	n.ConceptKey = "factorisation"
	return n
}

func nwGoalRooted(root string) *goal.Goal {
	fid := "01970000-0000-7000-a000-0000000000f1"
	r := root
	return &goal.Goal{
		GoalID: nwGoal, TenantID: nwTenant, LearnerGCID: nwGCID,
		RootConceptID: &r, AttachedCompanionID: &fid, NorthStarNote: "Own algebra",
	}
}

// Happy path: with both optional deps wired, the reveal carries sub_goal (F1),
// weakness (F2), and goal_title + ancestors (F3), joined on the WON node's key.
func TestCampaignNodeWon_CapB_CarriesEnrichment(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	g := nwGoalRooted("c-root")
	nodes := []*conceptgraph.ConceptNode{
		nwNode("c-root", "Algebra"),
		nwNode("c-mid", "Polynomials"),
		nwFocalNode(),
	}
	loader := &nwWeakness{wantKey: "factorisation", out: &lw.WeaknessContext{
		Descriptor:     "Drops the middle term when factoring",
		Misconceptions: []string{"treats x^2+5x+6 as (x+5)(x+6)"},
		Evidence:       []string{"marked (x+2)(x+3) wrong"},
	}}
	edges := &nwEdges{out: []*conceptgraph.Edge{
		nwHierEdge("c-root", "c-mid"),
		nwHierEdge("c-mid", "c-child"),
	}}
	sub := NewCampaignNodeWonSubscriber(
		&nwLedger{},
		&nwGoals{byID: map[string]*goal.Goal{nwGoal: g}},
		&nwConcepts{nodes: nodes},
		pub,
		&nwRevealEmitter{},
	).WithWeakness(loader).WithEdges(edges)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	p := evs[0].Payload
	if p["sub_goal"] != "Factor a quadratic into two binomials" {
		t.Errorf("sub_goal = %v", p["sub_goal"])
	}
	if p["goal_title"] != "Algebra" {
		t.Errorf("goal_title = %v; want Algebra (root concept title)", p["goal_title"])
	}
	anc, ok := p["ancestors"].([]string)
	if !ok || len(anc) != 1 || anc[0] != "Polynomials" {
		t.Errorf("ancestors = %#v; want [Polynomials] (c-mid, root excluded)", p["ancestors"])
	}
	if loader.gotKey != "factorisation" {
		t.Errorf("weakness loaded for key %q; want the won node's concept_key", loader.gotKey)
	}
	w, ok := p["weakness"].(map[string]any)
	if !ok {
		t.Fatalf("weakness is not an object: %#v", p["weakness"])
	}
	if w["descriptor"] != "Drops the middle term when factoring" {
		t.Errorf("weakness.descriptor = %v", w["descriptor"])
	}
}

// Backward-compat: a subscriber with NO weakness/edges deps must still publish the
// reveal. sub_goal rides straight off the focal node (no dep), while weakness is
// JSON null and ancestors is [] - the additive keys are present, degraded.
func TestCampaignNodeWon_CapB_NilDepsStillPublish(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	g := nwGoalRooted("c-root")
	nodes := []*conceptgraph.ConceptNode{nwNode("c-root", "Algebra"), nwFocalNode()}
	// No WithWeakness / WithEdges: the optional deps are absent.
	sub := NewCampaignNodeWonSubscriber(
		&nwLedger{},
		&nwGoals{byID: map[string]*goal.Goal{nwGoal: g}},
		&nwConcepts{nodes: nodes},
		pub,
		&nwRevealEmitter{},
	)

	if err := sub.Handle(context.Background(), nwPayload("c-child")); err != nil {
		t.Fatalf("Handle: %v (nil optional deps must not fail the reveal)", err)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1 (the win must always reveal)", len(evs))
	}
	p := evs[0].Payload
	if p["sub_goal"] != "Factor a quadratic into two binomials" {
		t.Errorf("sub_goal = %v; the focal sub-goal must travel without the optional deps", p["sub_goal"])
	}
	if wb, err := json.Marshal(p["weakness"]); err != nil {
		t.Fatalf("marshal weakness: %v", err)
	} else if string(wb) != "null" {
		t.Errorf("weakness marshalled to %s; want null with no loader", wb)
	}
	anc, ok := p["ancestors"].([]string)
	if !ok || len(anc) != 0 {
		t.Errorf("ancestors = %#v; want [] with no edge repo", p["ancestors"])
	}
}
