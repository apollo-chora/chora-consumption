// goals_campaign_answers_handler_test.go — WS-C7 (CHO-2086, ADR-227 D13
// addendum #3) RED tests for the campaign answers door:
//
//	POST /v1/me/goals/{id}/campaign/questions/answer
//
// Server-side ONLY: the stored question set is graded by option IDENTITY
// against its verbatim BatchCandidatePayload answer key (never client-graded),
// then folded into the campaign ladder via the WS-C1 Grader. White-box
// (package http) so it reuses the WS-C2 fixtures (campConceptRepo /
// campProgressRepo / newCampaignDose) + the questions-door goal/bank fakes
// (cqserveGoalRepo / cqserveBank) + the shared subtree stubs (fmStubConcepts /
// reStubEdges).
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// A realistic BatchCandidatePayload: one MCQ, correct option "b".
const cqanswerPayload = `{"candidates":[{"stem":"2+2?","question_type":"mcq","options":[
	{"option_id":"a","label":"A","text":"3","is_correct":false,"explainer":"too low"},
	{"option_id":"b","label":"B","text":"4","is_correct":true,"explainer":"exactly four"}]}]}`

const (
	cqanswerRoot = "01980000-0000-7000-9000-00000000ca00" // goal root
	cqanswerNode = "01980000-0000-7000-9000-00000000ca01" // answered node (in subtree)
	cqanswerOff  = "01980000-0000-7000-9000-00000000ca09" // live node OUTSIDE the subtree
)

// cqanswerGoal builds the learner's rooted goal (tenant/gcid matching
// newMeRequest so the leak guard admits it).
func cqanswerGoal() *goal.Goal {
	root := cqanswerRoot
	return &goal.Goal{GoalID: cqserveGoalID, TenantID: meTenantID, LearnerGCID: meGCID, RootConceptID: &root}
}

// newCQAnswerServer wires the answers door over a real grader (WS-C2 fixtures)
// + a subtree (root -> node) + a live off-map node.
func newCQAnswerServer(t *testing.T, progress *campProgressRepo, bank *cqserveBank) *ExtServer {
	t.Helper()
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: cqanswerRoot, TenantID: meTenantID, LearnerGCID: meGCID, Title: "Algebra", ConceptKey: "algebra"},
		{ConceptID: cqanswerNode, TenantID: meTenantID, LearnerGCID: meGCID, Title: "CSPO Basics", ConceptKey: "cspo-basics"},
		{ConceptID: cqanswerOff, TenantID: meTenantID, LearnerGCID: meGCID, Title: "Off-map", ConceptKey: "off-map"},
	}
	edges := []*conceptgraph.Edge{
		{EdgeID: "e-ca", TenantID: meTenantID, LearnerGCID: meGCID,
			SourceConceptID: cqanswerRoot, TargetConceptID: cqanswerNode, Class: conceptgraph.EdgeClassHierarchy},
	}
	doseConcepts := &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		cqanswerNode: {ConceptID: cqanswerNode, TenantID: meTenantID, LearnerGCID: meGCID, Title: "CSPO Basics", ConceptKey: "cspo-basics"},
	}}
	dose, _ := newCampaignDose(t, &campGoalRepo{}, doseConcepts, progress, nil)
	return &ExtServer{
		Goals:                &cqserveGoalRepo{g: cqanswerGoal()},
		Concepts:             &fmStubConcepts{out: nodes},
		ConceptEdges:         &reStubEdges{out: edges},
		CampaignDose:         dose,
		CampaignQuestionBank: bank,
	}
}

func servableBank() *cqserveBank {
	return &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: cqanswerNode, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusReady, QuestionsPayload: []byte(cqanswerPayload),
	}}
}

func cqanswerPOST(t *testing.T, srv *ExtServer, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	path := "/v1/me/goals/" + cqserveGoalID + "/campaign/questions/answer"
	srv.Routes().ServeHTTP(w, newMeRequest(http.MethodPost, path, body))
	return w
}

func decodeCQAnswer(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return body
}

// cqanswerServedID returns the opaque option id the SERVE door ships for the
// option displaying `label` — exactly the value a browser posts back. It is
// derived by running the real serve path over the same fixture (CHO-2244), so
// this test can never drift from the door it exercises. The learner never sees
// the stored ids ("a"/"b"): those name the answer key in some qgen batches.
func cqanswerServedID(label string) string {
	served, err := cq.SanitizeServedPayload([]byte(cqanswerPayload))
	if err != nil {
		panic("cqanswerPayload must be servable: " + err.Error())
	}
	var env struct {
		Candidates []struct {
			Options []struct {
				OptionID string `json:"option_id"`
				Label    string `json:"label"`
			} `json:"options"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(served, &env); err != nil {
		panic("cqanswerPayload serve must decode: " + err.Error())
	}
	for _, o := range env.Candidates[0].Options {
		if o.Label == label {
			return o.OptionID
		}
	}
	panic("no served option labelled " + label)
}

func correctAnswerBody() map[string]any {
	return map[string]any{"concept_id": cqanswerNode, "rung": 1, "question_index": 0,
		"selected_option_id": cqanswerServedID("B")}
}

// ---------- tests ----------

// A correct answer grades server-side, folds the ladder (counter=1 at rung 1),
// and reports the honest post-fold snapshot + needed_correct (default 2).
func TestCampaignAnswer_Correct_FoldsAndReports(t *testing.T) {
	progress := &campProgressRepo{}
	srv := newCQAnswerServer(t, progress, servableBank())

	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := decodeCQAnswer(t, w)
	if body["correct"] != true {
		t.Errorf("correct = %v; want true", body["correct"])
	}
	if body["counted"] != true {
		t.Errorf("counted = %v; want true (a correct at the current rung)", body["counted"])
	}
	if body["cleared_rung"] != float64(0) || body["won"] != false {
		t.Errorf("first correct must not clear/win: %v", body)
	}
	if body["rung"] != float64(1) || body["rungs_cleared"] != float64(0) || body["current_rung_correct"] != float64(1) {
		t.Errorf("ladder snapshot = %v", body)
	}
	if body["needed_correct"] != float64(2) {
		t.Errorf("needed_correct = %v; want 2 (default threshold)", body["needed_correct"])
	}
	if r, _ := body["retention_r"].(float64); r <= 0 {
		t.Errorf("retention_r = %v; want a planted score > 0", body["retention_r"])
	}
	// The grader actually mutated the ladder row.
	if p := progress.rows[cqanswerNode]; p == nil || p.CurrentRungCorrect != 1 {
		t.Fatalf("ladder not folded: %+v", p)
	}
}

// Post-grade reveal: because the serve door strips the answer key + explainer
// (Slice F), the answers door returns the correct option id + its explainer so
// the FE can show the explanation AFTER the learner commits (server-authoritative).
func TestCampaignAnswer_ReturnsPostGradeExplainer(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := decodeCQAnswer(t, w)
	if body["correct_option_id"] != cqanswerServedID("B") {
		t.Errorf("correct_option_id = %v; want the SERVED id %v (post-grade reveal — the FE matches this against what it rendered)",
			body["correct_option_id"], cqanswerServedID("B"))
	}
	if body["explainer"] != "exactly four" {
		t.Errorf("explainer = %v; want the correct option's explainer (post-grade)", body["explainer"])
	}
}

// A wrong answer grades incorrect (no counter advance) but still 200 + folds
// the retention review (the Ebbinghaus penalty side).
func TestCampaignAnswer_Wrong_GradesIncorrect(t *testing.T) {
	progress := &campProgressRepo{}
	srv := newCQAnswerServer(t, progress, servableBank())

	body := correctAnswerBody()
	body["selected_option_id"] = cqanswerServedID("A") // the distractor
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got := decodeCQAnswer(t, w)
	if got["correct"] != false || got["counted"] != false {
		t.Errorf("wrong answer must be incorrect + uncounted: %v", got)
	}
	if got["current_rung_correct"] != float64(0) {
		t.Errorf("wrong answer must not advance the counter: %v", got)
	}
}

// The second correct answer clears rung 1.
func TestCampaignAnswer_SecondCorrect_ClearsRung(t *testing.T) {
	progress := &campProgressRepo{}
	srv := newCQAnswerServer(t, progress, servableBank())

	if w := cqanswerPOST(t, srv, correctAnswerBody()); w.Code != http.StatusOK {
		t.Fatalf("answer 1: %d body=%s", w.Code, w.Body.String())
	}
	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusOK {
		t.Fatalf("answer 2: %d body=%s", w.Code, w.Body.String())
	}
	body := decodeCQAnswer(t, w)
	if body["cleared_rung"] != float64(1) || body["rungs_cleared"] != float64(1) {
		t.Errorf("second correct must clear rung 1: %v", body)
	}
}

// A set that is not servable (idle/none) → 409 SET_NOT_SERVABLE (never grade
// against a missing payload).
func TestCampaignAnswer_SetNotServable_409(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, &cqserveBank{set: nil})
	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "SET_NOT_SERVABLE" {
		t.Errorf("code = %v, want SET_NOT_SERVABLE", code)
	}
}

// A concept outside the goal subtree → 422 CONCEPT_OUTSIDE_CAMPAIGN.
func TestCampaignAnswer_ConceptOutsideSubtree_422(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	body := correctAnswerBody()
	body["concept_id"] = cqanswerOff
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "CONCEPT_OUTSIDE_CAMPAIGN" {
		t.Errorf("code = %v, want CONCEPT_OUTSIDE_CAMPAIGN", code)
	}
}

// A concept id that is not a live node → 404 CONCEPT_NOT_FOUND.
func TestCampaignAnswer_ConceptNotLive_404(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	body := correctAnswerBody()
	body["concept_id"] = "01980000-0000-7000-9000-0000000dead0"
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "CONCEPT_NOT_FOUND" {
		t.Errorf("code = %v, want CONCEPT_NOT_FOUND", code)
	}
}

// A won node has left the campaign (D9) — the grader refuses with ErrAlreadyWon
// → 409 NODE_WON.
func TestCampaignAnswer_WonNode_409NodeWon(t *testing.T) {
	p, err := campaign.NewNodeProgress(meTenantID, meGCID, cqanswerNode, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewNodeProgress: %v", err)
	}
	won := time.Now().UTC()
	p.WonAt = &won
	p.RungsCleared = 6
	progress := &campProgressRepo{rows: map[string]*campaign.NodeProgress{cqanswerNode: p}}

	srv := newCQAnswerServer(t, progress, servableBank())
	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "NODE_WON" {
		t.Errorf("code = %v, want NODE_WON", code)
	}
}

// A rung above the ladder frontier → the grader refuses with ErrRungNotUnlocked
// → 409 RUNG_NOT_UNLOCKED.
func TestCampaignAnswer_RungNotUnlocked_409(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: cqanswerNode, ConceptKey: "cspo-basics",
		Rung: 3, GenerationStatus: cq.StatusReady, QuestionsPayload: []byte(cqanswerPayload),
	}})
	body := correctAnswerBody()
	body["rung"] = 3 // frontier is rung 1 on a fresh ladder
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "RUNG_NOT_UNLOCKED" {
		t.Errorf("code = %v, want RUNG_NOT_UNLOCKED", code)
	}
}

// CHO-2253: a selected_option_id the serve door never minted → 422
// INVALID_ANSWER, and the ladder must NOT fold. Since CHO-2244 the served
// id-space is closed, so an id outside it means tampering / a stale bundle / an
// index drift — none of which may be charged to the learner as a wrong answer.
func TestCampaignAnswer_UnservedOptionID_422_NoFold(t *testing.T) {
	progress := &campProgressRepo{}
	srv := newCQAnswerServer(t, progress, servableBank())

	for _, id := range []string{"zzz", "b" /* a STORED id, never served */} {
		body := correctAnswerBody()
		body["selected_option_id"] = id
		w := cqanswerPOST(t, srv, body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("selected_option_id %q was never served: status = %d body=%s", id, w.Code, w.Body.String())
		}
		if code := decodeCQAnswer(t, w)["code"]; code != "INVALID_ANSWER" {
			t.Errorf("code = %v, want INVALID_ANSWER", code)
		}
	}
	// Never folded — a refused submission must leave the ladder untouched.
	if p := progress.rows[cqanswerNode]; p != nil && p.CurrentRungCorrect != 0 {
		t.Fatalf("a refused submission folded the ladder: %+v", p)
	}
}

// A served set whose payload carries no answer key fails loud (never
// client-grade) → 422 QUESTION_NOT_GRADEABLE.
func TestCampaignAnswer_Ungradable_422(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: cqanswerNode, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusReady,
		// Display content present so this fails for the NO-ANSWER-KEY reason,
		// not for CHO-2244's missing-display-content reason.
		QuestionsPayload: []byte(`{"candidates":[{"options":[{"option_id":"a","label":"only choice","is_correct":false}]}]}`),
	}})
	w := cqanswerPOST(t, srv, correctAnswerBody())
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "QUESTION_NOT_GRADEABLE" {
		t.Errorf("code = %v, want QUESTION_NOT_GRADEABLE", code)
	}
}

// An out-of-ladder rung → 400 INVALID_RUNG.
func TestCampaignAnswer_InvalidRung_400(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	body := correctAnswerBody()
	body["rung"] = 9
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "INVALID_RUNG" {
		t.Errorf("code = %v, want INVALID_RUNG", code)
	}
}

// A missing concept_id → 400.
func TestCampaignAnswer_MissingConceptID_400(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	body := correctAnswerBody()
	delete(body, "concept_id")
	w := cqanswerPOST(t, srv, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

// The answers door is POST-only.
func TestCampaignAnswer_MethodGet_405(t *testing.T) {
	srv := newCQAnswerServer(t, &campProgressRepo{}, servableBank())
	w := httptest.NewRecorder()
	path := "/v1/me/goals/" + cqserveGoalID + "/campaign/questions/answer"
	srv.Routes().ServeHTTP(w, newMeRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

// An unwired answers lane fails loud — 503 CAMPAIGN_NOT_WIRED (called directly
// so the route-level nil guard doesn't mask it).
func TestCampaignAnswer_Unwired_503(t *testing.T) {
	srv := &ExtServer{Goals: &cqserveGoalRepo{g: cqanswerGoal()}} // no dose/bank/graph
	w := httptest.NewRecorder()
	path := "/v1/me/goals/" + cqserveGoalID + "/campaign/questions/answer"
	srv.handleMeGoalCampaignAnswer(w, newMeRequest(http.MethodPost, path, correctAnswerBody()), cqserveGoalID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQAnswer(t, w)["code"]; code != "CAMPAIGN_NOT_WIRED" {
		t.Errorf("code = %v, want CAMPAIGN_NOT_WIRED", code)
	}
}
