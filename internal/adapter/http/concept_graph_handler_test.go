// concept_graph_handler_test.go — ADR-212 WS-2 re-root + concept-graph read
// handlers (CHO-1995 wire-up).
//
// Covers: the append-only re-root happy path (flip + Apply), the already-apex
// no-op (Apply NOT called), unknown-root 404, empty-target 422, the multiple-
// parents 409, unwired 503, apply-error 500, method 405, missing-context 400,
// and the GET list projection. Stubs reuse fmStubConcepts (ConceptNodeRepository)
// from the memory-read test in this package; edges + applier are stubbed here.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// ---- stubs (ConceptNodeRepository = fmStubConcepts, reused from the sibling test) ----

type reStubEdges struct {
	out []*conceptgraph.Edge
	err error
}

func (s *reStubEdges) Create(context.Context, *conceptgraph.Edge) error { return nil }
func (s *reStubEdges) GetByID(context.Context, string, string, string) (*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *reStubEdges) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.out, s.err
}
func (s *reStubEdges) ListByConcept(context.Context, string, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *reStubEdges) Update(context.Context, *conceptgraph.Edge) error { return nil }
func (s *reStubEdges) SoftDeleteByConcept(context.Context, string, string, string, time.Time) (int64, error) {
	return 0, nil
}

type reStubApplier struct {
	got    conceptgraph.ReRootPlan
	called bool
	err    error
}

func (s *reStubApplier) Apply(_ context.Context, plan conceptgraph.ReRootPlan) error {
	s.called = true
	s.got = plan
	return s.err
}

// reGraph builds a two-node learner hierarchy A(parent) -> B(child).
func reGraph() ([]*conceptgraph.ConceptNode, []*conceptgraph.Edge) {
	concepts := []*conceptgraph.ConceptNode{
		{ConceptID: "concept-a", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Agile"},
		{ConceptID: "concept-b", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Story Points"},
	}
	edges := []*conceptgraph.Edge{
		{EdgeID: "edge-ab", TenantID: fmTenantID, LearnerGCID: fmGCID,
			SourceConceptID: "concept-a", TargetConceptID: "concept-b",
			Class: conceptgraph.EdgeClassHierarchy, Provenance: conceptgraph.ProvenanceLearnerAuthored},
	}
	return concepts, edges
}

func reRerootServe(s *ExtServer, body, tenantID, gcid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/me/concept-graph/reroot", strings.NewReader(body))
	if tenantID != "" {
		r.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	w := httptest.NewRecorder()
	s.handleMeConceptGraphReroot(w, r)
	return w
}

// ---- re-root tests ----------------------------------------------------------

func TestReRoot_HappyPath_FlipsAndApplies(t *testing.T) {
	concepts, edges := reGraph()
	applier := &reStubApplier{}
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{out: edges},
		ConceptReRoot: applier,
	}
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !applier.called {
		t.Fatal("applier.Apply not called on a non-no-op re-root")
	}
	// Append-only: one edge tombstoned + one flipped edge created.
	if len(applier.got.ToSoftDelete) != 1 || len(applier.got.ToCreate) != 1 {
		t.Fatalf("plan = %+v; want 1 soft-delete + 1 create", applier.got)
	}
	if applier.got.ToCreate[0].SourceConceptID != "concept-b" || applier.got.ToCreate[0].TargetConceptID != "concept-a" {
		t.Errorf("flip endpoints = %s->%s; want concept-b->concept-a",
			applier.got.ToCreate[0].SourceConceptID, applier.got.ToCreate[0].TargetConceptID)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["changed"] != true {
		t.Errorf("changed = %v; want true", resp["changed"])
	}
	if resp["newRootId"] != "concept-b" {
		t.Errorf("newRootId = %v", resp["newRootId"])
	}
}

func TestReRoot_AlreadyApex_IsNoOp_ApplyNotCalled(t *testing.T) {
	concepts, edges := reGraph()
	applier := &reStubApplier{}
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{out: edges},
		ConceptReRoot: applier,
	}
	// concept-a is already the apex → no-op plan.
	w := reRerootServe(s, `{"newRootId":"concept-a"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if applier.called {
		t.Error("applier.Apply called on a no-op re-root; want skipped")
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["changed"] != false {
		t.Errorf("changed = %v; want false (already apex)", resp["changed"])
	}
}

func TestReRoot_UnknownRoot_Returns404(t *testing.T) {
	concepts, edges := reGraph()
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{out: edges},
		ConceptReRoot: &reStubApplier{},
	}
	w := reRerootServe(s, `{"newRootId":"concept-z"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (unknown root)", w.Code)
	}
}

func TestReRoot_EmptyTarget_Returns422(t *testing.T) {
	s := &ExtServer{
		Concepts:      &fmStubConcepts{},
		ConceptEdges:  &reStubEdges{},
		ConceptReRoot: &reStubApplier{},
	}
	w := reRerootServe(s, `{"newRootId":""}`, fmTenantID, fmGCID)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (empty target)", w.Code)
	}
}

func TestReRoot_MultipleParents_Returns409(t *testing.T) {
	// B has TWO live hierarchy parents (A and C) → ambiguous re-root.
	concepts := []*conceptgraph.ConceptNode{
		{ConceptID: "concept-a", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "A"},
		{ConceptID: "concept-b", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "B"},
		{ConceptID: "concept-c", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "C"},
	}
	edges := []*conceptgraph.Edge{
		{EdgeID: "e1", TenantID: fmTenantID, LearnerGCID: fmGCID, SourceConceptID: "concept-a", TargetConceptID: "concept-b", Class: conceptgraph.EdgeClassHierarchy},
		{EdgeID: "e2", TenantID: fmTenantID, LearnerGCID: fmGCID, SourceConceptID: "concept-c", TargetConceptID: "concept-b", Class: conceptgraph.EdgeClassHierarchy},
	}
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{out: edges},
		ConceptReRoot: &reStubApplier{},
	}
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (multiple parents)", w.Code)
	}
}

func TestReRoot_Unwired_Returns503(t *testing.T) {
	s := &ExtServer{} // no repos
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}

func TestReRoot_ApplyError_Returns500(t *testing.T) {
	concepts, edges := reGraph()
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{out: edges},
		ConceptReRoot: &reStubApplier{err: errors.New("tx failed")},
	}
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (apply failed)", w.Code)
	}
}

func TestReRoot_LoadEdgesError_Returns500(t *testing.T) {
	concepts, _ := reGraph()
	s := &ExtServer{
		Concepts:      &fmStubConcepts{out: concepts},
		ConceptEdges:  &reStubEdges{err: errors.New("db down")},
		ConceptReRoot: &reStubApplier{},
	}
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (edge load failed)", w.Code)
	}
}

func TestReRoot_MethodNotAllowed_Returns405(t *testing.T) {
	s := &ExtServer{Concepts: &fmStubConcepts{}, ConceptEdges: &reStubEdges{}, ConceptReRoot: &reStubApplier{}}
	r := httptest.NewRequest(http.MethodGet, "/v1/me/concept-graph/reroot", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	s.handleMeConceptGraphReroot(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestReRoot_MissingContext_Returns400(t *testing.T) {
	s := &ExtServer{Concepts: &fmStubConcepts{}, ConceptEdges: &reStubEdges{}, ConceptReRoot: &reStubApplier{}}
	w := reRerootServe(s, `{"newRootId":"concept-b"}`, "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (missing context)", w.Code)
	}
}

// ---- GET /v1/me/concept-graph list ------------------------------------------

func TestConceptGraphList_ProjectsConceptsAndEdges(t *testing.T) {
	concepts, edges := reGraph()
	s := &ExtServer{Concepts: &fmStubConcepts{out: concepts}, ConceptEdges: &reStubEdges{out: edges}}
	r := httptest.NewRequest(http.MethodGet, "/v1/me/concept-graph", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	s.handleMeConceptGraph(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cs, _ := resp["concepts"].([]any)
	es, _ := resp["edges"].([]any)
	if len(cs) != 2 || len(es) != 1 {
		t.Fatalf("concepts=%d edges=%d; want 2 + 1", len(cs), len(es))
	}
	e0, _ := es[0].(map[string]any)
	if e0["class"] != "hierarchy" || e0["sourceConceptId"] != "concept-a" || e0["targetConceptId"] != "concept-b" {
		t.Errorf("edge0 wrong: %+v", e0)
	}
}

func TestConceptGraphList_Unwired_Returns503(t *testing.T) {
	s := &ExtServer{}
	r := httptest.NewRequest(http.MethodGet, "/v1/me/concept-graph", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	s.handleMeConceptGraph(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}
