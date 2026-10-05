// concept_suggestion_handler_test.go — ADR-212 WS-4 learner curation surface:
// list pending Companion suggestions + Accept (mint concept/edge) / Dismiss.
// Mirrors concept_authoring_handler_test.go (recording stubs + caReq header
// helper + httptest).
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---- generate (WS-A3: resolve the map's Companion from the Goal, not a theme) ----

// csGeneratePayload finds the requested-event payload the generate handler emits.
func csGeneratePayload(t *testing.T, pub *events.InMemoryPublisher) map[string]any {
	t.Helper()
	evs := pub.Events()
	if len(evs) == 0 {
		t.Fatal("no concept_suggestion.requested event published")
	}
	return evs[len(evs)-1].Payload
}

func TestSuggestionGenerate_ResolvesCompanionFromGoal(t *testing.T) {
	fid := "01970000-0000-7000-a000-0000000000d1"
	g := mapsGoal("g-1", mapsPtr("c-root"))
	g.AttachedCompanionID = &fid
	pub := events.NewInMemoryPublisher()
	// WS-C4: the generate door now refuses whole-map requests on goal maps and
	// won-gates goal-subtree focals — this attribution test anchors on the WON
	// root so it exercises the post-gate happy path.
	s := &ExtServer{
		Concepts:         acqRootConcept("Scrum"),
		ConceptEdges:     &reStubEdges{},
		CampaignProgress: &gateProgress{rows: map[string]*campaign.NodeProgress{"c-root": gateWon("c-root")}},
		Goals:            &mapsGoalStub{byID: map[string]*goal.Goal{"g-1": g}},
		Publisher:        pub,
	}
	w := httptest.NewRecorder()
	s.handleMeSuggestionGenerate(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/generate", `{"goalId":"g-1","focalConceptId":"c-root"}`, fmTenantID, fmGCID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	p := csGeneratePayload(t, pub)
	if p["familiar_id"] != fid {
		t.Errorf("companion_id = %v; want %s (resolved from Goal.AttachedCompanionID)", p["familiar_id"], fid)
	}
	if p["map_theme"] != "Scrum" {
		t.Errorf("map_theme = %v; want Scrum (root concept title)", p["map_theme"])
	}
}

func TestSuggestionGenerate_NoGoal_UnattributedStillGenerates(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	s := &ExtServer{Concepts: acqRootConcept("Scrum"), Publisher: pub}
	w := httptest.NewRecorder()
	s.handleMeSuggestionGenerate(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/generate", `{}`, fmTenantID, fmGCID))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202 (%s)", w.Code, w.Body.String())
	}
	if p := csGeneratePayload(t, pub); p["familiar_id"] != "" {
		t.Errorf("companion_id = %v; want empty (no goal/Companion → unattributed fog)", p["familiar_id"])
	}
}

func TestSuggestionGenerate_Unwired503(t *testing.T) {
	s := &ExtServer{} // no Concepts / Publisher
	w := httptest.NewRecorder()
	s.handleMeSuggestionGenerate(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/generate", `{}`, fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

const (
	csSugA     = "01990000-0000-7000-5000-0000000000a1"
	csSugB     = "01990000-0000-7000-5000-0000000000b2"
	csConceptS = "01990000-0000-7000-c000-0000000000c3"
	csConceptT = "01990000-0000-7000-c000-0000000000d4"
)

// ---- recording stubs ----

type csStubSuggestions struct {
	byID         map[string]*conceptgraph.Suggestion
	pending      []*conceptgraph.Suggestion
	pendingFocal []*conceptgraph.Suggestion
	gotFocal     string // records the focal arg ListPendingForFocal was called with
	listAllHit   bool   // records the whole-map ListPending path was taken
	updated      *conceptgraph.Suggestion
	getErr       error
	listErr      error
	updateErr    error
}

func (s *csStubSuggestions) Create(context.Context, *conceptgraph.Suggestion) error { return nil }
func (s *csStubSuggestions) CreateBatch(context.Context, []*conceptgraph.Suggestion) error {
	return nil
}
func (s *csStubSuggestions) GetByID(_ context.Context, _, _, id string) (*conceptgraph.Suggestion, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.byID == nil {
		return nil, nil
	}
	return s.byID[id], nil
}
func (s *csStubSuggestions) GetBySourceEvent(context.Context, string, string, string) (*conceptgraph.Suggestion, error) {
	return nil, nil
}
func (s *csStubSuggestions) ListPending(context.Context, string, string) ([]*conceptgraph.Suggestion, error) {
	s.listAllHit = true
	return s.pending, s.listErr
}
func (s *csStubSuggestions) ListPendingForFocal(_ context.Context, _, _, focal string) ([]*conceptgraph.Suggestion, error) {
	s.gotFocal = focal
	return s.pendingFocal, s.listErr
}
func (s *csStubSuggestions) Update(_ context.Context, sug *conceptgraph.Suggestion) error {
	s.updated = sug
	return s.updateErr
}

type csStubAccepter struct {
	sug     *conceptgraph.Suggestion
	node    *conceptgraph.ConceptNode
	edge    *conceptgraph.Edge
	applied bool
	err     error
}

func (s *csStubAccepter) ApplyAccept(_ context.Context, sug *conceptgraph.Suggestion, node *conceptgraph.ConceptNode, edge *conceptgraph.Edge) error {
	s.applied = true
	s.sug, s.node, s.edge = sug, node, edge
	return s.err
}

func pendingConceptSug(id string) *conceptgraph.Suggestion {
	return &conceptgraph.Suggestion{
		SuggestionID: id, TenantID: fmTenantID, LearnerGCID: fmGCID,
		Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
		Title: "Fibonacci sequence", Rationale: "relates to story points",
		ModelID: "gemini-2.5-flash", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func pendingEdgeSug(id string) *conceptgraph.Suggestion {
	return &conceptgraph.Suggestion{
		SuggestionID: id, TenantID: fmTenantID, LearnerGCID: fmGCID,
		Kind: conceptgraph.SuggestionKindEdge, Status: conceptgraph.SuggestionStatusPending,
		SourceConceptID: csConceptS, TargetConceptID: csConceptT, EdgeClass: conceptgraph.EdgeClassHierarchy,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

// ---- list ----

func TestSuggestionsList_Happy(t *testing.T) {
	cs := &csStubSuggestions{pending: []*conceptgraph.Suggestion{pendingConceptSug(csSugA), pendingEdgeSug(csSugB)}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Suggestions []map[string]any `json:"suggestions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Suggestions) != 2 {
		t.Fatalf("got %d suggestions; want 2", len(resp.Suggestions))
	}
	if resp.Suggestions[0]["kind"] != "concept" || resp.Suggestions[0]["status"] != "pending" {
		t.Errorf("row0 = %+v", resp.Suggestions[0])
	}
}

// The suggestion LIST wire carries focalConceptId — the fog-ghost anchor the
// canvas needs to fan a ghost around its focal node (contract §6). Passthrough
// from the aggregate; a whole-map suggestion (empty focal) omits the key.
func TestSuggestionsList_CarriesFocalConceptID(t *testing.T) {
	focal := "01990000-0000-7000-c000-0000000000f9"
	withFocal := pendingConceptSug(csSugA)
	withFocal.FocalConceptID = focal
	wholeMap := pendingConceptSug(csSugB) // no focal (whole-map)
	cs := &csStubSuggestions{pending: []*conceptgraph.Suggestion{withFocal, wholeMap}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Suggestions []map[string]any `json:"suggestions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Suggestions) != 2 {
		t.Fatalf("got %d suggestions; want 2", len(resp.Suggestions))
	}
	if resp.Suggestions[0]["focalConceptId"] != focal {
		t.Errorf("row0 focalConceptId = %v; want %q", resp.Suggestions[0]["focalConceptId"], focal)
	}
	if _, ok := resp.Suggestions[1]["focalConceptId"]; ok {
		t.Errorf("whole-map suggestion must omit focalConceptId: %+v", resp.Suggestions[1])
	}
}

// A ?focalConceptId= query routes to the focal-scoped list (only this node's +
// whole-map suggestions), NOT the unfiltered ListPending — the bug fix so a
// stale prior-generate batch (a different focal) doesn't show on every node.
func TestSuggestionsList_FocalScopedRoutesToFocalList(t *testing.T) {
	focal := "01990000-0000-7000-c000-0000000000f9"
	cs := &csStubSuggestions{pendingFocal: []*conceptgraph.Suggestion{pendingConceptSug(csSugA)}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions?focalConceptId="+focal, "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if cs.gotFocal != focal {
		t.Errorf("ListPendingForFocal focal = %q; want %q", cs.gotFocal, focal)
	}
	if cs.listAllHit {
		t.Error("must NOT fall back to unfiltered ListPending when a focal is given")
	}
	var resp struct {
		Suggestions []map[string]any `json:"suggestions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Suggestions) != 1 {
		t.Fatalf("got %d suggestions; want 1 (focal-scoped)", len(resp.Suggestions))
	}
}

// No ?focalConceptId= ⇒ back-compat whole-map list via ListPending.
func TestSuggestionsList_NoFocalUsesListPending(t *testing.T) {
	cs := &csStubSuggestions{pending: []*conceptgraph.Suggestion{pendingConceptSug(csSugA)}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if !cs.listAllHit {
		t.Error("no focal ⇒ must use unfiltered ListPending (back-compat)")
	}
	if cs.gotFocal != "" {
		t.Errorf("no focal ⇒ ListPendingForFocal must not be called; gotFocal=%q", cs.gotFocal)
	}
}

func TestSuggestionsList_Unwired503(t *testing.T) {
	s := &ExtServer{}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

func TestSuggestionsList_MissingContext400(t *testing.T) {
	s := &ExtServer{Suggestions: &csStubSuggestions{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestions(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", "", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
}

// ---- accept ----

func TestSuggestionAccept_ConceptMintsNode(t *testing.T) {
	cs := &csStubSuggestions{byID: map[string]*conceptgraph.Suggestion{csSugA: pendingConceptSug(csSugA)}}
	acc := &csStubAccepter{}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: acc}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if !acc.applied || acc.node == nil {
		t.Fatalf("ApplyAccept not called with a minted node: %+v", acc)
	}
	if acc.edge != nil {
		t.Error("concept accept must not mint an edge")
	}
	if acc.node.Provenance != conceptgraph.ProvenanceCompanionSuggestedAccepted {
		t.Errorf("minted node provenance = %q", acc.node.Provenance)
	}
	if acc.sug.Status != conceptgraph.SuggestionStatusAccepted {
		t.Errorf("suggestion status = %q; want accepted", acc.sug.Status)
	}
}

func TestSuggestionAccept_EdgeMintsEdge(t *testing.T) {
	cs := &csStubSuggestions{byID: map[string]*conceptgraph.Suggestion{csSugB: pendingEdgeSug(csSugB)}}
	acc := &csStubAccepter{}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: acc}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugB+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if acc.edge == nil || acc.node != nil {
		t.Fatalf("edge accept must mint an edge only: %+v", acc)
	}
	if acc.edge.Class != conceptgraph.EdgeClassHierarchy {
		t.Errorf("minted edge class = %q", acc.edge.Class)
	}
}

func TestSuggestionAccept_NotFound404(t *testing.T) {
	s := &ExtServer{Suggestions: &csStubSuggestions{}, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestSuggestionAccept_AlreadyDecided409(t *testing.T) {
	decided := pendingConceptSug(csSugA)
	decided.Status = conceptgraph.SuggestionStatusAccepted
	cs := &csStubSuggestions{byID: map[string]*conceptgraph.Suggestion{csSugA: decided}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 (%s)", w.Code, w.Body.String())
	}
}

// ---- dismiss ----

func TestSuggestionDismiss_Happy(t *testing.T) {
	cs := &csStubSuggestions{byID: map[string]*conceptgraph.Suggestion{csSugA: pendingConceptSug(csSugA)}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/dismiss", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	if cs.updated == nil || cs.updated.Status != conceptgraph.SuggestionStatusDismissed {
		t.Fatalf("suggestion not dismissed: %+v", cs.updated)
	}
}

func TestSuggestionByID_UnknownAction404(t *testing.T) {
	cs := &csStubSuggestions{byID: map[string]*conceptgraph.Suggestion{csSugA: pendingConceptSug(csSugA)}}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/frobnicate", "", fmTenantID, fmGCID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
}

func TestSuggestionByID_Unwired503(t *testing.T) {
	s := &ExtServer{}
	w := httptest.NewRecorder()
	s.handleMeSuggestionByID(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", w.Code)
	}
}

// TestSuggestionRouting proves the extMux dispatches the exact list path and the
// {id}/accept prefix path to the right handlers (not 404) — the nesting under
// /concept-graph must not be shadowed by concepts/ or edges/.
func TestSuggestionRouting(t *testing.T) {
	cs := &csStubSuggestions{
		byID:    map[string]*conceptgraph.Suggestion{csSugA: pendingConceptSug(csSugA)},
		pending: []*conceptgraph.Suggestion{pendingConceptSug(csSugA)},
	}
	s := &ExtServer{Suggestions: cs, SuggestionAccept: &csStubAccepter{}}
	mux := s.Routes()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, caReq(http.MethodGet, "/v1/me/concept-graph/suggestions", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("list route status=%d (%s)", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, caReq(http.MethodPost, "/v1/me/concept-graph/suggestions/"+csSugA+"/accept", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("accept route status=%d (%s)", w.Code, w.Body.String())
	}
}
