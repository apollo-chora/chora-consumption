// transcript_handler_test.go — RED phase tests for the W6 Slice 1 read API:
//
//	GET /v1/me/transcript                         self, gcid-scoped
//	GET /v1/transcript/by-assessments?assessment_ids=a,b,c   tenant-scoped, role-gated
//
// package http (white-box, mirrors me_handlers_test.go) so the tests can set
// ExtServer.Transcript directly without a constructor seam.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// fakeTranscriptRepoForHandlerTest is an in-memory student_transcript.
// Repository for the read-handler tests.
type fakeTranscriptRepoForHandlerTest struct {
	byGCID       map[string][]*st.TranscriptEntry
	byAssessment []*st.TranscriptEntry
	listErr      error
}

func (f *fakeTranscriptRepoForHandlerTest) Upsert(context.Context, *st.TranscriptEntry) error {
	return nil
}

func (f *fakeTranscriptRepoForHandlerTest) ListByGCID(_ context.Context, _, gcid string, _ int) ([]*st.TranscriptEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byGCID[gcid], nil
}

// CountUnseenByGCID counts the learner's WHOLE stored set, and honours listErr
// so a store failure is a failure on both reads rather than only the list.
func (f *fakeTranscriptRepoForHandlerTest) CountUnseenByGCID(_ context.Context, _, gcid string) (int, error) {
	if f.listErr != nil {
		return 0, f.listErr
	}
	return st.CountUnseen(f.byGCID[gcid]), nil
}

func (f *fakeTranscriptRepoForHandlerTest) ListByAssessmentIDs(context.Context, string, []string) ([]*st.TranscriptEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byAssessment, nil
}

func newTranscriptEntry(t *testing.T, gcid, kind, sourceRef string, occurredAt time.Time) *st.TranscriptEntry {
	t.Helper()
	scoreEarned := 8.0
	scorePossible := 10.0
	passed := true
	e, err := st.New(st.NewEntryInput{
		TenantID:       meTenantID,
		GCID:           gcid,
		Kind:           st.Kind(kind),
		SourceRef:      sourceRef,
		Title:          "Quiz",
		ScoreEarned:    &scoreEarned,
		ScorePossible:  &scorePossible,
		Passed:         &passed,
		OccurredAt:     occurredAt,
		IdempotencyKey: "submission:" + sourceRef + ":graded",
	})
	if err != nil {
		t.Fatalf("newTranscriptEntry: %v", err)
	}
	return e
}

// ---------- GET /v1/me/transcript ----------

func TestGetMeTranscript_ReturnsGCIDScopedEntriesNewestFirst(t *testing.T) {
	srv := newMeServer()
	older := newTranscriptEntry(t, meGCID, "assessment", "assess-1", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	newer := newTranscriptEntry(t, meGCID, "certification", "cert-1", time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC))
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{
		byGCID: map[string][]*st.TranscriptEntry{meGCID: {newer, older}}, // repo returns newest-first already
	}
	r := newMeRequest("GET", "/v1/me/transcript", nil)
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
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.Items))
	}
	if resp.Items[0]["source_ref"] != "cert-1" {
		t.Errorf("items[0].source_ref = %v, want cert-1 (newest first)", resp.Items[0]["source_ref"])
	}
	if resp.Items[0]["kind"] != "certification" {
		t.Errorf("items[0].kind = %v, want certification", resp.Items[0]["kind"])
	}
	if resp.Items[1]["score_percent"] != float64(80) {
		t.Errorf("items[1].score_percent = %v, want 80", resp.Items[1]["score_percent"])
	}
	// gcid is NOT the caller-supplied header value by accident — must be the
	// entry's own gcid (defence against a handler that stamps the wrong field).
	if resp.Items[0]["gcid"] != meGCID {
		t.Errorf("items[0].gcid = %v, want %v", resp.Items[0]["gcid"], meGCID)
	}
}

func TestGetMeTranscript_RequiresHeaders(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := httptest.NewRequest("GET", "/v1/me/transcript", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing context)", w.Code)
	}
}

func TestGetMeTranscript_NotWired503(t *testing.T) {
	srv := newMeServer() // Transcript left nil
	r := newMeRequest("GET", "/v1/me/transcript", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (not wired)", w.Code)
	}
}

func TestGetMeTranscript_MethodNotAllowed(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := newMeRequest("POST", "/v1/me/transcript", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

func TestGetMeTranscript_EmptyListRendersEmptyItems(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{byGCID: map[string][]*st.TranscriptEntry{}}
	r := newMeRequest("GET", "/v1/me/transcript", nil)
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
	if resp.Items == nil {
		t.Error("items must be [] not null")
	}
}

// ---------- GET /v1/transcript/by-assessments ----------

func newAdminRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	r.Header.Set("x-mesh-user-roles", "instructor")
	return r
}

func TestGetTranscriptByAssessments_AdminRoleCanRead(t *testing.T) {
	srv := newMeServer()
	other1 := newTranscriptEntry(t, "01970000-0000-7000-9000-0000000000aa", "assessment", "assess-1", time.Now().UTC())
	other2 := newTranscriptEntry(t, "01970000-0000-7000-9000-0000000000bb", "assessment", "assess-2", time.Now().UTC())
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{byAssessment: []*st.TranscriptEntry{other1, other2}}
	r := newAdminRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1,assess-2")
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
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2 (OTHER learners' entries)", len(resp.Items))
	}
	// Must expose gcid so the R+ gradebook can attribute rows to a learner.
	gcids := map[string]bool{}
	for _, it := range resp.Items {
		gcids[it["gcid"].(string)] = true
	}
	if !gcids["01970000-0000-7000-9000-0000000000aa"] || !gcids["01970000-0000-7000-9000-0000000000bb"] {
		t.Errorf("expected both learner gcids present, got %v", gcids)
	}
}

func TestGetTranscriptByAssessments_NonAdminRoleForbidden(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := newAdminRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1")
	r.Header.Set("x-mesh-user-roles", "learner")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403 (non-admin role)", w.Code)
	}
}

func TestGetTranscriptByAssessments_MissingRoleHeaderForbidden(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := newAdminRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1")
	r.Header.Del("x-mesh-user-roles")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403 (fail-closed, no role header)", w.Code)
	}
}

func TestGetTranscriptByAssessments_AdminAliasRolesAccepted(t *testing.T) {
	for _, role := range []string{"admin", "training-admin", "training_admin", "tenant_admin"} {
		srv := newMeServer()
		srv.Transcript = &fakeTranscriptRepoForHandlerTest{byAssessment: []*st.TranscriptEntry{}}
		r := newAdminRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1")
		r.Header.Set("x-mesh-user-roles", role)
		w := httptest.NewRecorder()
		srv.Routes().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("role %q: status = %d; want 200", role, w.Code)
		}
	}
}

func TestGetTranscriptByAssessments_RequiresTenantHeader(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := httptest.NewRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1", nil)
	r.Header.Set("x-mesh-user-roles", "instructor")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing X-Tenant-Id)", w.Code)
	}
}

func TestGetTranscriptByAssessments_RequiresAssessmentIDsParam(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := newAdminRequest("GET", "/v1/transcript/by-assessments")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (missing assessment_ids)", w.Code)
	}
}

func TestGetTranscriptByAssessments_NotWired503(t *testing.T) {
	srv := newMeServer() // Transcript left nil
	r := newAdminRequest("GET", "/v1/transcript/by-assessments?assessment_ids=assess-1")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (not wired)", w.Code)
	}
}

func TestGetTranscriptByAssessments_MethodNotAllowed(t *testing.T) {
	srv := newMeServer()
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{}
	r := newAdminRequest("POST", "/v1/transcript/by-assessments?assessment_ids=assess-1")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", w.Code)
	}
}

// ---------- GET /v1/me/transcript — read-time course-title resolution (CHO-2242) ----------

// newCertEntry builds a certification TranscriptEntry exactly as the projection
// stores it: Title == CourseID == the course UUID. certification.issued.v1
// carries no course name, so transcript_subscriber.HandleCertIssued stamps the
// course_id into BOTH fields — resolving the real name is the read handler's job
// (course_directory), never the projection's (that behaviour is pinned by
// transcript_subscriber_test.go and must not change).
func newCertEntry(t *testing.T, gcid, certID, courseID string, occurredAt time.Time) *st.TranscriptEntry {
	t.Helper()
	e, err := st.New(st.NewEntryInput{
		TenantID:       meTenantID,
		GCID:           gcid,
		Kind:           st.KindCertification,
		SourceRef:      certID,
		Title:          courseID,
		CourseID:       courseID,
		OccurredAt:     occurredAt,
		IdempotencyKey: "cert:" + certID + ":issued",
	})
	if err != nil {
		t.Fatalf("newCertEntry: %v", err)
	}
	return e
}

// A cert row projected with title=course_id must render the REAL course name
// (resolved from course_directory) instead of the raw UUID; an assessment row in
// the same response keeps its own title; a cert row whose course_id is absent
// from the directory keeps its stored title. One batched lookup per request.
func TestGetMeTranscript_ResolvesCertTitlesFromCourseDirectory(t *testing.T) {
	srv := newMeServer()
	const courseHit = "01970000-0000-7000-b000-0000000000c1"
	const courseMiss = "01970000-0000-7000-b000-0000000000c2"
	when := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)

	certHit := newCertEntry(t, meGCID, "cert-hit", courseHit, when)
	assessment := newTranscriptEntry(t, meGCID, "assessment", "assess-1", when) // Title="Quiz", no course_id
	certMiss := newCertEntry(t, meGCID, "cert-miss", courseMiss, when)
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{
		byGCID: map[string][]*st.TranscriptEntry{meGCID: {certHit, assessment, certMiss}},
	}
	// The directory resolves courseHit only; courseMiss is deliberately absent.
	dir := &srvStubCourseDir{titles: map[string]string{courseHit: "Advanced Scrum Mastery"}}
	srv.CourseDirectory = dir

	r := newMeRequest("GET", "/v1/me/transcript", nil)
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
	byRef := map[string]map[string]any{}
	for _, it := range resp.Items {
		byRef[it["source_ref"].(string)] = it
	}
	if got := byRef["cert-hit"]["title"]; got != "Advanced Scrum Mastery" {
		t.Errorf("cert-hit title = %v; want resolved course name (not the %q UUID placeholder)", got, courseHit)
	}
	if got := byRef["assess-1"]["title"]; got != "Quiz" {
		t.Errorf("assessment title = %v; want Quiz (own title, untouched)", got)
	}
	if got := byRef["cert-miss"]["title"]; got != courseMiss {
		t.Errorf("cert-miss title = %v; want stored placeholder %q kept (directory miss)", got, courseMiss)
	}
	if dir.calls != 1 {
		t.Errorf("CourseDirectory.LookupTitles calls = %d; want 1 (single batched lookup)", dir.calls)
	}
}

// A nil CourseDirectory leaves every cert title byte-identical to what the
// projection stored (the resolution is purely additive, never destructive).
func TestGetMeTranscript_NilCourseDirectoryKeepsCertTitle(t *testing.T) {
	srv := newMeServer() // CourseDirectory left nil
	const courseID = "01970000-0000-7000-b000-0000000000c9"
	when := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{
		byGCID: map[string][]*st.TranscriptEntry{meGCID: {newCertEntry(t, meGCID, "cert-1", courseID, when)}},
	}
	r := newMeRequest("GET", "/v1/me/transcript", nil)
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
	if got := resp.Items[0]["title"]; got != courseID {
		t.Errorf("title = %v; want stored %q (nil directory = byte-identical fallback)", got, courseID)
	}
}

// A course_directory lookup ERROR must never fail the read (the enrichment is
// additive): the transcript still returns 200 and the cert row keeps its stored
// (UUID) title. lookupCourseTitles logs the failure LOUD; the log itself is not
// asserted (this package does not test log output).
func TestGetMeTranscript_LookupErrorNonFatalKeepsCertTitle(t *testing.T) {
	srv := newMeServer()
	const courseID = "01970000-0000-7000-b000-0000000000e1"
	when := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	srv.Transcript = &fakeTranscriptRepoForHandlerTest{
		byGCID: map[string][]*st.TranscriptEntry{meGCID: {newCertEntry(t, meGCID, "cert-1", courseID, when)}},
	}
	srv.CourseDirectory = &srvStubCourseDir{err: errors.New("directory down")}

	r := newMeRequest("GET", "/v1/me/transcript", nil)
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
	if got := resp.Items[0]["title"]; got != courseID {
		t.Errorf("title = %v; want stored %q kept on lookup error", got, courseID)
	}
}
