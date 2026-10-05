// maps_handler_test.go — WS-A2 (My Knowledge unification, CHO-2005): the map
// read-model. A map = a Goal (ADR-214 D1); its graph is the hierarchy sub-tree
// under Goal.RootConceptID, painted with the A1 shaky/mastery overlay.
//
//	GET /v1/me/maps                 list the learner's maps + per-map counts
//	GET /v1/me/maps/{goalId}/graph  the root-scoped painted subgraph
//
// Reuses the internal-package doubles fmStubConcepts / reStubEdges / kgLWStub /
// kgOverlayEdge + fmTenantID / fmGCID; adds a small goal.Repository stub.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ---- goal.Repository double (internal package) ------------------------------

type mapsGoalStub struct {
	list      []*goal.Goal
	listErr   error
	byID      map[string]*goal.Goal
	getErr    error
	updateErr error
	updated   *goal.Goal // captured last Update arg (acquire attach assertion)
}

func (s *mapsGoalStub) Create(context.Context, *goal.Goal) error { return nil }
func (s *mapsGoalStub) GetByID(_ context.Context, _, _, id string) (*goal.Goal, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if g, ok := s.byID[id]; ok {
		return g, nil
	}
	return nil, nil
}
func (s *mapsGoalStub) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	return s.list, s.listErr
}
func (s *mapsGoalStub) Update(_ context.Context, g *goal.Goal) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updated = g
	return nil
}

func mapsPtr(s string) *string { return &s }

// mapsFixtureConcepts: one themed tree (Algebra) + one out-of-tree concept.
func mapsFixtureConcepts() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Algebra"},
		{ConceptID: "c-lin", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Linear Equations"},
		{ConceptID: "c-quad", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Quadratics"},
		{ConceptID: "c-other", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Poetry"},
	}
}

// mapsFixtureEdges: root -> lin, root -> quad (hierarchy). c-other is disjoint.
func mapsFixtureEdges() []*conceptgraph.Edge {
	return []*conceptgraph.Edge{
		{EdgeID: "e-root-lin", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "c-root", TargetConceptID: "c-lin", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e-root-quad", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "c-root", TargetConceptID: "c-quad", Class: conceptgraph.EdgeClassHierarchy},
	}
}

// mapsFixtureWeakness: Linear Equations shaky (0.8); Quadratics grown/mastered (0.05).
func mapsFixtureWeakness(t *testing.T) *kgLWStub {
	t.Helper()
	return &kgLWStub{items: []lw.LearnerWeakness{
		kgOverlayEdge(t, "Linear Equations", 0.8),
		kgOverlayEdge(t, "Quadratics", 0.05),
	}}
}

func mapsGoal(id string, root *string) *goal.Goal {
	now := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	return &goal.Goal{
		GoalID: id, TenantID: fmTenantID, LearnerGCID: fmGCID,
		Kind: goal.KindCuriosity, Status: goal.StatusActive,
		NorthStarNote: "Own algebra", RootConceptID: root,
		CreatedAt: now, UpdatedAt: now,
	}
}

func mapsFullServer(t *testing.T) *ExtServer {
	t.Helper()
	g := mapsGoal("g-1", mapsPtr("c-root"))
	return &ExtServer{
		Goals:           &mapsGoalStub{list: []*goal.Goal{g}, byID: map[string]*goal.Goal{"g-1": g}},
		Concepts:        &fmStubConcepts{out: mapsFixtureConcepts()},
		ConceptEdges:    &reStubEdges{out: mapsFixtureEdges()},
		LearnerWeakness: mapsFixtureWeakness(t),
	}
}

func mapsServe(s *ExtServer, method, path string, withCtx bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if withCtx {
		r.Header.Set("X-Tenant-Id", fmTenantID)
		r.Header.Set("gcid", fmGCID)
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	return w
}

// ---- list ------------------------------------------------------------------

func TestMaps_List_CountsAndTitle(t *testing.T) {
	w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d; want 1", len(resp.Items))
	}
	m := resp.Items[0]
	if m["goalId"] != "g-1" {
		t.Errorf("goalId = %v; want g-1", m["goalId"])
	}
	if m["title"] != "Algebra" {
		t.Errorf("title = %v; want Algebra (root concept title)", m["title"])
	}
	if m["rootConceptId"] != "c-root" {
		t.Errorf("rootConceptId = %v; want c-root", m["rootConceptId"])
	}
	if got, _ := m["conceptCount"].(float64); got != 3 {
		t.Errorf("conceptCount = %v; want 3 (root+lin+quad, NOT out-of-tree)", m["conceptCount"])
	}
	if got, _ := m["shakyCount"].(float64); got != 1 {
		t.Errorf("shakyCount = %v; want 1 (Linear Equations)", m["shakyCount"])
	}
	if got, _ := m["masteredCount"].(float64); got != 1 {
		t.Errorf("masteredCount = %v; want 1 (Quadratics grown)", m["masteredCount"])
	}
}

func TestMaps_List_MethodNotAllowed(t *testing.T) {
	if w := mapsServe(mapsFullServer(t), http.MethodPost, "/v1/me/maps", true); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestMaps_List_RepoError(t *testing.T) {
	s := mapsFullServer(t)
	s.Goals = &mapsGoalStub{listErr: errors.New("boom")}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", w.Code)
	}
}

func TestMaps_List_ConceptRepoError(t *testing.T) {
	s := mapsFullServer(t)
	s.Concepts = &fmStubConcepts{err: errors.New("boom")}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (concept repo error)", w.Code)
	}
}

func TestMaps_List_EdgeRepoError(t *testing.T) {
	s := mapsFullServer(t)
	s.ConceptEdges = &reStubEdges{err: errors.New("boom")}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (edge repo error)", w.Code)
	}
}

func TestMaps_Graph_GoalGetError(t *testing.T) {
	s := mapsFullServer(t)
	s.Goals = &mapsGoalStub{getErr: errors.New("boom")}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (goal get error)", w.Code)
	}
}

func TestMaps_Graph_ConceptRepoError(t *testing.T) {
	s := mapsFullServer(t)
	s.Concepts = &fmStubConcepts{err: errors.New("boom")}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (concept repo error on graph)", w.Code)
	}
}

func TestMaps_Unwired503(t *testing.T) {
	s := &ExtServer{} // no Goals/Concepts/Edges
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps", true); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}

func TestMaps_MissingContext(t *testing.T) {
	if w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps", false); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

// ---- graph -----------------------------------------------------------------

func mapsDecodeGraph(t *testing.T, body []byte) (title string, concepts []map[string]any, edges []map[string]any) {
	t.Helper()
	var resp struct {
		Title    string           `json:"title"`
		Concepts []map[string]any `json:"concepts"`
		Edges    []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, string(body))
	}
	return resp.Title, resp.Concepts, resp.Edges
}

func TestMaps_Graph_RootScopedAndPainted(t *testing.T) {
	w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	title, concepts, edges := mapsDecodeGraph(t, w.Body.Bytes())
	if title != "Algebra" {
		t.Errorf("title = %q; want Algebra", title)
	}
	// Only the sub-tree (root, lin, quad) — NOT the out-of-tree Poetry concept.
	byTitle := map[string]map[string]any{}
	for _, c := range concepts {
		byTitle[c["title"].(string)] = c
	}
	if len(concepts) != 3 {
		t.Fatalf("concepts = %d; want 3 (root+lin+quad), got %v", len(concepts), byTitle)
	}
	if _, ok := byTitle["Poetry"]; ok {
		t.Error("out-of-tree concept Poetry leaked into the map graph")
	}
	if ge, _ := byTitle["Linear Equations"]["growthEdge"].(map[string]any); ge == nil {
		t.Error("Linear Equations should carry a shaky growthEdge overlay")
	}
	if byTitle["Quadratics"]["mastered"] != true {
		t.Errorf("Quadratics mastered = %v; want true", byTitle["Quadratics"]["mastered"])
	}
	if byTitle["Algebra"]["growthEdge"] != nil || byTitle["Algebra"]["mastered"] == true {
		t.Error("Algebra (root) should carry no overlay")
	}
	// Both hierarchy edges are intra-subtree; no edge references the out-of-tree node.
	if len(edges) != 2 {
		t.Fatalf("edges = %d; want 2", len(edges))
	}
}

func TestMaps_Graph_NotFound(t *testing.T) {
	if w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps/nope/graph", true); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

func TestMaps_Graph_CrossLearnerLeakGuard(t *testing.T) {
	// Goal returned by the repo belongs to a DIFFERENT learner → 404 (never leak).
	g := mapsGoal("g-x", mapsPtr("c-root"))
	g.LearnerGCID = "01970000-0000-7000-9000-0000000000zz"
	s := mapsFullServer(t)
	s.Goals = &mapsGoalStub{byID: map[string]*goal.Goal{"g-x": g}}
	if w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-x/graph", true); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (cross-learner leak guard)", w.Code)
	}
}

func TestMaps_Graph_RootlessGoalEmpty(t *testing.T) {
	g := mapsGoal("g-nr", nil) // no RootConceptID
	s := mapsFullServer(t)
	s.Goals = &mapsGoalStub{byID: map[string]*goal.Goal{"g-nr": g}}
	w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-nr/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	_, concepts, edges := mapsDecodeGraph(t, w.Body.Bytes())
	if len(concepts) != 0 || len(edges) != 0 {
		t.Fatalf("rootless map should be empty; got concepts=%d edges=%d", len(concepts), len(edges))
	}
}

func TestMaps_Graph_BadSuffix404(t *testing.T) {
	// The by-id subtree only serves /{goalId}/graph.
	if w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps/g-1", true); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 for missing /graph suffix", w.Code)
	}
}

func TestMaps_Graph_FailSoftOverlay(t *testing.T) {
	// No LearnerWeakness repo → graph still 200, unpainted (fail-soft).
	s := mapsFullServer(t)
	s.LearnerWeakness = nil
	w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	_, concepts, _ := mapsDecodeGraph(t, w.Body.Bytes())
	for _, c := range concepts {
		if c["growthEdge"] != nil || c["mastered"] == true {
			t.Errorf("%v: expected no paint without weakness repo", c["title"])
		}
	}
}

// ---- attached companion name (CHO-2109) ---------------------------------------

// mapsFamStub is a companion.InstanceRepository double with a configurable Get;
// embeds acqStubInstances for the remaining (unused) methods.
type mapsFamStub struct {
	acqStubInstances
	get    *companion.Instance
	getErr error
}

func (s *mapsFamStub) Get(context.Context, string) (*companion.Instance, error) {
	return s.get, s.getErr
}

// mapsBoundServer is mapsFullServer with the goal bound to companion fid.
func mapsBoundServer(t *testing.T, fid string, fam *mapsFamStub) *ExtServer {
	t.Helper()
	s := mapsFullServer(t)
	s.Goals.(*mapsGoalStub).byID["g-1"].AttachedCompanionID = &fid
	s.CompanionInstances = fam
	return s
}

func mapsGraphBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal graph resp: %v", err)
	}
	return body
}

func TestMaps_Graph_CompanionName_Present(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000f1"
	s := mapsBoundServer(t, fid, &mapsFamStub{get: &companion.Instance{
		CompanionID: fid, TenantID: fmTenantID, OwnerGCID: fmGCID, Name: "Ember",
	}})
	w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got, _ := mapsGraphBody(t, w)["attachedCompanionName"].(string); got != "Ember" {
		t.Fatalf("attachedCompanionName = %q, want \"Ember\"", got)
	}
}

func TestMaps_Graph_CompanionName_AbsentWhenUnbound(t *testing.T) {
	w := mapsServe(mapsFullServer(t), http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if _, present := mapsGraphBody(t, w)["attachedCompanionName"]; present {
		t.Fatalf("attachedCompanionName present on an unbound goal")
	}
}

func TestMaps_Graph_CompanionName_FailSoftOnReadError(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000f1"
	s := mapsBoundServer(t, fid, &mapsFamStub{getErr: errors.New("boom")})
	w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (name path must be fail-soft)", w.Code)
	}
	if _, present := mapsGraphBody(t, w)["attachedCompanionName"]; present {
		t.Fatalf("attachedCompanionName present despite instance read error")
	}
}

func TestMaps_Graph_CompanionName_NoCrossOwnerLeak(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000f1"
	s := mapsBoundServer(t, fid, &mapsFamStub{get: &companion.Instance{
		CompanionID: fid, TenantID: fmTenantID, OwnerGCID: "someone-else", Name: "NotYours",
	}})
	w := mapsServe(s, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if _, present := mapsGraphBody(t, w)["attachedCompanionName"]; present {
		t.Fatalf("attachedCompanionName leaked another owner's companion")
	}
}
