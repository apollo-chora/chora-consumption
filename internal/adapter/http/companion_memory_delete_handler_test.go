// companion_memory_delete_handler_test.go, register 6.4 R6: the learner's own
// delete over their conversation memory.
package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeMemoryEraser struct {
	rows     int64
	err      error
	gotTen   string
	gotGCID  string
	gotMemID string
	calls    int
}

func (f *fakeMemoryEraser) DeleteOwnedMemory(_ context.Context, tenantID, ownerGCID, memoryID string) (int64, error) {
	f.calls++
	f.gotTen, f.gotGCID, f.gotMemID = tenantID, ownerGCID, memoryID
	return f.rows, f.err
}

func deleteReq(memID string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/v1/me/companions/fam-1/memory/"+memID, nil)
	r.Header.Set("X-Tenant-Id", "t-1")
	r.Header.Set("gcid", "learner-1")
	r.SetPathValue("id", "fam-1")
	r.SetPathValue("memoryId", memID)
	return r
}

func TestCompanionMemoryDelete_ErasesAndReturns204(t *testing.T) {
	eraser := &fakeMemoryEraser{rows: 1}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, deleteReq("mem-1"))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	if eraser.gotMemID != "mem-1" {
		t.Errorf("memory id = %q, want mem-1", eraser.gotMemID)
	}
}

func TestCompanionMemoryDelete_TakesTheOwnerFromTheSessionNotTheRequest(t *testing.T) {
	// The GCID is the validated session subject. If it were ever read from the
	// path or body, one learner could erase another's memory by typing their id.
	eraser := &fakeMemoryEraser{rows: 1}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	r := deleteReq("mem-1")
	r.URL.RawQuery = "gcid=someone-else"
	h.ServeHTTP(httptest.NewRecorder(), r)

	if eraser.gotGCID != "learner-1" {
		t.Fatalf("owner = %q, want the session subject learner-1", eraser.gotGCID)
	}
	if eraser.gotTen != "t-1" {
		t.Fatalf("tenant = %q, want t-1", eraser.gotTen)
	}
}

func TestCompanionMemoryDelete_NoLiveRowIs404NotASilentSuccess(t *testing.T) {
	// 0 rows means nothing matched: unknown id, someone else's memory, or already
	// deleted. Answering 204 there would tell the learner their data was erased
	// when nothing was touched, which is the one lie this endpoint must not tell.
	eraser := &fakeMemoryEraser{rows: 0}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, deleteReq("mem-missing"))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no live row matched", w.Code)
	}
}

func TestCompanionMemoryDelete_RepoErrorIs500NotASilentSuccess(t *testing.T) {
	eraser := &fakeMemoryEraser{err: errors.New("boom")}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, deleteReq("mem-1"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestCompanionMemoryDelete_RejectsWrongMethod(t *testing.T) {
	h := &CompanionMemoryDeleteHandler{Memories: &fakeMemoryEraser{rows: 1}}
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/fam-1/memory/mem-1", nil)
	r.Header.Set("X-Tenant-Id", "t-1")
	r.Header.Set("gcid", "learner-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestCompanionMemoryDelete_RequiresSessionContext(t *testing.T) {
	eraser := &fakeMemoryEraser{rows: 1}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	r := httptest.NewRequest(http.MethodDelete, "/v1/me/companions/fam-1/memory/mem-1", nil)
	r.SetPathValue("memoryId", "mem-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without tenant/gcid", w.Code)
	}
	if eraser.calls != 0 {
		t.Fatalf("repo was called %d times without a session; want 0", eraser.calls)
	}
}

func TestCompanionMemoryDelete_RequiresAMemoryID(t *testing.T) {
	eraser := &fakeMemoryEraser{rows: 1}
	h := &CompanionMemoryDeleteHandler{Memories: eraser}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, deleteReq(""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 with no memory id", w.Code)
	}
	if eraser.calls != 0 {
		t.Fatalf("repo called %d times with no id; want 0", eraser.calls)
	}
}

func TestCompanionMemoryDelete_UnwiredIs503NotASilentSuccess(t *testing.T) {
	// No pgx pool at boot. A learner asking to erase their conversation must hear
	// that it did not happen, not a cheerful 204.
	h := &CompanionMemoryDeleteHandler{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, deleteReq("mem-1"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the eraser is not wired", w.Code)
	}
}

// The /v1/me/companions/ subtree is owned by handleCompanionInstanceByID, so the
// {id}/memory/{memoryId} subpath has to be dispatched there explicitly. Without
// this the DELETE falls through to the growth route and 404s while the handler
// above sits perfectly correct and unreachable.

func TestCompanionInstanceByID_DispatchesTheMemoryDeleteSubpath(t *testing.T) {
	eraser := &fakeMemoryEraser{rows: 1}
	s := &Server{CompanionMemoryDelete: NewCompanionMemoryDeleteHandler(eraser)}

	r := httptest.NewRequest(http.MethodDelete, "/v1/me/companions/fam-1/memory/mem-9", nil)
	r.Header.Set("X-Tenant-Id", "t-1")
	r.Header.Set("gcid", "learner-1")

	w := httptest.NewRecorder()
	s.handleCompanionInstanceByID(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; the subpath never reached the handler", w.Code)
	}
	if eraser.gotMemID != "mem-9" {
		t.Fatalf("memory id = %q, want mem-9 parsed from the path", eraser.gotMemID)
	}
}

func TestCompanionInstanceByID_MemoryReadSubpathStillWins(t *testing.T) {
	// Behaviour-neutral: adding the delete dispatch must not shadow GET {id}/memory.
	read := &stubReachedHandler{}
	s := &Server{
		CompanionMemoryRead:   read,
		CompanionMemoryDelete: NewCompanionMemoryDeleteHandler(&fakeMemoryEraser{rows: 1}),
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/me/companions/fam-1/memory", nil)
	r.Header.Set("X-Tenant-Id", "t-1")
	r.Header.Set("gcid", "learner-1")

	s.handleCompanionInstanceByID(httptest.NewRecorder(), r)
	if !read.reached {
		t.Fatal("GET {id}/memory no longer reaches the read handler")
	}
}

type stubReachedHandler struct{ reached bool }

func (s *stubReachedHandler) ServeHTTP(http.ResponseWriter, *http.Request) { s.reached = true }
