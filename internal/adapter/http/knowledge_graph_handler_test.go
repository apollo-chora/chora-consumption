// knowledge_graph_handler_test — EXT scope KG read endpoints.
//
//	GET /knowledge-graph/discover?topic=X&depth=N
//	GET /me/knowledge-graph/heatmap
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/knowledge_graph_read"
)

// stubGraph is an in-mem adjacency Graph for tests.
type stubGraph struct{ adj map[string][]string }

func (g *stubGraph) Neighbors(topic string) ([]string, error) {
	return g.adj[topic], nil
}

func newKGServer(g knowledge_graph_read.Graph) (*ExtServer, http.Handler) {
	s := NewExtServer(g)
	return s, http.Handler(s.Routes())
}

// TestExt_DiscoverKG returns BFS adjacency.
func TestExt_DiscoverKG(t *testing.T) {
	g := &stubGraph{adj: map[string][]string{
		"algebra": {"linear", "quadratic"},
	}}
	_, h := newKGServer(g)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge-graph/discover?topic=algebra&depth=1", nil)
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	topics, ok := resp["topics"].([]any)
	if !ok || len(topics) != 3 {
		t.Errorf("topics = %v, want 3 entries", resp["topics"])
	}
}

// TestExt_DiscoverKG_RejectsMissingTopic returns 400.
func TestExt_DiscoverKG_RejectsMissingTopic(t *testing.T) {
	_, h := newKGServer(&stubGraph{adj: map[string][]string{}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge-graph/discover?depth=1", nil)
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestExt_DiscoverKG_DepthCapped rejects depth > 5.
func TestExt_DiscoverKG_DepthCapped(t *testing.T) {
	_, h := newKGServer(&stubGraph{adj: map[string][]string{}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/knowledge-graph/discover?topic=a&depth=999", nil)
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestExt_HeatmapEmpty returns empty heatmap when no SM-2 state.
func TestExt_HeatmapEmpty(t *testing.T) {
	_, h := newKGServer(&stubGraph{adj: map[string][]string{}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/me/knowledge-graph/heatmap", nil)
	for k, v := range extHeaders() {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["gcid"] != tGCID {
		t.Errorf("gcid = %v", resp["gcid"])
	}
}
