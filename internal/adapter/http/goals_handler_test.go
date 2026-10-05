// goals_handler_test.go — RED tests for the learner-owned Goal CRUD API
// (ADR-204 §2): POST/GET /v1/me/goals + PATCH /v1/me/goals/{id}. Learner-scoped
// (GCID from session headers, never a query/body param). camelCase DTOs; the list
// carries the DERIVED primaryLens (ADR-204 §3).
package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const (
	goTenant    = "01970000-0000-7000-8000-0000000000c1"
	goGCID      = "01970000-0000-7000-9000-0000000000c1"
	goCompanion = "01970000-0000-7000-b000-0000000000c1"
)

// goalRepoFake is a map-backed goal.Repository for the handlers. GetByID returns
// by id WITHOUT learner filtering so the handler's cross-learner leak-guard can
// be exercised (the real pg repo filters in SQL; the guard is defence-in-depth).
type goalRepoFake struct {
	store     map[string]*goal.Goal
	createErr error
	getErr    error
	listErr   error
	updateErr error
	created   *goal.Goal
}

func newGoalRepoFake() *goalRepoFake { return &goalRepoFake{store: map[string]*goal.Goal{}} }

func (r *goalRepoFake) Create(_ context.Context, g *goal.Goal) error {
	if r.createErr != nil {
		return r.createErr
	}
	cp := *g
	r.store[g.GoalID] = &cp
	r.created = &cp
	return nil
}

func (r *goalRepoFake) GetByID(_ context.Context, _, _, id string) (*goal.Goal, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	g, ok := r.store[id]
	if !ok || g.DeletedAt != nil {
		return nil, nil
	}
	cp := *g // fresh copy: the handler mutates this then persists via Update
	return &cp, nil
}

func (r *goalRepoFake) ListByLearner(_ context.Context, _, _ string) ([]*goal.Goal, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]*goal.Goal, 0, len(r.store))
	for _, g := range r.store {
		if g.DeletedAt == nil {
			cp := *g
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *goalRepoFake) Update(_ context.Context, g *goal.Goal) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	cp := *g
	r.store[g.GoalID] = &cp
	return nil
}

func (r *goalRepoFake) seed(t *testing.T, in goal.NewGoalInput) *goal.Goal {
	t.Helper()
	if in.TenantID == "" {
		in.TenantID = goTenant
	}
	if in.LearnerGCID == "" {
		in.LearnerGCID = goGCID
	}
	g, err := goal.NewGoal(in)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	r.store[g.GoalID] = g
	return g
}

func goalServer(repo goal.Repository) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.Goals = repo
	// CHO-2013 P1: the attach door requires a Summoner (fail-loud 503 when
	// nil). The permissive fake keeps the pre-P1 attach/detach tests
	// exercising their own concern; summon-effect specifics live in
	// goals_summon_test.go.
	ext.Summoner = &fakeSummoner{}
	return ext
}

func goalReq(method, path string, body any) *http.Request {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rdr)
	r.Header.Set("X-Tenant-Id", goTenant)
	r.Header.Set("gcid", goGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	return r
}

func doGoal(srv *httpadapter.ExtServer, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w
}

// --- create ---

func TestCreateGoal_CuriosityOK(t *testing.T) {
	repo := newGoalRepoFake()
	w := doGoal(goalServer(repo), goalReq("POST", "/v1/me/goals", map[string]any{
		"kind": "curiosity", "northStarNote": "explore graphs",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["kind"] != "curiosity" || got["status"] != "active" {
		t.Errorf("kind/status = %v/%v", got["kind"], got["status"])
	}
	if got["goalId"] == nil || got["goalId"] == "" {
		t.Errorf("goalId missing: %v", got["goalId"])
	}
	if _, present := got["choraTargetRef"]; present {
		t.Errorf("curiosity goal must omit choraTargetRef; got %v", got["choraTargetRef"])
	}
	if repo.created == nil || repo.created.LearnerGCID != goGCID || repo.created.TenantID != goTenant {
		t.Errorf("goal not scoped to session learner: %+v", repo.created)
	}
}

// WS-B (CHO-2005 / ADR-214 D1): "＋New map" anchors the Goal to a root
// ConceptNode. The create request accepts rootConceptId; the DTO echoes it.
func TestCreateGoal_WithRootConceptID(t *testing.T) {
	repo := newGoalRepoFake()
	root := "01970000-0000-7000-c000-0000000000ab"
	w := doGoal(goalServer(repo), goalReq("POST", "/v1/me/goals", map[string]any{
		"kind": "curiosity", "rootConceptId": root, "northStarNote": "Scrum",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.created == nil || repo.created.RootConceptID == nil || *repo.created.RootConceptID != root {
		t.Fatalf("root concept not persisted on the goal: %+v", repo.created)
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["rootConceptId"] != root {
		t.Errorf("dto rootConceptId = %v; want %s", got["rootConceptId"], root)
	}
}

// theme_mastery still requires a target (ADR-214 removed only the credential
// kinds cert/course/path; the target-requiring personal kinds remain).
func TestCreateGoal_ThemeMasteryNeedsTarget(t *testing.T) {
	w := doGoal(goalServer(newGoalRepoFake()), goalReq("POST", "/v1/me/goals", map[string]any{
		"kind": "theme_mastery",
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (theme_mastery needs target)", w.Code)
	}
}

// ADR-214 §3: a removed credential kind is rejected at the edge (422).
func TestCreateGoal_RemovedCredentialKindRejected(t *testing.T) {
	w := doGoal(goalServer(newGoalRepoFake()), goalReq("POST", "/v1/me/goals", map[string]any{
		"kind": "cert", "choraTargetRef": "cert:pmp",
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (cert kind removed)", w.Code)
	}
}

func TestCreateGoal_ThemeMasteryWithTarget(t *testing.T) {
	repo := newGoalRepoFake()
	w := doGoal(goalServer(repo), goalReq("POST", "/v1/me/goals", map[string]any{
		"kind": "theme_mastery", "choraTargetRef": "path:data-eng", "conceptSet": []string{"sql", "etl"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["choraTargetRef"] != "path:data-eng" {
		t.Errorf("target = %v", got["choraTargetRef"])
	}
	cs, ok := got["conceptSet"].([]any)
	if !ok || len(cs) != 2 {
		t.Errorf("conceptSet = %v", got["conceptSet"])
	}
}

func TestCreateGoal_BadJSON(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/me/goals", bytes.NewReader([]byte("{not json")))
	r.Header.Set("X-Tenant-Id", goTenant)
	r.Header.Set("gcid", goGCID)
	if w := doGoal(goalServer(newGoalRepoFake()), r); w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestCreateGoal_RepoError(t *testing.T) {
	repo := newGoalRepoFake()
	repo.createErr = context.DeadlineExceeded
	w := doGoal(goalServer(repo), goalReq("POST", "/v1/me/goals", map[string]any{"kind": "curiosity"}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
}

// --- list + derived lens ---

func TestListGoals_DerivedLensCuriosity(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("theme:bio")})
	w := doGoal(goalServer(repo), goalReq("GET", "/v1/me/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items       []map[string]any `json:"items"`
		PrimaryLens string           `json:"primaryLens"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Items) != 2 {
		t.Fatalf("items=%d want 2", len(resp.Items))
	}
	if resp.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity", resp.PrimaryLens)
	}
}

// ADR-214 §4: with the credential kinds removed, NO goal-Kind drives the
// credential lens any more — it derives to curiosity until the ADR-216
// aspiration link lands (a theme_mastery goal is a personal-axis goal).
func TestListGoals_DerivedLensCuriosityUntilAspirationLink(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("cert:pmp")})
	w := doGoal(goalServer(repo), goalReq("GET", "/v1/me/goals", nil))
	var resp struct {
		PrimaryLens string `json:"primaryLens"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity (credential lens deferred to ADR-216)", resp.PrimaryLens)
	}
}

func TestListGoals_RepoError(t *testing.T) {
	repo := newGoalRepoFake()
	repo.listErr = context.DeadlineExceeded
	if w := doGoal(goalServer(repo), goalReq("GET", "/v1/me/goals", nil)); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
}

// --- list: verified progress (CHO-1921) ---

// goalEdge mints a Growth Edge for the progress overlay. strength <= the mastered
// threshold ⇒ grown (counts as mastered); above ⇒ active (does not count).
func goalEdge(t *testing.T, label string, strength float64) lw.LearnerWeakness {
	t.Helper()
	w, err := lw.New(lw.UpsertInput{
		TenantID:     goTenant,
		LearnerGCID:  goGCID,
		ConceptLabel: label,
		Embedding:    []float32{0.1},
		Strength:     strength,
		Source:       lw.SourceDerived,
		Now:          time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("goalEdge(%q): %v", label, err)
	}
	return *w
}

// firstItem decodes the goals list and returns the first item's raw map.
func firstItem(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items=%d want 1", len(resp.Items))
	}
	return resp.Items[0]
}

// assertProgress checks the three verified-progress DTO fields (JSON numbers
// decode to float64 in a map[string]any).
func assertProgress(t *testing.T, it map[string]any, pct, mastered, total float64) {
	t.Helper()
	if it["progressPercent"] != pct || it["masteredConcepts"] != mastered || it["totalConcepts"] != total {
		t.Errorf("progress = %v/%v/%v, want %v/%v/%v",
			it["progressPercent"], it["masteredConcepts"], it["totalConcepts"], pct, mastered, total)
	}
}

func TestListGoals_VerifiedProgress(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("theme:bio"), ConceptSet: []string{"alpha", "beta"}})
	lwRepo := &growthEdgeRepoStub{listAllResult: []lw.LearnerWeakness{
		goalEdge(t, "alpha", 0.05), // grown ⇒ mastered
		goalEdge(t, "beta", 0.7),   // active ⇒ not mastered
	}}
	srv := goalServer(repo)
	srv.LearnerWeakness = lwRepo

	it := firstItem(t, doGoal(srv, goalReq("GET", "/v1/me/goals", nil)))
	assertProgress(t, it, 50, 1, 2)

	// mastered-set load must request grown edges (the mastered class).
	if !lwRepo.listAllQuery.IncludeGrown {
		t.Errorf("ListAll must be called with IncludeGrown=true; got %+v", lwRepo.listAllQuery)
	}
}

func TestListGoals_VerifiedProgress_EmptyConceptSet(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity}) // no ConceptSet
	srv := goalServer(repo)
	srv.LearnerWeakness = &growthEdgeRepoStub{listAllResult: []lw.LearnerWeakness{goalEdge(t, "alpha", 0.05)}}

	it := firstItem(t, doGoal(srv, goalReq("GET", "/v1/me/goals", nil)))
	assertProgress(t, it, 0, 0, 0)
}

func TestListGoals_VerifiedProgress_FailSoftNilRepo(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("theme:bio"), ConceptSet: []string{"alpha", "beta"}})
	srv := goalServer(repo) // LearnerWeakness left nil ⇒ fail-soft

	it := firstItem(t, doGoal(srv, goalReq("GET", "/v1/me/goals", nil)))
	assertProgress(t, it, 0, 0, 2) // progress 0, but total still reflects the concept set
}

func TestListGoals_VerifiedProgress_FailSoftRepoErr(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("theme:bio"), ConceptSet: []string{"alpha", "beta"}})
	srv := goalServer(repo)
	srv.LearnerWeakness = &growthEdgeRepoStub{listAllErr: context.DeadlineExceeded}

	it := firstItem(t, doGoal(srv, goalReq("GET", "/v1/me/goals", nil)))
	assertProgress(t, it, 0, 0, 2) // a Growth-Edge read error must not break the goals read
}

// --- patch: status ---

func TestPatchGoal_Status(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("cert:pmp")})
	w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"status": "achieved"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].Status != goal.StatusAchieved {
		t.Errorf("not persisted: %q", repo.store[g.GoalID].Status)
	}
}

func TestPatchGoal_InvalidStatus(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"status": "paused"})); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", w.Code)
	}
}

// --- patch: personal completion (WS-D Mastery lens / ADR-213) ---

func TestPatchGoal_PersonalComplete(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	// Mark "done for me".
	w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"personalComplete": true}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].PersonalCompletedAt == nil {
		t.Fatal("personal completion not recorded")
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["personalCompletedAt"] == nil || got["personalCompletedAt"] == "" {
		t.Errorf("dto omits personalCompletedAt: %v", got["personalCompletedAt"])
	}
	// Reopen ("not done after all").
	w2 := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"personalComplete": false}))
	if w2.Code != http.StatusOK {
		t.Fatalf("reopen status=%d", w2.Code)
	}
	if repo.store[g.GoalID].PersonalCompletedAt != nil {
		t.Error("personal completion not cleared on reopen")
	}
}

// --- patch: re-anchor root concept (WS-C make-root / ADR-214 D1) ---

func TestPatchGoal_ReAnchorRootConcept(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: ptr("old-root")})
	newRoot := "01970000-0000-7000-c000-0000000000cd"
	w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"rootConceptId": newRoot}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].RootConceptID == nil || *repo.store[g.GoalID].RootConceptID != newRoot {
		t.Errorf("root not re-anchored on the goal: %v", repo.store[g.GoalID].RootConceptID)
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["rootConceptId"] != newRoot {
		t.Errorf("dto rootConceptId=%v want %s", got["rootConceptId"], newRoot)
	}
}

// --- patch: companion bond (1:1) ---

func TestPatchGoal_AttachCompanion(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["attachedCompanionId"] != goCompanion {
		t.Errorf("attachedCompanionId=%v", got["attachedCompanionId"])
	}
	if repo.store[g.GoalID].AttachedCompanionID == nil || *repo.store[g.GoalID].AttachedCompanionID != goCompanion {
		t.Errorf("bond not persisted")
	}
}

func TestPatchGoal_DetachCompanion(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	_ = g.AttachCompanion(goCompanion, g.CreatedAt)
	repo.store[g.GoalID] = g
	w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"detachCompanion": true}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].AttachedCompanionID != nil {
		t.Errorf("bond not cleared")
	}
}

// --- delete: soft-delete a whole map (Goal) (KG handoff, map-delete 2a) ---

// DELETE /v1/me/goals/{id} soft-deletes the whole map (204); the row persists
// with deleted_at set (pseudonymise-not-delete, ddd §5) and a subsequent read
// omits it.
func TestDeleteGoal_SoftDeletes(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	w := doGoal(goalServer(repo), goalReq(http.MethodDelete, "/v1/me/goals/"+g.GoalID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].DeletedAt == nil {
		t.Fatalf("deleted_at not set — soft-delete not persisted")
	}
	if got, _ := repo.GetByID(context.Background(), goTenant, goGCID, g.GoalID); got != nil {
		t.Errorf("GET still returns a soft-deleted goal: %+v", got)
	}
}

// An unknown id and a double-delete both render 404 (idempotent; GetByID
// excludes soft-deleted rows), never a 500 or a leak.
func TestDeleteGoal_UnknownOrDeleted404(t *testing.T) {
	repo := newGoalRepoFake()
	if w := doGoal(goalServer(repo), goalReq(http.MethodDelete, "/v1/me/goals/019f0000-0000-7000-8000-0000000000de", nil)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: status=%d want 404", w.Code)
	}
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	_ = doGoal(goalServer(repo), goalReq(http.MethodDelete, "/v1/me/goals/"+g.GoalID, nil))
	if w := doGoal(goalServer(repo), goalReq(http.MethodDelete, "/v1/me/goals/"+g.GoalID, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("double-delete: status=%d want 404", w.Code)
	}
}

// A goal owned by a DIFFERENT learner must 404 (never delete another's map) and
// stay intact — the handler's leak-guard, defence-in-depth over RLS.
func TestDeleteGoal_ForeignLearner404(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, LearnerGCID: "01970000-0000-7000-9000-00000000ffff"})
	if w := doGoal(goalServer(repo), goalReq(http.MethodDelete, "/v1/me/goals/"+g.GoalID, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("foreign: status=%d want 404", w.Code)
	}
	if repo.store[g.GoalID].DeletedAt != nil {
		t.Errorf("foreign goal was deleted — leak")
	}
}

func TestPatchGoal_AttachConflict(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	_ = g.AttachCompanion(goCompanion, g.CreatedAt)
	repo.store[g.GoalID] = g
	other := "01970000-0000-7000-b000-0000000000ff"
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": other})); w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (already bound)", w.Code)
	}
}

func TestPatchGoal_DetachNoneConflict(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"detachCompanion": true})); w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (none attached)", w.Code)
	}
}

func TestPatchGoal_AttachAndDetachRejected(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{
		"attachedCompanionId": goCompanion, "detachCompanion": true,
	})); w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (cannot attach+detach)", w.Code)
	}
}

func TestPatchGoal_NotFound(t *testing.T) {
	repo := newGoalRepoFake()
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/01970000-0000-7000-a000-0000ffffffff", map[string]any{"status": "retired"})); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestPatchGoal_CrossLearnerLeakGuard(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{TenantID: goTenant, LearnerGCID: "01970000-0000-7000-9000-00000000dead", Kind: goal.KindCuriosity})
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"status": "retired"})); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (cross-learner)", w.Code)
	}
}

func TestPatchGoal_BadJSON(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	r := httptest.NewRequest("PATCH", "/v1/me/goals/"+g.GoalID, bytes.NewReader([]byte("{bad")))
	r.Header.Set("X-Tenant-Id", goTenant)
	r.Header.Set("gcid", goGCID)
	if w := doGoal(goalServer(repo), r); w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestPatchGoal_UpdateRepoError(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	repo.updateErr = context.DeadlineExceeded
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"status": "achieved"})); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
}

// --- guards: nil repo, context, method, path ---

func TestGoals_NilRepo503(t *testing.T) {
	srv := httpadapter.NewExtServer(nil) // Goals unset
	for _, tc := range []struct{ m, p string }{
		{"GET", "/v1/me/goals"},
		{"POST", "/v1/me/goals"},
		{"PATCH", "/v1/me/goals/x"},
	} {
		if w := doGoal(srv, goalReq(tc.m, tc.p, nil)); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status=%d want 503", tc.m, tc.p, w.Code)
		}
	}
}

func TestGoals_MissingContext(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/me/goals", nil) // no headers
	if w := doGoal(goalServer(newGoalRepoFake()), r); w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

func TestGoals_MethodNotAllowed(t *testing.T) {
	repo := newGoalRepoFake()
	if w := doGoal(goalServer(repo), goalReq("DELETE", "/v1/me/goals", nil)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("collection DELETE: status=%d want 405", w.Code)
	}
	if w := doGoal(goalServer(repo), goalReq("PUT", "/v1/me/goals/x", nil)); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("item PUT: status=%d want 405", w.Code)
	}
}

func TestGoals_MissingPathID(t *testing.T) {
	repo := newGoalRepoFake()
	if w := doGoal(goalServer(repo), goalReq("PATCH", "/v1/me/goals/", nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (missing path id)", w.Code)
	}
}

func ptr(s string) *string { return &s }
