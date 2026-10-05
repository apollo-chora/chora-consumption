// me_handlers_cover_test.go — white-box tests for the remaining branch/error
// paths of submitMeAnswer + handleMeAtomSessions. Reuses newMeServer /
// newMeRequest / me* constants from me_handlers_test.go.
package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// meReqRaw issues a request with the standard me headers + a raw (possibly
// malformed) body.
func meReqRaw(method, path, rawBody string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(rawBody))
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestMeAtomSessions_BadJSON(t *testing.T) {
	srv := newMeServer()
	w := serveMe(srv, meReqRaw(http.MethodPost, "/v1/me/atom-sessions", "{not-json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("start bad JSON = %d; want 400", w.Code)
	}
}

func TestMeSubmitAnswer_BadJSON(t *testing.T) {
	srv := newMeServer()
	sess, err := atom_attempt.Start(meTenantID, meGCID, meAtomID, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Sessions.Save(context.Background(), sess)
	w := serveMe(srv, meReqRaw(http.MethodPost, "/v1/me/atom-sessions/"+sess.SessionID+"/answers", "{bad"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("submit bad JSON = %d; want 400", w.Code)
	}
}

func serveMe(srv *ExtServer, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w
}

func TestMeSubmitAnswer_MissingContext(t *testing.T) {
	srv := newMeServer()
	// no headers → 400 MISSING_CONTEXT.
	r := httptest.NewRequest(http.MethodPost, "/v1/me/atom-sessions/abc/answers", nil)
	w := serveMe(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("submit w/o context = %d; want 400", w.Code)
	}
}

func TestMeSubmitAnswer_SessionNotFound(t *testing.T) {
	srv := newMeServer()
	r := newMeRequest(http.MethodPost, "/v1/me/atom-sessions/no-such/answers",
		map[string]any{"answer_id": "a1"})
	w := serveMe(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("submit missing session = %d; want 404", w.Code)
	}
}

// RLS-leak guard: a session owned by a DIFFERENT tenant/gcid returns 404
// (never the row) when the caller's context mismatches.
func TestMeSubmitAnswer_CrossTenantReturns404(t *testing.T) {
	srv := newMeServer()
	// Seed a session owned by a DIFFERENT tenant.
	sess, err := atom_attempt.Start("other-tenant", "other-gcid", meAtomID, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Sessions.Save(context.Background(), sess)

	r := newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sess.SessionID+"/answers",
		map[string]any{"answer_id": "a1"})
	w := serveMe(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant submit = %d; want 404 (RLS leak guard)", w.Code)
	}
}

// Submitting an answer twice with the SAME answer_id is idempotent — the
// second submit returns 200 with duplicate=true and does NOT re-publish.
func TestMeSubmitAnswer_DuplicateAnswerID_NoRepublish(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		CourseID:        meCourseID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished, // CHO-2273
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)

	// Start.
	sw := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	if sw.Code != http.StatusCreated {
		t.Fatalf("start = %d", sw.Code)
	}
	var sr map[string]any
	_ = decodeJSON(sw, &sr)
	sid := sr["session_id"].(string)

	body := map[string]any{"answer_id": "dup-1", "selected_option_id": "opt-c"}
	w1 := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers", body))
	if w1.Code != http.StatusOK {
		t.Fatalf("first submit = %d body=%s", w1.Code, w1.Body.String())
	}
	// Second submit with the same answer_id → duplicate, still 200.
	w2 := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers", body))
	if w2.Code != http.StatusOK {
		t.Fatalf("dup submit = %d body=%s", w2.Code, w2.Body.String())
	}
	var ans map[string]any
	_ = decodeJSON(w2, &ans)
	if ans["duplicate"] != true {
		t.Errorf("duplicate = %v; want true on replay", ans["duplicate"])
	}
}

// A submit on an already-completed session is an invalid transition → 409.
func TestMeSubmitAnswer_InvalidTransition_409(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		CourseID:        meCourseID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished, // CHO-2273
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)

	sw := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	var sr map[string]any
	_ = decodeJSON(sw, &sr)
	sid := sr["session_id"].(string)

	// First (correct) answer completes the session.
	first := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "a1", "selected_option_id": "opt-c"}))
	if first.Code != http.StatusOK {
		t.Fatalf("first = %d body=%s", first.Code, first.Body.String())
	}
	// A NEW answer_id on the completed session → invalid transition → 409.
	second := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "a2", "selected_option_id": "opt-c"}))
	if second.Code != http.StatusConflict {
		t.Errorf("submit after completed = %d; want 409; body=%s", second.Code, second.Body.String())
	}
}

// submit-completed publish failure → 500.
func TestMeSubmitAnswer_CompletedPublishFailure_500(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:          meAtomID,
		TenantID:        meTenantID,
		CourseID:        meCourseID,
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		Status:          atom_index.StatusPublished, // CHO-2273
		PublishedAt:     time.Now().UTC(),
	})
	srv.AtomIndex.Save(context.Background(), a)

	sw := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	var sr map[string]any
	_ = decodeJSON(sw, &sr)
	sid := sr["session_id"].(string)

	srv.Publisher = failingPublisher{} // swap AFTER start so the start publish succeeds
	w := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sid+"/answers",
		map[string]any{"answer_id": "a1", "selected_option_id": "opt-c"}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("submit-complete w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

func TestMeStartSession_PublishFailure_500(t *testing.T) {
	srv := newMeServer()
	srv.Publisher = failingPublisher{}
	w := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions", map[string]string{"atom_id": meAtomID}))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("start w/ publish failure = %d; want 500; body=%s", w.Code, w.Body.String())
	}
}

// ── CHO-2030 buildout: server grading fails LOUD, never silently wrong ────
//
// P1.D walk finding #5: pre-dose-era atoms carry no CorrectOptionID in the
// atom_index projection; the old grade block silently marked every answer
// incorrect. Grading gaps must surface, not mis-grade.

func TestMeSubmitAnswer_UnindexedAtom_422NotGradeable(t *testing.T) {
	srv := newMeServer()
	// NO AtomIndex.Save — the projection has no row for the atom.
	sess, err := atom_attempt.Start(meTenantID, meGCID, meAtomID, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Sessions.Save(context.Background(), sess)

	w := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sess.SessionID+"/answers",
		map[string]any{"answer_id": "a1", "selected_option_id": "opt-a"}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d body=%s; want 422 ATOM_NOT_GRADEABLE", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = decodeJSON(w, &resp)
	if resp["code"] != "ATOM_NOT_GRADEABLE" {
		t.Errorf("code = %v; want ATOM_NOT_GRADEABLE", resp["code"])
	}
	// The attempt must NOT be consumed: the session stays pristine.
	got, _ := srv.Sessions.Get(context.Background(), sess.SessionID)
	if got.AnswerCount != 0 {
		t.Errorf("AnswerCount = %d; want 0 (ungradeable submit must not consume the attempt)", got.AnswerCount)
	}
}

func TestMeSubmitAnswer_KeylessMCQ_422NotGradeable(t *testing.T) {
	srv := newMeServer()
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID:      meAtomID,
		TenantID:    meTenantID,
		CourseID:    meCourseID,
		AtomType:    "mcq",
		AnswerCount: 4,
		Status:      atom_index.StatusPublished, // CHO-2273 — published, but no key (pre-dose-era)
		PublishedAt: time.Now().UTC(),
		// CorrectOptionID deliberately empty — authoring-era row without a key.
	})
	srv.AtomIndex.Save(context.Background(), a)

	sess, err := atom_attempt.Start(meTenantID, meGCID, meAtomID, atom_attempt.SystemClock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Sessions.Save(context.Background(), sess)

	w := serveMe(srv, newMeRequest(http.MethodPost, "/v1/me/atom-sessions/"+sess.SessionID+"/answers",
		map[string]any{"answer_id": "a1", "selected_option_id": "opt-a"}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d body=%s; want 422 for a keyless MCQ", w.Code, w.Body.String())
	}
}
