// maps_campaign_block_test.go — WS-C7 (CHO-2086, ADR-227 D15/D16) RED tests
// for the campaign overlay on the map read:
//
//	GET /v1/me/maps/{goalId}/graph  → adds a fail-soft "campaign" block
//
// The block projects the campaign state over the goal subtree: frontier
// (total / won / canSeal), per-node ladder states (rungsCleared /
// currentRungCorrect / wonAt / advancedToday) and the cooling cue computed
// from the CAMPAIGN's own concept-key retention (never growthEdge.isDue).
// Present whenever the goal has a root; omitted (no key) on any fail-soft read
// error — the map still renders, never a 5xx. White-box (package http) — reuses
// fmStubConcepts / reStubEdges / fmTenantID / fmGCID + the mapsGoalStub.
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
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// ---- campaign ladder progress double (returns rows from ListByLearner) ------

type mapsCampProgress struct {
	rows    []*campaign.NodeProgress
	listErr error
}

func (r *mapsCampProgress) GetByConcept(_ context.Context, _, _, conceptID string) (*campaign.NodeProgress, error) {
	for _, p := range r.rows {
		if p.ConceptID == conceptID {
			return p, nil
		}
	}
	return nil, nil
}
func (r *mapsCampProgress) ListByLearner(context.Context, string, string) ([]*campaign.NodeProgress, error) {
	return r.rows, r.listErr
}
func (r *mapsCampProgress) Save(context.Context, *campaign.NodeProgress) error { return nil }

// ---- fixture ----------------------------------------------------------------

// mapsCampaignConcepts: the Algebra tree, each carrying a concept_key (the
// cooling retention vocabulary) + one out-of-tree node.
func mapsCampaignConcepts() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Algebra", ConceptKey: "algebra"},
		{ConceptID: "c-lin", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Linear Equations", ConceptKey: "linear"},
		{ConceptID: "c-quad", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Quadratics", ConceptKey: "quad"},
		{ConceptID: "c-other", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Poetry", ConceptKey: "poetry"},
	}
}

func mapsWonRow(conceptID string, wonAt time.Time) *campaign.NodeProgress {
	return &campaign.NodeProgress{
		ID: "r-" + conceptID, TenantID: fmTenantID, LearnerGCID: fmGCID,
		ConceptID: conceptID, RungsCleared: 6, WonAt: &wonAt,
	}
}

func mapsClimbRow(conceptID string, rungs, cur int) *campaign.NodeProgress {
	return &campaign.NodeProgress{
		ID: "r-" + conceptID, TenantID: fmTenantID, LearnerGCID: fmGCID,
		ConceptID: conceptID, RungsCleared: rungs, CurrentRungCorrect: cur,
	}
}

// mapsCampaignServer wires the map read + campaign ladder + retention. focus
// toggles the goal's assigned focus; sealedAt stamps campaign_sealed_at.
func mapsCampaignServer(t *testing.T, progress *mapsCampProgress, retention topic_retention.Repository, focus *string, sealedAt *time.Time) *ExtServer {
	t.Helper()
	g := mapsGoal("g-1", mapsPtr("c-root"))
	g.FocusConceptID = focus
	g.CampaignSealedAt = sealedAt
	return &ExtServer{
		Goals:            &mapsGoalStub{list: []*goal.Goal{g}, byID: map[string]*goal.Goal{"g-1": g}},
		Concepts:         &fmStubConcepts{out: mapsCampaignConcepts()},
		ConceptEdges:     &reStubEdges{out: mapsFixtureEdges()},
		CampaignProgress: progress,
		CampaignDose:     &CampaignDose{Retention: retention},
	}
}

// mapsGraphCampaign decodes the optional campaign block; ok=false when absent.
func mapsGraphCampaign(t *testing.T, body []byte) (block map[string]any, present bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v body=%s", err, string(body))
	}
	c, ok := raw["campaign"]
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal(c, &block); err != nil {
		t.Fatalf("decode campaign: %v", err)
	}
	return block, true
}

// ---- tests ------------------------------------------------------------------

// A rooted goal with campaign progress paints the block: frontier totals, the
// focus pointer, and per-node ladder states (won node carries wonAt; a node
// with no ladder row is absent = unstarted).
func TestMapsCampaign_BlockPaintsFrontierAndNodes(t *testing.T) {
	won := time.Now().UTC().Add(-time.Hour)
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsWonRow("c-root", won),
		mapsClimbRow("c-lin", 2, 1),
		// c-quad: no row → unstarted frontier (absent from nodes).
		mapsWonRow("c-other", won), // out of tree → must NOT count
	}}
	focus := "c-lin"
	srv := mapsCampaignServer(t, progress, extinmem.NewTopicRetentionRepo(), &focus, nil)

	w := mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	block, present := mapsGraphCampaign(t, w.Body.Bytes())
	if !present {
		t.Fatalf("campaign block must be present for a rooted goal")
	}
	if block["focusConceptId"] != "c-lin" {
		t.Errorf("focusConceptId = %v; want c-lin", block["focusConceptId"])
	}
	if block["frontierTotal"] != float64(3) {
		t.Errorf("frontierTotal = %v; want 3 (root+lin+quad, NOT out-of-tree)", block["frontierTotal"])
	}
	if block["frontierWon"] != float64(1) {
		t.Errorf("frontierWon = %v; want 1 (c-root)", block["frontierWon"])
	}
	if block["canSeal"] != false {
		t.Errorf("canSeal = %v; want false (frontier not empty)", block["canSeal"])
	}
	nodes, _ := block["nodes"].(map[string]any)
	if nodes == nil {
		t.Fatalf("nodes map missing: %v", block)
	}
	if _, ok := nodes["c-quad"]; ok {
		t.Errorf("unstarted node (no ladder row) must be ABSENT from nodes")
	}
	if _, ok := nodes["c-other"]; ok {
		t.Errorf("out-of-tree node must never appear in the map campaign block")
	}
	root, _ := nodes["c-root"].(map[string]any)
	if root == nil || root["wonAt"] == nil || root["rungsCleared"] != float64(6) {
		t.Errorf("c-root node = %v; want won 6/6 with wonAt", root)
	}
	lin, _ := nodes["c-lin"].(map[string]any)
	if lin == nil || lin["rungsCleared"] != float64(2) || lin["currentRungCorrect"] != float64(1) {
		t.Errorf("c-lin node = %v; want climbing 2 rungs, counter 1", lin)
	}
	if lin["wonAt"] != nil {
		t.Errorf("c-lin is unwon; wonAt must be absent")
	}
}

// Cooling: an unwon climbing node with NO concept-key retention row cools
// (conservative refresher, mirrors DecideServe); a freshly-reviewed node does
// not.
func TestMapsCampaign_CoolingFromCampaignRetention(t *testing.T) {
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{mapsClimbRow("c-lin", 2, 0)}}

	// (a) no retention row for "linear" → cooling.
	srv := mapsCampaignServer(t, progress, extinmem.NewTopicRetentionRepo(), nil, nil)
	block, _ := mapsGraphCampaign(t, mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true).Body.Bytes())
	lin := block["nodes"].(map[string]any)["c-lin"].(map[string]any)
	if lin["cooling"] != true {
		t.Errorf("absent retention row on a climbing node must cool: %v", lin)
	}

	// (b) a fresh (just-reviewed) retention row for "linear" → not cooling.
	ret := extinmem.NewTopicRetentionRepo()
	score, err := topic_retention.New(fmTenantID, fmGCID, "linear", time.Now().UTC(), 5.0)
	if err != nil {
		t.Fatalf("New score: %v", err)
	}
	if err := ret.Save(context.Background(), score); err != nil {
		t.Fatalf("save score: %v", err)
	}
	srv2 := mapsCampaignServer(t, progress, ret, nil, nil)
	block2, _ := mapsGraphCampaign(t, mapsServe(srv2, http.MethodGet, "/v1/me/maps/g-1/graph", true).Body.Bytes())
	lin2 := block2["nodes"].(map[string]any)["c-lin"].(map[string]any)
	if lin2["cooling"] != false {
		t.Errorf("a warm (R>=0.6) retention row must NOT cool: %v", lin2)
	}
}

// canSeal is true once every live subtree node is won.
func TestMapsCampaign_CanSealWhenFrontierEmpty(t *testing.T) {
	won := time.Now().UTC().Add(-time.Hour)
	sealedAt := won.Add(-48 * time.Hour)
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{
		mapsWonRow("c-root", won), mapsWonRow("c-lin", won), mapsWonRow("c-quad", won),
	}}
	srv := mapsCampaignServer(t, progress, extinmem.NewTopicRetentionRepo(), nil, &sealedAt)

	block, _ := mapsGraphCampaign(t, mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true).Body.Bytes())
	if block["frontierWon"] != float64(3) || block["canSeal"] != true {
		t.Errorf("all-won frontier must be sealable: %v", block)
	}
	if block["campaignSealedAt"] == nil {
		t.Errorf("campaignSealedAt must surface on the block: %v", block)
	}
}

// advancedToday reflects the D7 pacing date.
func TestMapsCampaign_AdvancedToday(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	row := mapsClimbRow("c-lin", 1, 0)
	row.LastAdvanceDate = &today
	progress := &mapsCampProgress{rows: []*campaign.NodeProgress{row}}
	srv := mapsCampaignServer(t, progress, extinmem.NewTopicRetentionRepo(), nil, nil)

	block, _ := mapsGraphCampaign(t, mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true).Body.Bytes())
	lin := block["nodes"].(map[string]any)["c-lin"].(map[string]any)
	if lin["advancedToday"] != true {
		t.Errorf("advancedToday = %v; want true (LastAdvanceDate == today)", lin["advancedToday"])
	}
}

// Fail-soft: no CampaignProgress wired → the map still renders, campaign key
// omitted (never a 5xx).
func TestMapsCampaign_UnwiredProgress_OmitsBlock(t *testing.T) {
	w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if _, present := mapsGraphCampaign(t, w.Body.Bytes()); present {
		t.Errorf("campaign block must be omitted when the ladder repo is unwired")
	}
}

// Fail-soft: a ladder read error omits the block (the map still 200s).
func TestMapsCampaign_LadderReadError_OmitsBlock(t *testing.T) {
	progress := &mapsCampProgress{listErr: errors.New("boom")}
	srv := mapsCampaignServer(t, progress, extinmem.NewTopicRetentionRepo(), nil, nil)
	w := mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; campaign read failure must not 5xx the map", w.Code)
	}
	if _, present := mapsGraphCampaign(t, w.Body.Bytes()); present {
		t.Errorf("a ladder read error must omit the campaign block (fail-soft)")
	}
}

// A rootless goal never carries a campaign block (the read returns early).
func TestMapsCampaign_RootlessGoal_NoBlock(t *testing.T) {
	g := mapsGoal("g-nr", nil)
	srv := mapsCampaignServer(t, &mapsCampProgress{}, extinmem.NewTopicRetentionRepo(), nil, nil)
	srv.Goals = &mapsGoalStub{byID: map[string]*goal.Goal{"g-nr": g}}
	w := mapsServe(srv, http.MethodGet, "/v1/me/maps/g-nr/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if _, present := mapsGraphCampaign(t, w.Body.Bytes()); present {
		t.Errorf("a rootless goal must carry no campaign block")
	}
}
