// kg_handler_test.go — RED-phase tests for the per-user Knowledge
// Graph HTTP handlers (S5.2 mission deliverables 1 + 4 + 5 + 7).
//
// Endpoints under test:
//
//	GET    /v1/me/knowledge-graph/clusters
//	GET    /v1/me/knowledge-graph/clusters/{cluster_id}
//	POST   /v1/me/knowledge-graph/clusters
//	DELETE /v1/me/knowledge-graph/clusters/{cluster_id}
//	GET    /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/fog
//	POST   /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/promote/{neighbor_id}
//	POST   /v1/me/knowledge-graph/junctions/{junction_id}/accept
//	POST   /v1/me/knowledge-graph/junctions/{junction_id}/reject
//
// All require X-Tenant-Id + gcid + traceparent headers.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

const (
	kgTenantID = "01970000-0000-7000-8000-000000000010"
	kgGCID     = "01970000-0000-7000-9000-000000000010"
	kgAtomA    = "01970000-0000-7000-a000-000000000010"
	kgAtomB    = "01970000-0000-7000-a000-000000000011"
	kgAtomC    = "01970000-0000-7000-a000-000000000012"
)

func newKGRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("X-Tenant-Id", kgTenantID)
	r.Header.Set("gcid", kgGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

// ---------- POST /v1/me/knowledge-graph/clusters ----------

func TestPostKGClusters_CreatesCluster(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters", map[string]string{
		"seed_topic":   "agile",
		"seed_atom_id": kgAtomA,
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	cluster, _ := resp["cluster"].(map[string]any)
	if cluster == nil {
		t.Fatalf("missing 'cluster' in response: %v", resp)
	}
	if cluster["cluster_id"] == "" {
		t.Errorf("cluster_id empty: %v", cluster)
	}
	if cluster["status"] != "active" {
		t.Errorf("status = %v, want active", cluster["status"])
	}
}

func TestPostKGClusters_RequiresHeaders(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest("POST", "/v1/me/knowledge-graph/clusters",
		bytes.NewBufferString(`{"seed_topic":"agile","seed_atom_id":"a"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPostKGClusters_RejectsCapExceeded(t *testing.T) {
	srv := NewExtServer(nil)
	// Default cap = 3 (KG_DEFAULT_MAX_CLUSTERS_PER_USER). Pre-seed 3 clusters.
	for i := 0; i < 3; i++ {
		c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID,
			"topic-"+string(rune('a'+i)),
			"01970000-0000-7000-a000-00000000010"+string(rune('0'+i)), "")
		_ = srv.KGClusters.Save(context.Background(), c)
	}
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters", map[string]string{
		"seed_topic":   "fourth",
		"seed_atom_id": kgAtomA,
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (cap)", w.Code)
	}
}

func TestPostKGClusters_RejectsBadBody(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters", map[string]string{
		"seed_topic": "",
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ---------- GET /v1/me/knowledge-graph/clusters ----------

// TestGetKGClusters_ListsCallersClusters pins the FE-compatible shape
// emitted per docs/m13/kg-fog-aplus-integration-backend-handoff-2026-05-13.md §1.1.
// Response is {data: {clusters: [...], capRemaining, capMax}} — clusters
// from other GCIDs are filtered by RLS-defence guard.
func TestGetKGClusters_ListsCallersClusters(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)
	other, _ := userknowledgegraph.NewMapCluster(kgTenantID, "other-gcid", "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), other)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("response missing data envelope; got %v", resp)
	}
	clusters, _ := data["clusters"].([]any)
	if len(clusters) != 1 {
		t.Fatalf("clusters len = %d, want 1 (other-gcid leaked?)", len(clusters))
	}
	if data["capMax"].(float64) != 3 {
		t.Errorf("capMax = %v, want 3", data["capMax"])
	}
	if data["capRemaining"].(float64) != 2 {
		t.Errorf("capRemaining = %v, want 2 (3 cap - 1 active)", data["capRemaining"])
	}
	// FE-shape camelCase assertions on the cluster card.
	first := clusters[0].(map[string]any)
	if first["clusterId"] == nil {
		t.Errorf("clusterId missing from cluster card")
	}
	if first["seedTopic"] != "agile" {
		t.Errorf("seedTopic = %v; want agile", first["seedTopic"])
	}
}

// TestGetKGClusters_EmptyState pins the FE empty-state contract per §7
// step 2: clusters: [] + capRemaining = capMax when user has never
// explored. This is the response the FE dashboard panel renders the
// seed-input empty state against.
func TestGetKGClusters_EmptyState(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp["data"].(map[string]any)
	clusters, _ := data["clusters"].([]any)
	if len(clusters) != 0 {
		t.Errorf("expected empty clusters; got %d", len(clusters))
	}
	if data["capRemaining"].(float64) != 3 {
		t.Errorf("capRemaining = %v; want 3 (full cap available)", data["capRemaining"])
	}
}

// TestGetKGClusters_FiltersArchivedClusters pins that archived/merged
// clusters are NOT returned in the active list — only ACTIVE clusters
// surface to the dashboard panel.
func TestGetKGClusters_FiltersArchivedClusters(t *testing.T) {
	srv := NewExtServer(nil)
	active, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), active)

	archived, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = archived.Archive()
	_ = srv.KGClusters.Save(context.Background(), archived)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	data := resp["data"].(map[string]any)
	clusters, _ := data["clusters"].([]any)
	if len(clusters) != 1 {
		t.Errorf("expected 1 active cluster (archived filtered); got %d", len(clusters))
	}
	if data["capRemaining"].(float64) != 2 {
		t.Errorf("capRemaining = %v; want 2 (archived doesn't count)", data["capRemaining"])
	}
}

// ---------- GET /v1/me/knowledge-graph/clusters/{cluster_id} ----------

func TestGetKGClusterByID_ReturnsDetail(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["cluster_id"] != c.ClusterID {
		t.Errorf("got cluster_id = %v", resp["cluster_id"])
	}
	if _, ok := resp["explorations"]; !ok {
		t.Error("expected 'explorations' in detail")
	}
}

func TestGetKGClusterByID_RLSCrossUserReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, "other-gcid", "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-user RLS leak)", w.Code)
	}
}

// ---------- DELETE /v1/me/knowledge-graph/clusters/{cluster_id} ----------

func TestDeleteKGCluster_ArchivesCluster(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest("DELETE", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got, _ := srv.KGClusters.Load(context.Background(), c.ClusterID)
	if got.Status != userknowledgegraph.ClusterStatusArchived {
		t.Errorf("status = %q, want archived", got.Status)
	}
}

func TestDeleteKGCluster_RLSCrossUserReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, "other-gcid", "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest("DELETE", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-user delete attempt)", w.Code)
	}
}

// ---------- GET /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/fog ----------

func TestGetKGFog_CacheHit(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, kgFakeNeighbors(kgAtomA), "run-1", "model-1")
	_ = srv.KGHexagons.Upsert(ctx, hex)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/fog", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	hexagon, _ := resp["hexagon"].(map[string]any)
	if hexagon["hex_node_id"] != hex.HexNodeID {
		t.Errorf("hex_node_id mismatch: %v", hexagon)
	}
	neighbors, _ := hexagon["neighbors"].([]any)
	if len(neighbors) != 6 {
		t.Errorf("len neighbors = %d, want 6", len(neighbors))
	}
}

func TestGetKGFog_CacheMissReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/fog", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cache miss → fog orchestrator)", w.Code)
	}
}

// ---------- POST /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/promote/{neighbor_id} ----------

func TestPostKGPromote_AdvancesFocal(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	neighbors := kgFakeNeighbors(kgAtomA)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, neighbors, "run-1", "model-1")
	_ = srv.KGHexagons.Upsert(ctx, hex)

	target := neighbors[0].AtomID
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+target, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// Exploration's focal moved.
	got, _ := srv.KGExplorations.Load(ctx, exp.ExplorationID)
	if got.CurrentFocalAtomID != target {
		t.Errorf("focal = %q, want %q", got.CurrentFocalAtomID, target)
	}
	// Trail hop appended.
	trail, _ := srv.KGExplorations.LoadTrail(ctx, exp.ExplorationID)
	if len(trail) != 1 {
		t.Fatalf("trail len = %d, want 1", len(trail))
	}
	if trail[0].FromFocalAtomID != kgAtomA || trail[0].ToFocalAtomID != target {
		t.Errorf("hop = %+v", trail[0])
	}
}

func TestPostKGPromote_RejectsNeighborNotInHex(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, kgFakeNeighbors(kgAtomA), "r", "m")
	_ = srv.KGHexagons.Upsert(ctx, hex)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+kgAtomB, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	// kgAtomB is NOT in the neighbor pool (kgFakeNeighbors uses different IDs).
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (neighbor not in hex)", w.Code)
	}
}

// ---------- POST /v1/me/knowledge-graph/junctions/{junction_id}/accept ----------

func TestPostKGJunctionAccept_MergesClusters(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/accept",
		map[string]string{"via_atom_id": kgAtomC})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	gotJ, _ := srv.KGJunctions.Load(ctx, j.JunctionID)
	if gotJ.Status != userknowledgegraph.JunctionStatusAccepted {
		t.Errorf("junction status = %q", gotJ.Status)
	}
	// One of the clusters merged into the other.
	gotA, _ := srv.KGClusters.Load(ctx, cA.ClusterID)
	gotB, _ := srv.KGClusters.Load(ctx, cB.ClusterID)
	hasMerged := gotA.Status == userknowledgegraph.ClusterStatusMergedInto ||
		gotB.Status == userknowledgegraph.ClusterStatusMergedInto
	if !hasMerged {
		t.Errorf("expected one cluster merged_into; A=%q B=%q", gotA.Status, gotB.Status)
	}
}

func TestPostKGJunctionAccept_RejectsCrossUser(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, "other-gcid", "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/accept",
		map[string]string{"via_atom_id": kgAtomA})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-user)", w.Code)
	}
}

// ---------- POST /v1/me/knowledge-graph/junctions/{junction_id}/reject ----------

func TestPostKGJunctionReject_MarksRejected(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/reject", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	got, _ := srv.KGJunctions.Load(ctx, j.JunctionID)
	if got.Status != userknowledgegraph.JunctionStatusRejected {
		t.Errorf("status = %q", got.Status)
	}
}

// ---------- additional coverage tests ----------

func TestPostKGClusters_RejectsBadJSON(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest("POST", "/v1/me/knowledge-graph/clusters",
		bytes.NewBufferString(`{invalid json`))
	r.Header.Set("X-Tenant-Id", kgTenantID)
	r.Header.Set("gcid", kgGCID)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A missing seed_atom_id no longer 400s — the handler resolves it from
// the learner's atom universe (kg_cluster_create_seed_test.go). With an
// EMPTY universe the resolver finds nothing and the honest answer is a
// 422 INVALID_SEED, never a fabricated atom.
func TestPostKGClusters_UnresolvableSeedWithEmptyUniverse422(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters", map[string]string{
		"seed_topic": "agile",
	})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 INVALID_SEED (empty universe)", w.Code)
	}
}

func TestDeleteKGCluster_IdempotentOnDoubleArchive(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = c.Archive()
	_ = srv.KGClusters.Save(context.Background(), c)

	r := newKGRequest("DELETE", "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (idempotent re-archive)", w.Code)
	}
}

func TestGetKGFog_RLSCrossUserReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, "other-gcid", "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)
	r := newKGRequest("GET",
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/fog", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-user fog)", w.Code)
	}
}

func TestPromoteFocal_ClusterNotFoundReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST",
		"/v1/me/knowledge-graph/clusters/missing/focal/"+kgAtomA+"/promote/"+kgAtomB, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cluster missing)", w.Code)
	}
}

func TestPromoteFocal_NoActiveExplorationReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(context.Background(), c)
	// No exploration saved.
	r := newKGRequest("POST",
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+kgAtomB, nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no exploration)", w.Code)
	}
}

func TestKGJunction_RejectMissingJunctionReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/missing/reject", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestKGJunction_AcceptCrossUserReturns404Reject(t *testing.T) {
	srv := NewExtServer(nil)
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, "other-gcid", "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/reject", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestKGJunction_GETMethodReturns405(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("GET", "/v1/me/knowledge-graph/junctions/whatever/accept", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestKGJunction_BadRouteReturns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/whatever/unknown-action", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestKGClusters_PutMethodReturns405(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("PUT", "/v1/me/knowledge-graph/clusters", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// kgFakeNeighbors returns 6 neighbors avoiding the focal — used in
// hexagon-cache tests.
func kgFakeNeighbors(focal string) []userknowledgegraph.HexagonNeighbor {
	pool := []string{
		"01970000-0000-7000-a000-0000000000b1",
		"01970000-0000-7000-a000-0000000000b2",
		"01970000-0000-7000-a000-0000000000b3",
		"01970000-0000-7000-a000-0000000000b4",
		"01970000-0000-7000-a000-0000000000b5",
		"01970000-0000-7000-a000-0000000000b6",
		"01970000-0000-7000-a000-0000000000b7",
	}
	out := make([]userknowledgegraph.HexagonNeighbor, 0, 6)
	for _, p := range pool {
		if p == focal {
			continue
		}
		out = append(out, userknowledgegraph.HexagonNeighbor{
			AtomID:     p,
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.5,
			FogLabel:   "neighbor",
		})
		if len(out) == 6 {
			break
		}
	}
	return out
}
