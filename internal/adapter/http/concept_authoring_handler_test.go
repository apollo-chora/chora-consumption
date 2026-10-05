// concept_authoring_handler_test.go — ADR-212 D1/D2 learner authoring surface
// (CJ buildout): create/rename/attach-atoms/delete ConceptNodes + create/delete
// Edges + list Companion map-bindings. Without this the sovereign map can be read
// and re-rooted but never BUILT.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	caConceptA = "01980000-0000-7000-8000-0000000000a1"
	caConceptB = "01980000-0000-7000-8000-0000000000b2"
	caAtom1    = "01980000-0000-7000-9000-0000000000c3"
)

// ---- recording stubs ----

type caStubConcepts struct {
	byID      map[string]*conceptgraph.ConceptNode
	created   *conceptgraph.ConceptNode
	updated   *conceptgraph.ConceptNode
	createErr error
	getErr    error
	updateErr error
}

func (s *caStubConcepts) Create(_ context.Context, c *conceptgraph.ConceptNode) error {
	s.created = c
	return s.createErr
}
func (s *caStubConcepts) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.byID == nil {
		return nil, nil
	}
	return s.byID[id], nil
}
func (s *caStubConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (s *caStubConcepts) Update(_ context.Context, c *conceptgraph.ConceptNode) error {
	s.updated = c
	return s.updateErr
}

type caStubEdges struct {
	byID               map[string]*conceptgraph.Edge
	created            *conceptgraph.Edge
	updated            *conceptgraph.Edge
	createErr          error
	updateErr          error
	softDeletedConcept string
	softDeletedCount   int64
}

func (s *caStubEdges) Create(_ context.Context, e *conceptgraph.Edge) error {
	s.created = e
	return s.createErr
}
func (s *caStubEdges) GetByID(_ context.Context, _, _, id string) (*conceptgraph.Edge, error) {
	if s.byID == nil {
		return nil, nil
	}
	return s.byID[id], nil
}
func (s *caStubEdges) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *caStubEdges) ListByConcept(context.Context, string, string, string) ([]*conceptgraph.Edge, error) {
	return nil, nil
}
func (s *caStubEdges) Update(_ context.Context, e *conceptgraph.Edge) error {
	s.updated = e
	return s.updateErr
}
func (s *caStubEdges) SoftDeleteByConcept(_ context.Context, _, _, conceptID string, _ time.Time) (int64, error) {
	s.softDeletedConcept = conceptID
	return s.softDeletedCount, nil
}

func caReq(method, path, body, tenant, gcid string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if tenant != "" {
		r.Header.Set("X-Tenant-Id", tenant)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	return r
}

func liveConcept(id, title string) *conceptgraph.ConceptNode {
	return &conceptgraph.ConceptNode{ConceptID: id, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: title, Provenance: conceptgraph.ProvenanceLearnerAuthored}
}

// ---- concept create ----

func TestConceptCreate_Happy(t *testing.T) {
	cs := &caStubConcepts{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/concepts", `{"title":"Story Points","atomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (%s)", w.Code, w.Body.String())
	}
	if cs.created == nil || cs.created.Title != "Story Points" {
		t.Fatalf("concept not created: %+v", cs.created)
	}
	if cs.created.TenantID != fmTenantID || cs.created.LearnerGCID != fmGCID {
		t.Errorf("scope wrong: %+v", cs.created)
	}
	if len(cs.created.AtomRefs) != 1 || cs.created.AtomRefs[0] != caAtom1 {
		t.Errorf("atomRefs wrong: %+v", cs.created.AtomRefs)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["title"] != "Story Points" || resp["conceptId"] == "" {
		t.Errorf("resp wrong: %+v", resp)
	}
}

func TestConceptCreate_MissingTitle_422(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/concepts", `{"title":"  "}`, fmTenantID, fmGCID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", w.Code)
	}
}

func TestConceptCreate_BadAtomRef_422(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/concepts", `{"title":"X","atomRefs":["not-a-uuid"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422", w.Code)
	}
}

func TestConceptCreate_Unwired_503(t *testing.T) {
	s := &ExtServer{}
	w := httptest.NewRecorder()
	s.handleMeConceptCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/concepts", `{"title":"X"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

func TestConceptCreate_MissingContext_400(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/concepts", `{"title":"X"}`, "", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

// ---- concept patch (rename / attach / detach) ----

func TestConceptPatch_Rename(t *testing.T) {
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: liveConcept(caConceptA, "Old")}}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA, `{"title":"Story Point Estimation"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if cs.updated == nil || cs.updated.Title != "Story Point Estimation" {
		t.Errorf("not renamed: %+v", cs.updated)
	}
}

func TestConceptPatch_AttachAtom(t *testing.T) {
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: liveConcept(caConceptA, "C")}}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA, `{"addAtomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if cs.updated == nil || len(cs.updated.AtomRefs) != 1 || cs.updated.AtomRefs[0] != caAtom1 {
		t.Errorf("atom not attached: %+v", cs.updated)
	}
}

func TestConceptPatch_DetachAtom(t *testing.T) {
	c := liveConcept(caConceptA, "C")
	c.AtomRefs = []string{caAtom1}
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: c}}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA, `{"removeAtomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if cs.updated == nil || len(cs.updated.AtomRefs) != 0 {
		t.Errorf("atom not detached: %+v", cs.updated)
	}
}

func TestConceptPatch_NotFound_404(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA, `{"title":"X"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestConceptDelete_SoftDeletes(t *testing.T) {
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: liveConcept(caConceptA, "C")}}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodDelete, "/v1/me/concept-graph/concepts/"+caConceptA, "", fmTenantID, fmGCID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", w.Code)
	}
	if cs.updated == nil || cs.updated.DeletedAt == nil {
		t.Errorf("not soft-deleted: %+v", cs.updated)
	}
}

func TestConceptDelete_NotFound_404(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodDelete, "/v1/me/concept-graph/concepts/"+caConceptA, "", fmTenantID, fmGCID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

// ---- edge create / delete ----

func TestEdgeCreate_Happy(t *testing.T) {
	es := &caStubEdges{}
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: es}
	w := httptest.NewRecorder()
	body := `{"sourceConceptId":"` + caConceptA + `","targetConceptId":"` + caConceptB + `","class":"hierarchy"}`
	s.handleMeEdgeCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/edges", body, fmTenantID, fmGCID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (%s)", w.Code, w.Body.String())
	}
	if es.created == nil || es.created.SourceConceptID != caConceptA || es.created.TargetConceptID != caConceptB {
		t.Fatalf("edge not created: %+v", es.created)
	}
	if es.created.Class != conceptgraph.EdgeClassHierarchy {
		t.Errorf("class wrong: %v", es.created.Class)
	}
}

func TestEdgeCreate_SelfLoop_422(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	body := `{"sourceConceptId":"` + caConceptA + `","targetConceptId":"` + caConceptA + `","class":"lateral"}`
	s.handleMeEdgeCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/edges", body, fmTenantID, fmGCID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (self-loop)", w.Code)
	}
}

func TestEdgeCreate_BadClass_422(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	body := `{"sourceConceptId":"` + caConceptA + `","targetConceptId":"` + caConceptB + `","class":"nonsense"}`
	s.handleMeEdgeCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/edges", body, fmTenantID, fmGCID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (bad class)", w.Code)
	}
}

func TestEdgeDelete_SoftDeletes(t *testing.T) {
	e := &conceptgraph.Edge{EdgeID: "e1", TenantID: fmTenantID, LearnerGCID: fmGCID, SourceConceptID: caConceptA, TargetConceptID: caConceptB, Class: conceptgraph.EdgeClassHierarchy}
	es := &caStubEdges{byID: map[string]*conceptgraph.Edge{"e1": e}}
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: es}
	w := httptest.NewRecorder()
	s.handleMeEdgeByID(w, caReq(http.MethodDelete, "/v1/me/concept-graph/edges/e1", "", fmTenantID, fmGCID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", w.Code)
	}
	if es.updated == nil || es.updated.DeletedAt == nil {
		t.Errorf("edge not soft-deleted: %+v", es.updated)
	}
}

func TestEdgeDelete_NotFound_404(t *testing.T) {
	s := &ExtServer{Concepts: &caStubConcepts{}, ConceptEdges: &caStubEdges{}}
	w := httptest.NewRecorder()
	s.handleMeEdgeByID(w, caReq(http.MethodDelete, "/v1/me/concept-graph/edges/nope", "", fmTenantID, fmGCID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestEdgeCreate_Unwired_503(t *testing.T) {
	s := &ExtServer{}
	w := httptest.NewRecorder()
	s.handleMeEdgeCreate(w, caReq(http.MethodPost, "/v1/me/concept-graph/edges", `{}`, fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

// Bindings-list tests moved to companion_bindings_handler_test.go (WS-A3: the
// list is now sourced from Goals, not the retired CompanionMapBinding).

// ---- ADR-244 D4 (CHO-2303): the binding emits a domain event ----
//
// Without these the emission path is written and read by nothing, and a green
// suite proves only that no OTHER test touched it.

// A second atom so an attach can be distinguished from a full replacement.
const caAtom2 = "01980000-0000-7000-9000-0000000000c4"

type caStubConceptEvents struct {
	calls        int
	got          events.ConceptAtomsBoundInput
	deletedCalls int
	deleted      events.ConceptDeletedInput
}

func (s *caStubConceptEvents) ConceptAtomsBound(_ context.Context, in events.ConceptAtomsBoundInput) error {
	s.calls++
	s.got = in
	return nil
}

func (s *caStubConceptEvents) ConceptDeleted(_ context.Context, in events.ConceptDeletedInput) error {
	s.deletedCalls++
	s.deleted = in
	return nil
}

// CHO-2324: a concept soft-delete MUST emit chora.consumption.concept.deleted.v1
// so the edge-cleanup subscriber can cascade. Without this the emit path is dead
// and orphaned edges accumulate.
func TestConceptDelete_EmitsConceptDeleted(t *testing.T) {
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: liveConcept(caConceptA, "C")}}
	ev := &caStubConceptEvents{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}, ConceptEvents: ev}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodDelete, "/v1/me/concept-graph/concepts/"+caConceptA, "", fmTenantID, fmGCID))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", w.Code)
	}
	if ev.deletedCalls != 1 {
		t.Fatalf("concept.deleted emitted %d times; want exactly 1", ev.deletedCalls)
	}
	if ev.deleted.ConceptID != caConceptA {
		t.Errorf("emitted concept_id=%q; want %q", ev.deleted.ConceptID, caConceptA)
	}
	// The cascade is learner+tenant scoped; the emit must carry both.
	if ev.deleted.LearnerGCID == "" || ev.deleted.TenantID == "" {
		t.Errorf("emit not scoped for the cascade: %+v", ev.deleted)
	}
}

func TestConceptPatch_AttachEmitsAtomsBound(t *testing.T) {
	c := liveConcept(caConceptA, "C")
	c.AtomRefs = []string{caAtom2}
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: c}}
	ev := &caStubConceptEvents{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}, ConceptEvents: ev}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA,
		`{"addAtomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if ev.calls != 1 {
		t.Fatalf("emitted %d event(s), want 1", ev.calls)
	}
	if len(ev.got.AttachedAtomIDs) != 1 || ev.got.AttachedAtomIDs[0] != caAtom1 {
		t.Errorf("attached = %v, want [%s]", ev.got.AttachedAtomIDs, caAtom1)
	}
	if len(ev.got.DetachedAtomIDs) != 0 {
		t.Errorf("detached = %v, want empty", ev.got.DetachedAtomIDs)
	}
	// The resulting state must be the FULL set, which is what makes a consumer
	// self-healing rather than dependent on replaying every prior delta.
	if len(ev.got.ResultingAtomRefs) != 2 {
		t.Errorf("resulting = %v, want both atoms", ev.got.ResultingAtomRefs)
	}
	if ev.got.ChangeSource != events.ChangeSourceManualAttach {
		t.Errorf("change_source = %q, want %q", ev.got.ChangeSource, events.ChangeSourceManualAttach)
	}
	if ev.got.ConceptID != caConceptA || ev.got.LearnerGCID == "" || ev.got.TenantID == "" {
		t.Errorf("event unscoped: %+v", ev.got)
	}
}

func TestConceptPatch_DetachEmitsAtomsBound(t *testing.T) {
	c := liveConcept(caConceptA, "C")
	c.AtomRefs = []string{caAtom1}
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: c}}
	ev := &caStubConceptEvents{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}, ConceptEvents: ev}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA,
		`{"removeAtomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if ev.calls != 1 {
		t.Fatalf("emitted %d event(s), want 1", ev.calls)
	}
	if len(ev.got.DetachedAtomIDs) != 1 || ev.got.DetachedAtomIDs[0] != caAtom1 {
		t.Errorf("detached = %v, want [%s]", ev.got.DetachedAtomIDs, caAtom1)
	}
	if len(ev.got.ResultingAtomRefs) != 0 {
		t.Errorf("resulting = %v, want empty (an empty concept is first-class)", ev.got.ResultingAtomRefs)
	}
}

// An idempotent re-attach changes nothing, so it must not emit. The event is
// derived from the actual before/after sets, not from the request body.
func TestConceptPatch_IdempotentReattachEmitsNothing(t *testing.T) {
	c := liveConcept(caConceptA, "C")
	c.AtomRefs = []string{caAtom1}
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: c}}
	ev := &caStubConceptEvents{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}, ConceptEvents: ev}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA,
		`{"addAtomRefs":["`+caAtom1+`"]}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if ev.calls != 0 {
		t.Fatalf("a no-op re-attach emitted %d event(s), want 0", ev.calls)
	}
}

// A rename touches the concept but changes no bindings: emitting here would be
// phantom churn every consumer then has to defend against.
func TestConceptPatch_RenameEmitsNoBindingEvent(t *testing.T) {
	cs := &caStubConcepts{byID: map[string]*conceptgraph.ConceptNode{caConceptA: liveConcept(caConceptA, "C")}}
	ev := &caStubConceptEvents{}
	s := &ExtServer{Concepts: cs, ConceptEdges: &caStubEdges{}, ConceptEvents: ev}
	w := httptest.NewRecorder()
	s.handleMeConceptByID(w, caReq(http.MethodPatch, "/v1/me/concept-graph/concepts/"+caConceptA,
		`{"title":"Renamed"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if ev.calls != 0 {
		t.Fatalf("a rename emitted %d binding event(s), want 0", ev.calls)
	}
}
