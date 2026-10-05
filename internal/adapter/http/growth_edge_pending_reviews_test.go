package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// fakePendingLister is the narrow read-model port, not the write Repository.
type fakePendingLister struct {
	out        []wu.Upload
	err        error
	gotLearner string
	gotLimit   int
	callCount  int
}

func (f *fakePendingLister) ListAwaitingReview(_ context.Context, learnerGCID string, limit int) ([]wu.Upload, error) {
	f.callCount++
	f.gotLearner, f.gotLimit = learnerGCID, limit
	return f.out, f.err
}

func pendingServer(l wu.PendingReviewLister) *httpadapter.ExtServer {
	ext := httpadapter.NewExtServer(nil)
	ext.WeaknessPendingReviews = l
	return ext
}

func pendingReq(path string, withCtx bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if withCtx {
		r.Header.Set("X-Tenant-Id", "11111111-1111-7111-8111-111111111111")
		r.Header.Set("gcid", "22222222-2222-7222-8222-222222222222")
	}
	return r
}

func parkedUpload(id string) wu.Upload {
	return wu.Upload{
		UploadID:    id,
		LearnerGCID: "22222222-2222-7222-8222-222222222222",
		UploadKind:  "marked_test",
		Status:      wu.StatusAwaitingReview,
		GoalID:      "goal-1",
		CreatedAt:   time.Now().UTC(),
		Review: &wu.ReviewPanel{
			ProposedEdges: []wu.ProposedEdge{{ProposedEdgeID: "e-1", ConceptLabel: "Recursion"}},
		},
	}
}

func TestPendingReviews_ReturnsTheParkedList(t *testing.T) {
	l := &fakePendingLister{out: []wu.Upload{parkedUpload("u-1"), parkedUpload("u-2")}}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			UploadID string `json:"upload_id"`
			Status   string `json:"status"`
			Review   *struct {
				ProposedEdges []struct {
					ProposedEdgeID string `json:"proposed_edge_id"`
				} `json:"proposed_edges"`
			} `json:"review"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	if len(body.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(body.Items))
	}
	if body.Items[0].Status != "AWAITING_REVIEW" {
		t.Errorf("status = %q, want AWAITING_REVIEW", body.Items[0].Status)
	}
	if body.Items[0].Review == nil || len(body.Items[0].Review.ProposedEdges) != 1 {
		t.Error("the review panel must be carried on the list item")
	}
}

func TestPendingReviews_ScopesToTheCallersOwnGCID(t *testing.T) {
	// The positive control on identity: the learner id must come from the
	// verified header, never from a query parameter, or one learner could read
	// another's parked diagnoses by guessing an id.
	l := &fakePendingLister{}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w,
		pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review&learner_gcid=99999999-9999-7999-8999-999999999999", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if l.gotLearner != "22222222-2222-7222-8222-222222222222" {
		t.Errorf("listed for %q, want the header gcid; a query parameter must never widen the scope", l.gotLearner)
	}
}

func TestPendingReviews_EmptyRendersAnArrayNotNull(t *testing.T) {
	l := &fakePendingLister{out: []wu.Upload{}}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw["items"]) != "[]" {
		t.Errorf("items = %s, want []", raw["items"])
	}
}

func TestPendingReviews_GetWithoutTheStatusFilterIs400(t *testing.T) {
	// The mount is shared with the POST producer. A bare GET is a caller
	// mistake worth naming rather than quietly returning the whole upload
	// history, which is a different and much larger read.
	l := &fakePendingLister{}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads", true))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
	}
	if l.callCount != 0 {
		t.Error("a rejected request must not reach the repository")
	}
}

func TestPendingReviews_MissingContextIs400(t *testing.T) {
	l := &fakePendingLister{}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review", false))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if l.callCount != 0 {
		t.Error("a request with no tenant or gcid must not reach the repository")
	}
}

func TestPendingReviews_UnwiredPortIs503(t *testing.T) {
	// Fail loud rather than serving an empty list, which would tell the
	// learner they have nothing pending when the truth is we cannot look.
	ext := httpadapter.NewExtServer(nil)
	w := httptest.NewRecorder()
	ext.Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review", true))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", w.Code, w.Body.String())
	}
}

func TestPendingReviews_RepositoryErrorIs500NotAnEmptyList(t *testing.T) {
	l := &fakePendingLister{err: errors.New("connection refused")}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review", true))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestPendingReviews_LimitIsPassedThrough(t *testing.T) {
	l := &fakePendingLister{}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review&limit=5", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if l.gotLimit != 5 {
		t.Errorf("limit = %d, want 5", l.gotLimit)
	}
}

func TestPendingReviews_GarbageLimitFallsBackToTheDefault(t *testing.T) {
	// A non-numeric limit must not 400 the learner's home card; the repo
	// bounds it, so zero means "use the default".
	l := &fakePendingLister{}
	w := httptest.NewRecorder()
	pendingServer(l).Routes().ServeHTTP(w, pendingReq("/v1/me/growth-edges/uploads?status=awaiting_review&limit=lots", true))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if l.gotLimit != 0 {
		t.Errorf("limit = %d, want 0 so the repository applies its default", l.gotLimit)
	}
}

func TestPendingReviews_PostStillProducesAnUpload(t *testing.T) {
	// The regression that matters: this GET shares a mount with the POST
	// producer, so adding it must not turn the producer into a 405.
	ext := httpadapter.NewExtServer(nil)
	ext.WeaknessPendingReviews = &fakePendingLister{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/me/growth-edges/uploads", nil)
	r.Header.Set("X-Tenant-Id", "11111111-1111-7111-8111-111111111111")
	r.Header.Set("gcid", "22222222-2222-7222-8222-222222222222")
	ext.Routes().ServeHTTP(w, r)

	// The producer is unwired in this fixture, so it must answer 503 (its own
	// unwired arm) and NEVER 405, which would mean the GET arm swallowed it.
	if w.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST fell through to a 405; the GET arm must not displace the producer")
	}
}
