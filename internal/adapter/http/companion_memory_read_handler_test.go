// companion_memory_read_handler_test.go — ADR-215 WS-1 tier-(a) learner-facing
// Companion memory + context READ handler (GET /v1/me/companions/{id}/memory).
//
// Verifies: the composed view (memories + persona/rules/focus + resolved KG
// neighbour citations); the owner-leak 404 guard (a learner only reads their
// own Companion); the honest "no memory yet" empty-state (ADR-215 D5); UUID→title
// resolution with an "unknown" (blank) fallback that is NEVER fabricated; and
// that no raw vectors / distances / model internals appear in the wire shape.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	atomindex "github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	fmTenantID    = "01970000-0000-7000-8000-0000000000aa"
	fmGCID        = "01970000-0000-7000-9000-0000000000bb"
	fmOtherGCID   = "01970000-0000-7000-9000-0000000000cc"
	fmCompanionID = "01970000-0000-7000-a000-0000000000dd"
	fmAtomID      = "01970000-0000-7000-b000-0000000000ee"
	fmConceptID   = "01970000-0000-7000-c000-0000000000ff"
)

// ---- stub ports -------------------------------------------------------------

// fmStubMemoryReader serves BOTH reads the handler makes: the unfiltered recency
// read (the panel's `memories`) and the memory_type-filtered research read
// (CHO-2185's `researchNotes`). It dispatches on MemoryTypes so each read's Query
// is captured separately — `got` remains the unfiltered read's, as before.
type fmStubMemoryReader struct {
	out         []companionmind.EpisodicMemory // unfiltered recency read
	research    []companionmind.EpisodicMemory // memory_type=research read
	err         error
	got         companionmind.Query // the unfiltered read's query
	gotResearch companionmind.Query // the research read's query
}

func (s *fmStubMemoryReader) RecentMemories(_ context.Context, q companionmind.Query) ([]companionmind.EpisodicMemory, error) {
	if len(q.MemoryTypes) > 0 {
		s.gotResearch = q
		return s.research, s.err
	}
	s.got = q
	return s.out, s.err
}

type fmStubInstances struct {
	inst *companion.Instance
	err  error
}

func (s *fmStubInstances) Create(context.Context, *companion.Instance, int) error { return nil }
func (s *fmStubInstances) Get(_ context.Context, _ string) (*companion.Instance, error) {
	return s.inst, s.err
}
func (s *fmStubInstances) ListByOwner(context.Context, string, string) ([]*companion.Instance, error) {
	return nil, nil
}
func (s *fmStubInstances) ListRosterByOwner(context.Context, string, string) ([]*companion.RosterEntry, error) {
	return nil, nil
}
func (s *fmStubInstances) Update(context.Context, *companion.Instance) error { return nil }
func (s *fmStubInstances) SoftDelete(context.Context, string) error          { return nil }

type fmStubConcepts struct {
	out []*conceptgraph.ConceptNode
	err error
}

func (s *fmStubConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *fmStubConcepts) GetByID(context.Context, string, string, string) (*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (s *fmStubConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return s.out, s.err
}
func (s *fmStubConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

type fmStubAtoms struct {
	byID map[string]*atomindex.AtomIndex
}

func (s *fmStubAtoms) Save(context.Context, *atomindex.AtomIndex) error { return nil }
func (s *fmStubAtoms) Get(_ context.Context, atomID string) (*atomindex.AtomIndex, error) {
	if a, ok := s.byID[atomID]; ok {
		return a, nil
	}
	return nil, atomindex.ErrNotFound
}
func (s *fmStubAtoms) ListByCourse(context.Context, string, string) ([]*atomindex.AtomIndex, error) {
	return nil, nil
}
func (s *fmStubAtoms) MarkPublished(context.Context, string, atomindex.Status, string, string, int, string) error {
	return nil
}
func (s *fmStubAtoms) SearchForLearner(context.Context, string, string, int) ([]*atomindex.AtomIndex, error) {
	return nil, nil
}

// ---- helpers ----------------------------------------------------------------

func fmOwnedInstance() *companion.Instance {
	return &companion.Instance{
		CompanionID:     fmCompanionID,
		TenantID:        fmTenantID,
		OwnerGCID:       fmGCID,
		Name:            "Byte",
		Specialization:  "coding",
		EvolutionTier:   companion.TierAdept,
		PersonaSummary:  "a patient mentor",
		ConfiguredRules: map[string]string{"tone": "warm"},
		SkillGrants:     []string{"debugging"},
	}
}

// fmServe builds the handler + serves a GET with valid context by default.
func fmServe(h *CompanionMemoryReadHandler, method, tenantID, gcid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/v1/me/companions/"+fmCompanionID+"/memory", nil)
	if tenantID != "" {
		r.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// ---- tests ------------------------------------------------------------------

func TestCompanionMemoryRead_HappyPath_ComposesViewAndResolvesCitations(t *testing.T) {
	created := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: []companionmind.EpisodicMemory{
			{ID: "m1", MemoryType: "chat_turn", Content: "we discussed base cases", CreatedAt: created},
		}},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: []*conceptgraph.ConceptNode{
			{ConceptID: fmConceptID, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Recursion", AtomRefs: []string{fmAtomID}},
		}},
		&fmStubAtoms{byID: map[string]*atomindex.AtomIndex{
			fmAtomID: {AtomID: fmAtomID, Title: "What is a base case?", TopicTags: []string{"cs", "recursion"}},
		}},
	)

	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body=%s)", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["companionId"] != fmCompanionID {
		t.Errorf("companionId = %v", resp["companionId"])
	}
	if resp["name"] != "Byte" || resp["focus"] != "coding" || resp["persona"] != "a patient mentor" {
		t.Errorf("identity/context fields wrong: %+v", resp)
	}
	if resp["evolutionTier"] != "adept" {
		t.Errorf("evolutionTier = %v; want adept", resp["evolutionTier"])
	}
	if resp["hasMemory"] != true {
		t.Errorf("hasMemory = %v; want true", resp["hasMemory"])
	}
	rules, _ := resp["rules"].(map[string]any)
	if rules["tone"] != "warm" {
		t.Errorf("rules = %+v; want tone=warm", resp["rules"])
	}
	mems, _ := resp["memories"].([]any)
	if len(mems) != 1 {
		t.Fatalf("memories = %v; want 1", resp["memories"])
	}
	m0, _ := mems[0].(map[string]any)
	if m0["content"] != "we discussed base cases" || m0["memoryType"] != "chat_turn" {
		t.Errorf("memory[0] wrong: %+v", m0)
	}
	// D5: no raw vector / distance / model internals may leak.
	for _, banned := range []string{"embedding", "distance", "modelId", "model_id", "vector"} {
		if _, present := m0[banned]; present {
			t.Errorf("memory leaked forbidden field %q: %+v", banned, m0)
		}
	}
	// resolved neighbour + citation.
	nbrs, _ := resp["visibleNeighbors"].([]any)
	if len(nbrs) != 1 {
		t.Fatalf("visibleNeighbors = %v; want 1", resp["visibleNeighbors"])
	}
	n0, _ := nbrs[0].(map[string]any)
	if n0["conceptTitle"] != "Recursion" {
		t.Errorf("neighbor title = %v", n0["conceptTitle"])
	}
	cits, _ := n0["citations"].([]any)
	if len(cits) != 1 {
		t.Fatalf("citations = %v; want 1", n0["citations"])
	}
	c0, _ := cits[0].(map[string]any)
	if c0["atomId"] != fmAtomID || c0["atomTitle"] != "What is a base case?" || c0["topicNodePath"] != "cs / recursion" {
		t.Errorf("citation wrong: %+v", c0)
	}

	// The reader was scoped by (tenant, learner, companion).
	rd := h.Memories.(*fmStubMemoryReader)
	if rd.got.TenantID != fmTenantID || rd.got.LearnerGCID != fmGCID || rd.got.CompanionID != fmCompanionID {
		t.Errorf("reader query not scoped: %+v", rd.got)
	}
}

func TestCompanionMemoryRead_EmptyMemories_IsHonestNoMemoryYet(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{},
		&fmStubAtoms{},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["hasMemory"] != false {
		t.Errorf("hasMemory = %v; want false", resp["hasMemory"])
	}
	mems, ok := resp["memories"].([]any)
	if !ok {
		t.Fatalf("memories not an array: %v", resp["memories"])
	}
	if len(mems) != 0 {
		t.Errorf("memories = %v; want empty", mems)
	}
}

func TestCompanionMemoryRead_UnknownAtomTitle_StaysBlankNeverFabricated(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: []*conceptgraph.ConceptNode{
			{ConceptID: fmConceptID, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Recursion", AtomRefs: []string{fmAtomID}},
		}},
		&fmStubAtoms{byID: map[string]*atomindex.AtomIndex{}}, // atom NOT in the index → unknown
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	nbrs, _ := resp["visibleNeighbors"].([]any)
	n0, _ := nbrs[0].(map[string]any)
	cits, _ := n0["citations"].([]any)
	c0, _ := cits[0].(map[string]any)
	if c0["atomId"] != fmAtomID {
		t.Errorf("atomId not carried through: %+v", c0)
	}
	if c0["atomTitle"] != "" {
		t.Errorf("atomTitle = %v; want blank (unknown, never fabricated)", c0["atomTitle"])
	}
}

func TestCompanionMemoryRead_FailSoftNeighbours_On_ConceptReadError(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{err: errors.New("boom")}, // read fails
		&fmStubAtoms{},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (neighbours are enrichment, not load-bearing)", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	nbrs, ok := resp["visibleNeighbors"].([]any)
	if !ok || len(nbrs) != 0 {
		t.Errorf("visibleNeighbors = %v; want empty on soft-failed read", resp["visibleNeighbors"])
	}
}

func TestCompanionMemoryRead_OwnerLeakGuard_Returns404(t *testing.T) {
	inst := fmOwnedInstance()
	inst.OwnerGCID = fmOtherGCID // owned by someone else
	h := NewCompanionMemoryReadHandler(&fmStubMemoryReader{}, &fmStubInstances{inst: inst}, &fmStubConcepts{}, &fmStubAtoms{})
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (never reveal another learner's Companion)", w.Code)
	}
}

func TestCompanionMemoryRead_NotFound_Returns404(t *testing.T) {
	h := NewCompanionMemoryReadHandler(&fmStubMemoryReader{}, &fmStubInstances{inst: nil}, &fmStubConcepts{}, &fmStubAtoms{})
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", w.Code)
	}
}

func TestCompanionMemoryRead_MissingContext_Returns400(t *testing.T) {
	h := NewCompanionMemoryReadHandler(&fmStubMemoryReader{}, &fmStubInstances{inst: fmOwnedInstance()}, &fmStubConcepts{}, &fmStubAtoms{})
	w := fmServe(h, http.MethodGet, "", "") // no headers
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
}

func TestCompanionMemoryRead_MethodNotAllowed_Returns405(t *testing.T) {
	h := NewCompanionMemoryReadHandler(&fmStubMemoryReader{}, &fmStubInstances{inst: fmOwnedInstance()}, &fmStubConcepts{}, &fmStubAtoms{})
	w := fmServe(h, http.MethodPost, fmTenantID, fmGCID)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestCompanionMemoryRead_Unwired_Returns503(t *testing.T) {
	// nil essential deps (no pool at boot) → 503, not a panic.
	h := NewCompanionMemoryReadHandler(nil, nil, nil, nil)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}

func TestCompanionMemoryRead_InstanceReadError_Returns500(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{},
		&fmStubInstances{err: errors.New("db down")}, // load-bearing read fails
		&fmStubConcepts{}, &fmStubAtoms{},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (fail-loud on instance read error)", w.Code)
	}
}

func TestCompanionMemoryRead_InstanceNotFoundSentinel_Returns404(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{},
		&fmStubInstances{err: companion.ErrInstanceNotFound},
		&fmStubConcepts{}, &fmStubAtoms{},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 on ErrInstanceNotFound", w.Code)
	}
}

func TestCompanionMemoryRead_MemoryReadError_Returns500(t *testing.T) {
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{err: errors.New("recall failed")}, // load-bearing
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{}, &fmStubAtoms{},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (fail-loud on memory read error)", w.Code)
	}
}

func TestCompanionMemoryRead_MissingPathID_Returns400(t *testing.T) {
	h := NewCompanionMemoryReadHandler(&fmStubMemoryReader{}, &fmStubInstances{inst: fmOwnedInstance()}, &fmStubConcepts{}, &fmStubAtoms{})
	// Empty {id} segment → the path parser yields "" → 400 before any read.
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions//memory", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 on empty path id", w.Code)
	}
}

func TestCompanionMemoryRead_NeighboursAndCitations_AreCappedAndSkipBlanks(t *testing.T) {
	// Build MORE than MaxNeighbors concept nodes (one nil, to exercise the skip);
	// the first real node carries MORE than MaxCitationsPerNeighbor atom refs
	// (one blank, to exercise the skip). The view must cap both for a
	// learner-sized panel.
	nodes := []*conceptgraph.ConceptNode{nil}
	overRefs := []string{""}
	for i := 0; i < companionmind.MaxCitationsPerNeighbor+4; i++ {
		overRefs = append(overRefs, fmAtomID)
	}
	nodes = append(nodes, &conceptgraph.ConceptNode{
		ConceptID: fmConceptID, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Recursion", AtomRefs: overRefs,
	})
	for i := 0; i < companionmind.MaxNeighbors+4; i++ {
		nodes = append(nodes, &conceptgraph.ConceptNode{
			ConceptID: fmConceptID, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "More", AtomRefs: nil,
		})
	}

	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: nodes},
		&fmStubAtoms{byID: map[string]*atomindex.AtomIndex{
			fmAtomID: {AtomID: fmAtomID, Title: "What is a base case?", TopicTags: []string{"cs"}},
		}},
	)
	w := fmServe(h, http.MethodGet, fmTenantID, fmGCID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	nbrs, _ := resp["visibleNeighbors"].([]any)
	if len(nbrs) != companionmind.MaxNeighbors {
		t.Errorf("neighbours = %d; want capped at %d", len(nbrs), companionmind.MaxNeighbors)
	}
	n0, _ := nbrs[0].(map[string]any)
	cits, _ := n0["citations"].([]any)
	if len(cits) != companionmind.MaxCitationsPerNeighbor {
		t.Errorf("citations = %d; want capped at %d (blank ref skipped)", len(cits), companionmind.MaxCitationsPerNeighbor)
	}
}

func TestCompanionMemoryRead_LimitQueryParam_IsPassedToReader(t *testing.T) {
	rd := &fmStubMemoryReader{}
	h := NewCompanionMemoryReadHandler(rd, &fmStubInstances{inst: fmOwnedInstance()}, &fmStubConcepts{}, &fmStubAtoms{})
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/"+fmCompanionID+"/memory?limit=7", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if rd.got.MemoryLimit != 7 {
		t.Errorf("reader MemoryLimit = %d; want 7 (from ?limit=)", rd.got.MemoryLimit)
	}
}
