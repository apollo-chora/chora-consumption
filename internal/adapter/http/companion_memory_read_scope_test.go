// companion_memory_read_scope_test.go — ADR-214 goal scoping for the Companion
// memory panel's "What it can see" block.
//
// The panel renders INSIDE one goal map, but visibleNeighbors read the learner's
// whole flat ConceptNode space (ListByLearner, no subtree walk) and truncated at
// MaxNeighbors, so a Software Design map listed "Water Cycle" and "Food Chains"
// from other maps. That is section 2's retired "one map for the learner" premise
// outliving ADR-214, which made a goal a map. This is the only learner-facing
// read in the service that skipped the walk every other goal-scoped reader does.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	fmGoalID      = "01970000-0000-7000-d000-0000000000a1"
	fmRootConcept = "01970000-0000-7000-d000-0000000000a2"
	fmKidConcept  = "01970000-0000-7000-d000-0000000000a3"
	fmOffMapConc  = "01970000-0000-7000-d000-0000000000a4"
)

type fmStubGoals struct {
	g   *goal.Goal
	err error
}

func (s *fmStubGoals) GetByID(context.Context, string, string, string) (*goal.Goal, error) {
	return s.g, s.err
}

type fmStubEdges struct {
	out []*conceptgraph.Edge
	err error
}

func (s *fmStubEdges) ListByLearner(context.Context, string, string) ([]*conceptgraph.Edge, error) {
	return s.out, s.err
}

// fmScopeFixture: goal rooted at fmRootConcept with one child; fmOffMapConc
// belongs to a DIFFERENT map and must never appear on this one.
func fmScopeFixture() ([]*conceptgraph.ConceptNode, []*conceptgraph.Edge, *goal.Goal) {
	root := fmRootConcept
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: fmRootConcept, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Software Design"},
		{ConceptID: fmKidConcept, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Dependency Injection"},
		{ConceptID: fmOffMapConc, TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Water Cycle"},
	}
	edges := []*conceptgraph.Edge{
		{SourceConceptID: fmRootConcept, TargetConceptID: fmKidConcept, Class: "hierarchy"},
	}
	g := &goal.Goal{GoalID: fmGoalID, TenantID: fmTenantID, LearnerGCID: fmGCID, RootConceptID: &root}
	return nodes, edges, g
}

func fmServeScoped(h *CompanionMemoryReadHandler, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/"+fmCompanionID+"/memory"+query, nil)
	r.Header.Set("X-Tenant-Id", fmTenantID)
	r.Header.Set("gcid", fmGCID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func fmNeighbourTitles(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var resp struct {
		VisibleNeighbors []struct {
			ConceptTitle string `json:"conceptTitle"`
		} `json:"visibleNeighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make([]string, 0, len(resp.VisibleNeighbors))
	for _, n := range resp.VisibleNeighbors {
		out = append(out, n.ConceptTitle)
	}
	return out
}

func TestCompanionMemoryRead_GoalScope_ExcludesOtherMaps(t *testing.T) {
	nodes, edges, g := fmScopeFixture()
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: nodes},
		&fmStubAtoms{},
	)
	h.Goals = &fmStubGoals{g: g}
	h.ConceptEdges = &fmStubEdges{out: edges}

	w := fmServeScoped(h, "?goal_id="+fmGoalID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	titles := fmNeighbourTitles(t, w)
	for _, got := range titles {
		if got == "Water Cycle" {
			t.Fatalf("a concept from ANOTHER map leaked onto this goal: %v", titles)
		}
	}
	if len(titles) != 2 {
		t.Fatalf("titles = %v; want exactly the goal subtree (root + child)", titles)
	}
}

func TestCompanionMemoryRead_GoalScope_FailsClosedWhenPortsMissing(t *testing.T) {
	nodes, _, _ := fmScopeFixture()
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: nodes},
		&fmStubAtoms{},
	)
	// Goals/ConceptEdges deliberately nil: a goal was ASKED for and cannot be
	// honoured. Serving the unscoped set would silently leak every other map.
	w := fmServeScoped(h, "?goal_id="+fmGoalID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503 fail-closed (never a silent unscoped fallback)", w.Code)
	}
}

func TestCompanionMemoryRead_NoGoalParam_KeepsWholeGraph(t *testing.T) {
	nodes, edges, g := fmScopeFixture()
	h := NewCompanionMemoryReadHandler(
		&fmStubMemoryReader{out: nil},
		&fmStubInstances{inst: fmOwnedInstance()},
		&fmStubConcepts{out: nodes},
		&fmStubAtoms{},
	)
	h.Goals = &fmStubGoals{g: g}
	h.ConceptEdges = &fmStubEdges{out: edges}

	w := fmServeScoped(h, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if got := len(fmNeighbourTitles(t, w)); got != 3 {
		t.Fatalf("neighbours = %d; want all 3 when no goal is supplied", got)
	}
}
