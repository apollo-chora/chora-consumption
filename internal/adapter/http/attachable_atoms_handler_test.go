// attachable_atoms_handler_test.go - the ENROLMENT-entitled half of the concept
// attach picker.
//
// Why this endpoint exists (measured 2026-07-20): the picker previously offered
// only reuse-entitled atoms, and `E = mine UNION tenant-visible UNION granted`
// matched 2 of 367 live atoms for a plain learner, so 46 of 48 concept nodes
// carried no atom_refs and a goal-scoped dose had nothing to serve. Attaching an
// atom to your OWN private map is not ADR-229 reuse; the right question is
// whether the learner already has access, which enrolment answers.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	aaAtomMCQ    = "01980000-0000-7000-9000-00000000aa01"
	aaAtomEssay  = "01980000-0000-7000-9000-00000000aa02"
	aaAtomDraft  = "01980000-0000-7000-9000-00000000aa03"
	aaAtomKeyles = "01980000-0000-7000-9000-00000000aa04"
	aaAtomGhost  = "01980000-0000-7000-9000-00000000aa05"
)

// ---- stubs ----

// The embedded interface satisfies the wide port without hand-writing every
// method, AND makes any unexpected call panic loudly rather than silently
// returning a zero value: the handler must touch only ListByLearner.
type aaStubPaths struct {
	learning_path.Repo
	paths []*learning_path.LearningPath
	err   error
}

func (s *aaStubPaths) ListByLearner(_ context.Context, _, _ string, _ int) ([]*learning_path.LearningPath, error) {
	return s.paths, s.err
}

type aaStubIndex struct {
	atom_index.Repo
	byID map[string]*atom_index.AtomIndex
	err  error
	// errByID fails ONE atom's read while its neighbours resolve normally. A
	// blanket err cannot reproduce the case D5 actually cares about, where a
	// PARTIAL list is available to return and the temptation is to return it.
	errByID map[string]error
}

func (s *aaStubIndex) Get(_ context.Context, atomID string) (*atom_index.AtomIndex, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err, ok := s.errByID[atomID]; ok {
		return nil, err
	}
	a, ok := s.byID[atomID]
	if !ok {
		return nil, atom_index.ErrNotFound
	}
	return a, nil
}

func aaIndexFixture() *aaStubIndex {
	return &aaStubIndex{byID: map[string]*atom_index.AtomIndex{
		aaAtomMCQ: {AtomID: aaAtomMCQ, TenantID: fmTenantID, Title: "Adding fractions",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished,
			TopicTags: []string{"fractions"}},
		aaAtomEssay: {AtomID: aaAtomEssay, TenantID: fmTenantID, Title: "Explain equivalence",
			AtomType: "essay", Status: atom_index.StatusPublished, TopicTags: []string{"fractions"}},
		// Draft: not Playable.
		aaAtomDraft: {AtomID: aaAtomDraft, TenantID: fmTenantID, Title: "Half-written",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: "draft"},
		// Published MCQ with NO answer key: not IsAnswerable, an un-gradable dead end.
		aaAtomKeyles: {AtomID: aaAtomKeyles, TenantID: fmTenantID, Title: "Keyless",
			AtomType: "mcq", Status: atom_index.StatusPublished},
	}}
}

func aaPathsFixture() *aaStubPaths {
	return &aaStubPaths{paths: []*learning_path.LearningPath{
		{AtomIDs: []string{aaAtomMCQ, aaAtomDraft, aaAtomGhost}},
		// Second path repeats one atom: dedup across paths must hold.
		{AtomIDs: []string{aaAtomEssay, aaAtomMCQ, aaAtomKeyles}},
	}}
}

func aaDecode(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return resp.Items
}

// ---- tests ----

func TestAttachableAtoms_ServesOnlyServableEnrolledAtoms(t *testing.T) {
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (%s)", w.Code, w.Body.String())
	}
	items := aaDecode(t, w)
	got := map[string]bool{}
	for _, it := range items {
		got[it["atomId"].(string)] = true
	}
	// Kept: a gradable MCQ and an open-ended atom.
	if !got[aaAtomMCQ] || !got[aaAtomEssay] {
		t.Errorf("servable atoms missing: %+v", got)
	}
	// Dropped: draft (not Playable), keyless MCQ (not IsAnswerable), and an
	// enrolled-but-unprojected atom (honest omission, never an error).
	if got[aaAtomDraft] || got[aaAtomKeyles] || got[aaAtomGhost] {
		t.Errorf("unservable atom offered for attachment: %+v", got)
	}
	if len(items) != 2 {
		t.Errorf("got %d items; want 2 (deduped across paths)", len(items))
	}
}

func TestAttachableAtoms_LabelsItsEntitlementSource(t *testing.T) {
	// The picker mixes TWO entitlement models (enrolment here, tenant reuse via
	// chora-creation). Unlabelled mixing is exactly what later reads as a bug,
	// so every item states WHY it is available.
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	for _, it := range aaDecode(t, w) {
		if it["source"] != "enrolled" {
			t.Fatalf("item not labelled with its entitlement source: %+v", it)
		}
	}
}

func TestAttachableAtoms_NoEnrolmentsIsEmptyNotSynthetic(t *testing.T) {
	// Degeneration: with no paths the list is EMPTY. It must never fall back to
	// a synthetic catalogue, which would offer atoms the learner cannot study.
	s := &ExtServer{Paths: &aaStubPaths{}, AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	if items := aaDecode(t, w); len(items) != 0 {
		t.Fatalf("got %d items with no enrolments; want an empty list, never synthetic", len(items))
	}
}

func TestAttachableAtoms_RepoErrorFailsLoud(t *testing.T) {
	// Inherited from doseAtomUniverse: a repo error 500s. Silently serving a
	// short list on error masks the failure and looks like "you have no atoms".
	s := &ExtServer{Paths: &aaStubPaths{err: context.DeadlineExceeded}, AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 on a repo error (fail-loud)", w.Code)
	}
}

// ADR-243 D5: "A partial Source A must never be presented as a complete one."
// The paths-listing error above cannot exercise that sentence, because it fails
// before a single atom resolves and there is no partial list to be tempted by.
// This one fails the SECOND atom's projection read while the first has already
// resolved: the short list is sitting right there, and returning it would look
// to the learner exactly like "that atom is not attachable" rather than like the
// outage it is.
func TestAttachableAtoms_PartialResolveFailsLoudRatherThanServingAShortList(t *testing.T) {
	idx := aaIndexFixture()
	idx.errByID = map[string]error{aaAtomEssay: context.DeadlineExceeded}
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: idx}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500; a mid-resolve read error must surface, not truncate the list", w.Code)
	}
	// And the truncated list must not have leaked into the body alongside the
	// error: a 500 carrying items would invite a client to render them.
	if items := aaDecode(t, w); len(items) != 0 {
		t.Errorf("got %d items on a failed read; a partial list must never be served", len(items))
	}
}

// attachmentSourceVocabulary mirrors the closed set the A+ picker can render
// (`QuestionSearchResult['source']` in atom-question-picker.model.ts). ADR-243 D2
// requires the label map to be TOTAL over the contract: a row carrying a reason
// this set does not contain renders as a silently blank chip, which is precisely
// the failure the labels exist to prevent, and it is invisible from the server.
var attachmentSourceVocabulary = map[string]bool{
	"mine": true, "saved": true, "granted": true, "tenant": true, "enrolled": true,
}

// ADR-243's own delivery checklist, item 1: "Count the labelled rows, not the
// returned rows." A list that renders is not evidence the labels are total, so
// this asserts over every row that actually came off the wire (decoded JSON, not
// the DTO struct) and fails on a missing or unknown reason rather than on an
// empty response.
func TestAttachableAtoms_EveryServedRowCarriesAKnownReason(t *testing.T) {
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))

	items := aaDecode(t, w)
	if len(items) == 0 {
		// The positive control. Without it "every row is labelled" is also true
		// of a broken query that returned nothing at all.
		t.Fatalf("no rows served; the labelling assertion below would be vacuous")
	}
	labelled := 0
	for _, it := range items {
		src, ok := it["source"].(string)
		if !ok || src == "" {
			t.Errorf("row %v carries no entitlement reason", it["atomId"])
			continue
		}
		if !attachmentSourceVocabulary[src] {
			t.Errorf("row %v carries reason %q, which the picker cannot render", it["atomId"], src)
			continue
		}
		labelled++
	}
	if labelled != len(items) {
		t.Errorf("labelled %d of %d served rows; ADR-243 D2 requires all of them", labelled, len(items))
	}
}

func TestAttachableAtoms_Unwired503(t *testing.T) {
	s := &ExtServer{}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 when the ports are not wired", w.Code)
	}
}

func TestAttachableAtoms_MethodNotAllowed(t *testing.T) {
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.handleMeAttachableAtoms(w, caReq(http.MethodPost, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", w.Code)
	}
}

func TestAttachableAtoms_RoutedUnderConceptGraph(t *testing.T) {
	// Registering UNDER /v1/me/concept-graph means the gateway subtree proxy and
	// the Istio authz wildcard already admit it, so this needs no new edge
	// wiring. A route registered anywhere else would 404 at the gateway.
	s := &ExtServer{Paths: aaPathsFixture(), AtomIndex: aaIndexFixture()}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, caReq(http.MethodGet, "/v1/me/concept-graph/attachable-atoms", "", fmTenantID, fmGCID))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 through the real mux (%s)", w.Code, w.Body.String())
	}
}
