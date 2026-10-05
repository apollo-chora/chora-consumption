// me_handlers_test.go — RED phase tests for the S4.2 /v1/me/* endpoints.
//
// Endpoints under test:
//
//	POST /v1/me/atom-sessions                    start AtomAttempt
//	POST /v1/me/atom-sessions/{id}/answers       submit answer (server-side MCQ grade)
//	GET  /v1/me/learning-paths                   list learner's paths
//	GET  /v1/me/learning-paths/{id}              path detail
//	GET  /v1/me/topic-retention                  paginated retention scores
//
// All require X-Tenant-Id + gcid headers + traceparent.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

const (
	meTenantID = "01970000-0000-7000-8000-000000000001"
	meGCID     = "01970000-0000-7000-9000-000000000001"
	meAtomID   = "01970000-0000-7000-a000-000000000001"
	meCourseID = "01970000-0000-7000-b000-000000000001"
)

func newMeServer() *ExtServer {
	return NewExtServer(nil)
}

func newMeRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestPostMeAtomSessions_StartsSession(t *testing.T) {
	srv := newMeServer()
	r := newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["session_id"] == "" {
		t.Errorf("session_id empty: %v", resp)
	}
}

func TestPostMeAtomSessions_RequiresHeaders(t *testing.T) {
	srv := newMeServer()
	r := httptest.NewRequest("POST", "/v1/me/atom-sessions", bytes.NewBufferString(`{"atom_id":"a"}`))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing context)", w.Code)
	}
}

func TestPostMeAtomSessionsAnswers_AutoGradesMCQ(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		CourseID:        meCourseID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished, // CHO-2273 — learner take gates on Playable()
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)

	// Start session.
	r := newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var session map[string]any
	if err := json.NewDecoder(w.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	sid := session["session_id"].(string)

	// Submit a CORRECT answer (selected_option_id matches the seeded
	// correct_option_id "opt-c").
	r2 := newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers", map[string]any{
		"answer_id":          "ans-1",
		"selected_option_id": "opt-c",
	})
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", w2.Code, w2.Body.String())
	}
	var ans map[string]any
	if err := json.NewDecoder(w2.Body).Decode(&ans); err != nil {
		t.Fatal(err)
	}
	if ans["is_correct"] != true {
		t.Errorf("is_correct = %v; want true", ans["is_correct"])
	}
	if ans["status"] != "completed" {
		t.Errorf("status = %v; want completed", ans["status"])
	}
}

// CHO-2273 (SECURITY) — a learner must not be able to START or GRADE an
// unpublished DRAFT atom. CHO-2272 made option ids DERIVED at serve, which fixed
// the read leak for drafts too but exposed that a learner could take a draft and
// have it graded against a stale key. The learner take path must gate on
// atom_index.Playable() (status=published), mirroring the dose/companion/KG
// precedent. A MISSING row = projection lag (a published-but-unprojected atom)
// and MUST NOT be blocked (see TestPostMeAtomSessions_StartsSession, which starts
// with no atom_index row and still expects 201).

func TestStartMeAtomSession_RefusedOnDraft(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		Status:          atom_index.StatusDraft,
	})
	srv.AtomIndex.Save(context.Background(), a)

	r := newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code == http.StatusCreated {
		t.Errorf("start on a DRAFT atom returned 201; a draft must not be startable (non-gradeable)")
	}
	if w.Code >= 500 {
		t.Errorf("start on a draft = %d; want a 4xx refusal, not a 5xx", w.Code)
	}
}

func TestSubmitMeAnswer_RefusedOnDraft(t *testing.T) {
	srv := newMeServer()
	// Start with NO atom_index row (projection lag) — start is allowed (a
	// published-but-unprojected atom must not be blocked).
	r := newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("start (no row): %d %s", w.Code, w.Body.String())
	}
	var session map[string]any
	_ = json.NewDecoder(w.Body).Decode(&session)
	sid := session["session_id"].(string)

	// The projection now arrives as a DRAFT (author saved a draft; never
	// published). Save is non-downgrading, but with no prior published row this
	// lands as a draft.
	draft, _ := atom_index.New(atom_index.NewParams{
		AtomID: meAtomID, TenantID: meTenantID, AtomType: "mcq",
		CorrectOptionID: "opt-c", AnswerCount: 4,
		Status: atom_index.StatusDraft,
	})
	srv.AtomIndex.Save(context.Background(), draft)

	r2 := newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers", map[string]any{
		"answer_id": "ans-1", "selected_option_id": "opt-c",
	})
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, r2)
	if w2.Code == http.StatusOK {
		t.Errorf("grading a DRAFT atom returned 200; a draft must never be scored")
	}
	if w2.Code >= 500 {
		t.Errorf("grade on a draft = %d; want a 4xx refusal, not a 5xx", w2.Code)
	}
}

func TestPostMeAtomSessionsAnswers_IdempotentOnAnswerID(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-a",
		AnswerCount:     2,
		Status:          atom_index.StatusPublished, // CHO-2273 — learner take gates on Playable()
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)

	// Start session.
	r := newMeRequest("POST", "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	var session map[string]any
	json.NewDecoder(w.Body).Decode(&session)
	sid := session["session_id"].(string)

	body := map[string]any{"answer_id": "ans-x", "selected_option_id": "opt-a"}

	// First submit.
	w1 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w1, newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers", body))
	if w1.Code != http.StatusOK {
		t.Fatalf("first: %d %s", w1.Code, w1.Body.String())
	}
	// Replay — same answer_id.
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, newMeRequest("POST", "/v1/me/atom-sessions/"+sid+"/answers", body))
	if w2.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", w2.Code, w2.Body.String())
	}
	var replay map[string]any
	json.NewDecoder(w2.Body).Decode(&replay)
	if replay["duplicate"] != true {
		t.Errorf("duplicate = %v; want true", replay["duplicate"])
	}
}

func TestGetMeLearningPaths_ListsLearnerPaths(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    meTenantID,
		LearnerGCID: meGCID,
		CourseID:    meCourseID,
		AtomIDs:     []string{meAtomID},
		Now:         now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Errorf("len items = %d; want 1", len(items))
	}
}

// TestGetMeLearningPaths_List_EnrichesTitleFromCourseDirectory: the LIST
// endpoint (Continue-learning) mirrors the by-course enrich. A course-provenanced
// path carries a "Course <uuid>" placeholder title (enrollment.created carries no
// course name — chora_delivery owns it), so the heading would render a raw UUID.
// The real name is resolved from the local course_directory projection; a
// study-list path (no course_id) is left untouched, and one batched lookup covers
// the whole page.
func TestGetMeLearningPaths_List_EnrichesTitleFromCourseDirectory(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	coursePath, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{meAtomID}, Now: now,
	})
	srv.Paths.Save(context.Background(), coursePath)
	if coursePath.Title != "Course "+meCourseID {
		t.Fatalf("precondition: expected placeholder title, got %q", coursePath.Title)
	}
	// A study-list (collection-derived) path has no course_id and its own title —
	// it must survive the enrich untouched.
	studyPath, _ := learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID: meTenantID, OwnerGCID: meGCID, CollectionID: "coll-1",
		Title: "My Reading List", AtomIDs: []string{meAtomID}, Now: now,
	})
	srv.Paths.Save(context.Background(), studyPath)

	dir := &srvStubCourseDir{titles: map[string]string{meCourseID: "Advanced Scrum Mastery"}}
	srv.CourseDirectory = dir

	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	byPath := map[string]map[string]any{}
	for _, it := range resp.Items {
		byPath[it["path_id"].(string)] = it
	}
	if got := byPath[coursePath.PathID]["title"]; got != "Advanced Scrum Mastery" {
		t.Errorf("course path title = %v; want resolved course name (not the %q placeholder)", got, coursePath.Title)
	}
	if got := byPath[studyPath.PathID]["title"]; got != "My Reading List" {
		t.Errorf("study-list path title = %v; want its own title kept (no course_id, untouched)", got)
	}
	if dir.calls != 1 {
		t.Errorf("CourseDirectory.LookupTitles calls = %d; want 1 (single batched lookup)", dir.calls)
	}
}

// TestGetMeLearningPaths_List_KeepsPlaceholderWhenDirectoryMiss: a course absent
// from the directory keeps its "Course <uuid>" placeholder (additive +
// non-destructive), mirroring the by-course miss behaviour.
func TestGetMeLearningPaths_List_KeepsPlaceholderWhenDirectoryMiss(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	coursePath, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{meAtomID}, Now: now,
	})
	srv.Paths.Save(context.Background(), coursePath)
	srv.CourseDirectory = &srvStubCourseDir{titles: map[string]string{}} // no entry for meCourseID

	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(resp.Items))
	}
	if got := resp.Items[0]["title"]; got != "Course "+meCourseID {
		t.Errorf("title = %v; want placeholder kept on directory miss", got)
	}
}

// TestGetMeLearningPaths_List_LookupErrorNonFatalKeepsPlaceholder: a
// course_directory lookup ERROR must never fail the LIST read — it still returns
// 200 and the course path keeps its "Course <uuid>" placeholder. The helper logs
// the failure LOUD (fail-loud, non-fatal); the log is not asserted.
func TestGetMeLearningPaths_List_LookupErrorNonFatalKeepsPlaceholder(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	coursePath, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{meAtomID}, Now: now,
	})
	srv.Paths.Save(context.Background(), coursePath)
	srv.CourseDirectory = &srvStubCourseDir{err: errors.New("directory down")}

	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 (lookup error must be non-fatal)", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(resp.Items))
	}
	if got := resp.Items[0]["title"]; got != "Course "+meCourseID {
		t.Errorf("title = %v; want placeholder kept on lookup error", got)
	}
}

// ---------- GET /v1/me/learning-paths?course_id={id} (open-course / course-learn page) ----------

func TestGetMeLearningPaths_ByCourse_404WhenNoBootstrap(t *testing.T) {
	srv := newMeServer()
	// No path bootstrapped for course — FE poller treats 404 as "not-yet-ready, retry".
	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (fail-loud when no path yet)", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["code"] != "PATH_NOT_BOOTSTRAPPED" {
		t.Errorf("code = %q; want PATH_NOT_BOOTSTRAPPED", resp["code"])
	}
}

func TestGetMeLearningPaths_ByCourse_ReturnsHydratedShape(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	// Seed atom_index for the course atoms (no cross-DB to chora_creation).
	atom1, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-1", TenantID: meTenantID, CourseID: meCourseID,
		Title: "Intro to Scrum", PublishedAt: now,
	})
	atom2, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-2", TenantID: meTenantID, CourseID: meCourseID,
		Title: "Scrum Roles", PublishedAt: now,
	})
	srv.AtomIndex.Save(context.Background(), atom1)
	srv.AtomIndex.Save(context.Background(), atom2)

	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:     meTenantID,
		LearnerGCID:  meGCID,
		CourseID:     meCourseID,
		EnrollmentID: "enroll-1",
		AtomIDs:      []string{"atom-1", "atom-2"},
		Title:        "CJ2 E2E",
		Now:          now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["learning_path_id"] != p.PathID {
		t.Errorf("learning_path_id = %v; want %s", resp["learning_path_id"], p.PathID)
	}
	if resp["course_id"] != meCourseID {
		t.Errorf("course_id = %v; want %s", resp["course_id"], meCourseID)
	}
	if resp["mode"] != "straight_up" {
		t.Errorf("mode = %v; want straight_up", resp["mode"])
	}
	if resp["title"] != "CJ2 E2E" {
		t.Errorf("title = %v; want CJ2 E2E", resp["title"])
	}
	atoms, _ := resp["atoms"].([]any)
	if len(atoms) != 2 {
		t.Fatalf("len atoms = %d; want 2", len(atoms))
	}
	a0, _ := atoms[0].(map[string]any)
	if a0["atom_id"] != "atom-1" {
		t.Errorf("atom[0].atom_id = %v; want atom-1", a0["atom_id"])
	}
	if a0["title"] != "Intro to Scrum" {
		t.Errorf("atom[0].title = %v; want Intro to Scrum (hydrated from atom_index)", a0["title"])
	}
	if int(a0["order"].(float64)) != 1 {
		t.Errorf("atom[0].order = %v; want 1", a0["order"])
	}
	if a0["state"] != "in_progress" {
		t.Errorf("atom[0].state = %v; want in_progress (CurrentIndex=0)", a0["state"])
	}
	a1, _ := atoms[1].(map[string]any)
	if a1["state"] != "not_started" {
		t.Errorf("atom[1].state = %v; want not_started", a1["state"])
	}
}

// TestGetMeLearningPaths_ByCourse_EnrichesTitleFromCourseDirectory: the
// enrollment-bootstrapped path carries a "Course <uuid>" placeholder title
// (enrollment.created carries no course name — chora_delivery owns it). The
// course-learn read must resolve the REAL course name from the local
// course_directory projection so the A+ heading shows a name, not a raw UUID.
func TestGetMeLearningPaths_ByCourse_EnrichesTitleFromCourseDirectory(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{}, Now: now,
	})
	srv.Paths.Save(context.Background(), p)
	if p.Title != "Course "+meCourseID {
		t.Fatalf("precondition: expected placeholder title, got %q", p.Title)
	}

	// course_directory holds the real name.
	dir := &srvStubCourseDir{titles: map[string]string{meCourseID: "Advanced Scrum Mastery"}}
	srv.CourseDirectory = dir

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["title"] != "Advanced Scrum Mastery" {
		t.Errorf("title = %v; want real course name from course_directory (not the %q placeholder)", resp["title"], p.Title)
	}
	if dir.calls != 1 {
		t.Errorf("CourseDirectory.LookupTitles calls = %d; want 1", dir.calls)
	}
}

// TestGetMeLearningPaths_ByCourse_KeepsPathTitleWhenDirectoryMiss: enrichment is
// additive + non-destructive — a course absent from the directory (or a lookup
// error) keeps the path's own title rather than blanking it.
func TestGetMeLearningPaths_ByCourse_KeepsPathTitleWhenDirectoryMiss(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{}, Title: "Explicit Title", Now: now,
	})
	srv.Paths.Save(context.Background(), p)
	srv.CourseDirectory = &srvStubCourseDir{titles: map[string]string{}} // no entry for meCourseID

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["title"] != "Explicit Title" {
		t.Errorf("title = %v; want path title kept on directory miss", resp["title"])
	}
}

func TestGetMeLearningPaths_ByCourse_AtomStateMatchesCurrentIndex(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-1", "a-2", "a-3"} {
		a, _ := atom_index.New(atom_index.NewParams{
			AtomID: id, TenantID: meTenantID, CourseID: meCourseID,
			Title: "title-" + id, PublishedAt: now,
		})
		srv.AtomIndex.Save(context.Background(), a)
	}
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		AtomIDs: []string{"a-1", "a-2", "a-3"}, Now: now,
	})
	// Advance past a-1 → CurrentIndex moves to 1.
	if _, err := p.Advance("a-1", now); err != nil {
		t.Fatal(err)
	}
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	atoms, _ := resp["atoms"].([]any)
	if len(atoms) != 3 {
		t.Fatalf("len atoms = %d", len(atoms))
	}
	want := []string{"completed", "in_progress", "not_started"}
	for i, w := range want {
		got := atoms[i].(map[string]any)["state"]
		if got != w {
			t.Errorf("atom[%d].state = %v; want %s", i, got, w)
		}
	}
}

func TestGetMeLearningPaths_ByCourse_CrossTenantReturns404(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	// Path belongs to a DIFFERENT tenant — must NOT leak to meTenantID.
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: "other-tenant", LearnerGCID: "other-gcid",
		CourseID: meCourseID, AtomIDs: []string{meAtomID}, Now: now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (RLS leak guard)", w.Code)
	}
}

func TestGetMeLearningPaths_ByCourse_MissingHeadersReturns400(t *testing.T) {
	srv := newMeServer()
	r := httptest.NewRequest("GET", "/v1/me/learning-paths?course_id="+meCourseID, nil)
	// No X-Tenant-Id / gcid headers.
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing context)", w.Code)
	}
}

func TestGetMeLearningPathByID(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    meTenantID,
		LearnerGCID: meGCID,
		CourseID:    meCourseID,
		AtomIDs:     []string{meAtomID},
		Now:         now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths/"+p.PathID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
}

func TestGetMeLearningPathByID_RejectsCrossTenant(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    "other-tenant",
		LearnerGCID: "other-gcid",
		CourseID:    meCourseID,
		AtomIDs:     []string{meAtomID},
		Now:         now,
	})
	srv.Paths.Save(context.Background(), p)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths/"+p.PathID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	// RLS leak guard — different tenant + GCID -> 404 (not 200, not the
	// path).
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-tenant leak guard)", w.Code)
	}
}

func TestGetMeTopicRetention_ListsLearnerScores(t *testing.T) {
	srv := newMeServer()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	for _, topic := range []string{"agile", "scrum"} {
		s, _ := topic_retention.New(meTenantID, meGCID, topic, now, 1.0)
		if err := srv.Retention.Save(context.Background(), s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// Other learner — must be excluded.
	other, _ := topic_retention.New(meTenantID, "other-gcid", "agile", now, 1.0)
	if err := srv.Retention.Save(context.Background(), other); err != nil {
		t.Fatalf("Save: %v", err)
	}

	r := httptest.NewRequest("GET", "/v1/me/topic-retention?limit=10", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	items, _ := resp["items"].([]any)
	if len(items) != 2 {
		t.Errorf("len items = %d; want 2 (cross-learner leak guard)", len(items))
	}
}
