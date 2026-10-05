// kg_canvas_handler_cover_test.go — white-box tests for the deep success +
// guard branches of kg_canvas_handler.go not exercised by the existing suite:
//
//   - focal:move where the TARGET atom already has a fresh cached hexagon
//     (hits canvasBuildLayoutFromHex with a non-empty trail)
//   - focal:move on an INACTIVE cluster → 409
//   - junction decide=accept where the survivor has an active exploration +
//     cached hexagon (returns a populated layout)
//   - management endpoint enrichment with a stale (cache-miss) hexagon
//   - missing-context + bad-json guards on focal:move / decide / management
//
// Reuses kgCanvasSeed / kgCanvasSixNeighbors / newKGRequest / kg* constants.
package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// focal:move where the target neighbor ALSO has a fresh cached hexagon — the
// handler returns canvasBuildLayoutFromHex(newHex, ...) (line 382 branch) and
// canvasBuildLayoutFromHex iterates the appended trail hop.
func TestCanvasFocalMove_TargetHasCachedHex_ReturnsNewLayout(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, c, exp := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	target := kgAtomB // neighbor 0 in kgCanvasSixNeighbors

	// Seed a fresh hexagon for the TARGET atom so the post-move lookup hits.
	// The target hex's 6 neighbors MUST NOT include the target itself (focal),
	// so use a distinct neighbor pool.
	targetNeighbors := make([]userknowledgegraph.HexagonNeighbor, 6)
	pool := []string{
		"01970000-0000-7000-c000-000000000030",
		"01970000-0000-7000-c000-000000000031",
		"01970000-0000-7000-c000-000000000032",
		"01970000-0000-7000-c000-000000000033",
		"01970000-0000-7000-c000-000000000034",
		"01970000-0000-7000-c000-000000000035",
	}
	for i := range targetNeighbors {
		targetNeighbors[i] = userknowledgegraph.HexagonNeighbor{
			AtomID:     pool[i],
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.5,
			FogLabel:   "tn",
		}
	}
	targetHex, err := userknowledgegraph.NewHexagonNode(
		c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID,
		target, targetNeighbors, "00000000-0000-0000-0000-000000000002", "test-model",
	)
	if err != nil {
		t.Fatalf("new target hex: %v", err)
	}
	if err := srv.KGHexagons.Upsert(context.Background(), targetHex); err != nil {
		t.Fatalf("upsert target hex: %v", err)
	}

	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": target})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("focal:move = %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			FocalAtomID string `json:"focalAtomId"`
			Neighbors   []any  `json:"neighbors"`
			Trail       []any  `json:"trail"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.FocalAtomID != target {
		t.Errorf("focalAtomId=%q; want %q (moved)", env.Data.FocalAtomID, target)
	}
	if len(env.Data.Neighbors) != 6 {
		t.Errorf("neighbors=%d; want 6 (target hex cached)", len(env.Data.Neighbors))
	}
	if len(env.Data.Trail) != 1 {
		t.Errorf("trail=%d; want 1 hop", len(env.Data.Trail))
	}
}

func TestCanvasFocalMove_InactiveCluster_409(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, c, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	// Archive the cluster so it's inactive.
	_ = c.Archive()
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("focal:move inactive cluster = %d; want 409", w.Code)
	}
}

func TestCanvasFocalMove_ClusterNotFound(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/no-such/explorations/eid/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("focal:move unknown cluster = %d; want 404", w.Code)
	}
}

func TestCanvasFocalMove_FogNotCached_422(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, nil) // nil → NO cached hexagon
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("focal:move w/o cached hex = %d; want 422 FOG_NOT_CACHED", w.Code)
	}
}

func TestCanvasJunctionDecide_CrossUser_404(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, "other-gcid", "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "decline"})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("decide cross-user = %d; want 404", w.Code)
	}
}

func TestCanvasJunctionDecide_DeclineAlreadyRejected_409(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = j.Reject(time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "decline"})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("decline already-rejected = %d; want 409 REJECT_FAILED", w.Code)
	}
}

// decide=accept where the survivor has an active exploration but NO cached
// hexagon → returns a no-neighbors placeholder layout (line 543 miss leg).
func TestCanvasJunctionDecide_AcceptSurvivorHexMiss_Placeholder(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	// survivor=cA (both NodeCount 0): give cA an active exploration but NO hex.
	expA, _ := userknowledgegraph.NewExploration(cA.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, expA)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decide accept (hex miss) = %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			IsCacheFresh bool  `json:"isCacheFresh"`
			Neighbors    []any `json:"neighbors"`
		} `json:"data"`
	}
	_ = decodeJSON(w, &env)
	if env.Data.IsCacheFresh {
		t.Errorf("placeholder layout should have isCacheFresh=false")
	}
	if len(env.Data.Neighbors) != 0 {
		t.Errorf("placeholder layout should have 0 neighbors")
	}
}

func TestCanvasRename_BadJSON(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	req := httptest.NewRequest(http.MethodPatch, "/v1/me/knowledge-graph/clusters/"+c.ClusterID, bytes.NewBufferString("{bad"))
	req.Header.Set("X-Tenant-Id", kgTenantID)
	req.Header.Set("gcid", kgGCID)
	w := serveCanvas(srv, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("rename bad JSON = %d; want 400", w.Code)
	}
}

func TestCanvasFocalMove_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	r := httptest.NewRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("focal:move w/o context = %d; want 400", w.Code)
	}
}

func TestCanvasFocalMove_BadJSON(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	// newKGRequest with nil body sends empty buffer → json.Decode errors → 400.
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("focal:move empty body = %d; want 400 BAD_JSON", w.Code)
	}
}

func TestCanvasFocalMove_ExplorationNotFound(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+cid+"/explorations/no-such-exp/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("focal:move unknown exploration = %d; want 404", w.Code)
	}
}

// decide=accept where the survivor cluster has an active exploration + cached
// hexagon → returns a populated layout (covers the survivor-hex branch).
func TestCanvasJunctionDecide_AcceptReturnsSurvivorLayout(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()

	// Cluster A is the survivor (give it a higher node count). Seed A with an
	// active exploration + a fresh hexagon so the accept path returns a layout.
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)

	expA, _ := userknowledgegraph.NewExploration(cA.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, expA)
	hexA, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, expA.ExplorationID, kgTenantID, kgGCID, kgAtomA, kgCanvasSixNeighbors(), "00000000-0000-0000-0000-000000000003", "m")
	_ = srv.KGHexagons.Upsert(ctx, hexA)

	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decide accept = %d body=%s", w.Code, w.Body.String())
	}
	// The accepted junction must be persisted as accepted.
	gotJ, _ := srv.KGJunctions.Load(ctx, j.JunctionID)
	if gotJ.Status != userknowledgegraph.JunctionStatusAccepted {
		t.Errorf("junction status=%q; want accepted", gotJ.Status)
	}
}

// decide=decline on a pending junction → 200 with the null-data envelope
// (covers the decline branch's Reject + publish + empty-envelope return).
func TestCanvasJunctionDecide_DeclineReturnsNullEnvelope(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "decline"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decline = %d body=%s", w.Code, w.Body.String())
	}
	got, _ := srv.KGJunctions.Load(ctx, j.JunctionID)
	if got.Status != userknowledgegraph.JunctionStatusRejected {
		t.Errorf("junction status=%q; want rejected", got.Status)
	}
}

// decide=accept where the cluster chosen as `merged` is already archived →
// MergeInto errors → 409 MERGE_FAILED. Covers the merge-failure error leg.
func TestCanvasJunctionDecide_AcceptMergeConflict_409(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	// Both NodeCount=0 → survivor=cA, merged=cB. Archive cB so MergeInto fails.
	_ = cB.Archive()
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := serveKG(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("accept onto archived cluster = %d; want 409 MERGE_FAILED; body=%s", w.Code, w.Body.String())
	}
}

func TestCanvasJunctionDecide_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/jid/decide", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("decide w/o context = %d; want 400", w.Code)
	}
}

func TestCanvasJunctionDecide_WrongMethod(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/junctions/jid/decide", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET decide = %d; want 405", w.Code)
	}
}

// management endpoint where the active exploration's focal has NO fresh hexagon
// → sum.IsStale = true branch.
func TestCanvasManagement_StaleWhenNoCachedHex(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	// No hexagon cached → enrichment sets IsStale=true.

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("management = %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []struct {
			ClusterID          string `json:"clusterId"`
			CurrentFocalAtomID string `json:"currentFocalAtomId"`
			IsStale            bool   `json:"isStale"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("clusters=%d; want 1", len(env.Data))
	}
	if !env.Data[0].IsStale {
		t.Errorf("isStale=false; want true (no cached hexagon)")
	}
	if env.Data[0].CurrentFocalAtomID != kgAtomA {
		t.Errorf("currentFocalAtomId=%q; want %q", env.Data[0].CurrentFocalAtomID, kgAtomA)
	}
}

// handleCanvasHexagonGet: missing-context guard.
func TestCanvasHexagon_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest(http.MethodGet,
		"/v1/me/knowledge-graph/clusters/cid/explorations/eid/hexagon", nil)
	w := serveCanvas(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("hexagon w/o context = %d; want 400", w.Code)
	}
}

// handleCanvasHexagonGet: cluster found but exploration belongs to a different
// cluster → EXPLORATION_NOT_FOUND.
func TestCanvasHexagon_ExplorationMismatch_404(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	// Exploration under a DIFFERENT cluster id.
	otherExp, _ := userknowledgegraph.NewExploration("other-cluster", kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, otherExp)
	r := newKGRequest(http.MethodGet,
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/explorations/"+otherExp.ExplorationID+"/hexagon", nil)
	w := serveCanvas(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("hexagon exploration-mismatch = %d; want 404", w.Code)
	}
}

func serveCanvas(srv *ExtServer, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w
}

// tenant config GET missing-context guard.
func TestCanvasTenantConfigGet_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest(http.MethodGet, "/v1/tenants/"+kgTenantID+"/knowledge-graph/config", nil)
	w := serveCanvas(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("config GET w/o context = %d; want 400", w.Code)
	}
}

// tenant config PATCH where path tenant != header tenant → 404 TENANT_NOT_FOUND.
func TestCanvasTenantConfigPatch_CrossTenant_404(t *testing.T) {
	srv := NewExtServer(nil)
	// header tenant = kgTenantID, path tenant = a DIFFERENT id.
	r := newKGRequest(http.MethodPatch, "/v1/tenants/other-tenant-id/knowledge-graph/config",
		map[string]any{"maxConcurrentKgClustersPerUser": 5})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("config PATCH cross-tenant = %d; want 404 TENANT_NOT_FOUND", w.Code)
	}
}

// management with a FRESH cached hexagon → IsStale=false (the non-stale leg).
func TestCanvasManagement_FreshHexNotStale(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, kgCanvasSixNeighbors(), "r", "m")
	_ = srv.KGHexagons.Upsert(ctx, hex)

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/management", nil)
	w := serveCanvas(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("management = %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []struct {
			IsStale bool `json:"isStale"`
		} `json:"data"`
	}
	_ = decodeJSON(w, &env)
	if len(env.Data) != 1 || env.Data[0].IsStale {
		t.Errorf("isStale = true; want false (fresh hexagon)")
	}
}

// rename an ARCHIVED cluster → Rename returns ErrCannotRenameInactive → 409.
func TestCanvasRename_InactiveCluster_409(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = c.Archive()
	_ = srv.KGClusters.Save(ctx, c)
	r := newKGRequest(http.MethodPatch, "/v1/me/knowledge-graph/clusters/"+c.ClusterID,
		map[string]string{"displayName": "New"})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("rename inactive cluster = %d; want 409 CLUSTER_INACTIVE; body=%s", w.Code, w.Body.String())
	}
}

// decide=accept where cluster B has the higher node_count → survivor swaps to
// B (covers the cB.NodeCount > cA.NodeCount branch).
func TestCanvasJunctionDecide_AcceptSurvivorSwapsToHigherNodeCount(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	// Make cB larger so survivor=cB.
	cB.IncrementNodeCount()
	cB.IncrementNodeCount()
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := serveCanvas(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("decide accept = %d body=%s", w.Code, w.Body.String())
	}
	// cA (smaller) merged into cB (survivor).
	gotA, _ := srv.KGClusters.Load(ctx, cA.ClusterID)
	if gotA.Status != userknowledgegraph.ClusterStatusMergedInto {
		t.Errorf("cA status = %q; want merged_into (cB is the larger survivor)", gotA.Status)
	}
}

// management skips an exploration whose status is NOT active (line 590 skip
// branch) — seed an active cluster with only a NON-active exploration.
func TestCanvasManagement_SkipsNonActiveExploration(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = exp.Archive() // make it non-active so the management loop skips it
	_ = srv.KGExplorations.Save(ctx, exp)

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/management", nil)
	w := serveCanvas(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("management = %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data []struct {
			CurrentFocalAtomID string `json:"currentFocalAtomId"`
		} `json:"data"`
	}
	_ = decodeJSON(w, &env)
	if len(env.Data) != 1 {
		t.Fatalf("clusters=%d; want 1", len(env.Data))
	}
	// No active exploration → currentFocalAtomId stays empty.
	if env.Data[0].CurrentFocalAtomID != "" {
		t.Errorf("currentFocalAtomId = %q; want empty (non-active exploration skipped)", env.Data[0].CurrentFocalAtomID)
	}
}

func TestCanvasManagement_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("management w/o context = %d; want 400", w.Code)
	}
}

func TestCanvasRename_MissingContext(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest(http.MethodPatch, "/v1/me/knowledge-graph/clusters/cid", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("rename w/o context = %d; want 400", w.Code)
	}
}

func TestCanvasRename_NotFound(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest(http.MethodPatch, "/v1/me/knowledge-graph/clusters/no-such",
		map[string]string{"displayName": "X"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("rename missing cluster = %d; want 404", w.Code)
	}
}
