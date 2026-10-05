// companion_acquire_handler_test.go — WS-A3 (My Knowledge unification, CHO-2005):
// the Companion acquisition handler repointed onto the Goal (ADR-214 D1 / owner
// full-consolidation sign-off 2026-07-02). Acquire now mints a persisted Instance
// for a MAP (goalId) and attaches it to that map's Goal (Goal.AttachCompanion) —
// the Goal is the single source of truth; the theme-keyed CompanionMapBinding is
// retired.
//
// Covers: the happy dev-hatch path (mints Instance + attaches to the Goal), the
// already-attached 409 pre-check (no orphan), missing-goalId 422, invalid-mode
// 422, goal-not-found 404, cross-learner 404, roster-cap 409 (no attach), the
// Goal-update failure that COMPENSATES the orphan Instance then 500, the
// northStarNote specialization fallback, unwired 503, method 405, missing-context
// 400.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// ---- Instance stub (captures Create + SoftDelete for the compensation test) ----

type acqStubInstances struct {
	createErr   error
	created     *companion.Instance
	softDeleted []string
}

func (s *acqStubInstances) Create(_ context.Context, inst *companion.Instance, _ int) error {
	s.created = inst
	return s.createErr
}
func (s *acqStubInstances) Get(context.Context, string) (*companion.Instance, error) { return nil, nil }
func (s *acqStubInstances) ListByOwner(context.Context, string, string) ([]*companion.Instance, error) {
	return nil, nil
}
func (s *acqStubInstances) ListRosterByOwner(context.Context, string, string) ([]*companion.RosterEntry, error) {
	return nil, nil
}
func (s *acqStubInstances) Update(context.Context, *companion.Instance) error { return nil }
func (s *acqStubInstances) SoftDelete(_ context.Context, companionID string) error {
	s.softDeleted = append(s.softDeleted, companionID)
	return nil
}

// acqConcepts is a ConceptNodeRepository double with a working GetByID (the
// shared fmStubConcepts only implements ListByLearner) — the acquire handler
// resolves the map's theme via GetByID(RootConceptID).
type acqConcepts struct {
	byID map[string]*conceptgraph.ConceptNode
}

func (s *acqConcepts) Create(context.Context, *conceptgraph.ConceptNode) error { return nil }
func (s *acqConcepts) GetByID(_ context.Context, _, _, id string) (*conceptgraph.ConceptNode, error) {
	return s.byID[id], nil
}
func (s *acqConcepts) ListByLearner(context.Context, string, string) ([]*conceptgraph.ConceptNode, error) {
	return nil, nil
}
func (s *acqConcepts) Update(context.Context, *conceptgraph.ConceptNode) error { return nil }

// acqRootConcept is the map's root concept (its Title becomes the Instance
// specialization / theme).
func acqRootConcept(title string) *acqConcepts {
	return &acqConcepts{byID: map[string]*conceptgraph.ConceptNode{
		"c-root": {ConceptID: "c-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: title},
	}}
}

// acqServer builds an acquire-ready ExtServer for goal "g-1".
func acqServer(withCompanion bool, root *string, concepts *acqConcepts) (*ExtServer, *acqStubInstances, *mapsGoalStub) {
	g := mapsGoal("g-1", root)
	if withCompanion {
		fid := "01970000-0000-7000-a000-0000000000d1"
		g.AttachedCompanionID = &fid
	}
	insts := &acqStubInstances{}
	goals := &mapsGoalStub{byID: map[string]*goal.Goal{"g-1": g}}
	// CHO-2013 P1: acquire requires the born-hatch growth port (fail-loud
	// 503 when nil); species-specific behaviour lives in
	// companion_acquire_species_test.go.
	s := &ExtServer{Goals: goals, CompanionInstances: insts, Concepts: concepts, GrowthInit: &fakeBornHatcher{}}
	return s, insts, goals
}

func acqServe(s *ExtServer, body, tenantID, gcid string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/me/companions/acquire", strings.NewReader(body))
	if tenantID != "" {
		r.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	w := httptest.NewRecorder()
	s.handleMeCompanionAcquire(w, r)
	return w
}

// ---- tests ------------------------------------------------------------------

func TestAcquire_HappyDevHatch_MintsInstanceAndAttachesToGoal(t *testing.T) {
	s, insts, goals := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	w := acqServe(s, `{"goalId":"g-1","companionName":"Sprint","mode":"dev_hatched"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (body=%s)", w.Code, w.Body.String())
	}
	if insts.created == nil {
		t.Fatal("expected an Instance to be persisted")
	}
	if insts.created.OwnerGCID != fmGCID || insts.created.TenantID != fmTenantID {
		t.Errorf("instance scope wrong: %+v", insts.created)
	}
	if insts.created.Specialization != "Scrum" {
		t.Errorf("specialization = %q; want Scrum (root concept title)", insts.created.Specialization)
	}
	if insts.created.Name != "Sprint" {
		t.Errorf("name = %q; want Sprint", insts.created.Name)
	}
	// The Goal is the source of truth: it must be attached + persisted.
	if goals.updated == nil || goals.updated.AttachedCompanionID == nil || *goals.updated.AttachedCompanionID != insts.created.CompanionID {
		t.Fatalf("goal not attached to the minted companion: %+v", goals.updated)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["goalId"] != "g-1" || resp["acquisition"] != "dev_hatched" {
		t.Errorf("resp wrong: %+v", resp)
	}
	if resp["companionId"] == "" || resp["companionId"] == nil {
		t.Errorf("resp missing companionId: %+v", resp)
	}
}

func TestAcquire_AlreadyAttached_Returns409_NoOrphan(t *testing.T) {
	s, insts, goals := acqServer(true, mapsPtr("c-root"), acqRootConcept("Scrum"))
	w := acqServe(s, `{"goalId":"g-1"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (map already has a Companion)", w.Code)
	}
	if insts.created != nil || goals.updated != nil {
		t.Error("nothing should be persisted when the map already has a Companion")
	}
}

func TestAcquire_MissingGoalID_Returns422(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	if w := acqServe(s, `{"goalId":"   "}`, fmTenantID, fmGCID); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (missing goalId)", w.Code)
	}
}

func TestAcquire_InvalidMode_Returns422(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	if w := acqServe(s, `{"goalId":"g-1","mode":"free_lunch"}`, fmTenantID, fmGCID); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (invalid mode)", w.Code)
	}
}

func TestAcquire_GoalNotFound_Returns404(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	if w := acqServe(s, `{"goalId":"nope"}`, fmTenantID, fmGCID); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (goal not found)", w.Code)
	}
}

func TestAcquire_CrossLearner_Returns404(t *testing.T) {
	g := mapsGoal("g-x", mapsPtr("c-root"))
	g.LearnerGCID = "01970000-0000-7000-9000-0000000000zz"
	insts := &acqStubInstances{}
	s := &ExtServer{
		Goals:              &mapsGoalStub{byID: map[string]*goal.Goal{"g-x": g}},
		CompanionInstances: insts,
		Concepts:           acqRootConcept("Scrum"),
		GrowthInit:         &fakeBornHatcher{},
	}
	if w := acqServe(s, `{"goalId":"g-x"}`, fmTenantID, fmGCID); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (cross-learner leak guard)", w.Code)
	}
	if insts.created != nil {
		t.Error("no Instance should be minted for another learner's goal")
	}
}

func TestAcquire_RosterCapReached_Returns409_NoAttach(t *testing.T) {
	s, insts, goals := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	insts.createErr = companion.ErrRosterCapReached
	if w := acqServe(s, `{"goalId":"g-1"}`, fmTenantID, fmGCID); w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409 (roster cap)", w.Code)
	}
	if goals.updated != nil {
		t.Error("goal must not be attached when the Instance create fails")
	}
}

func TestAcquire_GoalUpdateFails_CompensatesOrphanThen500(t *testing.T) {
	s, insts, goals := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	goals.updateErr = errors.New("update boom")
	w := acqServe(s, `{"goalId":"g-1"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (goal update failed)", w.Code)
	}
	if len(insts.softDeleted) != 1 || insts.created == nil || insts.softDeleted[0] != insts.created.CompanionID {
		t.Errorf("orphan Instance not compensated: softDeleted=%v created=%v", insts.softDeleted, insts.created)
	}
}

func TestAcquire_SpecializationFallsBackToNorthStar(t *testing.T) {
	// No root concept resolvable → specialization falls back to the goal's note.
	s, insts, _ := acqServer(false, nil, &acqConcepts{})
	w := acqServe(s, `{"goalId":"g-1"}`, fmTenantID, fmGCID)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201 (body=%s)", w.Code, w.Body.String())
	}
	if insts.created.Specialization != "Own algebra" {
		t.Errorf("specialization = %q; want the northStarNote fallback", insts.created.Specialization)
	}
}

func TestAcquire_Unwired_Returns503(t *testing.T) {
	if w := acqServe(&ExtServer{}, `{"goalId":"g-1"}`, fmTenantID, fmGCID); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", w.Code)
	}
}

func TestAcquire_MethodNotAllowed_Returns405(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/acquire", nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	s.handleMeCompanionAcquire(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestAcquire_MissingContext_Returns400(t *testing.T) {
	s, _, _ := acqServer(false, mapsPtr("c-root"), acqRootConcept("Scrum"))
	if w := acqServe(s, `{"goalId":"g-1"}`, "", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (missing context)", w.Code)
	}
}
