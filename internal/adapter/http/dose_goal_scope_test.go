// dose_goal_scope_test.go - WS-3 adapter lane: GET /companion/daily-dose?goal_id=
// resolves the browsed goal into the composer's GoalScopeInput so the goal's
// material leads the dose.
//
// Every case here drives a REAL http.Request with a REAL query string through
// the router and reads the REAL JSON response. A hand-filled GoalScopeInput
// would pass forever even if the handler never read the query param, so the
// param has to travel the wire for these to mean anything.
//
// TDD strict (RED first): before dose_goal_scope.go (adapter) lands the handler
// ignores ?goal_id= entirely, so the goal's atoms sit wherever the unscoped
// cascade puts them and TestDailyDose_GoalScope_GoalMaterialLeads fails.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// fakeKgExploreMap fakes the learner's live ConceptNode list. (fakeKgExploreEdges
// and esGoalReader are declared by the scout / edge-scout test files in this
// package and reused here.)
type doseScopeNodes struct {
	nodes []*conceptgraph.ConceptNode
	err   error
}

func (f *doseScopeNodes) ListByLearner(_ context.Context, _, _ string) ([]*conceptgraph.ConceptNode, error) {
	return f.nodes, f.err
}

const (
	doseScopeGoalID = "019e0000-0000-7000-d000-0000000000g1"
	doseScopeRootID = "019e0000-0000-7000-e000-000000000001"
	doseScopeKidID  = "019e0000-0000-7000-e000-000000000002"
)

// goalAtomIDs are the goal's material; otherAtomIDs are the learner's other
// enrolled atoms. Ordered so the goal atoms sort LAST by atom_id - without goal
// scoping the atom_id-ascending fills would never put them first, so a passing
// "leads" assertion cannot be an accident of ordering.
var (
	goalAtomIDs  = []string{"019e0000-0000-7000-f000-000000000008", "019e0000-0000-7000-f000-000000000009"}
	otherAtomIDs = []string{
		"019e0000-0000-7000-f000-000000000001",
		"019e0000-0000-7000-f000-000000000002",
		"019e0000-0000-7000-f000-000000000003",
		"019e0000-0000-7000-f000-000000000004",
	}
)

// seedGoalScopedUniverse enrols every atom on one LearningPath, projects each
// into atom_index (goal atoms on the "fractions" topic, the rest on
// "photosynthesis"), and wires a goal whose root concept binds the goal atoms.
func seedGoalScopedUniverse(t *testing.T, srv *Server) {
	t.Helper()
	all := append(append([]string{}, otherAtomIDs...), goalAtomIDs...)

	paths := repoinmem.NewLearningPathRepo()
	p, err := learning_path.New(testTenant, testGCID, "Mixed enrolment", all)
	if err != nil {
		t.Fatalf("learning_path.New: %v", err)
	}
	if err := paths.Save(context.Background(), p); err != nil {
		t.Fatalf("paths.Save: %v", err)
	}
	idx := repoinmem.NewAtomIndexRepo()
	for _, id := range otherAtomIDs {
		saveAtomProjection(t, idx, id, "Off-goal atom", "photosynthesis", atom_index.StatusPublished)
	}
	for _, id := range goalAtomIDs {
		saveAtomProjection(t, idx, id, "Goal atom", "fractions", atom_index.StatusPublished)
	}
	srv.LearningPaths = paths
	srv.AtomIndex = idx

	root := doseScopeRootID
	srv.Goals = &esGoalReader{goals: map[string]*goal.Goal{
		doseScopeGoalID: {
			GoalID:        doseScopeGoalID,
			TenantID:      testTenant,
			LearnerGCID:   testGCID,
			Kind:          goal.KindCuriosity,
			Status:        goal.StatusActive,
			ConceptSet:    []string{"fractions"},
			RootConceptID: &root,
		},
	}}
	srv.KgExploreMap = &doseScopeNodes{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: doseScopeRootID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Fractions", ConceptKey: "fractions"},
		{ConceptID: doseScopeKidID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Improper fractions",
			ConceptKey: "improper-fractions", AtomRefs: goalAtomIDs},
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{
		{EdgeID: "019e0000-0000-7000-e000-0000000000e1", TenantID: testTenant, LearnerGCID: testGCID,
			SourceConceptID: doseScopeRootID, TargetConceptID: doseScopeKidID, Class: conceptgraph.EdgeClassHierarchy},
	}}
}

func atomIDsOf(entries []struct{ AtomID, Reason string }) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.AtomID)
	}
	return out
}

func isGoalAtom(id string) bool {
	for _, g := range goalAtomIDs {
		if g == id {
			return true
		}
	}
	return false
}

// TestDailyDose_GoalScope_GoalMaterialLeads is the owner ask at the HTTP edge:
// ?goal_id={id} puts the goal's atoms at the front of the served dose.
func TestDailyDose_GoalScope_GoalMaterialLeads(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)

	// Positive control: WITHOUT the param the goal atoms do NOT lead (they sort
	// last by atom_id). Without this control a passing test below could just be
	// the default ordering.
	unscoped := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose"))
	if len(unscoped) == 0 {
		t.Fatalf("unscoped dose is empty - the fixture never composed anything")
	}
	if isGoalAtom(unscoped[0]) {
		t.Fatalf("unscoped dose[0] = %q is already a goal atom; the fixture cannot detect goal scoping", unscoped[0])
	}

	scoped := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose?goal_id="+doseScopeGoalID))
	if len(scoped) != 5 {
		t.Fatalf("scoped dose size = %d, want 5; dose = %v", len(scoped), scoped)
	}
	for i := 0; i < len(goalAtomIDs); i++ {
		if !isGoalAtom(scoped[i]) {
			t.Fatalf("scoped dose[%d] = %q, want a goal atom (goal material leads); dose = %v", i, scoped[i], scoped)
		}
	}
}

// TestDailyDose_NoGoalScope_Unchanged is the backward-compatibility proof at the
// edge: adding the goal-scope wiring must not move a single card on the plain
// call. The comparison is against a server with NO goal ports wired at all,
// which is the shape every pre-WS-3 deployment has.
func TestDailyDose_NoGoalScope_Unchanged(t *testing.T) {
	withPorts := NewServer()
	seedGoalScopedUniverse(t, withPorts)

	withoutPorts := NewServer()
	seedGoalScopedUniverse(t, withoutPorts)
	withoutPorts.Goals = nil
	withoutPorts.KgExploreMap = nil
	withoutPorts.KgExploreEdges = nil

	a := atomIDsOf(doseEntries(t, withPorts, "/companion/daily-dose"))
	b := atomIDsOf(doseEntries(t, withoutPorts, "/companion/daily-dose"))
	if len(a) != len(b) {
		t.Fatalf("unscoped dose size drifted: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("unscoped dose drifted at %d: %v vs %v (no goal_id must behave exactly as before)", i, a, b)
		}
	}
}

// TestDailyDose_GoalScope_UnknownGoal_StillFullDose - an unknown/foreign goal id
// is not an error and not a filter: the dose degrades to the learner-wide
// universe at full budget. A 404 here would break the "bias, never filter"
// contract and hand the learner an empty practice surface.
func TestDailyDose_GoalScope_UnknownGoal_StillFullDose(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)

	scoped := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose?goal_id=019e0000-0000-7000-d000-00000000dead"))
	if len(scoped) != 5 {
		t.Fatalf("unknown-goal dose size = %d, want 5 (degrade to learner-wide, never stunt); dose = %v",
			len(scoped), scoped)
	}
	unscoped := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose"))
	for i := range unscoped {
		if scoped[i] != unscoped[i] {
			t.Fatalf("unknown-goal dose = %v, want the unscoped %v", scoped, unscoped)
		}
	}
}

// TestDailyDose_GoalScope_ThinGoal_DoesNotStunt - a goal binding exactly ONE
// atom still yields a full dose: the goal atom leads and the learner-wide
// universe finishes the budget.
func TestDailyDose_GoalScope_ThinGoal_DoesNotStunt(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)
	// Rebind the goal's concept to a SINGLE atom and drop the topic slug, so the
	// goal's whole material is one card.
	srv.Goals.(*esGoalReader).goals[doseScopeGoalID].ConceptSet = nil
	srv.KgExploreMap.(*doseScopeNodes).nodes[1].AtomRefs = goalAtomIDs[:1]

	scoped := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose?goal_id="+doseScopeGoalID))
	if len(scoped) != 5 {
		t.Fatalf("thin-goal dose size = %d, want 5; dose = %v", len(scoped), scoped)
	}
	if scoped[0] != goalAtomIDs[0] {
		t.Fatalf("thin-goal dose[0] = %q, want the one goal atom %q; dose = %v", scoped[0], goalAtomIDs[0], scoped)
	}
}

// TestDailyDose_GoalScope_AcceptsCamelCaseParam - the surface accepts both
// ?goal_id= (the house convention, matching ?growth_edge_id=) and ?goalId=.
// A concurrent lane owns the caller, so the backend tolerating both spellings is
// what stops a naming mismatch shipping as a silently inert feature.
func TestDailyDose_GoalScope_AcceptsCamelCaseParam(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)

	snake := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose?goal_id="+doseScopeGoalID))
	camel := atomIDsOf(doseEntries(t, srv, "/companion/daily-dose?goalId="+doseScopeGoalID))
	if len(camel) != len(snake) {
		t.Fatalf("goalId dose size = %d, want the goal_id %d", len(camel), len(snake))
	}
	for i := range snake {
		if snake[i] != camel[i] {
			t.Fatalf("goalId dose = %v, want the goal_id dose %v (both spellings must scope)", camel, snake)
		}
	}
	if !isGoalAtom(camel[0]) {
		t.Fatalf("goalId dose[0] = %q, want a goal atom (the param was ignored)", camel[0])
	}
}

// TestDailyDose_GoalScope_ConceptReadFailure_FailsLoud - ADR-242 D3, owner
// re-ruled 2026-08-17 over the WS-3 fail-soft posture this test previously
// locked in: goal resolution is a POSITIVE scope claim attached to a visible
// promise, and with the D2 disclosure a failed goal read cannot render an
// honest scope line (it either fabricates one or omits it, reading as
// never-scoped). A concept-graph REPO ERROR therefore 500s, exactly like
// doseAtomUniverse's posture. Empty-but-readable goals stay quiet and disclose
// zeros; this is only about read failures.
func TestDailyDose_GoalScope_ConceptReadFailure_FailsLoud(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)
	srv.KgExploreMap = &doseScopeNodes{err: context.DeadlineExceeded}

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose?goal_id="+doseScopeGoalID, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status on concept-read failure = %d, want 500 (ADR-242 D3 fail-loud); body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DOSE_GOAL_SCOPE_FAILED") {
		t.Fatalf("error body must name the goal-scope failure: %s", w.Body.String())
	}
}

// doseScopeBlock unmarshals just the ADR-242 D2 scope block off the response.
type doseScopeBlock struct {
	Scope *struct {
		GoalID   string `json:"goal_id"`
		FromGoal int    `json:"from_goal"`
		OnTopics int    `json:"on_topics"`
		Broader  int    `json:"broader"`
	} `json:"scope"`
}

// TestDailyDose_GoalScope_DisclosesThreeWaySplit - ADR-242 D2 (owner-ruled
// three-way 2026-08-17): a goal-scoped dose states its own composition, and an
// unscoped dose carries no scope block at all (D5).
func TestDailyDose_GoalScope_DisclosesThreeWaySplit(t *testing.T) {
	srv := NewServer()
	seedGoalScopedUniverse(t, srv)

	w := authedReq(t, srv, http.MethodGet, "/companion/daily-dose?goal_id="+doseScopeGoalID, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got doseScopeBlock
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Scope == nil {
		t.Fatalf("goal-scoped dose must disclose its split (D2); body=%s", w.Body.String())
	}
	if got.Scope.GoalID != doseScopeGoalID {
		t.Errorf("scope.goal_id = %q, want %q", got.Scope.GoalID, doseScopeGoalID)
	}
	if got.Scope.FromGoal < len(goalAtomIDs) {
		t.Errorf("both bound goal atoms are served and must count from_goal: %+v", got.Scope)
	}
	if sum := got.Scope.FromGoal + got.Scope.OnTopics + got.Scope.Broader; sum != 5 {
		t.Errorf("split must sum to the served card count: %d != 5 (%+v)", sum, got.Scope)
	}

	unscoped := authedReq(t, srv, http.MethodGet, "/companion/daily-dose", nil)
	var plain doseScopeBlock
	if err := json.Unmarshal(unscoped.Body.Bytes(), &plain); err != nil {
		t.Fatalf("unmarshal unscoped: %v", err)
	}
	if plain.Scope != nil {
		t.Fatalf("unscoped dose must carry NO scope block (D5), got %+v", plain.Scope)
	}
}
