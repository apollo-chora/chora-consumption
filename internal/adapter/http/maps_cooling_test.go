// maps_cooling_test.go: C4 RED tests for the cooling counts on the Atlas read.
//
//	GET /v1/me/maps  -> each card gains coolingCount, coolingHexLabel,
//	                    attachedCompanionName; the envelope gains coolingPartial
//
// This is the read behind the home's defend_hex card (UX Track U, plan §4). It
// lives on the EXISTING Atlas endpoint rather than a new route for two reasons:
// the Atlas card is where a "needs defending" badge belongs anyway, and the
// Istio authz allowlist for chora-consumption is per-path, so a brand new
// top-level route would be admitted nowhere until an infra change landed
// (chora-infra/k8s/services/chora-consumption/authz-allow-gateway.yaml). One
// read model, two consumers, no dark route.
//
// # What counts as cooling, and why not what the card's brief said
//
// ADR-227 D8 is titled "Decay lives in the climb and gates advancement, NEVER
// holdings", and D15 puts the cooling cue on the FRONTIER state (claimed,
// climbing, refresh-gated), never on a province (won, permanent). So a cooling
// hex is an UNWON node with at least one cleared rung whose concept-key
// retention has fallen below campaign.RefreshThreshold. A won province cooling
// would contradict a ratified owner decision and needs an ADR amendment, not a
// handler that quietly invents decay on holdings.
//
// White-box (package http), reusing the sibling map-test fixtures.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	extinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// ---- fixture ----------------------------------------------------------------

// coolingConcepts: two disjoint rooted trees, so a per-goal count that leaked
// across goals is visible. Every node carries the concept_key that the campaign
// retention vocabulary is keyed on.
func coolingConcepts() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Algebra", ConceptKey: "algebra"},
		{ConceptID: "c-lin", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Linear Equations", ConceptKey: "linear"},
		{ConceptID: "c-quad", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Quadratics", ConceptKey: "quad"},
		{ConceptID: "d-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Poetry", ConceptKey: "poetry"},
		{ConceptID: "d-son", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Sonnets", ConceptKey: "sonnets"},
	}
}

func coolingEdges() []*conceptgraph.Edge {
	return []*conceptgraph.Edge{
		{EdgeID: "e-a1", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "c-root", TargetConceptID: "c-lin", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e-a2", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "c-root", TargetConceptID: "c-quad", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e-b1", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "d-root", TargetConceptID: "d-son", Class: conceptgraph.EdgeClassHierarchy},
	}
}

// coolingWarmRetention returns a retention repo where every named key was
// reviewed just now with a long strength, so R is comfortably above the refresh
// threshold: those nodes are NOT cooling.
func coolingWarmRetention(t *testing.T, now time.Time, keys ...string) topic_retention.Repository {
	t.Helper()
	repo := extinmem.NewTopicRetentionRepo()
	for _, k := range keys {
		s, err := topic_retention.New(fmTenantID, fmGCID, k, now, 30.0)
		if err != nil {
			t.Fatalf("seed retention %s: %v", k, err)
		}
		if err := repo.Save(context.Background(), s); err != nil {
			t.Fatalf("save retention %s: %v", k, err)
		}
	}
	return repo
}

// coolingInstances is an InstanceRepository double with only Get implemented:
// the Atlas read resolves the stationed companion's display name and touches
// nothing else on the port.
type coolingInstances struct {
	companion.InstanceRepository
	name string
	err  error
}

func (s *coolingInstances) Get(_ context.Context, companionID string) (*companion.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &companion.Instance{
		CompanionID: companionID, TenantID: fmTenantID, OwnerGCID: fmGCID, Name: s.name,
	}, nil
}

// coolingGoal builds a goal with an explicit id, root and status.
func coolingGoal(id, root string, status goal.Status) *goal.Goal {
	g := mapsGoal(id, mapsPtr(root))
	g.Status = status
	return g
}

// coolingServer wires the Atlas read with a campaign ladder and retention.
func coolingServer(goals []*goal.Goal, progress *mapsCampProgress, retention topic_retention.Repository) *ExtServer {
	byID := map[string]*goal.Goal{}
	for _, g := range goals {
		byID[g.GoalID] = g
	}
	return &ExtServer{
		Goals:            &mapsGoalStub{list: goals, byID: byID},
		Concepts:         &fmStubConcepts{out: coolingConcepts()},
		ConceptEdges:     &reStubEdges{out: coolingEdges()},
		CampaignProgress: progress,
		CampaignDose:     &CampaignDose{Retention: retention},
	}
}

// ---- wire shape -------------------------------------------------------------

type coolingCardWire struct {
	GoalID                string `json:"goalId"`
	CoolingCount          int    `json:"coolingCount"`
	CoolingHexLabel       string `json:"coolingHexLabel"`
	AttachedCompanionName string `json:"attachedCompanionName"`
}

type coolingListWire struct {
	Items          []coolingCardWire `json:"items"`
	CoolingPartial bool              `json:"coolingPartial"`
}

func coolingGet(t *testing.T, srv *ExtServer) coolingListWire {
	t.Helper()
	w := mapsServe(srv, http.MethodGet, "/v1/me/maps", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var out coolingListWire
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return out
}

func coolingCard(t *testing.T, list coolingListWire, goalID string) coolingCardWire {
	t.Helper()
	for _, c := range list.Items {
		if c.GoalID == goalID {
			return c
		}
	}
	t.Fatalf("goal %s absent from atlas", goalID)
	return coolingCardWire{}
}

// ---- tests ------------------------------------------------------------------

// The count is per goal and does not leak across goals: two rooted trees, a
// cooling node in each, one card each with a count of one.
func TestMapsCooling_CountIsPerGoalAndDoesNotLeak(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsClimbRow("c-lin", 2, 1), // cooling: no retention row at all
		mapsClimbRow("d-son", 1, 0), // cooling: no retention row at all
	}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive), coolingGoal("g-b", "d-root", goal.StatusActive)},
		progress, extinmem.NewTopicRetentionRepo())

	got := coolingGet(t, srv)
	if c := coolingCard(t, got, "g-a").CoolingCount; c != 1 {
		t.Errorf("g-a coolingCount = %d; want 1", c)
	}
	if c := coolingCard(t, got, "g-b").CoolingCount; c != 1 {
		t.Errorf("g-b coolingCount = %d; want 1", c)
	}
	if got.CoolingPartial {
		t.Errorf("coolingPartial = true on a clean read; want false")
	}
}

// ADR-227 D8: decay gates the climb and never holdings. A WON province is
// permanent, so it is never counted as cooling however stale its retention.
// Without this the card would invent decay on a holding, which the ADR forbids.
func TestMapsCooling_AWonProvinceIsNeverCooling(t *testing.T) {
	now := time.Now().UTC()
	won := now.Add(-90 * 24 * time.Hour)
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsWonRow("c-lin", won), // won long ago, no retention row: still not cooling
	}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)},
		progress, extinmem.NewTopicRetentionRepo())

	if c := coolingCard(t, coolingGet(t, srv), "g-a").CoolingCount; c != 0 {
		t.Errorf("coolingCount = %d; want 0 (ADR-227 D8: decay never touches holdings)", c)
	}
}

// An unstarted node (no ladder row) has nothing to refresh, and a warm climbing
// node is not cooling. Both are the negative controls that stop a count which
// simply totals the subtree.
func TestMapsCooling_UnstartedAndWarmNodesAreNotCooling(t *testing.T) {
	now := time.Now().UTC()
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsClimbRow("c-lin", 2, 1), // warm: retention seeded below
		// c-quad: no ladder row at all -> unstarted, nothing to refresh.
	}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)},
		progress, coolingWarmRetention(t, now, "linear"))

	if c := coolingCard(t, coolingGet(t, srv), "g-a").CoolingCount; c != 0 {
		t.Errorf("coolingCount = %d; want 0 (warm climber + unstarted node)", c)
	}
}

// The card names ONE hex so the home can say which province is cooling. The
// pick must be deterministic, or the sentence changes on every render for a
// learner with two cooling hexes.
func TestMapsCooling_HexLabelIsTheDeterministicPick(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsClimbRow("c-quad", 1, 0),
		mapsClimbRow("c-lin", 2, 1),
	}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)},
		progress, extinmem.NewTopicRetentionRepo())

	first := coolingCard(t, coolingGet(t, srv), "g-a")
	if first.CoolingCount != 2 {
		t.Fatalf("coolingCount = %d; want 2", first.CoolingCount)
	}
	if first.CoolingHexLabel != "Linear Equations" {
		t.Errorf("coolingHexLabel = %q; want %q (lowest conceptId: c-lin before c-quad)",
			first.CoolingHexLabel, "Linear Equations")
	}
	// Re-read: the same data must produce the same sentence.
	if second := coolingCard(t, coolingGet(t, srv), "g-a"); second.CoolingHexLabel != first.CoolingHexLabel {
		t.Errorf("coolingHexLabel moved between reads: %q then %q", first.CoolingHexLabel, second.CoolingHexLabel)
	}
}

// The home card names the defender, so the Atlas card carries the stationed
// companion's display name beside the id it already carried. Fail-soft: an
// unbound goal simply has none.
func TestMapsCooling_CardNamesTheStationedCompanion(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{mapsClimbRow("c-lin", 2, 1)}}
	g := coolingGoal("g-a", "c-root", goal.StatusActive)
	g.AttachedCompanionID = mapsPtr("f-1")
	srv := coolingServer([]*goal.Goal{g}, progress, extinmem.NewTopicRetentionRepo())
	srv.CompanionInstances = &coolingInstances{name: "Ember"}

	if n := coolingCard(t, coolingGet(t, srv), "g-a").AttachedCompanionName; n != "Ember" {
		t.Errorf("attachedCompanionName = %q; want Ember", n)
	}
}

func TestMapsCooling_AnUnboundGoalNamesNoCompanion(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{mapsClimbRow("c-lin", 2, 1)}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)},
		progress, extinmem.NewTopicRetentionRepo())

	if n := coolingCard(t, coolingGet(t, srv), "g-a").AttachedCompanionName; n != "" {
		t.Errorf("attachedCompanionName = %q; want empty (no companion stationed)", n)
	}
}

// A failed ladder read sets coolingPartial rather than serving zeroes. A zero
// count would tell the learner nothing is cooling on the strength of a read
// that never happened, and the home would rank the defend card away entirely.
// The Atlas itself still renders: the overlay is additive and never a 5xx.
func TestMapsCooling_AFailedLadderReadIsPartialNotZero(t *testing.T) {
	progress := &mapsCampProgress{listErr: errors.New("ladder repo down")}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)},
		progress, extinmem.NewTopicRetentionRepo())

	got := coolingGet(t, srv)
	if !got.CoolingPartial {
		t.Errorf("coolingPartial = false after a failed ladder read; want true")
	}
	if c := coolingCard(t, got, "g-a").CoolingCount; c != 0 {
		t.Errorf("coolingCount = %d; want 0 alongside the partial flag", c)
	}
}

// An unwired campaign lane is the same honest answer: partial, not zero.
func TestMapsCooling_AnUnwiredCampaignLaneIsPartial(t *testing.T) {
	srv := coolingServer([]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusActive)}, nil, nil)
	srv.CampaignProgress = nil

	if !coolingGet(t, srv).CoolingPartial {
		t.Errorf("coolingPartial = false with no campaign lane wired; want true")
	}
}

// A retired goal is the archive (ruling 1: retired IS archived). It is served in
// the Atlas list, but it must never contribute a defend card: nobody defends a
// map they put away.
func TestMapsCooling_ARetiredGoalCountsNoCoolingHexes(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{mapsClimbRow("c-lin", 2, 1)}}
	srv := coolingServer(
		[]*goal.Goal{coolingGoal("g-a", "c-root", goal.StatusRetired)},
		progress, extinmem.NewTopicRetentionRepo())

	if c := coolingCard(t, coolingGet(t, srv), "g-a").CoolingCount; c != 0 {
		t.Errorf("coolingCount = %d; want 0 (a retired map is archived, never defended)", c)
	}
}
