package http_test

// goals_summon_test.go — CHO-2013 P1 (R3-1): the goals-PATCH attach door
// gains the companion-side Summon effects. Attaching an EXISTING companion
// (egg-born, previously detached — Ember's path) must:
//
//   1. pre-validate the companion exists + is caller-owned (404 otherwise,
//      BEFORE the Goal mutates),
//   2. persist the bond (existing behaviour),
//   3. derive specialization from the map theme + clear the resonant pick +
//      emit specialization_changed.v1 (via the Summoner),
//   4. fail loud: Summoner unwired → 503; companion-side failure after the
//      bond persisted → 500 SUMMON_FAILED (PATCH retry heals: re-attach is
//      idempotent and the derive no-ops on same theme).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// fakeSummoner records calls; satisfies httpadapter.GoalSummoner.
type fakeSummoner struct {
	validateErr error
	boundErr    error
	validated   []string
	bound       []companion.SummonInput
}

func (f *fakeSummoner) ValidateOwned(_ context.Context, _, _, companionID string) (*companion.Instance, error) {
	if f.validateErr != nil {
		return nil, f.validateErr
	}
	f.validated = append(f.validated, companionID)
	return &companion.Instance{CompanionID: companionID}, nil
}

func (f *fakeSummoner) OnGoalBound(_ context.Context, in companion.SummonInput) (bool, error) {
	if f.boundErr != nil {
		return false, f.boundErr
	}
	f.bound = append(f.bound, in)
	return true, nil
}

func goalServerWithSummoner(repo goal.Repository, s httpadapter.GoalSummoner) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.Goals = repo
	ext.Summoner = s
	return ext
}

func TestPatchGoal_AttachRunsSummonEffects(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Astronomy Foundations"})
	sm := &fakeSummoner{}
	w := doGoal(goalServerWithSummoner(repo, sm), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(sm.validated) != 1 || sm.validated[0] != goCompanion {
		t.Fatalf("ValidateOwned calls = %v", sm.validated)
	}
	if len(sm.bound) != 1 {
		t.Fatalf("OnGoalBound calls = %d", len(sm.bound))
	}
	in := sm.bound[0]
	if in.CompanionID != goCompanion || in.TenantID == "" || in.OwnerGCID == "" {
		t.Fatalf("SummonInput identity: %+v", in)
	}
	if in.Theme != "Astronomy Foundations" {
		t.Fatalf("Theme = %q (want the map theme via resolveMapTheme)", in.Theme)
	}
	if repo.store[g.GoalID].AttachedCompanionID == nil || *repo.store[g.GoalID].AttachedCompanionID != goCompanion {
		t.Fatalf("bond not persisted")
	}
}

func TestPatchGoal_AttachUnknownCompanionRejectedBeforeBond(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	sm := &fakeSummoner{validateErr: companion.ErrInstanceNotFound}
	w := doGoal(goalServerWithSummoner(repo, sm), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", w.Code)
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["code"] != "COMPANION_NOT_FOUND" {
		t.Fatalf("code=%v", got["code"])
	}
	if repo.store[g.GoalID].AttachedCompanionID != nil {
		t.Fatalf("bond persisted despite invalid companion")
	}
}

func TestPatchGoal_AttachWithoutSummonerFailsLoud(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	ext := httpadapter.NewExtServer(nil)
	ext.Goals = repo // Summoner deliberately nil
	w := doGoal(ext, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 (fail-loud unwired summoner)", w.Code)
	}
	if repo.store[g.GoalID].AttachedCompanionID != nil {
		t.Fatalf("bond persisted despite unwired summoner")
	}
}

func TestPatchGoal_SummonFailureAfterBondIs500(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	sm := &fakeSummoner{boundErr: errors.New("outbox down")}
	w := doGoal(goalServerWithSummoner(repo, sm), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", w.Code)
	}
	var got map[string]any
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got["code"] != "SUMMON_FAILED" {
		t.Fatalf("code=%v", got["code"])
	}
	// The bond persisted (goal side committed first); the retryable PATCH
	// heals the companion side (idempotent re-attach + same-theme no-op).
	if repo.store[g.GoalID].AttachedCompanionID == nil {
		t.Fatalf("bond should have persisted before the summon failure")
	}
}

func TestPatchGoal_DetachDoesNotSummon(t *testing.T) {
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	_ = g.AttachCompanion(goCompanion, g.CreatedAt)
	repo.store[g.GoalID] = g
	sm := &fakeSummoner{}
	w := doGoal(goalServerWithSummoner(repo, sm), goalReq("PATCH", "/v1/me/goals/"+g.GoalID, map[string]any{"detachCompanion": true}))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(sm.validated) != 0 || len(sm.bound) != 0 {
		t.Fatalf("detach must not invoke the summoner: %v %v", sm.validated, sm.bound)
	}
}

// ── CHO-2403: one Companion, one knowledge graph ─────────────────────────
//
// The bond was 1:1 in ONE direction only. Goal.AttachCompanion guards the
// goal's own slot, so a goal cannot hold two Companions; nothing anywhere
// stopped one Companion being attached to any number of goals. The comments
// all said "1:1", which is exactly why nobody noticed: they mean goal to
// companion, not companion to goal.
//
// The uniqueness spans Goal INSTANCES, so it cannot live inside the Goal
// aggregate (an aggregate cannot see its siblings). It is enforced here at
// the application edge, and guaranteed underneath by the partial unique
// index in migration 0109.

func TestPatchGoal_AttachCompanionHeldByAnotherGoalIs409(t *testing.T) {
	repo := newGoalRepoFake()
	held := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Astronomy"})
	sm := &fakeSummoner{}
	srv := goalServerWithSummoner(repo, sm)

	// Bind the Companion to the FIRST goal.
	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+held.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusOK {
		t.Fatalf("first attach status=%d body=%s", w.Code, w.Body.String())
	}

	// The SAME Companion must not also bind to a second goal.
	other := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Chemistry"})
	w2 := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+other.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second attach status=%d want 409; body=%s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&got)
	if got["code"] != "COMPANION_ALREADY_ASSIGNED" {
		t.Fatalf("code=%v want COMPANION_ALREADY_ASSIGNED", got["code"])
	}
	if repo.store[other.GoalID].AttachedCompanionID != nil {
		t.Fatalf("bond persisted on the second goal despite the 409")
	}
	// The refusal must not disturb the goal that legitimately holds it.
	if repo.store[held.GoalID].AttachedCompanionID == nil ||
		*repo.store[held.GoalID].AttachedCompanionID != goCompanion {
		t.Fatalf("the holding goal lost its bond")
	}
}

func TestPatchGoal_ReattachSameCompanionToSameGoalStillIdempotent(t *testing.T) {
	// The guard must not turn the existing idempotent re-attach into a 409:
	// a double-tap or a PATCH retry has to keep healing, not start failing.
	repo := newGoalRepoFake()
	g := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Astronomy"})
	sm := &fakeSummoner{}
	srv := goalServerWithSummoner(repo, sm)

	body := map[string]any{"attachedCompanionId": goCompanion}
	if w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, body)); w.Code != http.StatusOK {
		t.Fatalf("first attach status=%d", w.Code)
	}
	w2 := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+g.GoalID, body))
	if w2.Code != http.StatusOK {
		t.Fatalf("re-attach status=%d want 200; body=%s", w2.Code, w2.Body.String())
	}
}

func TestPatchGoal_AttachCompanionFreedByDetachSucceeds(t *testing.T) {
	// Detaching must actually free the Companion. If the guard read stale or
	// soft-deleted rows, a learner could never move a Companion between maps.
	repo := newGoalRepoFake()
	first := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Astronomy"})
	sm := &fakeSummoner{}
	srv := goalServerWithSummoner(repo, sm)

	if w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+first.GoalID, map[string]any{"attachedCompanionId": goCompanion})); w.Code != http.StatusOK {
		t.Fatalf("attach status=%d", w.Code)
	}
	if w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+first.GoalID, map[string]any{"detachCompanion": true})); w.Code != http.StatusOK {
		t.Fatalf("detach status=%d", w.Code)
	}
	second := repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity, NorthStarNote: "Chemistry"})
	w := doGoal(srv, goalReq("PATCH", "/v1/me/goals/"+second.GoalID, map[string]any{"attachedCompanionId": goCompanion}))
	if w.Code != http.StatusOK {
		t.Fatalf("re-home after detach status=%d want 200; body=%s", w.Code, w.Body.String())
	}
}
