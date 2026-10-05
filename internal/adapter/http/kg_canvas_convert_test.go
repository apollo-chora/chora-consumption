package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/clusterprojection"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

type fakeProjector struct {
	goalID string
	err    error
	called bool
}

func (f *fakeProjector) Project(ctx context.Context, tenantID, gcid, clusterID string) (string, error) {
	f.called = true
	return f.goalID, f.err
}

func convertReq(clusterID string, withCtx bool) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/"+clusterID+"/convert", nil)
	if withCtx {
		r.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-000000000001")
		r.Header.Set("gcid", "01970000-0000-7000-9000-000000000001")
	}
	return r
}

func TestHandleCanvasConvert_Success(t *testing.T) {
	fp := &fakeProjector{goalID: "01970000-0000-7000-d000-000000000001"}
	s := &ExtServer{ClusterProjector: fp}
	w := httptest.NewRecorder()
	s.handleCanvasConvert(w, convertReq("cid", true), "cid")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), fp.goalID) {
		t.Errorf("body missing goalId: %s", w.Body.String())
	}
	if !fp.called {
		t.Error("projector was not called")
	}
}

func TestHandleCanvasConvert_Unavailable(t *testing.T) {
	s := &ExtServer{} // ClusterProjector nil ⇒ fail-loud 503
	w := httptest.NewRecorder()
	s.handleCanvasConvert(w, convertReq("cid", true), "cid")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandleCanvasConvert_NotOwnedIs404(t *testing.T) {
	s := &ExtServer{ClusterProjector: &fakeProjector{err: clusterprojection.ErrClusterNotOwned}}
	w := httptest.NewRecorder()
	s.handleCanvasConvert(w, convertReq("cid", true), "cid")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleCanvasConvert_MergedIs409(t *testing.T) {
	s := &ExtServer{ClusterProjector: &fakeProjector{err: userknowledgegraph.ErrAlreadyMerged}}
	w := httptest.NewRecorder()
	s.handleCanvasConvert(w, convertReq("cid", true), "cid")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestHandleCanvasConvert_MissingContextIs400(t *testing.T) {
	s := &ExtServer{ClusterProjector: &fakeProjector{goalID: "g"}}
	w := httptest.NewRecorder()
	s.handleCanvasConvert(w, convertReq("cid", false), "cid")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
