// kg_canvas_handler_test.go — RED-phase tests for the FE-facing
// KG canvas endpoints landed under E2E-BE-KG-CANVAS (2026-05-16).
//
// Endpoints under test (all per chora-web's KgFogService — see
// chora-web/src/app/features/surfaces/aplus/dashboard/kg-fog/kg-fog.service.ts):
//
//	GET    /v1/me/knowledge-graph/clusters/{clusterId}/explorations/{explorationId}/hexagon
//	POST   /v1/me/knowledge-graph/clusters/{clusterId}/explorations/{explorationId}/focal:move
//	POST   /v1/me/knowledge-graph/clusters/{clusterId}/archive
//	POST   /v1/me/knowledge-graph/junctions/{junctionId}/decide
//	GET    /v1/me/knowledge-graph/clusters/management
//	PATCH  /v1/me/knowledge-graph/clusters/{clusterId}
//	GET    /v1/tenants/{tenantId}/knowledge-graph/config
//	PATCH  /v1/tenants/{tenantId}/knowledge-graph/config
//
// All endpoints emit camelCase fields wrapped in `{ "data": ... }`
// envelope per chora-gateway BFF convention. All require X-Tenant-Id +
// gcid + traceparent headers.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// kgCanvasSixNeighbors returns 6 distinct test neighbors (the canonical
// HexagonNeighborCount). neighbor 0 = kgAtomB, neighbor 1 = kgAtomC, and
// 4 synthetic UUIDs for slots 2-5.
func kgCanvasSixNeighbors() []userknowledgegraph.HexagonNeighbor {
	atoms := []string{
		kgAtomB,
		kgAtomC,
		"01970000-0000-7000-a000-000000000020",
		"01970000-0000-7000-a000-000000000021",
		"01970000-0000-7000-a000-000000000022",
		"01970000-0000-7000-a000-000000000023",
	}
	confidences := []float32{0.85, 0.4, 0.65, 0.55, 0.3, 0.75}
	relations := []userknowledgegraph.NeighborRelation{
		userknowledgegraph.NeighborRelationExtends,
		userknowledgegraph.NeighborRelationPrerequisiteOf,
		userknowledgegraph.NeighborRelationAnalogyOf,
		userknowledgegraph.NeighborRelationContrastsWith,
		userknowledgegraph.NeighborRelationAppliedIn,
		userknowledgegraph.NeighborRelationCuriosityJump,
	}
	out := make([]userknowledgegraph.HexagonNeighbor, 6)
	for i := 0; i < 6; i++ {
		out[i] = userknowledgegraph.HexagonNeighbor{
			AtomID:     atoms[i],
			Relation:   relations[i],
			Confidence: confidences[i],
			FogLabel:   fmt.Sprintf("neighbor-%d", i),
		}
	}
	return out
}

// kgCanvasSeed creates a deterministic cluster + exploration + cached
// hexagon for the canvas tests. Returns clusterID + explorationID for
// the test to plug into the request URLs.
//
// When neighbors is non-nil it MUST contain exactly 6 entries
// (HexagonNeighborCount). Use kgCanvasSixNeighbors() for the canonical seed.
func kgCanvasSeed(t *testing.T, srv *ExtServer, neighbors []userknowledgegraph.HexagonNeighbor) (string, string, *userknowledgegraph.MapCluster, *userknowledgegraph.Exploration) {
	t.Helper()
	c, err := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	if err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if err := srv.KGClusters.Save(context.Background(), c); err != nil {
		t.Fatalf("save cluster: %v", err)
	}
	exp, err := userknowledgegraph.NewExploration(c.ClusterID, kgTenantID, kgGCID, c.SeedAtomID)
	if err != nil {
		t.Fatalf("seed exploration: %v", err)
	}
	if err := srv.KGExplorations.Save(context.Background(), exp); err != nil {
		t.Fatalf("save exploration: %v", err)
	}
	if neighbors != nil {
		hex, err := userknowledgegraph.NewHexagonNode(
			c.ClusterID, exp.ExplorationID, kgTenantID, kgGCID,
			c.SeedAtomID, neighbors, "00000000-0000-0000-0000-000000000001", "test-model",
		)
		if err != nil {
			t.Fatalf("new hex: %v", err)
		}
		if err := srv.KGHexagons.Upsert(context.Background(), hex); err != nil {
			t.Fatalf("upsert hex: %v", err)
		}
	}
	return c.ClusterID, exp.ExplorationID, c, exp
}

// =============================================================================
// #1 — GET /v1/me/knowledge-graph/clusters/{cid}/explorations/{eid}/hexagon
// =============================================================================

func TestCanvasHexagon_ReturnsCamelCaseEnvelope(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected `data` envelope: %v", env)
	}
	if data["clusterId"] != cid {
		t.Errorf("clusterId = %v, want %s", data["clusterId"], cid)
	}
	if data["explorationId"] != eid {
		t.Errorf("explorationId = %v, want %s", data["explorationId"], eid)
	}
	if data["focalAtomId"] != kgAtomA {
		t.Errorf("focalAtomId = %v, want %s", data["focalAtomId"], kgAtomA)
	}
	neighborsArr, ok := data["neighbors"].([]any)
	if !ok || len(neighborsArr) != 6 {
		t.Fatalf("neighbors = %v (want 6)", data["neighbors"])
	}
	n0 := neighborsArr[0].(map[string]any)
	if n0["atomId"] != kgAtomB {
		t.Errorf("neighbors[0].atomId = %v", n0["atomId"])
	}
	// Confidence must be bucketed to 'low' | 'med' | 'high' per FE model.
	if conf, _ := n0["confidence"].(string); conf != "high" {
		t.Errorf("confidence bucket = %v, want high (0.85)", conf)
	}
	// Each neighbor must have a position in {N, NE, SE, S, SW, NW}.
	seenPositions := make(map[string]bool)
	validPositions := map[string]bool{"N": true, "NE": true, "SE": true, "S": true, "SW": true, "NW": true}
	for i, ni := range neighborsArr {
		nm := ni.(map[string]any)
		pos, _ := nm["position"].(string)
		if !validPositions[pos] {
			t.Errorf("neighbors[%d].position = %q, not in N/NE/SE/S/SW/NW", i, pos)
		}
		if seenPositions[pos] {
			t.Errorf("duplicate position %q at index %d", pos, i)
		}
		seenPositions[pos] = true
	}
}

// A cold cache no longer 404s — it triggers on-demand fog generation
// (see kg_canvas_fog_gen_test.go). With NO FogOrchestrator wired the
// honest answer is a loud 503, never a silent demo fallback.
// ADR-254 D13: the hexagon fog GENERATION lane is retired; a cold cache is
// answered 410 KG_FOG_GENERATION_RETIRED, never regenerated (the hexagon
// backend stays for cached reads only).
func TestCanvasHexagon_CacheMiss_FailsLoudRetired(t *testing.T) {
	srv := NewExtServer(nil)
	// Seed cluster + exploration but NO hexagon (and no fog client).
	cid, eid, _, _ := kgCanvasSeed(t, srv, nil)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d body=%s, want 410 KG_FOG_GENERATION_RETIRED", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "KG_FOG_GENERATION_RETIRED") {
		t.Fatalf("body = %s, want code KG_FOG_GENERATION_RETIRED", w.Body.String())
	}
}

func TestCanvasHexagon_RequiresHeaders(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, nil)
	r := httptest.NewRequest("GET", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCanvasHexagon_TenantMismatch_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/hexagon", nil)
	// Tamper tenant.
	r.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-deadbeefbeef")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d body=%s, want 404 (RLS leak guard)", w.Code, w.Body.String())
	}
}

// =============================================================================
// #2 — POST .../focal:move
// =============================================================================

func TestCanvasFocalMove_AdvancesFocal(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())

	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": kgAtomB})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected `data` envelope: %v", env)
	}
	if data["focalAtomId"] != kgAtomB {
		t.Errorf("focalAtomId after move = %v, want %s", data["focalAtomId"], kgAtomB)
	}
}

func TestCanvasFocalMove_RejectsNeighborNotInHex(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{"targetAtomId": "01970000-0000-7000-a000-aaaaaaaaaaaa"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

func TestCanvasFocalMove_RequiresTargetAtomID(t *testing.T) {
	srv := NewExtServer(nil)
	cid, eid, _, _ := kgCanvasSeed(t, srv, kgCanvasSixNeighbors())
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/explorations/"+eid+"/focal:move",
		map[string]string{})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 400 or 422", w.Code)
	}
}

// =============================================================================
// #3 — POST .../clusters/{cid}/archive
// =============================================================================

func TestCanvasArchive_Returns204(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want 204", w.Code, w.Body.String())
	}
	// Verify cluster transitioned to archived.
	c, _ := srv.KGClusters.Load(context.Background(), cid)
	if c.Status != userknowledgegraph.ClusterStatusArchived {
		t.Errorf("status post-archive = %q, want archived", c.Status)
	}
}

func TestCanvasArchive_DoubleArchiveIsIdempotent(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("first archive: status = %d", w.Code)
	}
	// Second call must NOT 5xx.
	r2 := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, r2)
	if w2.Code != http.StatusNoContent && w2.Code != http.StatusOK {
		t.Errorf("second archive status = %d, want 204 or 200 (idempotent)", w2.Code)
	}
}

func TestCanvasArchive_TenantMismatch_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	r.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-deadbeefbeef")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// =============================================================================
// #4 — POST /v1/me/knowledge-graph/junctions/{jid}/decide
// =============================================================================

func TestCanvasJunctionDecide_AcceptMerges(t *testing.T) {
	srv := NewExtServer(nil)
	// Seed two clusters + a pending junction.
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), cA)
	_ = srv.KGClusters.Save(context.Background(), cB)
	j, err := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomA}, time.Now().UTC())
	if err != nil {
		t.Fatalf("new junction: %v", err)
	}
	_ = srv.KGJunctions.Save(context.Background(), j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "accept"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	jLoaded, _ := srv.KGJunctions.Load(context.Background(), j.JunctionID)
	if jLoaded.Status != userknowledgegraph.JunctionStatusAccepted {
		t.Errorf("status post-accept = %q, want accepted", jLoaded.Status)
	}
}

func TestCanvasJunctionDecide_DeclineRejects(t *testing.T) {
	srv := NewExtServer(nil)
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), cA)
	_ = srv.KGClusters.Save(context.Background(), cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(context.Background(), j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "decline"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	jLoaded, _ := srv.KGJunctions.Load(context.Background(), j.JunctionID)
	if jLoaded.Status != userknowledgegraph.JunctionStatusRejected {
		t.Errorf("status post-decline = %q, want rejected", jLoaded.Status)
	}
}

func TestCanvasJunctionDecide_RejectsUnknownDecision(t *testing.T) {
	srv := NewExtServer(nil)
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), cA)
	_ = srv.KGClusters.Save(context.Background(), cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(context.Background(), j)

	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		map[string]string{"decision": "maybe"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 422 or 400", w.Code)
	}
}

// =============================================================================
// #5 — GET /v1/me/knowledge-graph/clusters/management
// =============================================================================

func TestCanvasManagement_ListsClusters(t *testing.T) {
	srv := NewExtServer(nil)
	// Seed two clusters.
	c1, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "Agile Map")
	c2, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), c1)
	_ = srv.KGClusters.Save(context.Background(), c2)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	if err := json.NewDecoder(w.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	items, ok := env["data"].([]any)
	if !ok {
		t.Fatalf("expected `data` array: %v", env)
	}
	if len(items) != 2 {
		t.Errorf("items = %d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["clusterId"] == "" {
		t.Errorf("clusterId missing: %v", first)
	}
	if first["displayName"] == "" {
		t.Errorf("displayName missing: %v", first)
	}
	if first["seedTopic"] == "" {
		t.Errorf("seedTopic missing: %v", first)
	}
}

func TestCanvasManagement_OmitsArchived(t *testing.T) {
	srv := NewExtServer(nil)
	c1, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	c2, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = c2.Archive()
	_ = srv.KGClusters.Save(context.Background(), c1)
	_ = srv.KGClusters.Save(context.Background(), c2)

	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var env map[string]any
	_ = json.NewDecoder(w.Body).Decode(&env)
	items, _ := env["data"].([]any)
	if len(items) != 1 {
		t.Errorf("items = %d, want 1 (archived hidden)", len(items))
	}
}

// =============================================================================
// #6 — PATCH /v1/me/knowledge-graph/clusters/{cid} (rename)
// =============================================================================

func TestCanvasRename_Returns204(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)

	r := newKGRequest("PATCH", "/v1/me/knowledge-graph/clusters/"+cid,
		map[string]string{"displayName": "My Agile Map"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want 204", w.Code, w.Body.String())
	}
	c, _ := srv.KGClusters.Load(context.Background(), cid)
	if c.DisplayName != "My Agile Map" {
		t.Errorf("displayName = %q, want %q", c.DisplayName, "My Agile Map")
	}
}

func TestCanvasRename_RejectsEmpty(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest("PATCH", "/v1/me/knowledge-graph/clusters/"+cid,
		map[string]string{"displayName": ""})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 422 or 400", w.Code)
	}
}

func TestCanvasRename_RejectsTooLong(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	longName := strings.Repeat("x", 200)
	r := newKGRequest("PATCH", "/v1/me/knowledge-graph/clusters/"+cid,
		map[string]string{"displayName": longName})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 422 or 400", w.Code)
	}
}

func TestCanvasRename_TenantMismatch_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest("PATCH", "/v1/me/knowledge-graph/clusters/"+cid,
		map[string]string{"displayName": "Other"})
	r.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-deadbeefbeef")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// =============================================================================
// #7 — GET /v1/tenants/{tid}/knowledge-graph/config
// =============================================================================

func TestCanvasTenantConfig_GetDefault(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("GET", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	_ = json.NewDecoder(w.Body).Decode(&env)
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data envelope: %v", env)
	}
	// Defaults per ADR-143 §8.
	if v, _ := data["maxConcurrentKgClustersPerUser"].(float64); int(v) != 3 {
		t.Errorf("maxConcurrentKgClustersPerUser = %v, want 3", data["maxConcurrentKgClustersPerUser"])
	}
	if v, _ := data["kgFogInvalidationGraceSeconds"].(float64); int(v) != 300 {
		t.Errorf("kgFogInvalidationGraceSeconds = %v, want 300", data["kgFogInvalidationGraceSeconds"])
	}
	if data["updatedAt"] == "" {
		t.Errorf("updatedAt missing")
	}
}

func TestCanvasTenantConfig_TenantPathScopedToHeader(t *testing.T) {
	srv := NewExtServer(nil)
	// Path tenant differs from header tenant — must 404 to avoid info leak.
	r := newKGRequest("GET", "/v1/tenants/01970000-0000-7000-8000-99999999/knowledge-graph/config", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 404/403 (path tenant must match header)", w.Code)
	}
}

// =============================================================================
// #8 — PATCH /v1/tenants/{tid}/knowledge-graph/config
// =============================================================================

func TestCanvasTenantConfig_Patch(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("PATCH", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config",
		map[string]int{"maxConcurrentKgClustersPerUser": 5, "kgFogInvalidationGraceSeconds": 600})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	_ = json.NewDecoder(w.Body).Decode(&env)
	data, _ := env["data"].(map[string]any)
	if v, _ := data["maxConcurrentKgClustersPerUser"].(float64); int(v) != 5 {
		t.Errorf("maxConcurrentKgClustersPerUser = %v, want 5", data["maxConcurrentKgClustersPerUser"])
	}
	if v, _ := data["kgFogInvalidationGraceSeconds"].(float64); int(v) != 600 {
		t.Errorf("kgFogInvalidationGraceSeconds = %v, want 600", data["kgFogInvalidationGraceSeconds"])
	}

	// Subsequent GET returns the updated values.
	r2 := newKGRequest("GET", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config", nil)
	w2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w2, r2)
	var env2 map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&env2)
	data2, _ := env2["data"].(map[string]any)
	if v, _ := data2["maxConcurrentKgClustersPerUser"].(float64); int(v) != 5 {
		t.Errorf("GET after PATCH maxConcurrent = %v, want 5", v)
	}
}

func TestCanvasTenantConfig_PatchRejectsOutOfBounds(t *testing.T) {
	srv := NewExtServer(nil)
	// Max is 10 per ADR-143 §8.
	r := newKGRequest("PATCH", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config",
		map[string]int{"maxConcurrentKgClustersPerUser": 50})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity && w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 422 or 400", w.Code)
	}
}

func TestCanvasTenantConfig_PatchPartial(t *testing.T) {
	srv := NewExtServer(nil)
	// Use a fresh tenant ID — the canvas tenant config store is a
	// process-level singleton (see kg_canvas_handler.go) so reusing
	// kgTenantID across tests would leak state. Tests are explicit about
	// their own tenant scope.
	freshTenant := "01970000-0000-7000-8000-0000000000aa"
	freshGCID := "01970000-0000-7000-9000-0000000000aa"
	body, _ := json.Marshal(map[string]int{"kgFogInvalidationGraceSeconds": 1200})
	r := httptest.NewRequest("PATCH", "/v1/tenants/"+freshTenant+"/knowledge-graph/config",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", freshTenant)
	r.Header.Set("gcid", freshGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var env map[string]any
	_ = json.NewDecoder(w.Body).Decode(&env)
	data, _ := env["data"].(map[string]any)
	if v, _ := data["maxConcurrentKgClustersPerUser"].(float64); int(v) != 3 {
		t.Errorf("partial PATCH preserved maxConcurrent = %v, want 3 default", v)
	}
	if v, _ := data["kgFogInvalidationGraceSeconds"].(float64); int(v) != 1200 {
		t.Errorf("partial PATCH grace = %v, want 1200", v)
	}
}

// =============================================================================
// Additional edge cases — handler error paths.
// =============================================================================

// TestCanvasHexagon_UnknownCluster_Returns404 confirms a non-existent
// clusterID short-circuits with 404 before exploration lookup.
func TestCanvasHexagon_UnknownCluster_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/019e30b5-0000-0000-0000-000000000000/explorations/019e30b5-0000-0000-0000-000000000001/hexagon", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestCanvasArchive_UnknownCluster_Returns404 confirms a non-existent
// clusterID short-circuits with 404.
func TestCanvasArchive_UnknownCluster_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/019e30b5-0000-0000-0000-000000000000/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestCanvasJunctionDecide_UnknownJunction_Returns404.
func TestCanvasJunctionDecide_UnknownJunction_Returns404(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/junctions/019e30b5-0000-0000-0000-000000000099/decide",
		map[string]string{"decision": "accept"})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestCanvasJunctionDecide_BadJSON_Returns400.
func TestCanvasJunctionDecide_BadJSON_Returns400(t *testing.T) {
	srv := NewExtServer(nil)
	cA, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "agile", kgAtomA, "")
	cB, _ := userknowledgegraph.NewMapCluster(kgTenantID, kgGCID, "scrum", kgAtomB, "")
	_ = srv.KGClusters.Save(context.Background(), cA)
	_ = srv.KGClusters.Save(context.Background(), cB)
	j, _ := userknowledgegraph.NewJunction(kgTenantID, kgGCID, cA.ClusterID, cB.ClusterID, []string{kgAtomA}, time.Now().UTC())
	_ = srv.KGJunctions.Save(context.Background(), j)
	r := httptest.NewRequest("POST", "/v1/me/knowledge-graph/junctions/"+j.JunctionID+"/decide",
		strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", kgTenantID)
	r.Header.Set("gcid", kgGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestCanvasManagement_RequiresHeaders confirms 400 without auth context.
func TestCanvasManagement_RequiresHeaders(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest("GET", "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestCanvasTenantConfig_BadJSON_Returns400.
func TestCanvasTenantConfig_BadJSON_Returns400(t *testing.T) {
	srv := NewExtServer(nil)
	r := httptest.NewRequest("PATCH", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config",
		strings.NewReader("not-json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", kgTenantID)
	r.Header.Set("gcid", kgGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestCanvasTenantConfig_PatchGraceOutOfBounds.
func TestCanvasTenantConfig_PatchGraceOutOfBounds(t *testing.T) {
	srv := NewExtServer(nil)
	freshTenant := "01970000-0000-7000-8000-0000000000bb"
	freshGCID := "01970000-0000-7000-9000-0000000000bb"
	body, _ := json.Marshal(map[string]int{"kgFogInvalidationGraceSeconds": 50}) // below 60 bound
	r := httptest.NewRequest("PATCH", "/v1/tenants/"+freshTenant+"/knowledge-graph/config",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Tenant-Id", freshTenant)
	r.Header.Set("gcid", freshGCID)
	r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

// TestCanvasTenantSubpath_MethodNotAllowed confirms PUT etc. are 405.
func TestCanvasTenantSubpath_MethodNotAllowed(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("PUT", "/v1/tenants/"+kgTenantID+"/knowledge-graph/config", map[string]int{"x": 1})
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestCanvasTenantSubpath_NotFoundPath confirms unknown paths under
// /v1/tenants/.../knowledge-graph return 404.
func TestCanvasTenantSubpath_NotFoundPath(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("GET", "/v1/tenants/"+kgTenantID+"/knowledge-graph/other", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestCanvasClusterMgmtRoute_MethodNotAllowed.
func TestCanvasClusterMgmtRoute_MethodNotAllowed(t *testing.T) {
	srv := NewExtServer(nil)
	r := newKGRequest("POST", "/v1/me/knowledge-graph/clusters/management", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// TestCanvasArchive_MethodNotAllowed.
func TestCanvasArchive_MethodNotAllowed(t *testing.T) {
	srv := NewExtServer(nil)
	cid, _, _, _ := kgCanvasSeed(t, srv, nil)
	r := newKGRequest("GET", "/v1/me/knowledge-graph/clusters/"+cid+"/archive", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// =============================================================================
// confidence bucketing — pure helper unit tests
// =============================================================================

func TestCanvasBucketConfidence(t *testing.T) {
	cases := []struct {
		c    float32
		want string
	}{
		{0.0, "low"},
		{0.25, "low"},
		{0.34, "med"},
		{0.5, "med"},
		{0.66, "med"},
		{0.67, "high"},
		{0.9, "high"},
		{1.0, "high"},
	}
	for _, tc := range cases {
		got := canvasBucketConfidence(tc.c)
		if got != tc.want {
			t.Errorf("bucket(%.2f) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestCanvasPositionAt(t *testing.T) {
	cases := []struct {
		i    int
		want string
	}{
		{0, "N"},
		{1, "NE"},
		{2, "SE"},
		{3, "S"},
		{4, "SW"},
		{5, "NW"},
		{-1, "N"}, // out-of-range falls back to N
		{99, "N"}, // out-of-range falls back to N
	}
	for _, tc := range cases {
		got := canvasPositionAt(tc.i)
		if got != tc.want {
			t.Errorf("position(%d) = %q, want %q", tc.i, got, tc.want)
		}
	}
}

// =============================================================================
// helpers — for parity with kg_handler_test.go
// =============================================================================

// (re-use the kgTenantID/kgGCID/kgAtom* constants + newKGRequest helper
// from kg_handler_test.go in the same package.)
