// kg_handler_cover_test.go — white-box tests for the remaining branch/error
// paths in kg_handler.go: dispatcher routing (method-not-allowed, missing id,
// unknown subroute), missing-context guards, the toKGClusterFEDTO enrichment
// path (active exploration + cached hexagon), and junction-accept merge.
//
// Reuses newKGRequest / NewExtServer / kgFakeNeighbors and the kg* constants
// from kg_handler_test.go (same package).
package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

func newKGSrv() *ExtServer { return NewExtServer(nil) }

func serveKG(srv *ExtServer, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	return w
}

// ---------------------------------------------------------------------------
// handleKGClusters dispatcher
// ---------------------------------------------------------------------------

func TestKGClusters_ListMissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("list w/o context = %d; want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// toKGClusterFEDTO enrichment — exercise the active-exploration + cached-hex
// branches via the list endpoint.
// ---------------------------------------------------------------------------

func TestKGClusters_List_EnrichesFromExplorationAndHexagon(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err := srv.KGClusters.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	if err := srv.KGExplorations.Save(ctx, exp); err != nil {
		t.Fatal(err)
	}
	neighbors := kgFakeNeighbors(kgAtomA)
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, neighbors, "run-1", "model-1")
	if err := srv.KGHexagons.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Clusters []struct {
				ClusterID          string `json:"clusterId"`
				CurrentFocalAtomID string `json:"currentFocalAtomId"`
				NeighborCount      int    `json:"neighborCount"`
				IsStale            bool   `json:"isStale"`
			} `json:"clusters"`
			CapRemaining int `json:"capRemaining"`
			CapMax       int `json:"capMax"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Clusters) != 1 {
		t.Fatalf("clusters=%d; want 1", len(resp.Data.Clusters))
	}
	card := resp.Data.Clusters[0]
	if card.CurrentFocalAtomID != kgAtomA {
		t.Errorf("currentFocalAtomId=%q; want enriched %q", card.CurrentFocalAtomID, kgAtomA)
	}
	if card.NeighborCount != 6 {
		t.Errorf("neighborCount=%d; want 6 (enriched from fresh hexagon)", card.NeighborCount)
	}
	if card.IsStale {
		t.Errorf("isStale=true; want false (hexagon is fresh)")
	}
	// cap: default max 3, one active → remaining 2.
	if card.ClusterID != c.ClusterID {
		t.Errorf("clusterId=%q; want %q", card.ClusterID, c.ClusterID)
	}
	if resp.Data.CapMax != 3 || resp.Data.CapRemaining != 2 {
		t.Errorf("cap = max %d / remaining %d; want 3 / 2", resp.Data.CapMax, resp.Data.CapRemaining)
	}
}

// toKGClusterFEDTO must surface the ACTIVE exploration's id (activeExplorationId)
// and resolve the focal atom's human title (currentFocalTitle) from the IN-DOMAIN
// atom_index projection — no cross-DB read to chora_creation.
func TestKGClusters_List_PopulatesActiveExplorationAndFocalTitle(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err := srv.KGClusters.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	if err := srv.KGExplorations.Save(ctx, exp); err != nil {
		t.Fatal(err)
	}
	// Seed the in-domain atom_index projection so the focal title resolves
	// without any cross-DB dependency (the same projection the recommender
	// RAG path reads).
	if err := srv.AtomIndex.Save(ctx, &atom_index.AtomIndex{
		AtomID:      kgAtomA,
		TenantID:    kgTenantID,
		Title:       "Sprint Planning Basics",
		AtomType:    "mcq",
		PublishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Clusters []struct {
				ActiveExplorationID string `json:"activeExplorationId"`
				CurrentFocalAtomID  string `json:"currentFocalAtomId"`
				CurrentFocalTitle   string `json:"currentFocalTitle"`
			} `json:"clusters"`
		} `json:"data"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Clusters) != 1 {
		t.Fatalf("clusters=%d; want 1", len(resp.Data.Clusters))
	}
	card := resp.Data.Clusters[0]
	if card.ActiveExplorationID != exp.ExplorationID {
		t.Errorf("activeExplorationId=%q; want active exploration %q", card.ActiveExplorationID, exp.ExplorationID)
	}
	if card.CurrentFocalAtomID != kgAtomA {
		t.Errorf("currentFocalAtomId=%q; want %q", card.CurrentFocalAtomID, kgAtomA)
	}
	if card.CurrentFocalTitle != "Sprint Planning Basics" {
		t.Errorf("currentFocalTitle=%q; want %q (resolved from atom_index)", card.CurrentFocalTitle, "Sprint Planning Basics")
	}
}

// ---------------------------------------------------------------------------
// handleKGClusterByID dispatcher branches
// ---------------------------------------------------------------------------

func TestKGClusterByID_MissingClusterID(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/", nil)
	w := serveKG(srv, r)
	// The collection route owns the no-trailing-id case (200 list), so a bare
	// trailing slash should not 500; the by-id MISSING path is hit when the
	// canvas subpath dispatcher forwards. Assert no 5xx.
	if w.Code >= 500 {
		t.Errorf("trailing-slash list = %d; want <500", w.Code)
	}
}

// PATCH /clusters/{cid} is owned by the canvas dispatcher → rename (NOT a 405).
// A valid rename returns 204 No Content. Characterizes the real route wiring.
func TestKGClusterByID_PatchRenames(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	r := newKGRequest(http.MethodPatch, "/v1/me/knowledge-graph/clusters/"+c.ClusterID,
		map[string]string{"displayName": "Renamed Cluster"})
	w := serveKG(srv, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH rename = %d; want 204; body=%s", w.Code, w.Body.String())
	}
	got, _ := srv.KGClusters.Load(ctx, c.ClusterID)
	if got.DisplayName != "Renamed Cluster" {
		t.Errorf("displayName = %q; want Renamed Cluster", got.DisplayName)
	}
}

// A bare-cluster-id request with a method that is neither GET nor DELETE
// (and not PATCH→rename) hits the inner-switch default → 405.
func TestKGClusterByID_BarePut_405(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	// PUT is not PATCH (canvas rename) → delegates to handleKGClusterByID →
	// inner GET/DELETE switch default → 405.
	r := newKGRequest(http.MethodPut, "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT bare cluster = %d; want 405", w.Code)
	}
}

// getKGClusterByID with a cluster that HAS an exploration exercises the
// exploration-list loop (line 512) in the detail response.
func TestKGClusterByID_GetWithExploration(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/"+c.ClusterID, nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("get detail = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Explorations []map[string]any `json:"explorations"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Explorations) != 1 {
		t.Errorf("explorations = %d; want 1", len(resp.Explorations))
	}
}

// listKGClusters when the active cluster count EXCEEDS the cap clamps
// capRemaining to 0 (line 302 branch). Seed 4 active clusters directly
// (bypassing the create-cap) so active(4) > capMax(3).
func TestKGClusters_List_CapRemainingClampedToZero(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID,
			"topic", "01970000-0000-7000-a000-00000000020"+string(rune('0'+i)), "")
		_ = srv.KGClusters.Save(ctx, c)
	}
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			CapRemaining int `json:"capRemaining"`
		} `json:"data"`
	}
	_ = decodeJSON(w, &resp)
	if resp.Data.CapRemaining != 0 {
		t.Errorf("capRemaining = %d; want 0 (clamped when active > cap)", resp.Data.CapRemaining)
	}
}

// toKGClusterFEDTO with an active exploration but NO cached hexagon →
// IsStale=true (the cache-miss else-branch at line 347).
func TestKGClusters_List_StaleWhenHexMissing(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	// No hexagon → FindFresh errors → IsStale=true.
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters", nil)
	w := serveKG(srv, r)
	var resp struct {
		Data struct {
			Clusters []struct {
				IsStale bool `json:"isStale"`
			} `json:"clusters"`
		} `json:"data"`
	}
	_ = decodeJSON(w, &resp)
	if len(resp.Data.Clusters) != 1 || !resp.Data.Clusters[0].IsStale {
		t.Errorf("isStale = false; want true (no cached hexagon)")
	}
}

func TestKGClusterByID_GetMissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/some-id", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("get by-id w/o context = %d; want 400", w.Code)
	}
}

func TestKGClusterByID_GetNotFound(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/no-such-cluster", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing cluster = %d; want 404", w.Code)
	}
}

func TestKGClusterByID_UnknownDeepRoute(t *testing.T) {
	srv := newKGSrv()
	// .../{cid}/foo/bar with no recognized verb → 404.
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/cid/foo/bar", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown deep route = %d; want 404", w.Code)
	}
}

func TestKGClusterByID_FogWrongMethod(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/cid/focal/aid/fog", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST fog = %d; want 405", w.Code)
	}
}

func TestKGClusterByID_PromoteWrongMethod(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/cid/focal/aid/promote/nid", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET promote = %d; want 405", w.Code)
	}
}

// ---------------------------------------------------------------------------
// archiveKGCluster / getKGFog / promoteFocal missing-context + guards
// ---------------------------------------------------------------------------

func TestKGArchive_MissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodDelete, "/v1/me/knowledge-graph/clusters/cid", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("archive w/o context = %d; want 400", w.Code)
	}
}

func TestKGArchive_NotFound(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodDelete, "/v1/me/knowledge-graph/clusters/no-such", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("archive missing = %d; want 404", w.Code)
	}
}

func TestKGFog_MissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/cid/focal/aid/fog", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("fog w/o context = %d; want 400", w.Code)
	}
}

func TestKGPromote_MissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/cid/focal/aid/promote/nid", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("promote w/o context = %d; want 400", w.Code)
	}
}

// promoteFocal on an archived (inactive) cluster → 409 CLUSTER_INACTIVE.
func TestKGPromote_InactiveCluster_409(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = c.Archive()
	_ = srv.KGClusters.Save(ctx, c)
	r := newKGRequest(http.MethodPost,
		"/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+kgAtomB, nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("promote on inactive cluster = %d; want 409 CLUSTER_INACTIVE", w.Code)
	}
}

// getKGFog surfaces JunctionOpportunity entries for neighbors flagged
// IsJunction (covers the junction-loop branch in getKGFog).
func TestKGFog_SurfacesJunctionOpportunities(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	neighbors := kgFakeNeighbors(kgAtomA)
	neighbors[0].IsJunction = true // flag the first neighbor as a junction opportunity
	hex, _ := userknowledgegraph.NewHexagonNode(c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID, kgAtomA, neighbors, "r", "m")
	_ = srv.KGHexagons.Upsert(ctx, hex)

	r := newKGRequest(http.MethodGet, "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/fog", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusOK {
		t.Fatalf("fog = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Junctions []map[string]any `json:"junctions"`
	}
	if err := decodeJSON(w, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Junctions) != 1 {
		t.Errorf("junctions = %d; want 1 (one IsJunction neighbor)", len(resp.Junctions))
	}
}

func TestKGPromote_FogNotCached(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	_ = srv.KGClusters.Save(ctx, c)
	exp, _ := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, kgAtomA)
	_ = srv.KGExplorations.Save(ctx, exp)
	// No hexagon cached → FOG_NOT_CACHED 422.
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/clusters/"+c.ClusterID+"/focal/"+kgAtomA+"/promote/"+kgAtomB, nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("promote w/o cached hex = %d; want 422", w.Code)
	}
}

// ---------------------------------------------------------------------------
// junction dispatcher branches
// ---------------------------------------------------------------------------

func TestKGJunction_MissingID(t *testing.T) {
	srv := newKGSrv()
	// "/junctions/" → SplitN yields 1 part → MISSING_JUNCTION_ID 400.
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing junction id = %d; want 400", w.Code)
	}
}

func TestKGJunction_AcceptMissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/jid/accept", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("accept w/o context = %d; want 400", w.Code)
	}
}

func TestKGJunction_AcceptNotFound(t *testing.T) {
	srv := newKGSrv()
	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/no-such/accept",
		map[string]string{"via_atom_id": kgAtomA})
	w := serveKG(srv, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("accept missing junction = %d; want 404", w.Code)
	}
}

func TestKGJunction_AcceptBadJSON(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/accept", nil)
	// newKGRequest with nil body sends an empty buffer → json.Decode errors → 400.
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("accept w/ empty body = %d; want 400 BAD_JSON; body=%s", w.Code, w.Body.String())
	}
}

// acceptKGJunction where the `merged` cluster is already archived → MergeInto
// fails → 409 MERGE_FAILED. Covers the merge-failure leg of the legacy accept.
func TestKGJunction_AcceptMergeConflict_409(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = cB.Archive() // merged=cB (both NodeCount 0) → MergeInto errors
	_ = srv.KGClusters.Save(ctx, cA)
	_ = srv.KGClusters.Save(ctx, cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomC}, time.Now().UTC())
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/accept",
		map[string]string{"via_atom_id": kgAtomC})
	w := serveKG(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("accept onto archived cluster = %d; want 409 MERGE_FAILED; body=%s", w.Code, w.Body.String())
	}
}

func TestKGJunction_RejectMissingContext(t *testing.T) {
	srv := newKGSrv()
	r := httptest.NewRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/jid/reject", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("reject w/o context = %d; want 400", w.Code)
	}
}

func TestKGJunction_RejectAlreadyResolvedConflict(t *testing.T) {
	srv := newKGSrv()
	ctx := context.Background()
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, "ca", "cb", []string{kgAtomA}, time.Now().UTC())
	_ = j.Reject(time.Now().UTC()) // pre-reject so the handler's Reject errors
	_ = srv.KGJunctions.Save(ctx, j)

	r := newKGRequest(http.MethodPost, "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/reject", nil)
	w := serveKG(srv, r)
	if w.Code != http.StatusConflict {
		t.Errorf("reject already-rejected = %d; want 409 REJECT_FAILED; body=%s", w.Code, w.Body.String())
	}
}
