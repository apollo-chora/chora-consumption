// goals_handler_reroot_invalidation_test.go — CHO-2118 sub-phase B, the SEVENTH
// invalidation signal: a goal re-root (ADR-214).
//
// Goal CRUD emits no event, so this one cannot arrive on the six-topic inbox. A
// re-rooted goal points at a DIFFERENT concept subtree, which makes its cached
// reflection not merely stale but about the wrong thing — the strongest
// invalidation signal in the table. It must therefore be invalidated in-handler,
// at the only place that knows the root moved.
package http_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// gkrInvalidator records InvalidateGoal calls.
type gkrInvalidator struct {
	calls []gkrCall
	err   error
}

type gkrCall struct {
	tenantID, learnerGCID, goalID string
	reason                        fgk.InvalidationReason
}

func (i *gkrInvalidator) InvalidateGoal(_ context.Context, tenantID, learnerGCID, goalID string, reason fgk.InvalidationReason) (int, error) {
	i.calls = append(i.calls, gkrCall{tenantID, learnerGCID, goalID, reason})
	if i.err != nil {
		return 0, i.err
	}
	return 1, nil
}

func TestPatchGoal_ReRoot_InvalidatesCachedReflection(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: ptr("concept-old")})

	inv := &gkrInvalidator{}
	srv := goalServer(repo)
	srv.GoalKnowledge = inv

	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"rootConceptId": "concept-new"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.store[g.GoalID].RootConceptID == nil || *repo.store[g.GoalID].RootConceptID != "concept-new" {
		t.Fatal("re-root not persisted")
	}
	if len(inv.calls) != 1 {
		t.Fatalf("InvalidateGoal called %d times, want 1 — a re-rooted goal's reflection is about a different subtree", len(inv.calls))
	}
	got := inv.calls[0]
	if got.goalID != g.GoalID {
		t.Errorf("goalID = %q, want %q", got.goalID, g.GoalID)
	}
	if got.reason != fgk.ReasonGoalRerooted {
		t.Errorf("reason = %q, want goal_rerooted", got.reason)
	}
	if got.tenantID != goTenant || got.learnerGCID != goGCID {
		t.Errorf("identity = (%q,%q), want (%q,%q)", got.tenantID, got.learnerGCID, goTenant, goGCID)
	}
}

// Re-anchoring to the SAME root changes nothing about the subtree — the
// reflection is still true, so it must not be needlessly staled (that would burn
// a regen on every idempotent PATCH retry).
func TestPatchGoal_ReAnchorToSameRoot_DoesNotInvalidate(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: ptr("concept-same")})

	inv := &gkrInvalidator{}
	srv := goalServer(repo)
	srv.GoalKnowledge = inv

	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"rootConceptId": "concept-same"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(inv.calls) != 0 {
		t.Errorf("a no-op re-anchor invalidated the cache (%d calls)", len(inv.calls))
	}
}

// A PATCH that does not touch the root leaves the reflection alone.
func TestPatchGoal_NonRootPatch_DoesNotInvalidate(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("cert:pmp")})

	inv := &gkrInvalidator{}
	srv := goalServer(repo)
	srv.GoalKnowledge = inv

	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"status": "achieved"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(inv.calls) != 0 {
		t.Errorf("a status-only PATCH invalidated the cache (%d calls)", len(inv.calls))
	}
}

// The invalidation runs BEFORE the goal is persisted, and a failure fails the
// whole PATCH. Ordering is the point: if it ran after the write, a failure would
// 500 the learner, their retry would find the root already moved (a no-op
// re-anchor), and the invalidation would NEVER fire — a reflection about an
// abandoned subtree, served as current, forever. Invalidate-then-write means the
// retry re-runs both; over-invalidating merely costs one regen.
func TestPatchGoal_ReRoot_InvalidationFailureFailsTheWriteLoud(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: ptr("concept-old")})

	inv := &gkrInvalidator{err: errors.New("pg down")}
	srv := goalServer(repo)
	srv.GoalKnowledge = inv

	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"rootConceptId": "concept-new"}))
	if w.Code == http.StatusOK {
		t.Fatal("a failed invalidation must fail the PATCH, not silently leave a wrong reflection")
	}
	// The re-root must NOT have persisted — otherwise the retry sees a no-op
	// re-anchor and the invalidation is lost for good.
	if stored := repo.store[g.GoalID].RootConceptID; stored != nil && *stored == "concept-new" {
		t.Error("goal persisted despite the invalidation failure — the retry can no longer detect the re-root")
	}
}

// Unwired invalidator (nil) must not panic — the path is simply dark.
func TestPatchGoal_ReRoot_NilInvalidatorIsDarkNotFatal(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, RootConceptID: ptr("concept-old")})

	srv := goalServer(repo) // GoalKnowledge deliberately nil
	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"rootConceptId": "concept-new"}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
