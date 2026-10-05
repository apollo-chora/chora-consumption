// RLS-context regression guard for the /v1/me/learning-paths?course_id read.
//
// The pg LearningPathRepo + AtomIndex repos derive the RLS tenant/gcid from
// the request context via rls.ApplySession (tracing.{TenantID,GCID}FromContext).
// The course-learn read handler MUST wrap r.Context() with tracing.WithTenantID
// + WithGCID before calling those repos — else the pg query runs with no RLS
// session set and the RLS-bound role returns 0 rows, yielding a spurious
// 404 PATH_NOT_BOOTSTRAPPED even though the path exists.
//
// Surfaced 2026-06-01 (OPEN-1 end-to-end): the enrollment bootstrap wrote the
// 3-atom path to pg, but GET /v1/me/learning-paths?course_id=... still 404'd
// because the handler passed bare r.Context(). The in-memory test repos ignore
// the ctx, so the existing happy-path tests masked the regression — hence this
// capturing fake that asserts the ctx carries the RLS identity.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	extinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ctxCapturingPaths embeds the in-memory repo and records the ctx handed to
// GetByCourseAndGCID so the test can assert the handler established the RLS
// identity on it.
type ctxCapturingPaths struct {
	*extinmem.LearningPathRepo
	lastCtx context.Context
}

func (c *ctxCapturingPaths) GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*learning_path.LearningPath, error) {
	c.lastCtx = ctx
	return c.LearningPathRepo.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
}

func TestGetMeLearningPaths_ByCourse_WrapsRLSContext(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	atom1, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-1", TenantID: meTenantID, CourseID: meCourseID, Title: "A1", PublishedAt: now,
	})
	_ = srv.AtomIndex.Save(context.Background(), atom1)

	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{"atom-1"}, Title: "RLS", Now: now,
	})
	capRepo := &ctxCapturingPaths{LearningPathRepo: extinmem.NewLearningPathRepo()}
	_ = capRepo.Save(context.Background(), p)
	srv.Paths = capRepo

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if capRepo.lastCtx == nil {
		t.Fatal("GetByCourseAndGCID was not called")
	}
	if got := tracing.TenantIDFromContext(capRepo.lastCtx); got != meTenantID {
		t.Errorf("repo ctx TenantIDFromContext = %q; want %q (handler must wrap r.Context() with tracing.WithTenantID — pg rls.ApplySession needs it)", got, meTenantID)
	}
	if got := tracing.GCIDFromContext(capRepo.lastCtx); got != meGCID {
		t.Errorf("repo ctx GCIDFromContext = %q; want %q (handler must wrap with tracing.WithGCID)", got, meGCID)
	}
}

// ctxCapturingAtomIndex embeds the in-memory atom_index repo and records the
// ctx handed to Get so the test can assert the answer/grade handler set the
// RLS identity (the pg atom_index repo is RLS-bound like learning_path).
type ctxCapturingAtomIndex struct {
	*extinmem.AtomIndexRepo
	lastGetCtx context.Context
}

func (c *ctxCapturingAtomIndex) Get(ctx context.Context, atomID string) (*atom_index.AtomIndex, error) {
	c.lastGetCtx = ctx
	return c.AtomIndexRepo.Get(ctx, atomID)
}

// submitMeAnswer reads atom_index (for MCQ grading) — it MUST wrap r.Context()
// with the RLS identity, else the pg atom_index read returns 0 rows and MCQ
// grading silently can't find the answer key. Same class as the learning-paths
// reads; surfaced auditing the OPEN-1 fix (me_handlers.go:184), 2026-06-01.
func TestSubmitMeAnswer_WrapsRLSContextForAtomIndex(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID: meAtomID, TenantID: meTenantID, CourseID: meCourseID,
		AtomType: "mcq", CorrectOptionID: "opt-c", AnswerCount: 4, Status: atom_index.StatusPublished, PublishedAt: time.Now().UTC(),
	})
	capAI := &ctxCapturingAtomIndex{AtomIndexRepo: extinmem.NewAtomIndexRepo()}
	_ = capAI.Save(context.Background(), a)
	srv.AtomIndex = capAI

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	if w.Code != http.StatusCreated {
		t.Fatalf("start session: %d %s", w.Code, w.Body.String())
	}
	var session map[string]any
	if err := json.NewDecoder(w.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	sid := session["session_id"].(string)

	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "ans-1", "selected_option_id": "opt-c"}))
	if w2.Code != http.StatusOK {
		t.Fatalf("submit answer: %d %s", w2.Code, w2.Body.String())
	}

	if capAI.lastGetCtx == nil {
		t.Fatal("AtomIndex.Get was not called during grading")
	}
	if got := tracing.TenantIDFromContext(capAI.lastGetCtx); got != meTenantID {
		t.Errorf("grade-read ctx TenantIDFromContext = %q; want %q (handler must wrap r.Context() — pg atom_index is RLS-bound)", got, meTenantID)
	}
	if got := tracing.GCIDFromContext(capAI.lastGetCtx); got != meGCID {
		t.Errorf("grade-read ctx GCIDFromContext = %q; want %q", got, meGCID)
	}
}

// rlsAssertingPaths mimics the pg LearningPathRepo's RLS behaviour: every
// ctx-taking method fails exactly like rls.ApplySession when the ctx lacks
// the tenant identity. The in-memory repos silently ignore ctx, which is how
// the bare-r.Context() side-effect call stayed green in tests while 500ing
// in production.
type rlsAssertingPaths struct {
	*extinmem.LearningPathRepo
}

func rlsAssert(ctx context.Context) error {
	if tracing.TenantIDFromContext(ctx) == "" {
		return errors.New("rls: tenant_id missing on context")
	}
	return nil
}

func (c *rlsAssertingPaths) ListByLearnerWithAtom(ctx context.Context, tenantID, gcid, atomID string) ([]*learning_path.LearningPath, error) {
	if err := rlsAssert(ctx); err != nil {
		return nil, err
	}
	return c.LearningPathRepo.ListByLearnerWithAtom(ctx, tenantID, gcid, atomID)
}

func (c *rlsAssertingPaths) Save(ctx context.Context, p *learning_path.LearningPath) error {
	if err := rlsAssert(ctx); err != nil {
		return err
	}
	return c.LearningPathRepo.Save(ctx, p)
}

// The session-completion side-effect fan-out (LearningPath.Advance +
// TopicRetention.Review + IMDA evidence) runs against the pg-backed RLS-bound
// repos in production — the answers handler MUST hand SessionCompletion.Handle
// the same RLS-wrapped ctx it built for MCQ grading, never bare r.Context().
// Surfaced live 2026-06-10 (L4 walk): every CORRECT answer 500'd
// SIDE_EFFECT_FAILED "rls: tenant_id missing on context" while wrong answers
// returned 200 — grading read used the wrapped ctx, the fan-out did not.
func TestSubmitMeAnswer_CompletionSideEffect_WrapsRLSContext(t *testing.T) {
	srv := newMeServer()
	now := time.Now().UTC()

	a, _ := atom_index.New(atom_index.NewParams{
		AtomID: meAtomID, TenantID: meTenantID, CourseID: meCourseID,
		AtomType: "mcq", CorrectOptionID: "opt-b", AnswerCount: 4, Status: atom_index.StatusPublished, PublishedAt: now,
	})
	_ = srv.AtomIndex.Save(context.Background(), a)

	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-rls-side-effect", AtomIDs: []string{meAtomID}, Title: "RLS side-effect", Now: now,
	})
	rlsPaths := &rlsAssertingPaths{LearningPathRepo: extinmem.NewLearningPathRepo()}
	if err := rlsPaths.LearningPathRepo.Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// Production wiring shape: WireLearningPathPg rebuilds SessionCompletion
	// against the (RLS-bound) paths repo.
	srv.WireLearningPathPg(rlsPaths)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	if w.Code != http.StatusCreated {
		t.Fatalf("start session: %d %s", w.Code, w.Body.String())
	}
	var session map[string]any
	if err := json.NewDecoder(w.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	sid := session["session_id"].(string)

	// Correct answer → session completes → side-effect fan-out hits the
	// RLS-asserting paths repo.
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "ans-1", "selected_option_id": "opt-b"}))
	if w2.Code != http.StatusOK {
		t.Fatalf("correct-answer submit = %d body=%s; want 200 (completion side-effect must receive the RLS-wrapped ctx)", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if got, _ := resp["is_correct"].(bool); !got {
		t.Fatalf("is_correct = %v; want true", resp["is_correct"])
	}
	if got, _ := resp["paths_advanced"].(float64); got != 1 {
		t.Errorf("paths_advanced = %v; want 1 (LearningPath.Advance must run under the RLS ctx)", resp["paths_advanced"])
	}
}
