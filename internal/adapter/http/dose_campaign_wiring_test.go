// dose_campaign_wiring_test.go — CHO-2081 / ADR-227 D12 (WS-C2): the campaign
// axis adapters. Compose side: the daily-dose handler elects TODAY's one
// campaign (goals with a focus node, dose-pref exclusions honoured, won nodes
// skipped) and threads a CampaignInput into the composer — fail-soft, the
// dose NEVER breaks on campaign enrichment. Answer side: the server-graded
// /v1/me/atom-sessions/{id}/answers door folds a graded answer on today's
// campaign material into the campaign Grader — fail-loud (verified progress
// must never be silently lost), with ErrAlreadyWon a benign no-op.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	extinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---------- fakes ----------

type campGoalRepo struct {
	goals   []*goal.Goal
	listErr error
}

func (r *campGoalRepo) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	return r.goals, r.listErr
}

type campConceptRepo struct {
	nodes map[string]*conceptgraph.ConceptNode
}

func (r *campConceptRepo) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (r *campConceptRepo) GetByID(_ context.Context, _, _, conceptID string) (*conceptgraph.ConceptNode, error) {
	return r.nodes[conceptID], nil
}
func (r *campConceptRepo) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (r *campConceptRepo) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

type campProgressRepo struct {
	rows      map[string]*campaign.NodeProgress // keyed by concept_id
	getErr    error
	saveErr   error
	saveCount int
}

func (r *campProgressRepo) GetByConcept(_ context.Context, _, _ string, conceptID string) (*campaign.NodeProgress, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.rows[conceptID], nil
}
func (r *campProgressRepo) ListByLearner(context.Context, string, string) ([]*campaign.NodeProgress, error) {
	return nil, nil
}
func (r *campProgressRepo) Save(_ context.Context, p *campaign.NodeProgress) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saveCount++
	if r.rows == nil {
		r.rows = map[string]*campaign.NodeProgress{}
	}
	r.rows[p.ConceptID] = p
	return nil
}

type campPrefRepo struct {
	excluded map[string]bool
}

func (r *campPrefRepo) List(context.Context, string, string) ([]dose_pref.DoseKGPref, error) {
	return nil, nil
}
func (r *campPrefRepo) ExcludedMapIDs(context.Context, string, string) (map[string]bool, error) {
	return r.excluded, nil
}
func (r *campPrefRepo) Set(_ context.Context, _, _, _ string, _ bool, _ time.Time) (dose_pref.DoseKGPref, error) {
	return dose_pref.DoseKGPref{}, nil
}

type campSink struct {
	rungCleared []campaign.RungClearedEvent
	nodeWon     []campaign.NodeWonEvent
}

func (s *campSink) CampaignRungCleared(_ context.Context, e campaign.RungClearedEvent) error {
	s.rungCleared = append(s.rungCleared, e)
	return nil
}
func (s *campSink) CampaignNodeWon(_ context.Context, e campaign.NodeWonEvent) error {
	s.nodeWon = append(s.nodeWon, e)
	return nil
}

// ---------- fixture ----------

const (
	campGoalID    = "01980000-0000-7000-8000-00000000c0a1"
	campGoalB     = "01980000-0000-7000-8000-00000000c0b2"
	campConceptID = "01980000-0000-7000-9000-00000000cc01"
	campConceptB  = "01980000-0000-7000-9000-00000000cc02"
)

func campGoal(goalID, conceptID string) *goal.Goal {
	focus := conceptID
	return &goal.Goal{
		GoalID:         goalID,
		TenantID:       testTenant,
		LearnerGCID:    testGCID,
		FocusConceptID: &focus,
	}
}

// newCampaignDose builds a CampaignDose over fakes + a REAL grader (so serve
// decisions + folds run the true WS-C1 domain core).
func newCampaignDose(t *testing.T, goals *campGoalRepo, concepts *campConceptRepo, progress *campProgressRepo, prefs dose_pref.Repository) (*CampaignDose, *campSink) {
	t.Helper()
	sink := &campSink{}
	retention := extinmem.NewTopicRetentionRepo()
	grader, err := campaign.NewGrader(campaign.GraderConfig{
		Progress:  progress,
		Retention: retention,
		Events:    sink,
	})
	if err != nil {
		t.Fatalf("NewGrader: %v", err)
	}
	return &CampaignDose{
		Goals:     goals,
		Prefs:     prefs,
		Progress:  progress,
		Retention: retention,
		Concepts:  concepts,
		Grader:    grader,
	}, sink
}

// campaignServer builds a dose Server with the real-atom universe + a
// campaign whose focus node references the first two universe atoms.
func campaignServer(t *testing.T) (*Server, *campProgressRepo) {
	t.Helper()
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.ComposerV2Enabled = true

	goals := &campGoalRepo{goals: []*goal.Goal{campGoal(campGoalID, campConceptID)}}
	concepts := &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {
			ConceptID:   campConceptID,
			TenantID:    testTenant,
			LearnerGCID: testGCID,
			Title:       "CSPO Basics",
			ConceptKey:  "cspo-basics", // no topic slug match — candidates carry the slot
			AtomRefs:    []string{realDoseAtomIDs[0], realDoseAtomIDs[1]},
		},
	}}
	progress := &campProgressRepo{}
	dose, _ := newCampaignDose(t, goals, concepts, progress, nil)
	srv.CampaignDose = dose
	return srv, progress
}

type campDoseResp struct {
	Entries []struct {
		AtomID             string `json:"atom_id"`
		DoseReason         string `json:"dose_reason"`
		CampaignGoalID     string `json:"campaign_goal_id"`
		CampaignConceptID  string `json:"campaign_concept_id"`
		CampaignConceptKey string `json:"campaign_concept_key"`
		CampaignRung       int    `json:"campaign_rung"`
	} `json:"entries"`
}

func decodeCampDose(t *testing.T, w *httptest.ResponseRecorder) campDoseResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp campDoseResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// ---------- compose side ----------

func TestDailyDose_CampaignDay_ServesFocusNodeCandidates(t *testing.T) {
	srv, _ := campaignServer(t)
	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))

	var camp []string
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" {
			camp = append(camp, e.AtomID)
			if e.CampaignGoalID != campGoalID || e.CampaignConceptID != campConceptID ||
				e.CampaignConceptKey != "cspo-basics" || e.CampaignRung != 1 {
				t.Fatalf("campaign metadata wrong: %+v", e)
			}
		}
	}
	if len(camp) != 2 || camp[0] != realDoseAtomIDs[0] || camp[1] != realDoseAtomIDs[1] {
		t.Fatalf("campaign picks = %v, want the focus node's two atom refs in order", camp)
	}
}

func TestDailyDose_NoFocus_ByteIdenticalToUnwired(t *testing.T) {
	// Same universe, campaign wired but NO goal has a focus → the response
	// must be byte-identical to a server with no campaign wiring at all.
	wired := NewServer()
	seedRealAtomUniverse(t, wired, realDoseAtomIDs)
	wired.ComposerV2Enabled = true
	goals := &campGoalRepo{goals: []*goal.Goal{{GoalID: campGoalID, TenantID: testTenant, LearnerGCID: testGCID}}}
	dose, _ := newCampaignDose(t, goals, &campConceptRepo{}, &campProgressRepo{}, nil)
	wired.CampaignDose = dose

	bare := NewServer()
	seedRealAtomUniverse(t, bare, realDoseAtomIDs)
	bare.ComposerV2Enabled = true

	wiredBody := stripGeneratedAt(authedReq(t, wired, http.MethodGet, "/companion/daily-dose", nil).Body.String())
	bareBody := stripGeneratedAt(authedReq(t, bare, http.MethodGet, "/companion/daily-dose", nil).Body.String())
	if wiredBody != bareBody {
		t.Fatalf("no-focus dose must degenerate byte-identically\nwired=%s\nbare =%s", wiredBody, bareBody)
	}
	if strings.Contains(wiredBody, "campaign") {
		t.Fatalf("no-focus dose must carry no campaign keys: %s", wiredBody)
	}
}

// stripGeneratedAt blanks the per-request wall-clock timestamp so two
// responses composed microseconds apart can be compared byte-for-byte.
func stripGeneratedAt(body string) string {
	return regexp.MustCompile(`"generated_at":"[^"]+"`).ReplaceAllString(body, `"generated_at":""`)
}

func TestDailyDose_ExcludedGoal_NeverElected(t *testing.T) {
	srv := NewServer()
	seedRealAtomUniverse(t, srv, realDoseAtomIDs)
	srv.ComposerV2Enabled = true

	goals := &campGoalRepo{goals: []*goal.Goal{
		campGoal(campGoalID, campConceptID),
		campGoal(campGoalB, campConceptB),
	}}
	concepts := &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "CSPO Basics", ConceptKey: "cspo-basics", AtomRefs: []string{realDoseAtomIDs[0]}},
		campConceptB: {ConceptID: campConceptB, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "Sprint Reviews", ConceptKey: "sprint-reviews", AtomRefs: []string{realDoseAtomIDs[2]}},
	}}
	dose, _ := newCampaignDose(t, goals, concepts, &campProgressRepo{},
		&campPrefRepo{excluded: map[string]bool{campGoalID: true}})
	srv.CampaignDose = dose

	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" && e.CampaignGoalID != campGoalB {
			t.Fatalf("excluded goal was elected: %+v", e)
		}
	}
}

func TestDailyDose_WonFocus_DegeneratesToNoCampaign(t *testing.T) {
	srv, progress := campaignServer(t)
	p, err := campaign.NewNodeProgress(testTenant, testGCID, campConceptID, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewNodeProgress: %v", err)
	}
	won := time.Now().UTC()
	p.WonAt = &won
	p.RungsCleared = 6
	progress.rows = map[string]*campaign.NodeProgress{campConceptID: p}

	body := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil).Body.String()
	if strings.Contains(body, "campaign") {
		t.Fatalf("won focus node must not campaign: %s", body)
	}
}

func TestDailyDose_GoalsReadError_FailsSoft(t *testing.T) {
	srv, _ := campaignServer(t)
	srv.CampaignDose.Goals = &campGoalRepo{listErr: errors.New("boom")}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("campaign enrichment failure must not break the dose: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "campaign") {
		t.Fatalf("failed enrichment must degenerate cleanly: %s", w.Body.String())
	}
}

func TestDailyDose_FocusedPracticeParam_WinsOverCampaign(t *testing.T) {
	srv, _ := campaignServer(t)
	// ?growth_edge_id routes to focused practice; the campaign axis must not fire.
	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose?growth_edge_id=nonexistent", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "campaign") {
		t.Fatalf("focused practice must bypass the campaign axis: %s", w.Body.String())
	}
}

func TestDailyDose_UnenrolledAtomRef_AugmentsUniverse(t *testing.T) {
	// An indexed, gradable atom OUTSIDE the enrolled universe, referenced by
	// the focus node: the campaign must augment the seed universe with it
	// (mirrors the focused-practice CHO-1895 augment) so the march is
	// servable even when its material is not on an enrolled LearningPath.
	const extraAtom = "01980000-0000-7000-a000-00000000aaaa"
	srv, _ := campaignServer(t)
	saveAtomProjection(t, srv.AtomIndex, extraAtom, "Extra Drill", "unrelated-topic", atom_index.StatusPublished)
	srv.CampaignDose.Concepts = &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "CSPO Basics", ConceptKey: "cspo-basics", AtomRefs: []string{extraAtom}},
	}}

	resp := decodeCampDose(t, authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil))
	found := false
	for _, e := range resp.Entries {
		if e.DoseReason == "campaign" && e.AtomID == extraAtom {
			found = true
		}
	}
	if !found {
		t.Fatalf("unenrolled atom ref must be augmented + served, entries=%+v", resp.Entries)
	}
}

// ---------- answer side ----------

// campaignExt builds an ExtServer whose answers door grades meAtomID (opt-a
// correct) and whose campaign focus node references meAtomID.
func campaignExt(t *testing.T) (*ExtServer, *campProgressRepo, *campSink) {
	t.Helper()
	srv := NewExtServer(nil)

	// Index the answered atom (published MCQ, answer key opt-a).
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		CourseID:        meCourseID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished, // CHO-2273 — learner take gates on Playable()
		PublishedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("atom_index.New: %v", err)
	}
	if err := srv.AtomIndex.Save(context.Background(), a); err != nil {
		t.Fatalf("index save: %v", err)
	}

	goals := &campGoalRepo{goals: []*goal.Goal{campGoal(campGoalID, campConceptID)}}
	concepts := &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: meTenantID, LearnerGCID: meGCID,
			Title: "CSPO Basics", ConceptKey: "cspo-basics", AtomRefs: []string{meAtomID}},
	}}
	progress := &campProgressRepo{}
	dose, sink := newCampaignDose(t, goals, concepts, progress, nil)
	srv.CampaignDose = dose
	return srv, progress, sink
}

func startMeSession(t *testing.T, srv *ExtServer) string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	if w.Code != http.StatusCreated {
		t.Fatalf("start session: %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.SessionID
}

func submitCampAnswer(t *testing.T, srv *ExtServer, sessionID, answerID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest("POST", "/v1/me/atom-sessions/"+sessionID+"/answers", map[string]any{
		"answer_id":          answerID,
		"selected_option_id": "opt-a",
	}))
	return w
}

func TestMeAnswer_CampaignAtom_FoldsLadder(t *testing.T) {
	srv, progress, sink := campaignExt(t)

	sid := startMeSession(t, srv)
	if w := submitCampAnswer(t, srv, sid, "ans-1"); w.Code != http.StatusOK {
		t.Fatalf("answer 1: %d body=%s", w.Code, w.Body.String())
	}
	p := progress.rows[campConceptID]
	if p == nil || p.CurrentRungCorrect != 1 {
		t.Fatalf("first correct answer must fold the ladder (counter=1), got %+v", p)
	}

	sid2 := startMeSession(t, srv)
	if w := submitCampAnswer(t, srv, sid2, "ans-2"); w.Code != http.StatusOK {
		t.Fatalf("answer 2: %d body=%s", w.Code, w.Body.String())
	}
	p = progress.rows[campConceptID]
	if p == nil || p.RungsCleared != 1 {
		t.Fatalf("second correct answer must clear rung 1, got %+v", p)
	}
	if len(sink.rungCleared) != 1 || sink.rungCleared[0].GoalID != campGoalID {
		t.Fatalf("rung_cleared must emit once with the goal id, got %+v", sink.rungCleared)
	}
	// Retention row planted under the node's concept_key (addendum #2).
	score, err := srv.CampaignDose.Retention.Get(context.Background(), meTenantID, meGCID, "cspo-basics")
	if err != nil || score == nil {
		t.Fatalf("campaign grading must plant the concept-key retention row, got %v err=%v", score, err)
	}
}

// CHO-2315: the growth-edge dose answer must SURFACE the campaign fold outcome
// (won / cleared / paced / counted), not just apply it silently. The hex-tap
// lane already returns it; the dose lane discarded it before this.
func TestMeAnswer_CampaignAtom_SurfacesOutcome(t *testing.T) {
	srv, _, _ := campaignExt(t)

	type campBlock struct {
		ConceptKey  string `json:"concept_key"`
		Rung        int    `json:"rung"`
		ClearedRung int    `json:"cleared_rung"`
		Won         bool   `json:"won"`
		PacedToday  bool   `json:"paced_today"`
		Counted     bool   `json:"counted"`
	}
	answer := func(answerID string) *campBlock {
		t.Helper()
		w := submitCampAnswer(t, srv, startMeSession(t, srv), answerID)
		if w.Code != http.StatusOK {
			t.Fatalf("answer %s: %d body=%s", answerID, w.Code, w.Body.String())
		}
		var resp struct {
			Campaign *campBlock `json:"campaign"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp.Campaign
	}

	// First correct answer: counted at the frontier rung; the clear threshold
	// (2) is not yet met, so nothing clears. The dose lane must SEE this.
	c1 := answer("ans-1")
	if c1 == nil {
		t.Fatalf("dose answer must surface the campaign fold outcome, got no campaign block")
	}
	if !c1.Counted || c1.ClearedRung != 0 || c1.Won || c1.PacedToday || c1.ConceptKey != "cspo-basics" {
		t.Fatalf("first correct: want counted=true cleared=0 key=cspo-basics, got %+v", c1)
	}
	if c1.Rung < 1 {
		t.Fatalf("first correct: want a valid frontier rung, got %+v", c1)
	}

	// Second correct answer: threshold met, first advance of the day → rung clears.
	c2 := answer("ans-2")
	if c2 == nil || c2.ClearedRung != 1 {
		t.Fatalf("second correct must surface cleared_rung=1, got %+v", c2)
	}
}

func TestMeAnswer_NonCampaignAtom_NoFold(t *testing.T) {
	srv, progress, sink := campaignExt(t)
	// Repoint the focus node away from the answered atom (no ref, no slug match).
	srv.CampaignDose.Concepts = &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {ConceptID: campConceptID, TenantID: meTenantID, LearnerGCID: meGCID,
			Title: "Elsewhere", ConceptKey: "elsewhere", AtomRefs: []string{campConceptB}},
	}}

	sid := startMeSession(t, srv)
	w := submitCampAnswer(t, srv, sid, "ans-1")
	if w.Code != http.StatusOK {
		t.Fatalf("answer: %d", w.Code)
	}
	if progress.saveCount != 0 || len(sink.rungCleared) != 0 {
		t.Fatalf("non-campaign atom must not fold: saves=%d events=%+v", progress.saveCount, sink.rungCleared)
	}
	// No fold → the dose answer must omit the campaign block entirely (CHO-2315).
	var resp struct {
		Campaign json.RawMessage `json:"campaign"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Campaign) != 0 {
		t.Fatalf("non-campaign answer must omit the campaign block, got %s", resp.Campaign)
	}
}

func TestMeAnswer_DuplicateReplay_NoDoubleFold(t *testing.T) {
	srv, progress, _ := campaignExt(t)

	sid := startMeSession(t, srv)
	if w := submitCampAnswer(t, srv, sid, "ans-dup"); w.Code != http.StatusOK {
		t.Fatalf("answer: %d", w.Code)
	}
	saves := progress.saveCount
	if w := submitCampAnswer(t, srv, sid, "ans-dup"); w.Code != http.StatusOK {
		t.Fatalf("replay: %d", w.Code)
	}
	if progress.saveCount != saves {
		t.Fatalf("idempotent replay must not re-fold the ladder: %d → %d", saves, progress.saveCount)
	}
}

func TestMeAnswer_WonNode_BenignNoop(t *testing.T) {
	srv, progress, sink := campaignExt(t)
	p, err := campaign.NewNodeProgress(meTenantID, meGCID, campConceptID, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewNodeProgress: %v", err)
	}
	won := time.Now().UTC()
	p.WonAt = &won
	p.RungsCleared = 6
	progress.rows = map[string]*campaign.NodeProgress{campConceptID: p}

	sid := startMeSession(t, srv)
	if w := submitCampAnswer(t, srv, sid, "ans-1"); w.Code != http.StatusOK {
		t.Fatalf("a won node must be a benign no-op, got %d body=%s", w.Code, w.Body.String())
	}
	if len(sink.rungCleared)+len(sink.nodeWon) != 0 {
		t.Fatalf("won node must not emit, got %+v %+v", sink.rungCleared, sink.nodeWon)
	}
}

func TestMeAnswer_FoldFailure_FailsLoud(t *testing.T) {
	srv, progress, _ := campaignExt(t)
	progress.getErr = errors.New("db down")

	sid := startMeSession(t, srv)
	w := submitCampAnswer(t, srv, sid, "ans-1")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a campaign fold failure must fail loud (500), got %d body=%s", w.Code, w.Body.String())
	}
}
