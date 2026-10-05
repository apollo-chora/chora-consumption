// goals_campaign_questions_handler_test.go — WS-C3 (CHO-2082, ADR-227 D13)
// RED tests for the campaign question-serve door:
//
//	GET /v1/me/goals/{id}/campaign/questions[?rung=N]
//
// "Generate once, retrieve forever" (D13) — the reuse READ side (AC3): a
// persisted, servable set is returned VERBATIM at zero LLM cost; a
// miss / in-flight / failed / won state returns a poll status the FE re-polls
// on. White-box (package http) so it reuses the WS-C2 fixtures in
// dose_campaign_wiring_test.go (campConceptRepo / campProgressRepo /
// newCampaignDose / campConceptID) + the /v1/me request helper; the goal repo
// + question-bank fakes are local (cqserve-prefixed) to avoid collisions.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	cq "github.com/apollo-chora/chora-consumption/internal/domain/campaignquestion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---------- local fakes ----------

// cqserveGoalRepo is a minimal goal.Repository serving ONE seeded goal (the
// campGoalRepo in dose_campaign_wiring_test.go has no GetByID, so the serve
// door — which loads via s.Goals.GetByID + the cross-learner leak guard —
// needs its own).
type cqserveGoalRepo struct {
	g      *goal.Goal
	getErr error
}

func (r *cqserveGoalRepo) Create(context.Context, *goal.Goal) error { return nil }
func (r *cqserveGoalRepo) GetByID(_ context.Context, _, _, goalID string) (*goal.Goal, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.g != nil && r.g.GoalID == goalID {
		return r.g, nil
	}
	return nil, nil
}
func (r *cqserveGoalRepo) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	if r.g == nil {
		return nil, nil
	}
	return []*goal.Goal{r.g}, nil
}
func (r *cqserveGoalRepo) Update(context.Context, *goal.Goal) error { return nil }

// cqserveBank is a campaignquestion.Repository returning one configured set +
// recording the rung it was queried at (so the default-rung path is asserted).
type cqserveBank struct {
	set     *cq.QuestionSet
	getErr  error
	gotRung int
}

func (b *cqserveBank) GetByConceptRung(_ context.Context, _, _, _ string, rung int) (*cq.QuestionSet, error) {
	b.gotRung = rung
	if b.getErr != nil {
		return nil, b.getErr
	}
	return b.set, nil
}
func (b *cqserveBank) GetByAssistID(context.Context, string, string) (*cq.QuestionSet, error) {
	return nil, nil
}
func (b *cqserveBank) CountRequestedOn(context.Context, string, string, cq.RequestOrigin, time.Time) (int, error) {
	return 0, nil
}
func (b *cqserveBank) Save(context.Context, *cq.QuestionSet) error { return nil }

// ---------- fixture ----------

const cqserveGoalID = "01980000-0000-7000-8000-00000000cf01"

// cqserveGoal builds the learner's goal (tenant/gcid matching newMeRequest so
// the leak guard admits it); focusSet toggles the assigned campaign focus.
func cqserveGoal(focusSet bool) *goal.Goal {
	g := &goal.Goal{GoalID: cqserveGoalID, TenantID: meTenantID, LearnerGCID: meGCID}
	if focusSet {
		f := campConceptID
		g.FocusConceptID = &f
	}
	return g
}

// newCQServeServer wires an ExtServer whose serve door reads the focus node
// (campConceptID → "cspo-basics") through the same real grader the WS-C2
// fixtures build, over the supplied progress + question bank.
func newCQServeServer(t *testing.T, g *goal.Goal, progress *campProgressRepo, bank *cqserveBank) *ExtServer {
	t.Helper()
	concepts := &campConceptRepo{nodes: map[string]*conceptgraph.ConceptNode{
		campConceptID: {
			ConceptID:   campConceptID,
			TenantID:    meTenantID,
			LearnerGCID: meGCID,
			Title:       "CSPO Basics",
			ConceptKey:  "cspo-basics",
		},
	}}
	dose, _ := newCampaignDose(t, &campGoalRepo{}, concepts, progress, nil)
	return &ExtServer{
		Goals:                &cqserveGoalRepo{g: g},
		CampaignDose:         dose,
		CampaignQuestionBank: bank,
	}
}

func cqservePath(query string) string {
	return "/v1/me/goals/" + cqserveGoalID + "/campaign/questions" + query
}

func cqserveGET(t *testing.T, srv *ExtServer, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest(http.MethodGet, cqservePath(query), nil))
	return w
}

func decodeCQServe(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return body
}

// ---------- tests ----------

// (a) a ready set is served verbatim + tagged source question_bank, and the
// default rung (empty ladder → rung 1) reaches the bank.
func TestCampaignQuestions_ReadyRow_ServedVerbatim(t *testing.T) {
	payload := []byte(`{"items":[{"prompt":"What is a Sprint?","options":["a","b","c","d"],"correct":0}]}`)
	bank := &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: campConceptID, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusReady, QuestionsPayload: payload,
	}}
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)

	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if bank.gotRung != 1 {
		t.Errorf("empty ladder must default to rung 1 (grader serve decision), bank got rung %d", bank.gotRung)
	}
	body := decodeCQServe(t, w)
	if body["status"] != "ready" || body["source"] != "question_bank" {
		t.Errorf("status/source = %v/%v", body["status"], body["source"])
	}
	if body["concept_id"] != campConceptID || body["concept_key"] != "cspo-basics" || body["rung"] != float64(1) {
		t.Errorf("identity = %v/%v/%v", body["concept_id"], body["concept_key"], body["rung"])
	}
	var want any
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body["questions"], want) {
		t.Errorf("questions not served verbatim: got %v want %v", body["questions"], want)
	}
}

// (b) no persisted set → status none (the FE knows to request generation).
func TestCampaignQuestions_NoRow_StatusNone(t *testing.T) {
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, &cqserveBank{set: nil})
	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := decodeCQServe(t, w)
	if body["status"] != "none" {
		t.Errorf("status = %v, want none", body["status"])
	}
	if _, ok := body["questions"]; ok {
		t.Errorf("miss must carry no questions: %v", body)
	}
	if _, ok := body["source"]; ok {
		t.Errorf("miss must carry no source: %v", body)
	}
}

// (c) an in-flight generation → status requested (poll again).
func TestCampaignQuestions_RequestedRow_StatusRequested(t *testing.T) {
	bank := &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: campConceptID, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusRequested, AssistID: "assist-1",
	}}
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)
	body := decodeCQServe(t, cqserveGET(t, srv, ""))
	if body["status"] != "requested" {
		t.Errorf("status = %v, want requested", body["status"])
	}
}

// (d) a failed generation → status failed + the surfaced reason (honest, not
// a silent empty set).
func TestCampaignQuestions_FailedRow_StatusFailedWithReason(t *testing.T) {
	bank := &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: campConceptID, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusFailed, FailureReason: "qgen critic refused",
	}}
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)
	body := decodeCQServe(t, cqserveGET(t, srv, ""))
	if body["status"] != "failed" || body["failure_reason"] != "qgen critic refused" {
		t.Errorf("failed body = %v", body)
	}
}

// ---------- the practice-door gap trigger (CHO-2087 walk-found) ----------
//
// The serve door is no longer a pure read: a resolved GAP (no set, failed
// set, or a stale-REQUESTED set whose terminal was lost on an earlier day)
// fires the SAME daily-capped qgen request the dose feeder uses — a learner
// tapping "Practice this hex" on a non-focus node must not stay bricked
// waiting for that node to be re-elected as focus. A same-day in-flight or
// ready set never re-fires; at-cap stays honest.

// cqserveTriggerServer wires the serve fixture PLUS the gap-trigger deps
// (dose.Bank + dose.QGen fakes from dose_campaign_qgen_test.go).
// The serve door is the explicit-tap entrance, so its budget is the TAP budget
// (tapToday); a test can additionally set gb.countToday to model a spent MARCH
// budget and prove the two are independent.
func cqserveTriggerServer(t *testing.T, bank *cqserveBank, tapToday int) (*ExtServer, *qgenBank, *qgenPublisher) {
	t.Helper()
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)
	gb := &qgenBank{tapToday: tapToday}
	pub := &qgenPublisher{}
	srv.CampaignDose.Bank = gb
	srv.CampaignDose.QGen = pub
	return srv, gb, pub
}

func TestCampaignQuestions_GapFiresBoundedRequest(t *testing.T) {
	srv, gb, pub := cqserveTriggerServer(t, &cqserveBank{}, 0)
	body := decodeCQServe(t, cqserveGET(t, srv, ""))
	if body["status"] != "requested" {
		t.Fatalf("a gap with budget must flip to requested, got %v", body)
	}
	if len(pub.topics) != 1 || pub.topics[0] != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("gap must publish ONE started.v2 request, got %v", pub.topics)
	}
	row := gb.rows[qgenKey(campConceptID, 1)]
	if row == nil || row.GenerationStatus != cq.StatusRequested || row.AssistID != pub.payloads[0]["assist_id"] {
		t.Fatalf("trigger must persist a requested row keyed to the assist id, got %+v", row)
	}
}

func TestCampaignQuestions_StaleRequestedRefires(t *testing.T) {
	stale := staleRequestedRow(t)
	stale.TenantID, stale.LearnerGCID = meTenantID, meGCID
	srv, _, pub := cqserveTriggerServer(t, &cqserveBank{set: stale}, 0)
	body := decodeCQServe(t, cqserveGET(t, srv, "?rung=1"))
	if body["status"] != "requested" {
		t.Fatalf("a stale-REQUESTED set must re-fire, got %v", body)
	}
	if len(pub.topics) != 1 {
		t.Fatalf("stale re-fire must publish exactly once, got %v", pub.topics)
	}
	if stale.AssistID == "assist-lost" || stale.AssistID != pub.payloads[0]["assist_id"] {
		t.Fatalf("re-fire must supersede the lost assist id, got %q", stale.AssistID)
	}
}

func TestCampaignQuestions_InFlightToday_NoRefire(t *testing.T) {
	fresh, err := cq.New(meTenantID, meGCID, campConceptID, "cspo-basics", 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("cq.New: %v", err)
	}
	if err := fresh.MarkRequested("assist-today", time.Now().UTC()); err != nil {
		t.Fatalf("MarkRequested: %v", err)
	}
	srv, _, pub := cqserveTriggerServer(t, &cqserveBank{set: fresh}, 1)
	body := decodeCQServe(t, cqserveGET(t, srv, "?rung=1"))
	if body["status"] != "requested" {
		t.Fatalf("same-day in-flight keeps the poll status, got %v", body)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("same-day in-flight must never re-publish, got %v", pub.topics)
	}
}

// A spent TAP budget returns an honest, actionable tap_capped (the FE shows a
// resets-tomorrow message), never a vague empty none.
func TestCampaignQuestions_TapBudgetSpent_TapCapped(t *testing.T) {
	srv, _, pub := cqserveTriggerServer(t, &cqserveBank{}, cq.DefaultDailyTapCap)
	body := decodeCQServe(t, cqserveGET(t, srv, ""))
	if body["status"] != "tap_capped" {
		t.Fatalf("a spent tap budget must return tap_capped, got %v", body)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("at-cap must not publish (D13), got %v", pub.topics)
	}
}

// The fix: an explicit hex tap generates even when the automatic dose-march has
// already spent its (separate) budget today; the tap budget is independent.
func TestCampaignQuestions_TapGeneratesDespiteSpentMarchBudget(t *testing.T) {
	srv, gb, pub := cqserveTriggerServer(t, &cqserveBank{}, 0) // tap budget free
	gb.countToday = cq.DefaultDailyMarchCap                    // march budget SPENT
	body := decodeCQServe(t, cqserveGET(t, srv, ""))
	if body["status"] != "requested" {
		t.Fatalf("a tap must generate despite a spent march budget, got %v", body)
	}
	if len(pub.topics) != 1 || pub.topics[0] != "chora.creation.ai_assist.started.v2" {
		t.Fatalf("tap must publish ONE started.v2 request, got %v", pub.topics)
	}
	row := gb.rows[qgenKey(campConceptID, 1)]
	if row == nil || row.GenerationStatus != cq.StatusRequested {
		t.Fatalf("tap must mint a requested row, got %+v", row)
	}
	if row.RequestOrigin != cq.OriginTap {
		t.Fatalf("a tap-minted row must be stamped origin=tap, got %q", row.RequestOrigin)
	}
}

func TestCampaignQuestions_ReadySet_NeverRefires(t *testing.T) {
	ready, err := cq.New(meTenantID, meGCID, campConceptID, "cspo-basics", 1, time.Now().UTC())
	if err != nil {
		t.Fatalf("cq.New: %v", err)
	}
	if err := ready.MarkRequested("assist-done", time.Now().UTC()); err != nil {
		t.Fatalf("MarkRequested: %v", err)
	}
	if err := ready.MarkReady([]byte(`{"candidates":[{"question_text":"q"}]}`), time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	srv, _, pub := cqserveTriggerServer(t, &cqserveBank{set: ready}, 0)
	body := decodeCQServe(t, cqserveGET(t, srv, "?rung=1"))
	if body["status"] != "ready" {
		t.Fatalf("ready must serve, got %v", body)
	}
	if len(pub.topics) != 0 {
		t.Fatalf("ready must never regenerate (reuse forever), got %v", pub.topics)
	}
}

// (e) a goal without an assigned focus cannot serve — 409 NO_FOCUS.
func TestCampaignQuestions_NoFocus_409(t *testing.T) {
	srv := newCQServeServer(t, cqserveGoal(false), &campProgressRepo{}, &cqserveBank{})
	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "NO_FOCUS" {
		t.Errorf("code = %v, want NO_FOCUS", code)
	}
}

// (f) a won focus node has left the campaign (D9) — the default serve decision
// refuses with 409 NODE_WON.
func TestCampaignQuestions_WonNode_409NodeWon(t *testing.T) {
	p, err := campaign.NewNodeProgress(meTenantID, meGCID, campConceptID, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewNodeProgress: %v", err)
	}
	won := time.Now().UTC()
	p.WonAt = &won
	p.RungsCleared = 6
	progress := &campProgressRepo{rows: map[string]*campaign.NodeProgress{campConceptID: p}}

	srv := newCQServeServer(t, cqserveGoal(true), progress, &cqserveBank{})
	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "NODE_WON" {
		t.Errorf("code = %v, want NODE_WON", code)
	}
}

// (g) an out-of-ladder explicit rung → 400 INVALID_RUNG.
func TestCampaignQuestions_InvalidRung_400(t *testing.T) {
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, &cqserveBank{})
	w := cqserveGET(t, srv, "?rung=9")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "INVALID_RUNG" {
		t.Errorf("code = %v, want INVALID_RUNG", code)
	}
}

// (h) the serve door is GET-only.
func TestCampaignQuestions_MethodPost_405(t *testing.T) {
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, &cqserveBank{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest(http.MethodPost, cqservePath(""), nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

// (bonus) an unwired question bank fails loud — 503 CAMPAIGN_NOT_WIRED. Called
// directly (the route-level s.Goals nil guard would otherwise mask it).
func TestCampaignQuestions_Unwired_503(t *testing.T) {
	srv := &ExtServer{Goals: &cqserveGoalRepo{g: cqserveGoal(true)}} // no bank, no dose
	w := httptest.NewRecorder()
	srv.handleMeGoalCampaignQuestions(w, newMeRequest(http.MethodGet, cqservePath(""), nil), cqserveGoalID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "CAMPAIGN_NOT_WIRED" {
		t.Errorf("code = %v, want CAMPAIGN_NOT_WIRED", code)
	}
}

// ---------- Slice F: served payload is sanitized (no answer key leak) --------

// A ready set is served with EVERY answer-revealing field stripped (is_correct,
// explainer, model_answer), while the display fields survive — the browser
// never receives the answer key pre-submit.
func TestCampaignQuestions_ServedPayloadSanitized(t *testing.T) {
	payload := []byte(`{"candidates":[{"stem":"2+2?","question_type":"mcq","options":[
		{"option_id":"a","label":"A","text":"3","is_correct":false,"explainer":"too low"},
		{"option_id":"b","label":"B","text":"4","is_correct":true,"explainer":"exactly four"}]}]}`)
	bank := &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: campConceptID, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusReady, QuestionsPayload: payload,
	}}
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)

	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{"is_correct", "explainer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("serve door leaked %q pre-submit:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "option_id") || !strings.Contains(body, "2+2?") || !strings.Contains(body, "\"text\"") {
		t.Errorf("serve door dropped display fields (question no longer answerable):\n%s", body)
	}
	m := decodeCQServe(t, w)
	if m["status"] != "ready" || m["source"] != "question_bank" {
		t.Errorf("sanitised serve must stay status=ready source=question_bank: %v", m)
	}
	if m["questions"] == nil {
		t.Errorf("sanitised serve must still carry questions: %v", m)
	}
}

// A stored payload that cannot be sanitised is NEVER served raw — the door
// fails loud with an honest failed status (no leak).
func TestCampaignQuestions_UnsanitizablePayload_FailsLoud(t *testing.T) {
	bank := &cqserveBank{set: &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: campConceptID, ConceptKey: "cspo-basics",
		Rung: 1, GenerationStatus: cq.StatusReady, QuestionsPayload: []byte(`not valid json at all`),
	}}
	srv := newCQServeServer(t, cqserveGoal(true), &campProgressRepo{}, bank)

	w := cqserveGET(t, srv, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	m := decodeCQServe(t, w)
	if m["status"] != "failed" {
		t.Errorf("an un-sanitisable payload must serve status=failed, got %v", m["status"])
	}
	if _, ok := m["questions"]; ok {
		t.Errorf("must NOT serve questions when sanitisation fails: %v", m)
	}
	if strings.Contains(w.Body.String(), "not valid json") {
		t.Errorf("must NOT leak the raw payload on sanitisation failure: %s", w.Body.String())
	}
}

// ---------- concept_id param (WS-C7 addendum #3 node-scoped serve) ----------

const (
	cqserveRoot = "01980000-0000-7000-9000-00000000cd00"
	cqserveNode = "01980000-0000-7000-9000-00000000cd01"
	cqserveOff  = "01980000-0000-7000-9000-00000000cd09" // live, outside the subtree
)

func cqserveGoalRooted() *goal.Goal {
	root := cqserveRoot
	return &goal.Goal{GoalID: cqserveGoalID, TenantID: meTenantID, LearnerGCID: meGCID, RootConceptID: &root}
}

// newCQServeServerGraph wires the serve door over a rooted goal WITHOUT a focus
// + a subtree (root -> node) + a live off-map node, so the concept_id path can
// serve a node the goal never focused.
func newCQServeServerGraph(t *testing.T, progress *campProgressRepo, bank *cqserveBank) *ExtServer {
	t.Helper()
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: cqserveRoot, TenantID: meTenantID, LearnerGCID: meGCID, Title: "Algebra", ConceptKey: "algebra"},
		{ConceptID: cqserveNode, TenantID: meTenantID, LearnerGCID: meGCID, Title: "CSPO Basics", ConceptKey: "cspo-basics"},
		{ConceptID: cqserveOff, TenantID: meTenantID, LearnerGCID: meGCID, Title: "Off-map", ConceptKey: "off-map"},
	}
	edges := []*conceptgraph.Edge{
		{EdgeID: "e-cd", TenantID: meTenantID, LearnerGCID: meGCID,
			SourceConceptID: cqserveRoot, TargetConceptID: cqserveNode, Class: conceptgraph.EdgeClassHierarchy},
	}
	dose, _ := newCampaignDose(t, &campGoalRepo{}, &campConceptRepo{}, progress, nil)
	return &ExtServer{
		Goals:                &cqserveGoalRepo{g: cqserveGoalRooted()},
		Concepts:             &fmStubConcepts{out: nodes},
		ConceptEdges:         &reStubEdges{out: edges},
		CampaignDose:         dose,
		CampaignQuestionBank: bank,
	}
}

func cqserveReadySet(rung int) *cq.QuestionSet {
	return &cq.QuestionSet{
		TenantID: meTenantID, LearnerGCID: meGCID, ConceptID: cqserveNode, ConceptKey: "cspo-basics",
		Rung: rung, GenerationStatus: cq.StatusReady,
		// label/text are contract-required (OpenAPI MCQOption) and CHO-2244
		// derives the served option id from them — an option with no display
		// content is unservable, so a fixture without one is not realistic.
		QuestionsPayload: []byte(`{"candidates":[{"options":[{"option_id":"a","label":"A","text":"the answer","is_correct":true,"explainer":"x"}]}]}`),
	}
}

// concept_id serves a node the goal never focused: NO_FOCUS does NOT apply, the
// default serve decision (empty ladder → rung 1) reaches the bank, and the
// response identity is the requested node.
func TestCampaignQuestions_ConceptIDParam_ServesWithoutFocus(t *testing.T) {
	bank := &cqserveBank{set: cqserveReadySet(1)}
	srv := newCQServeServerGraph(t, &campProgressRepo{}, bank)

	w := cqserveGET(t, srv, "?concept_id="+cqserveNode)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if bank.gotRung != 1 {
		t.Errorf("empty ladder must default to rung 1, bank got %d", bank.gotRung)
	}
	body := decodeCQServe(t, w)
	if body["status"] != "ready" || body["concept_id"] != cqserveNode || body["concept_key"] != "cspo-basics" {
		t.Errorf("node-scoped serve body = %v", body)
	}
}

// concept_id outside the goal subtree → 422 CONCEPT_OUTSIDE_CAMPAIGN.
func TestCampaignQuestions_ConceptIDParam_OutsideSubtree_422(t *testing.T) {
	srv := newCQServeServerGraph(t, &campProgressRepo{}, &cqserveBank{})
	w := cqserveGET(t, srv, "?concept_id="+cqserveOff)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "CONCEPT_OUTSIDE_CAMPAIGN" {
		t.Errorf("code = %v, want CONCEPT_OUTSIDE_CAMPAIGN", code)
	}
}

// concept_id that is not a live node → 404 CONCEPT_NOT_FOUND.
func TestCampaignQuestions_ConceptIDParam_NotLive_404(t *testing.T) {
	srv := newCQServeServerGraph(t, &campProgressRepo{}, &cqserveBank{})
	w := cqserveGET(t, srv, "?concept_id=01980000-0000-7000-9000-0000000dead0")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "CONCEPT_NOT_FOUND" {
		t.Errorf("code = %v, want CONCEPT_NOT_FOUND", code)
	}
}

// An explicit ?rung still wins over the serve decision on the concept_id path.
func TestCampaignQuestions_ConceptIDParam_ExplicitRungWins(t *testing.T) {
	bank := &cqserveBank{set: cqserveReadySet(3)}
	srv := newCQServeServerGraph(t, &campProgressRepo{}, bank)
	w := cqserveGET(t, srv, "?concept_id="+cqserveNode+"&rung=3")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if bank.gotRung != 3 {
		t.Errorf("explicit rung must win, bank got %d", bank.gotRung)
	}
}

// concept_id present but the graph repos are unwired → 503 CAMPAIGN_NOT_WIRED.
func TestCampaignQuestions_ConceptIDParam_GraphUnwired_503(t *testing.T) {
	dose, _ := newCampaignDose(t, &campGoalRepo{}, &campConceptRepo{}, &campProgressRepo{}, nil)
	srv := &ExtServer{
		Goals:                &cqserveGoalRepo{g: cqserveGoalRooted()},
		CampaignDose:         dose,
		CampaignQuestionBank: &cqserveBank{},
	} // no Concepts / ConceptEdges
	w := cqserveGET(t, srv, "?concept_id="+cqserveNode)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if code := decodeCQServe(t, w)["code"]; code != "CAMPAIGN_NOT_WIRED" {
		t.Errorf("code = %v, want CAMPAIGN_NOT_WIRED", code)
	}
}
