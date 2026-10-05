// goals_lead_test.go: RED first for the LEAD COUNTERS on GET /v1/me/goals
// (UX Track U package B5; master plan section 4.1, ruling R4).
//
// The A+ home is one ranked list served by the existing /api/me/dashboard
// aggregator, so the lead has to arrive as raw signals beside `primaryLens`,
// not as a mode. These tests pin the four wire fields and the credential flip
// that the lens constant used to make impossible.
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// leadPathRepo is the real in-memory path repo with one seam: ListByLearner can
// be made to fail, so the degraded-course-axis case is exercised through the
// SAME port the handler calls rather than a hand-rolled stub of all seven
// methods.
type leadPathRepo struct {
	learning_path.Repo
	listErr error
}

func newLeadPathRepo() *leadPathRepo {
	return &leadPathRepo{Repo: repoinmem.NewLearningPathRepo()}
}

func (r *leadPathRepo) ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*learning_path.LearningPath, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.Repo.ListByLearner(ctx, tenantID, gcid, limit)
}

// leadResp is the wire shape under test: the existing envelope plus the four
// counters. The timestamps are pointers so an ABSENT axis is distinguishable
// from a zero time (a zero time would rank a new learner as the stalest one).
type leadResp struct {
	Items                  []map[string]any `json:"items"`
	PrimaryLens            string           `json:"primaryLens"`
	ActiveGoals            int              `json:"activeGoals"`
	ActiveCourseBoundPaths int              `json:"activeCourseBoundPaths"`
	LastCuriosityAt        *string          `json:"lastCuriosityAt"`
	LastCourseAt           *string          `json:"lastCourseAt"`
	// LeadPartial marks the course axis as UNREAD this request. Without it an
	// empty axis is ambiguous: the ranker cannot tell "no course-bound paths"
	// from "we could not look", and would happily rank a credential learner as
	// a pure explorer on a transient blip.
	LeadPartial bool `json:"leadPartial"`
}

func getLead(t *testing.T, repo goal.Repository, paths learning_path.Repo) leadResp {
	t.Helper()
	srv := goalServer(repo)
	if paths != nil {
		srv.Paths = paths
	}
	w := doGoal(srv, goalReq("GET", "/v1/me/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp leadResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func coursePathRepo(t *testing.T, courseIDs ...string) learning_path.Repo {
	t.Helper()
	repo := newLeadPathRepo()
	for i, cid := range courseIDs {
		p, err := learning_path.New(goTenant, goGCID, "Path "+cid, []string{"atom-" + cid})
		if err != nil {
			t.Fatalf("learning_path.New: %v", err)
		}
		p.CourseID = cid
		if cid != "" {
			p.SourceType = learning_path.SourceTypeCourse
			p.SourceID = cid
		}
		p.UpdatedAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		if err := repo.Save(context.Background(), p); err != nil {
			t.Fatalf("paths.Save: %v", err)
		}
	}
	return repo
}

// A learner with goals but no course-bound path stays curiosity-led and the
// course axis reports nothing rather than a zero time.
func TestListGoals_LeadCountersCuriosityLed(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindThemeMastery, ChoraTargetRef: ptr("theme:bio")})

	got := getLead(t, repo, coursePathRepo(t, ""))
	if got.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity", got.PrimaryLens)
	}
	if got.ActiveGoals != 2 {
		t.Errorf("activeGoals=%d want 2", got.ActiveGoals)
	}
	if got.ActiveCourseBoundPaths != 0 {
		t.Errorf("activeCourseBoundPaths=%d want 0 (the un-bound path does not count)", got.ActiveCourseBoundPaths)
	}
	if got.LastCuriosityAt == nil {
		t.Error("lastCuriosityAt absent; want the latest goal touch")
	}
	if got.LastCourseAt != nil {
		t.Errorf("lastCourseAt=%v want absent (no course-bound path)", *got.LastCourseAt)
	}
	if got.LeadPartial {
		t.Error("leadPartial=true on a clean read; a genuinely empty axis is not a gap")
	}
}

// The defect this package closes: a learner enrolled on a course used to read
// `curiosity` forever because lens.go returned a constant.
func TestListGoals_LeadCountersCredentialLed(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})

	got := getLead(t, repo, coursePathRepo(t, "course-a", "", "course-b"))
	if got.PrimaryLens != "credential" {
		t.Errorf("primaryLens=%q want credential (a live course-bound path leads)", got.PrimaryLens)
	}
	if got.ActiveCourseBoundPaths != 2 {
		t.Errorf("activeCourseBoundPaths=%d want 2", got.ActiveCourseBoundPaths)
	}
	if got.LastCourseAt == nil {
		t.Fatal("lastCourseAt absent; want the latest course-bound touch")
	}
	if _, err := time.Parse(time.RFC3339, *got.LastCourseAt); err != nil {
		t.Errorf("lastCourseAt=%q is not RFC3339: %v", *got.LastCourseAt, err)
	}
}

// A learner with nothing at all: every counter zero, both axes absent, and the
// lens still the safe default the FE falls back to.
func TestListGoals_LeadCountersEmptyLearner(t *testing.T) {
	got := getLead(t, newGoalRepoFake(), nil)
	if got.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity", got.PrimaryLens)
	}
	if got.ActiveGoals != 0 || got.ActiveCourseBoundPaths != 0 {
		t.Errorf("counters=%d/%d want 0/0", got.ActiveGoals, got.ActiveCourseBoundPaths)
	}
	if got.LastCuriosityAt != nil || got.LastCourseAt != nil {
		t.Errorf("timestamps=%v/%v want absent/absent", got.LastCuriosityAt, got.LastCourseAt)
	}
}

// A failed path read must NOT 500 the goal list: the lead is additive dashboard
// data and a degraded course axis is reported as ABSENT, never as zero counts
// dressed up as fact. The goals themselves still render.
func TestListGoals_PathReadFailureDegradesTheCourseAxisOnly(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})

	failing := newLeadPathRepo()
	failing.listErr = context.DeadlineExceeded

	got := getLead(t, repo, failing)
	if got.ActiveGoals != 1 {
		t.Errorf("activeGoals=%d want 1 (the goal axis is unaffected)", got.ActiveGoals)
	}
	if got.ActiveCourseBoundPaths != 0 {
		t.Errorf("activeCourseBoundPaths=%d want 0 on a failed read", got.ActiveCourseBoundPaths)
	}
	if got.LastCourseAt != nil {
		t.Errorf("lastCourseAt=%v want absent on a failed read", *got.LastCourseAt)
	}
	if got.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity (never guess credential from a failed read)", got.PrimaryLens)
	}
	if !got.LeadPartial {
		t.Error("leadPartial=false on a failed path read; the empty course axis must be declared UNREAD, not reported as fact")
	}
}

// An UNWIRED path port is a gap, not an answer. A composition root that never
// swapped in the learning-path repo must not have its silence read as "this
// learner is enrolled on nothing".
func TestListGoals_UnwiredPathPortDeclaresTheCourseAxisPartial(t *testing.T) {
	repo := newGoalRepoFake()
	repo.seed(t, goal.NewGoalInput{Kind: goal.KindCuriosity})

	srv := goalServer(repo)
	srv.Paths = nil
	w := doGoal(srv, goalReq("GET", "/v1/me/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got leadResp
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ActiveGoals != 1 {
		t.Errorf("activeGoals=%d want 1 (the goal axis is unaffected)", got.ActiveGoals)
	}
	if !got.LeadPartial {
		t.Error("leadPartial=false with no path port wired; an unwired axis must be declared, never reported as zero")
	}
	if got.PrimaryLens != "curiosity" {
		t.Errorf("primaryLens=%q want curiosity", got.PrimaryLens)
	}
}
