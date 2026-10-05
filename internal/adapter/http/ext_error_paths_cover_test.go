// ext_error_paths_cover_test.go — white-box tests for the error/branch paths
// of the EXT-scope handlers (learning_path, atom_attempt, me, knowledge_graph).
//
// These pin the transport-layer contract: each handler returns the correct
// status + error code for method-not-allowed, missing context/headers, bad
// JSON, unknown sub-routes, not-found lookups, and invalid query params.
// Reuses extReq / extHeaders / extServer / stubGraph / newKGServer from the
// sibling test files in this package.
package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// decodeJSON unmarshals a recorder body into v.
func decodeJSON(w *httptest.ResponseRecorder, v any) error {
	return json.Unmarshal(w.Body.Bytes(), v)
}

// learningPathEnroll constructs a fresh Enrollment for tests.
func learningPathEnroll(tenantID, pathID, gcid string) (*learning_path.Enrollment, error) {
	return learning_path.Enroll(tenantID, pathID, gcid)
}

// ---------------------------------------------------------------------------
// learning_path_handler.go branches
// ---------------------------------------------------------------------------

func TestExt_LearningPaths_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /learning-paths = %d; want 405", w.Code)
	}
}

func TestExt_LearningPaths_BadJSON(t *testing.T) {
	_, h := extServer(t)
	req := httptest.NewRequest(http.MethodPost, "/learning-paths", bytes.NewBufferString("{not-json"))
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d; want 400", w.Code)
	}
}

func TestExt_LearningPathByID_MissingPathID(t *testing.T) {
	_, h := extServer(t)
	// Path "/learning-paths/" trims to empty id.
	w := extReq(t, h, http.MethodPost, "/learning-paths/", nil, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty path id = %d; want 400", w.Code)
	}
}

func TestExt_LearningPathByID_BareID_NotFound(t *testing.T) {
	_, h := extServer(t)
	// "/learning-paths/{id}" with no sub-route → 404 NOT_FOUND.
	w := extReq(t, h, http.MethodGet, "/learning-paths/some-id", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("bare id = %d; want 404", w.Code)
	}
}

func TestExt_LearningPathByID_UnknownSubroute(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths/some-id/bogus", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

func TestExt_Enrollments_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths/p1/enrollments", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET enrollments = %d; want 405", w.Code)
	}
}

func TestExt_Enrollments_PathNotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/learning-paths/missing-path/enrollments", map[string]any{}, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("enroll into missing path = %d; want 404", w.Code)
	}
}

func TestExt_Enrollments_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/learning-paths/p1/enrollments", map[string]any{}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("enroll w/o context = %d; want 400", w.Code)
	}
}

func TestExt_Progress_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/learning-paths/p1/progress", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST progress = %d; want 405", w.Code)
	}
}

func TestExt_Progress_MissingGCID(t *testing.T) {
	_, h := extServer(t)
	// No gcid query AND no gcid header → 400 MISSING_GCID.
	w := extReq(t, h, http.MethodGet, "/learning-paths/p1/progress", nil, map[string]string{
		"X-Tenant-Id": tTenant,
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("progress w/o gcid = %d; want 400", w.Code)
	}
}

func TestExt_Progress_FallsBackToHeaderGCID_ButEnrollmentNotFound(t *testing.T) {
	// gcid supplied via header (not query) — passes the MISSING_GCID guard,
	// X-Tenant-Id present, but the path doesn't exist → PATH_NOT_FOUND 404.
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/learning-paths/missing/progress", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("progress header-gcid into missing path = %d; want 404", w.Code)
	}
}

func TestExt_Progress_EnrollmentNotFound(t *testing.T) {
	// Create a path but never enroll — progress lookup 404s on enrollment.
	s, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/learning-paths",
		map[string]any{"title": "T", "atom_ids": []string{tAtom1}}, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("create path = %d", w.Code)
	}
	var pr map[string]any
	_ = decodeJSON(w, &pr)
	pathID, _ := pr["path_id"].(string)
	_ = s
	w2 := extReq(t, h, http.MethodGet, "/learning-paths/"+pathID+"/progress?gcid="+tGCID, nil, extHeaders())
	if w2.Code != http.StatusNotFound {
		t.Errorf("progress w/o enrollment = %d; want 404 ENROLLMENT_NOT_FOUND; body=%s", w2.Code, w2.Body.String())
	}
}

// progress with a completed atom exercises the completed-atoms counting loop
// (pathSet membership) in getProgress.
func TestExt_Progress_CountsCompletedAtoms(t *testing.T) {
	s, h := extServer(t)
	// Create a 2-atom path.
	cw := extReq(t, h, http.MethodPost, "/learning-paths",
		map[string]any{"title": "T", "atom_ids": []string{tAtom1, tAtom2}}, extHeaders())
	if cw.Code != http.StatusCreated {
		t.Fatalf("create = %d", cw.Code)
	}
	var pr map[string]any
	_ = decodeJSON(cw, &pr)
	pathID, _ := pr["path_id"].(string)

	// Enroll, then mark one atom complete directly on the enrollment.
	e, err := learningPathEnroll(tTenant, pathID, tGCID)
	if err != nil {
		t.Fatal(err)
	}
	e.MarkAtomComplete(tAtom1)
	e.MarkAtomComplete("not-in-path") // ignored — not in pathSet
	s.Enrollments.Save(e)

	pw := extReq(t, h, http.MethodGet, "/learning-paths/"+pathID+"/progress?gcid="+tGCID, nil, extHeaders())
	if pw.Code != http.StatusOK {
		t.Fatalf("progress = %d body=%s", pw.Code, pw.Body.String())
	}
	var resp map[string]any
	_ = decodeJSON(pw, &resp)
	if resp["completed_atoms"] != float64(1) {
		t.Errorf("completed_atoms = %v; want 1 (the out-of-path atom is ignored)", resp["completed_atoms"])
	}
	if resp["total_atoms"] != float64(2) {
		t.Errorf("total_atoms = %v; want 2", resp["total_atoms"])
	}
}

// ---------------------------------------------------------------------------
// atom_attempt_handler.go branches
// ---------------------------------------------------------------------------

func TestExt_Sessions_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /sessions = %d; want 405", w.Code)
	}
}

func TestExt_StartSession_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions", map[string]any{"atom_id": tAtom1}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("start w/o context = %d; want 400", w.Code)
	}
}

func TestExt_StartSession_BadJSON(t *testing.T) {
	_, h := extServer(t)
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewBufferString("{"))
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d; want 400", w.Code)
	}
}

func TestExt_StartSession_InvalidStart(t *testing.T) {
	_, h := extServer(t)
	// empty atom_id → atom_attempt.Start rejects → 400 INVALID_START.
	w := extReq(t, h, http.MethodPost, "/sessions", map[string]any{"atom_id": ""}, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty atom_id = %d; want 400", w.Code)
	}
}

func TestExt_SessionByID_MissingID(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/", nil, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty session id = %d; want 400", w.Code)
	}
}

func TestExt_SessionByID_UnknownVerb(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions/abc:frobnicate", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown verb = %d; want 404", w.Code)
	}
}

func TestExt_SessionByID_AbandonWrongMethod(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/abc:abandon", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET :abandon = %d; want 405", w.Code)
	}
}

func TestExt_SessionByID_AnswerWrongMethod(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/abc/answer", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /answer = %d; want 405", w.Code)
	}
}

func TestExt_SessionByID_BareWrongMethod(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodDelete, "/sessions/abc", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /sessions/{id} = %d; want 405", w.Code)
	}
}

func TestExt_SessionByID_UnknownSubroute(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/abc/bogus", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

func TestExt_GetSession_NotFound_Cover(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/sessions/missing-session", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing session = %d; want 404", w.Code)
	}
}

func TestExt_SubmitAnswer_SessionNotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPatch, "/sessions/missing/answer",
		map[string]any{"answer": "a", "correct": true}, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("submit to missing session = %d; want 404", w.Code)
	}
}

func TestExt_SubmitAnswer_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPatch, "/sessions/abc/answer",
		map[string]any{"answer": "a"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("submit w/o context = %d; want 400", w.Code)
	}
}

func TestExt_AbandonSession_NotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions/missing:abandon", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("abandon missing session = %d; want 404", w.Code)
	}
}

func TestExt_AbandonSession_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions/abc:abandon", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("abandon w/o context = %d; want 400", w.Code)
	}
}

// Full lifecycle: start → submit (correct, completes) → abandon (409 invalid
// transition from a terminal state). Exercises the completed-event publish
// branch in submitAnswer + the ErrInvalidTransition branch in abandonSession.
func TestExt_Session_Lifecycle_CompleteThenAbandonConflict(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/sessions", map[string]any{"atom_id": tAtom1}, extHeaders())
	if w.Code != http.StatusCreated {
		t.Fatalf("start = %d body=%s", w.Code, w.Body.String())
	}
	var sr map[string]any
	_ = decodeJSON(w, &sr)
	sid, _ := sr["session_id"].(string)

	w2 := extReq(t, h, http.MethodPatch, "/sessions/"+sid+"/answer",
		map[string]any{"answer": "x", "correct": true}, extHeaders())
	if w2.Code != http.StatusOK {
		t.Fatalf("submit = %d body=%s", w2.Code, w2.Body.String())
	}
	var ar map[string]any
	_ = decodeJSON(w2, &ar)
	if ar["status"] != "completed" {
		t.Fatalf("status after correct submit = %v; want completed", ar["status"])
	}

	// Abandoning a completed session is an invalid transition → 409.
	w3 := extReq(t, h, http.MethodPost, "/sessions/"+sid+":abandon", nil, extHeaders())
	if w3.Code != http.StatusConflict {
		t.Errorf("abandon completed = %d; want 409; body=%s", w3.Code, w3.Body.String())
	}
}

// ---------------------------------------------------------------------------
// knowledge_graph_handler.go branches
// ---------------------------------------------------------------------------

func TestExt_Discover_MethodNotAllowed(t *testing.T) {
	_, h := newKGServer(&stubGraph{adj: map[string][]string{}})
	w := extReq(t, h, http.MethodPost, "/knowledge-graph/discover?topic=x", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST discover = %d; want 405", w.Code)
	}
}

func TestExt_Discover_GraphNotWired(t *testing.T) {
	// nil graph → 503 GRAPH_NOT_WIRED.
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/knowledge-graph/discover?topic=algebra", nil, extHeaders())
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("discover w/ nil graph = %d; want 503", w.Code)
	}
}

func TestExt_Discover_InvalidDepth(t *testing.T) {
	_, h := newKGServer(&stubGraph{adj: map[string][]string{"algebra": {"x"}}})
	w := extReq(t, h, http.MethodGet, "/knowledge-graph/discover?topic=algebra&depth=abc", nil, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid depth = %d; want 400", w.Code)
	}
}

func TestExt_Heatmap_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/me/knowledge-graph/heatmap", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST heatmap = %d; want 405", w.Code)
	}
}

func TestExt_Heatmap_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/me/knowledge-graph/heatmap", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("heatmap w/o context = %d; want 400", w.Code)
	}
}

func TestExt_Heatmap_OK(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/me/knowledge-graph/heatmap", nil, extHeaders())
	if w.Code != http.StatusOK {
		t.Errorf("heatmap = %d; want 200; body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// me_handlers.go branches
// ---------------------------------------------------------------------------

func TestExt_MeAtomSessions_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/atom-sessions", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/me/atom-sessions = %d; want 405", w.Code)
	}
}

func TestExt_MeAtomSessions_MissingAtomID(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/atom-sessions", map[string]any{"atom_id": "  "}, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("blank atom_id = %d; want 400 MISSING_ATOM_ID", w.Code)
	}
}

func TestExt_MeAtomSessionsByID_MissingID(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/atom-sessions/", nil, extHeaders())
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty id = %d; want 400", w.Code)
	}
}

func TestExt_MeAtomSessionsByID_AnswersWrongMethod(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/atom-sessions/abc/answers", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET answers = %d; want 405", w.Code)
	}
}

func TestExt_MeAtomSessionsByID_BareWrongMethod(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/atom-sessions/abc", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST bare session = %d; want 405", w.Code)
	}
}

func TestExt_MeAtomSessionsByID_UnknownSubroute(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/atom-sessions/abc/bogus", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subroute = %d; want 404", w.Code)
	}
}

func TestExt_SubmitMeAnswer_SessionNotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/atom-sessions/missing/answers",
		map[string]any{"answer_id": "a1"}, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("submit to missing me-session = %d; want 404", w.Code)
	}
}

func TestExt_GetMeSession_NotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/atom-sessions/missing", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing me-session = %d; want 404", w.Code)
	}
}

func TestExt_MeLearningPaths_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/learning-paths", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/me/learning-paths = %d; want 405", w.Code)
	}
}

func TestExt_MeLearningPaths_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/learning-paths", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("list paths w/o context = %d; want 400", w.Code)
	}
}

func TestExt_MeLearningPaths_ByCourse_NotBootstrapped(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/learning-paths?course_id=course-x", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("course not bootstrapped = %d; want 404 PATH_NOT_BOOTSTRAPPED; body=%s", w.Code, w.Body.String())
	}
}

func TestExt_MeLearningPathByID_MissingID(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/learning-paths/", nil, extHeaders())
	// Trailing slash hits the collection route (no id) → 200 list, not the
	// by-id handler. Assert it does NOT 500.
	if w.Code >= 500 {
		t.Errorf("trailing-slash list = %d; want < 500", w.Code)
	}
}

func TestExt_MeLearningPathByID_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodDelete, "/v1/me/learning-paths/abc", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE by-id = %d; want 405", w.Code)
	}
}

func TestExt_MeLearningPathByID_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/learning-paths/abc", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("by-id w/o context = %d; want 400", w.Code)
	}
}

func TestExt_MeLearningPathByID_NotFound(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/learning-paths/missing", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("by-id missing = %d; want 404", w.Code)
	}
}

func TestExt_MeTopicRetention_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/topic-retention", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST topic-retention = %d; want 405", w.Code)
	}
}

func TestExt_MeTopicRetention_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/topic-retention", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("topic-retention w/o context = %d; want 400", w.Code)
	}
}

func TestExt_ParseLimit_Branches(t *testing.T) {
	// parseLimit: empty → default; bad → default; <=0 → default; valid → n.
	mk := func(q string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/x?"+q, nil)
	}
	if got := parseLimit(mk(""), 50); got != 50 {
		t.Errorf("empty limit = %d; want default 50", got)
	}
	if got := parseLimit(mk("limit=abc"), 50); got != 50 {
		t.Errorf("non-numeric limit = %d; want default 50", got)
	}
	if got := parseLimit(mk("limit=0"), 50); got != 50 {
		t.Errorf("zero limit = %d; want default 50", got)
	}
	if got := parseLimit(mk("limit=-5"), 50); got != 50 {
		t.Errorf("negative limit = %d; want default 50", got)
	}
	if got := parseLimit(mk("limit=7"), 50); got != 7 {
		t.Errorf("valid limit = %d; want 7", got)
	}
}

func TestExt_AtomStateForIndex(t *testing.T) {
	cases := []struct {
		i, current   int
		pathComplete bool
		want         string
	}{
		{0, 2, false, "completed"},   // i < current
		{2, 2, false, "in_progress"}, // i == current, not complete
		{2, 2, true, "completed"},    // i == current, path complete
		{3, 2, false, "not_started"}, // i > current
	}
	for _, c := range cases {
		if got := atomStateForIndex(c.i, c.current, c.pathComplete); got != c.want {
			t.Errorf("atomStateForIndex(%d,%d,%v) = %q; want %q", c.i, c.current, c.pathComplete, got, c.want)
		}
	}
}

func TestExt_MeCourseContent_MethodNotAllowed(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodPost, "/v1/me/courses/c1/content", nil, extHeaders())
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST course content = %d; want 405", w.Code)
	}
}

func TestExt_MeCourseContent_MissingContext(t *testing.T) {
	_, h := extServer(t)
	w := extReq(t, h, http.MethodGet, "/v1/me/courses/c1/content", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("course content w/o context = %d; want 400", w.Code)
	}
}

func TestExt_MeCourseContent_BadPath(t *testing.T) {
	_, h := extServer(t)
	// Missing /content suffix → NOT_FOUND.
	w := extReq(t, h, http.MethodGet, "/v1/me/courses/c1", nil, extHeaders())
	if w.Code != http.StatusNotFound {
		t.Errorf("bad course-content path = %d; want 404", w.Code)
	}
}

func TestExt_TraceFromHeaders_Branches(t *testing.T) {
	const w3cExample = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"
	const wellFormed = "00-11111111111111111111111111111111-2222222222222222-01"
	const tpShape = `^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`

	// A LIVE span wins over the inbound header: it is the id Cloud Trace
	// exported for this request, so it is the one a consumer can resolve.
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    mustTraceID(t, "1234567890abcdef1234567890abcdef"),
		SpanID:     mustSpanID(t, "abcdef0123456789"),
		TraceFlags: oteltrace.FlagsSampled,
	})
	rs := httptest.NewRequest(http.MethodGet, "/x", nil)
	rs.Header.Set("traceparent", wellFormed)
	rs.Header.Set("tracestate", "vendor=1")
	rs = rs.WithContext(oteltrace.ContextWithSpanContext(rs.Context(), sc))
	tps, tss := extTraceFromHeaders(rs)
	if tps != "00-1234567890abcdef1234567890abcdef-abcdef0123456789-01" {
		t.Errorf("live span traceparent = %q; want the span context value", tps)
	}
	if tss != "vendor=1" {
		t.Errorf("tracestate = %q; want vendor=1", tss)
	}

	// No live span: a well-formed inbound header is forwarded.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("traceparent", wellFormed)
	r.Header.Set("tracestate", "vendor=1")
	tp, ts := extTraceFromHeaders(r)
	if tp != wellFormed || ts != "vendor=1" {
		t.Errorf("present headers = (%q,%q)", tp, ts)
	}

	// Absent traceparent mints a FRESH root, never the W3C example
	// constant, which would collapse every unparented request onto one
	// trace id (FINDING-02, 2026-08-07).
	r2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	tp2, ts2 := extTraceFromHeaders(r2)
	if tp2 == w3cExample {
		t.Error("fallback must never be the W3C example traceparent")
	}
	if !regexp.MustCompile(tpShape).MatchString(tp2) {
		t.Errorf("fallback traceparent = %q; want a well-formed W3C value", tp2)
	}
	if tp3, _ := extTraceFromHeaders(httptest.NewRequest(http.MethodGet, "/x", nil)); tp3 == tp2 {
		t.Error("two unparented requests must not share a trace id")
	}
	if ts2 != "" {
		t.Errorf("absent tracestate = %q; want empty", ts2)
	}

	// A MALFORMED inbound header is replaced, not forwarded: an envelope
	// carrying a structurally invalid traceparent resolves to nothing.
	r4 := httptest.NewRequest(http.MethodGet, "/x", nil)
	r4.Header.Set("traceparent", "00-aaaa-bbbb-01")
	tp4, _ := extTraceFromHeaders(r4)
	if tp4 == "00-aaaa-bbbb-01" {
		t.Error("a malformed traceparent must not be forwarded verbatim")
	}
	if !regexp.MustCompile(tpShape).MatchString(tp4) {
		t.Errorf("replacement traceparent = %q; want a well-formed W3C value", tp4)
	}
}

func mustTraceID(t *testing.T, hex string) oteltrace.TraceID {
	t.Helper()
	id, err := oteltrace.TraceIDFromHex(hex)
	if err != nil {
		t.Fatalf("TraceIDFromHex(%q): %v", hex, err)
	}
	return id
}

func mustSpanID(t *testing.T, hex string) oteltrace.SpanID {
	t.Helper()
	id, err := oteltrace.SpanIDFromHex(hex)
	if err != nil {
		t.Fatalf("SpanIDFromHex(%q): %v", hex, err)
	}
	return id
}
