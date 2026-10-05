package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

var errLEApplyBoom = errors.New("apply boom")

// leRoot is a fixed (non-existent-is-fine) root concept id — the mint places new
// learning-edge concepts UNDER it; the domain never checks it exists.
const leRoot = "01970000-0000-7000-a000-0000000000e1"

func lePtr(s string) *string { return &s }

// fakeLearningEdgeApplier captures the minted batch the handler hands it.
type fakeLearningEdgeApplier struct {
	got []conceptgraph.MintedLearningEdge
	err error
}

func (f *fakeLearningEdgeApplier) ApplyLearningEdges(_ context.Context, m []conceptgraph.MintedLearningEdge) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, m...)
	return nil
}

func learningEdgesPath(goalID string) string { return "/v1/me/goals/" + goalID + "/learning-edges" }

func TestLearningEdges_HappyPath_MintsAndPersistsUnderRoot(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	applier := &fakeLearningEdgeApplier{}
	srv := goalServer(repo)
	srv.LearningEdges = applier

	body := map[string]any{"edges": []map[string]any{
		{"title": "Adding fractions", "intent": "remediate"},
		{"title": "Continued fractions", "intent": "explore"},
	}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201. body=%s", w.Code, w.Body.String())
	}
	if len(applier.got) != 2 {
		t.Fatalf("applier got %d minted; want 2", len(applier.got))
	}
	for _, m := range applier.got {
		if m.Node == nil || m.Edge == nil {
			t.Fatalf("minted must carry node+edge: %+v", m)
		}
		if m.Edge.SourceConceptID != leRoot {
			t.Errorf("edge source = %q; want root %q", m.Edge.SourceConceptID, leRoot)
		}
		if m.Edge.Class != conceptgraph.EdgeClassHierarchy {
			t.Errorf("edge class = %q; want hierarchy", m.Edge.Class)
		}
	}
	var resp struct {
		LearningEdges []struct {
			ConceptID, Title, Intent, EdgeID string
		} `json:"learningEdges"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.LearningEdges) != 2 {
		t.Fatalf("resp learningEdges = %d; want 2", len(resp.LearningEdges))
	}
	if resp.LearningEdges[0].Intent != "remediate" || resp.LearningEdges[0].ConceptID == "" {
		t.Errorf("resp[0] = %+v; want remediate + a concept id", resp.LearningEdges[0])
	}
}

func TestLearningEdges_NoRoot_422(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity}) // no RootConceptID
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}

	body := map[string]any{"edges": []map[string]any{{"title": "X", "intent": "remediate"}}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (no root to place on)", w.Code)
	}
}

func TestLearningEdges_UnlabelledIntent_422(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}

	body := map[string]any{"edges": []map[string]any{{"title": "X", "intent": ""}}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (unlabelled intent)", w.Code)
	}
}

func TestLearningEdges_EmptyBatch_422(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}

	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), map[string]any{"edges": []any{}}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (empty batch)", w.Code)
	}
}

func TestLearningEdges_CrossLearner_404(t *testing.T) {
	repo := newGoalRepoFake()
	// A goal owned by a DIFFERENT learner; the fake GetByID does not filter, so the
	// handler's leak-guard must 404.
	g := repo.seed(t, goal.NewGoalInput{
		Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot),
		LearnerGCID: "01970000-0000-7000-9000-0000000000ff",
	})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}

	body := map[string]any{"edges": []map[string]any{{"title": "X", "intent": "remediate"}}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 (cross-learner leak-guard)", w.Code)
	}
}

func TestLearningEdges_Unwired_503(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo) // LearningEdges left nil

	body := map[string]any{"edges": []map[string]any{{"title": "X", "intent": "remediate"}}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503 (applier not wired)", w.Code)
	}
}

func TestLearningEdges_MethodNotAllowed(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{}

	w := doGoal(srv, goalReq("GET", learningEdgesPath(g.GoalID), nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", w.Code)
	}
}

func TestLearningEdges_ApplierError_500(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: lePtr(leRoot)})
	srv := goalServer(repo)
	srv.LearningEdges = &fakeLearningEdgeApplier{err: errLEApplyBoom}

	body := map[string]any{"edges": []map[string]any{{"title": "X", "intent": "remediate"}}}
	w := doGoal(srv, goalReq("POST", learningEdgesPath(g.GoalID), body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500 (applier failed, whole batch rolls back)", w.Code)
	}
}
