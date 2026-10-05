// kg_canvas_handler.go — FE-facing camelCase + envelope KG canvas
// endpoints landed under E2E-BE-KG-CANVAS (2026-05-16).
//
// FE source: chora-web/src/app/features/surfaces/aplus/dashboard/kg-fog/kg-fog.service.ts
// (stripped of its 6 stub fallbacks at commit e335c364 — endpoints
// now fail-loud in the panel, blocking the canvas screens).
//
// Wire contract: docs/m13/kg-fog-aplus-integration-backend-handoff-2026-05-13.md.
//
// Routes:
//
//	GET    /v1/me/knowledge-graph/clusters/{cid}/explorations/{eid}/hexagon
//	POST   /v1/me/knowledge-graph/clusters/{cid}/explorations/{eid}/focal:move
//	POST   /v1/me/knowledge-graph/clusters/{cid}/archive
//	POST   /v1/me/knowledge-graph/junctions/{jid}/decide
//	GET    /v1/me/knowledge-graph/clusters/management
//	PATCH  /v1/me/knowledge-graph/clusters/{cid}
//	GET    /v1/tenants/{tid}/knowledge-graph/config
//	PATCH  /v1/tenants/{tid}/knowledge-graph/config
//
// All emit `{"data": {...}}` envelopes (camelCase fields). For 4xx
// responses the envelope is omitted; the legacy `{code, message}`
// shape from extWriteError is kept for backwards-compat with
// kg_handler.go (v1.0 snake_case).
//
// RLS guard: per multi-tenant-rls skill, every handler 404s on
// (cluster tenant ≠ header tenant) OR (cluster gcid ≠ header gcid).
// NEVER 403 or leak the row — RLS at the DB tier is the second line.
//
// Per ADR-143 §6, deciding a junction with `accept` triggers the
// cluster merge command (same as the v1.0 accept handler). The
// via-atom is picked deterministically as the first OverlapAtomIDs[0]
// since the FE doesn't surface a per-overlap chooser (single overlap
// per detection in the dashboard panel scope).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/clusterprojection"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------- canvas FE-shaped DTOs ----------

// canvasEnvelope is the BFF { data, error? } envelope shape FE expects.
type canvasEnvelope struct {
	Data  any              `json:"data,omitempty"`
	Error *canvasErrorBody `json:"error,omitempty"`
}

type canvasErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// canvasNeighbor mirrors kg-fog.model.ts HexagonNeighbor.
type canvasNeighbor struct {
	AtomID             string `json:"atomId"`
	Title              string `json:"title,omitempty"`
	Topic              string `json:"topic,omitempty"`
	Position           string `json:"position"`
	Relation           string `json:"relation"`
	Confidence         string `json:"confidence"`
	PreviewSnippet     string `json:"previewSnippet,omitempty"`
	GeneratedByModelID string `json:"generatedByModelId,omitempty"`
	// GrowthEdge paints the W5 read-overlay (ADR-204 Slice A) when the
	// neighbor's fog label slug-matches one of the learner's active Growth
	// Edges. Reuses the camelCase kgGrowthEdgeFEDTO shape the cluster-LIST
	// already emits. Omitted (fail-soft) when no active edge matches.
	GrowthEdge *kgGrowthEdgeFEDTO `json:"growthEdge,omitempty"`
}

// canvasTrailEntry mirrors kg-fog.model.ts TrailEntry.
type canvasTrailEntry struct {
	AtomID        string `json:"atomId"`
	Title         string `json:"title,omitempty"`
	Topic         string `json:"topic,omitempty"`
	ExplorationID string `json:"explorationId,omitempty"`
}

// canvasJunctionCandidate mirrors kg-fog.model.ts JunctionCandidate.
type canvasJunctionCandidate struct {
	JunctionID            string `json:"junctionId"`
	OtherClusterID        string `json:"otherClusterId"`
	OtherClusterSeedTopic string `json:"otherClusterSeedTopic,omitempty"`
	ViaAtomID             string `json:"viaAtomId"`
	ViaAtomTitle          string `json:"viaAtomTitle,omitempty"`
}

// canvasHexagonLayout mirrors kg-fog.model.ts HexagonLayout.
type canvasHexagonLayout struct {
	ClusterID          string                   `json:"clusterId"`
	ExplorationID      string                   `json:"explorationId"`
	FocalAtomID        string                   `json:"focalAtomId"`
	FocalTitle         string                   `json:"focalTitle,omitempty"`
	FocalTopic         string                   `json:"focalTopic,omitempty"`
	FocalPreview       string                   `json:"focalPreview,omitempty"`
	Neighbors          []canvasNeighbor         `json:"neighbors"`
	Trail              []canvasTrailEntry       `json:"trail"`
	GeneratedAt        time.Time                `json:"generatedAt"`
	InvalidatedAt      *time.Time               `json:"invalidatedAt,omitempty"`
	IsCacheFresh       bool                     `json:"isCacheFresh"`
	GeneratedByModelID string                   `json:"generatedByModelId,omitempty"`
	JunctionCandidate  *canvasJunctionCandidate `json:"junctionCandidate,omitempty"`
	TotalCostMicros    int64                    `json:"totalCostMicros"`
	// FocalGrowthEdge paints the W5 read-overlay (ADR-204 Slice A) for the
	// focal atom, matched via its atom_index topic tags. Omitted (fail-soft)
	// when no active edge matches.
	FocalGrowthEdge *kgGrowthEdgeFEDTO `json:"focalGrowthEdge,omitempty"`
}

// canvasClusterMgmtSummary mirrors kg-fog.model.ts ClusterManagementSummary.
type canvasClusterMgmtSummary struct {
	ClusterID          string    `json:"clusterId"`
	SeedTopic          string    `json:"seedTopic"`
	DisplayName        string    `json:"displayName"`
	CurrentFocalAtomID string    `json:"currentFocalAtomId,omitempty"`
	CurrentFocalTitle  string    `json:"currentFocalTitle,omitempty"`
	TrailDepth         int       `json:"trailDepth"`
	CreatedAt          time.Time `json:"createdAt"`
	LastVisitedAt      time.Time `json:"lastVisitedAt"`
	TotalCostMicros    int64     `json:"totalCostMicros"`
	IsStale            bool      `json:"isStale"`
}

// canvasTenantKgConfig mirrors kg-fog.model.ts TenantKgConfig.
type canvasTenantKgConfig struct {
	MaxConcurrentKgClustersPerUser int       `json:"maxConcurrentKgClustersPerUser"`
	KgFogInvalidationGraceSeconds  int       `json:"kgFogInvalidationGraceSeconds"`
	UpdatedAt                      time.Time `json:"updatedAt"`
	UpdatedByDisplayName           string    `json:"updatedByDisplayName,omitempty"`
}

// canvasFocalMoveRequest is FE's POST .../focal:move body shape.
type canvasFocalMoveRequest struct {
	TargetAtomID string `json:"targetAtomId"`
}

// canvasJunctionDecideRequest is FE's POST .../junctions/{id}/decide body shape.
type canvasJunctionDecideRequest struct {
	Decision string `json:"decision"`
}

// canvasClusterRenameRequest is FE's PATCH .../clusters/{id} body shape.
type canvasClusterRenameRequest struct {
	DisplayName string `json:"displayName"`
}

// canvasTenantConfigPatchRequest is FE's PATCH /v1/tenants/{id}/knowledge-graph/config body shape.
type canvasTenantConfigPatchRequest struct {
	MaxConcurrentKgClustersPerUser *int `json:"maxConcurrentKgClustersPerUser,omitempty"`
	KgFogInvalidationGraceSeconds  *int `json:"kgFogInvalidationGraceSeconds,omitempty"`
}

// ---------- helpers ----------

// canvasWriteData writes a 200 OK envelope with the payload as `data`.
func canvasWriteData(w http.ResponseWriter, status int, data any) {
	extWriteJSON(w, status, canvasEnvelope{Data: data})
}

// canvasWriteError writes an error envelope (matches legacy 4xx shape
// since FE's `BffEnvelope<T>` reads `env.error?.message` as the
// fallback. Kept as the bare {code, message} shape via extWriteError
// for back-compat with v1.0 — FE handles both shapes per its unwrap()).
func canvasWriteError(w http.ResponseWriter, status int, code, message string) {
	extWriteError(w, status, code, message)
}

// canvasBucketConfidence maps the 0..1 confidence float to the FE's
// 3-bucket enum 'low' | 'med' | 'high'. Thresholds: <0.34 = low,
// <0.67 = med, >=0.67 = high. Mirrors the FE 3-dot calibration scale.
func canvasBucketConfidence(c float32) string {
	switch {
	case c >= 0.67:
		return "high"
	case c >= 0.34:
		return "med"
	default:
		return "low"
	}
}

// canvasPositionAt returns the hexagon position for index i (0..5).
// Order N → NE → SE → S → SW → NW (60° clockwise) per FE handoff §2.
func canvasPositionAt(i int) string {
	positions := []string{"N", "NE", "SE", "S", "SW", "NW"}
	if i < 0 || i >= len(positions) {
		return "N"
	}
	return positions[i]
}

// neighborsToCanvas maps the domain neighbors onto the FE camelCase shape.
// paint applies the W5 Growth-Edge read-overlay (ADR-204 Slice A) per neighbor
// by slug-matching its fog label, plus the orthogonal due-⏰ (ADR-204 P2);
// nil/empty paint (or no match) leaves GrowthEdge nil — strictly read-time,
// fail-soft. paint.matchFE is nil-safe so a nil paint is tolerated without a
// guard.
func neighborsToCanvas(neighbors []userknowledgegraph.HexagonNeighbor, modelID string, paint *growthPaint) []canvasNeighbor {
	out := make([]canvasNeighbor, 0, len(neighbors))
	for i, n := range neighbors {
		out = append(out, canvasNeighbor{
			AtomID:             n.AtomID,
			Title:              n.FogLabel, // FE expects `title`; FogLabel is the LLM-generated teaser
			Position:           canvasPositionAt(i),
			Relation:           string(n.Relation),
			Confidence:         canvasBucketConfidence(n.Confidence),
			GeneratedByModelID: modelID,
			GrowthEdge:         paint.matchFE(n.FogLabel),
		})
	}
	return out
}

// canvasBuildLayoutFromHex assembles the FE-shaped HexagonLayout from
// the cached hexagon + cluster + exploration.
//
// paint carries the learner's active Growth Edges (ADR-204 Slice A) + their
// topic retention (ADR-204 P2 due-⏰): the focal is painted by its atom_index
// topic tags and each neighbor by its fog label — the same read-time, fail-soft
// overlay the cluster-LIST + legacy snake_case fog paths apply. A nil/empty
// paint leaves the growthEdge fields nil (omitted). ctx threads the RLS identity
// to the focal atom_index lookup.
func (s *ExtServer) canvasBuildLayoutFromHex(
	ctx context.Context,
	hex *userknowledgegraph.HexagonNode,
	cluster *userknowledgegraph.MapCluster,
	exp *userknowledgegraph.Exploration,
	trail []*userknowledgegraph.TrailHop,
	paint *growthPaint,
) canvasHexagonLayout {
	layout := canvasHexagonLayout{
		ClusterID:          cluster.ClusterID,
		ExplorationID:      exp.ExplorationID,
		FocalAtomID:        hex.FocalAtomID,
		Neighbors:          neighborsToCanvas(hex.Neighbors, hex.GeneratedByModelID, paint),
		Trail:              make([]canvasTrailEntry, 0, len(trail)),
		GeneratedAt:        hex.GeneratedAt,
		IsCacheFresh:       hex.IsCacheFresh(),
		GeneratedByModelID: hex.GeneratedByModelID,
		TotalCostMicros:    0, // populated by chora-ai-kernel cost ledger; left zero in v1
	}
	// Focal overlay — matched via the focal atom's atom_index topic tags
	// (mirrors annotateFogOverlay). Best-effort: a missing projection / read
	// error leaves FocalGrowthEdge nil rather than failing the read.
	if !paint.Empty() && s.AtomIndex != nil {
		if atom, err := s.AtomIndex.Get(ctx, hex.FocalAtomID); err == nil && atom != nil {
			layout.FocalGrowthEdge = paint.matchFE(atom.TopicTags...)
		}
	}
	if hex.InvalidatedAt != nil {
		t := *hex.InvalidatedAt
		layout.InvalidatedAt = &t
	}
	for _, hop := range trail {
		layout.Trail = append(layout.Trail, canvasTrailEntry{
			AtomID:        hop.FromFocalAtomID,
			ExplorationID: exp.ExplorationID,
		})
	}
	return layout
}

// ---------- #1 — GET .../hexagon ----------

// handleCanvasHexagonGet handles
// GET /v1/me/knowledge-graph/clusters/{cid}/explorations/{eid}/hexagon
// per E2E-BE-KG-CANVAS §1.
func (s *ExtServer) handleCanvasHexagonGet(w http.ResponseWriter, r *http.Request, clusterID, explorationID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	exp, err := s.KGExplorations.Load(r.Context(), explorationID)
	if err != nil || exp.ClusterID != clusterID || exp.TenantID != tenantID || exp.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "EXPLORATION_NOT_FOUND", "")
		return
	}
	hex, err := s.KGHexagons.FindFresh(r.Context(), exp.ExplorationID, exp.CurrentFocalAtomID)
	switch {
	case err == nil:
		// fresh cache hit — render below.
	case errors.Is(err, userknowledgegraph.ErrNotFound):
		// Cold cache (or invalidated row): generate the fog ON DEMAND via
		// the FogOrchestrator and persist it, replacing the former
		// `404 FOG_CACHE_MISS` dead-end (the FE canvas could only render
		// pre-cached demo data). The helper writes its own error response
		// and returns nil when generation fails.
		// ADR-254 D13: the hexagon fog GENERATION lane (fog orchestrator) is
		// retired; the hexagon backend stays for cached reads only (CLAUDE.md:
		// kg_hexagon_nodes retires by projection, not deletion). A cold cache is
		// answered loudly, never regenerated: the canvas the SPA mounts is the
		// concept graph (/a/knowledge); new neighbours arrive through the
		// kg_explore suggestion lane.
		canvasWriteError(w, http.StatusGone, "KG_FOG_GENERATION_RETIRED",
			"hexagon fog generation is retired (ADR-254 D13); this exploration has no cached hexagon for its focal")
		return
	default:
		// Storage failure ≠ cache miss — fail loud, never mask as a miss.
		canvasWriteError(w, http.StatusInternalServerError, "HEX_LOOKUP_FAILED", err.Error())
		return
	}
	trail, _ := s.KGExplorations.LoadTrail(r.Context(), exp.ExplorationID)
	canvasWriteData(w, http.StatusOK, s.canvasBuildLayoutFromHex(r.Context(), hex, c, exp, trail, s.learnerGrowthPaint(r.Context(), tenantID, gcid)))
}

// handleCanvasFocalMove handles
// POST /v1/me/knowledge-graph/clusters/{cid}/explorations/{eid}/focal:move
// per E2E-BE-KG-CANVAS §2.
func (s *ExtServer) handleCanvasFocalMove(w http.ResponseWriter, r *http.Request, clusterID, explorationID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req canvasFocalMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		canvasWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	target := strings.TrimSpace(req.TargetAtomID)
	if target == "" {
		canvasWriteError(w, http.StatusUnprocessableEntity, "MISSING_TARGET_ATOM", "targetAtomId required")
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if c.Status != userknowledgegraph.ClusterStatusActive {
		canvasWriteError(w, http.StatusConflict, "CLUSTER_INACTIVE", "")
		return
	}
	exp, err := s.KGExplorations.Load(r.Context(), explorationID)
	if err != nil || exp.ClusterID != clusterID {
		canvasWriteError(w, http.StatusNotFound, "EXPLORATION_NOT_FOUND", "")
		return
	}
	hex, err := s.KGHexagons.FindFresh(r.Context(), exp.ExplorationID, exp.CurrentFocalAtomID)
	if err != nil {
		canvasWriteError(w, http.StatusUnprocessableEntity, "FOG_NOT_CACHED",
			"hexagon for current focal not in cache; cannot validate neighbor")
		return
	}
	// Verify target is in the cached 6 neighbors.
	relation := userknowledgegraph.NeighborRelationCuriosityJump
	matched := false
	for _, n := range hex.Neighbors {
		if n.AtomID == target {
			matched = true
			relation = n.Relation
			break
		}
	}
	if !matched {
		canvasWriteError(w, http.StatusUnprocessableEntity, "NEIGHBOR_NOT_IN_HEX",
			"target atom is not one of the cached 6 neighbors")
		return
	}
	prevFocal := exp.CurrentFocalAtomID
	if err := exp.MoveFocal(target); err != nil {
		canvasWriteError(w, http.StatusConflict, "MOVE_FAILED", err.Error())
		return
	}
	idx, _ := s.KGExplorations.LatestStepIndex(r.Context(), exp.ExplorationID)
	hop, err := userknowledgegraph.NewTrailHop(
		exp.ExplorationID, c.ClusterID, tenantID, gcid,
		prevFocal, target,
		relation,
		idx+1,
	)
	if err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "TRAIL_FAILED", err.Error())
		return
	}
	if err := s.KGExplorations.AppendTrailHop(r.Context(), exp, hop); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "TRAIL_SAVE_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishExplorationFocalChanged(ctx, exp, hop); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	// Build new layout. The new focal's fog may not yet exist; we return
	// the layout for the PREVIOUS focal (with the new focalAtomId) so
	// FE keeps rendering the trail + last-cached neighbors. The next
	// fog-orchestrator call will populate the new focal's hex.
	newHex, hexErr := s.KGHexagons.FindFresh(r.Context(), exp.ExplorationID, target)
	if hexErr != nil {
		// Synthesize a layout with no neighbors + isCacheFresh=false so
		// FE renders the placeholder.
		layout := canvasHexagonLayout{
			ClusterID:     c.ClusterID,
			ExplorationID: exp.ExplorationID,
			FocalAtomID:   target,
			Neighbors:     []canvasNeighbor{},
			Trail:         []canvasTrailEntry{},
			GeneratedAt:   time.Now().UTC(),
			IsCacheFresh:  false,
		}
		trail, _ := s.KGExplorations.LoadTrail(r.Context(), exp.ExplorationID)
		for _, h := range trail {
			layout.Trail = append(layout.Trail, canvasTrailEntry{
				AtomID:        h.FromFocalAtomID,
				ExplorationID: exp.ExplorationID,
			})
		}
		canvasWriteData(w, http.StatusOK, layout)
		return
	}
	trail, _ := s.KGExplorations.LoadTrail(r.Context(), exp.ExplorationID)
	canvasWriteData(w, http.StatusOK, s.canvasBuildLayoutFromHex(r.Context(), newHex, c, exp, trail, s.learnerGrowthPaint(r.Context(), tenantID, gcid)))
}

// ---------- #3 — POST .../clusters/{cid}/archive ----------

// handleCanvasArchive handles
// POST /v1/me/knowledge-graph/clusters/{cid}/archive
// per E2E-BE-KG-CANVAS §3. Returns 204 on success; idempotent on re-archive.
func (s *ExtServer) handleCanvasArchive(w http.ResponseWriter, r *http.Request, clusterID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if err := c.Archive(); err != nil {
		if errors.Is(err, userknowledgegraph.ErrAlreadyArchived) {
			// Idempotent — 204 No Content (matches first-call success).
			w.WriteHeader(http.StatusNoContent)
			return
		}
		canvasWriteError(w, http.StatusConflict, "ARCHIVE_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), c); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishMapClusterArchived(ctx, c); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- #3b — POST .../clusters/{cid}/convert (ADR-223) ----------

// clusterProjector re-projects a retired fog MapCluster into a sovereign Goal.
// Satisfied by *clusterprojection.Projector.
type clusterProjector interface {
	Project(ctx context.Context, tenantID, gcid, clusterID string) (string, error)
}

// handleCanvasConvert handles
// POST /v1/me/knowledge-graph/clusters/{cid}/convert
// (ADR-223): re-projects the fog cluster into a sovereign Goal (learner-driven
// fog retirement). Returns 200 {"goalId": "..."}. Idempotent — an
// already-projected cluster returns its recorded goal.
func (s *ExtServer) handleCanvasConvert(w http.ResponseWriter, r *http.Request, clusterID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	if s.ClusterProjector == nil {
		canvasWriteError(w, http.StatusServiceUnavailable, "PROJECTION_UNAVAILABLE", "cluster projection not configured")
		return
	}
	goalID, err := s.ClusterProjector.Project(r.Context(), tenantID, gcid, clusterID)
	if err != nil {
		switch {
		case errors.Is(err, userknowledgegraph.ErrNotFound), errors.Is(err, clusterprojection.ErrClusterNotOwned):
			canvasWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		case errors.Is(err, userknowledgegraph.ErrAlreadyMerged):
			canvasWriteError(w, http.StatusConflict, "CLUSTER_MERGED", "a merged cluster cannot be projected")
		default:
			canvasWriteError(w, http.StatusInternalServerError, "PROJECTION_FAILED", err.Error())
		}
		return
	}
	canvasWriteData(w, http.StatusOK, map[string]any{"goalId": goalID})
}

// ---------- #4 — POST .../junctions/{jid}/decide ----------

// handleCanvasJunctionDecide handles
// POST /v1/me/knowledge-graph/junctions/{jid}/decide
// per E2E-BE-KG-CANVAS §4. Body: {"decision": "accept" | "decline"}.
func (s *ExtServer) handleCanvasJunctionDecide(w http.ResponseWriter, r *http.Request, junctionID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req canvasJunctionDecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		canvasWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	decision := strings.TrimSpace(req.Decision)
	if decision != "accept" && decision != "decline" {
		canvasWriteError(w, http.StatusUnprocessableEntity, "INVALID_DECISION",
			"decision must be 'accept' or 'decline'")
		return
	}
	j, err := s.KGJunctions.Load(r.Context(), junctionID)
	if err != nil {
		canvasWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	if j.TenantID != tenantID || j.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	now := time.Now().UTC()
	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)

	if decision == "decline" {
		if err := j.Reject(now); err != nil {
			canvasWriteError(w, http.StatusConflict, "REJECT_FAILED", err.Error())
			return
		}
		if err := s.KGJunctions.Save(r.Context(), j); err != nil {
			canvasWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
			return
		}
		if err := s.KGEvents.PublishJunctionRejected(ctx, j); err != nil {
			canvasWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}
		// FE expects `null` data on decline — return 200 with absent envelope data.
		extWriteJSON(w, http.StatusOK, canvasEnvelope{})
		return
	}

	// decision == "accept" — pick first overlap atom as via-atom.
	if len(j.OverlapAtomIDs) == 0 {
		canvasWriteError(w, http.StatusUnprocessableEntity, "EMPTY_OVERLAP",
			"junction has no overlap atoms")
		return
	}
	viaAtom := j.OverlapAtomIDs[0]
	if err := j.Accept(viaAtom, now); err != nil {
		canvasWriteError(w, http.StatusConflict, "ACCEPT_FAILED", err.Error())
		return
	}
	if err := s.KGJunctions.Save(r.Context(), j); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	cA, errA := s.KGClusters.Load(r.Context(), j.ClusterAID)
	cB, errB := s.KGClusters.Load(r.Context(), j.ClusterBID)
	if errA != nil || errB != nil {
		canvasWriteError(w, http.StatusInternalServerError, "CLUSTER_LOAD_FAILED", "")
		return
	}
	survivor, merged := cA, cB
	if cB.NodeCount > cA.NodeCount {
		survivor, merged = cB, cA
	}
	if err := merged.MergeInto(survivor.ClusterID, viaAtom); err != nil {
		canvasWriteError(w, http.StatusConflict, "MERGE_FAILED", err.Error())
		return
	}
	if err := survivor.AbsorbMerged(merged, viaAtom); err != nil {
		canvasWriteError(w, http.StatusConflict, "ABSORB_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), survivor); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "SAVE_SURVIVOR_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), merged); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "SAVE_MERGED_FAILED", err.Error())
		return
	}

	if err := s.KGEvents.PublishJunctionAccepted(ctx, j); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	if err := s.KGEvents.PublishMapClusterMerged(ctx, survivor, merged, viaAtom); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	// Return a HexagonLayout pointing at the survivor's most-recent
	// exploration + its cached hex (when present). Falls back to a
	// no-neighbors placeholder when fog has not yet been generated.
	exps, _ := s.KGExplorations.ListByCluster(r.Context(), survivor.ClusterID)
	var surExp *userknowledgegraph.Exploration
	for _, e := range exps {
		if e.Status == userknowledgegraph.ExplorationStatusActive {
			surExp = e
			break
		}
	}
	if surExp == nil {
		extWriteJSON(w, http.StatusOK, canvasEnvelope{})
		return
	}
	hex, hexErr := s.KGHexagons.FindFresh(r.Context(), surExp.ExplorationID, surExp.CurrentFocalAtomID)
	if hexErr != nil {
		canvasWriteData(w, http.StatusOK, canvasHexagonLayout{
			ClusterID:     survivor.ClusterID,
			ExplorationID: surExp.ExplorationID,
			FocalAtomID:   surExp.CurrentFocalAtomID,
			Neighbors:     []canvasNeighbor{},
			Trail:         []canvasTrailEntry{},
			GeneratedAt:   time.Now().UTC(),
			IsCacheFresh:  false,
		})
		return
	}
	trail, _ := s.KGExplorations.LoadTrail(r.Context(), surExp.ExplorationID)
	canvasWriteData(w, http.StatusOK, s.canvasBuildLayoutFromHex(r.Context(), hex, survivor, surExp, trail, s.learnerGrowthPaint(r.Context(), tenantID, gcid)))
}

// ---------- #5 — GET /v1/me/knowledge-graph/clusters/management ----------

// handleCanvasManagement handles
// GET /v1/me/knowledge-graph/clusters/management
// per E2E-BE-KG-CANVAS §5.
func (s *ExtServer) handleCanvasManagement(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	clusters, err := s.KGClusters.ListAllByUser(r.Context(), tenantID, gcid)
	if err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	out := make([]canvasClusterMgmtSummary, 0, len(clusters))
	for _, c := range clusters {
		if c.Status != userknowledgegraph.ClusterStatusActive {
			continue
		}
		sum := canvasClusterMgmtSummary{
			ClusterID:     c.ClusterID,
			SeedTopic:     c.SeedTopic,
			DisplayName:   c.DisplayName,
			CreatedAt:     c.CreatedAt,
			LastVisitedAt: c.UpdatedAt,
		}
		if s.KGExplorations != nil {
			exps, _ := s.KGExplorations.ListByCluster(r.Context(), c.ClusterID)
			for _, e := range exps {
				if e.Status != userknowledgegraph.ExplorationStatusActive {
					continue
				}
				sum.CurrentFocalAtomID = e.CurrentFocalAtomID
				if idx, ierr := s.KGExplorations.LatestStepIndex(r.Context(), e.ExplorationID); ierr == nil {
					sum.TrailDepth = idx
				}
				if s.KGHexagons != nil {
					hex, herr := s.KGHexagons.FindFresh(r.Context(), e.ExplorationID, e.CurrentFocalAtomID)
					if herr != nil {
						sum.IsStale = true
					} else {
						sum.IsStale = !hex.IsCacheFresh()
					}
				}
				break
			}
		}
		out = append(out, sum)
	}
	canvasWriteData(w, http.StatusOK, out)
}

// ---------- #6 — PATCH .../clusters/{cid} (rename) ----------

// handleCanvasRename handles
// PATCH /v1/me/knowledge-graph/clusters/{cid}
// per E2E-BE-KG-CANVAS §6.
func (s *ExtServer) handleCanvasRename(w http.ResponseWriter, r *http.Request, clusterID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req canvasClusterRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		canvasWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		canvasWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if err := c.Rename(req.DisplayName); err != nil {
		// Map domain errors to HTTP codes.
		switch {
		case errors.Is(err, userknowledgegraph.ErrDisplayNameRequired):
			canvasWriteError(w, http.StatusUnprocessableEntity, "MISSING_DISPLAY_NAME", err.Error())
		case errors.Is(err, userknowledgegraph.ErrDisplayNameTooLong):
			canvasWriteError(w, http.StatusUnprocessableEntity, "DISPLAY_NAME_TOO_LONG", err.Error())
		case errors.Is(err, userknowledgegraph.ErrCannotRenameInactive):
			canvasWriteError(w, http.StatusConflict, "CLUSTER_INACTIVE", err.Error())
		default:
			canvasWriteError(w, http.StatusBadRequest, "RENAME_FAILED", err.Error())
		}
		return
	}
	if err := s.KGClusters.Save(r.Context(), c); err != nil {
		canvasWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- #7 + #8 — /v1/tenants/{tid}/knowledge-graph/config ----------

// canvasTenantConfigStore is the in-memory per-tenant config store
// behind the GET/PATCH tenant config endpoints. The real chora-tenancy
// gRPC adapter (per ADR-142 precedent) lands later; this in-memory
// store is sufficient for the FE unblock + persists across the same
// process lifetime.
//
// Per feedback_no_inline_config: defaults come from the existing
// StaticKGConfigReader bounds at clients.KGDefaultMaxClustersPerUser /
// clients.KGDefaultFogInvalidationGraceS.
type canvasTenantConfigStore struct {
	mu      sync.RWMutex
	configs map[string]canvasTenantKgConfig
}

func newCanvasTenantConfigStore() *canvasTenantConfigStore {
	return &canvasTenantConfigStore{configs: make(map[string]canvasTenantKgConfig)}
}

func (s *canvasTenantConfigStore) get(tenantID string) canvasTenantKgConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cfg, ok := s.configs[tenantID]; ok {
		return cfg
	}
	return canvasTenantKgConfig{
		MaxConcurrentKgClustersPerUser: clients.KGDefaultMaxClustersPerUser,
		KgFogInvalidationGraceSeconds:  clients.KGDefaultFogInvalidationGraceS,
		UpdatedAt:                      time.Now().UTC(),
	}
}

func (s *canvasTenantConfigStore) set(tenantID string, cfg canvasTenantKgConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[tenantID] = cfg
}

// Package-level singleton — the in-memory store is shared across
// goroutines + tests within the same process. Production wiring will
// inject a chora-tenancy-backed adapter when the projection lands.
var canvasTenantStore = newCanvasTenantConfigStore()

// handleCanvasTenantConfigGet handles
// GET /v1/tenants/{tid}/knowledge-graph/config
// per E2E-BE-KG-CANVAS §7.
func (s *ExtServer) handleCanvasTenantConfigGet(w http.ResponseWriter, r *http.Request, tenantID string) {
	headerTenant, _, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	// Path tenant MUST match header tenant — otherwise return 404 to
	// avoid info leak across tenants.
	if !strings.EqualFold(tenantID, headerTenant) {
		canvasWriteError(w, http.StatusNotFound, "TENANT_NOT_FOUND", "")
		return
	}
	cfg := canvasTenantStore.get(tenantID)
	canvasWriteData(w, http.StatusOK, cfg)
}

// handleCanvasTenantConfigPatch handles
// PATCH /v1/tenants/{tid}/knowledge-graph/config
// per E2E-BE-KG-CANVAS §8.
func (s *ExtServer) handleCanvasTenantConfigPatch(w http.ResponseWriter, r *http.Request, tenantID string) {
	headerTenant, _, err := extRequireContext(r)
	if err != nil {
		canvasWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	if !strings.EqualFold(tenantID, headerTenant) {
		canvasWriteError(w, http.StatusNotFound, "TENANT_NOT_FOUND", "")
		return
	}
	var req canvasTenantConfigPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		canvasWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current := canvasTenantStore.get(tenantID)
	if req.MaxConcurrentKgClustersPerUser != nil {
		v := *req.MaxConcurrentKgClustersPerUser
		if v < 1 || v > 10 {
			canvasWriteError(w, http.StatusUnprocessableEntity, "OUT_OF_BOUNDS",
				"maxConcurrentKgClustersPerUser must be 1..10")
			return
		}
		current.MaxConcurrentKgClustersPerUser = v
	}
	if req.KgFogInvalidationGraceSeconds != nil {
		v := *req.KgFogInvalidationGraceSeconds
		if v < 60 || v > 3600 {
			canvasWriteError(w, http.StatusUnprocessableEntity, "OUT_OF_BOUNDS",
				"kgFogInvalidationGraceSeconds must be 60..3600")
			return
		}
		current.KgFogInvalidationGraceSeconds = v
	}
	current.UpdatedAt = time.Now().UTC()
	canvasTenantStore.set(tenantID, current)
	canvasWriteData(w, http.StatusOK, current)
}

// ---------- route dispatch ----------

// handleCanvasClusterMgmtRoute dispatches the canvas mgmt endpoint
// (GET /v1/me/knowledge-graph/clusters/management).
func (s *ExtServer) handleCanvasClusterMgmtRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	s.handleCanvasManagement(w, r)
}

// handleCanvasClusterSubpath dispatches the per-cluster canvas routes:
//
//	POST   .../clusters/{cid}/archive
//	GET    .../clusters/{cid}/explorations/{eid}/hexagon
//	POST   .../clusters/{cid}/explorations/{eid}/focal:move
//	PATCH  .../clusters/{cid}  (rename)
//
// Mounted at prefix /v1/me/knowledge-graph/clusters/. Because the
// v1.0 kg_handler.go already owns this prefix via handleKGClusterByID,
// we install a wrapper that delegates to the canvas handler when the
// path tail matches one of the canvas patterns, else falls back to
// the legacy handler.
func (s *ExtServer) handleCanvasClusterSubpath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/knowledge-graph/clusters/")
	rest = strings.TrimSuffix(rest, "/")

	// /management — list summaries (GET only).
	if rest == "management" {
		s.handleCanvasClusterMgmtRoute(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		s.handleKGClusterByID(w, r)
		return
	}
	clusterID := parts[0]

	// /clusters/{cid}  — PATCH = canvas rename; otherwise delegate.
	if len(parts) == 1 {
		if r.Method == http.MethodPatch {
			s.handleCanvasRename(w, r, clusterID)
			return
		}
		s.handleKGClusterByID(w, r)
		return
	}

	// /clusters/{cid}/archive — canvas archive (POST).
	if len(parts) == 2 && parts[1] == "archive" {
		if r.Method != http.MethodPost {
			canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleCanvasArchive(w, r, clusterID)
		return
	}

	// /clusters/{cid}/convert — ADR-223: re-project this fog cluster into a
	// sovereign Goal (learner-triggered fog retirement, POST).
	if len(parts) == 2 && parts[1] == "convert" {
		if r.Method != http.MethodPost {
			canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleCanvasConvert(w, r, clusterID)
		return
	}

	// /clusters/{cid}/explorations/{eid}/hexagon
	// /clusters/{cid}/explorations/{eid}/focal:move
	if len(parts) == 4 && parts[1] == "explorations" {
		explorationID := parts[2]
		switch parts[3] {
		case "hexagon":
			if r.Method != http.MethodGet {
				canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
				return
			}
			s.handleCanvasHexagonGet(w, r, clusterID, explorationID)
			return
		case "focal:move":
			if r.Method != http.MethodPost {
				canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
				return
			}
			s.handleCanvasFocalMove(w, r, clusterID, explorationID)
			return
		}
	}

	// Delegate to legacy handler (handles /clusters/{cid}/focal/{atom_id}/fog etc).
	s.handleKGClusterByID(w, r)
}

// handleCanvasJunctionSubpath dispatches the canvas junction routes:
//
//	POST /v1/me/knowledge-graph/junctions/{jid}/decide
//
// Falls back to legacy /accept and /reject handlers for non-decide paths.
func (s *ExtServer) handleCanvasJunctionSubpath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/knowledge-graph/junctions/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		s.handleKGJunctionByID(w, r)
		return
	}
	if parts[1] == "decide" {
		if r.Method != http.MethodPost {
			canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.handleCanvasJunctionDecide(w, r, parts[0])
		return
	}
	s.handleKGJunctionByID(w, r)
}

// handleCanvasTenantSubpath dispatches GET + PATCH for
// /v1/tenants/{tid}/knowledge-graph/config.
func (s *ExtServer) handleCanvasTenantSubpath(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/tenants/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "knowledge-graph" || parts[2] != "config" {
		canvasWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
		return
	}
	tenantID := parts[0]
	switch r.Method {
	case http.MethodGet:
		s.handleCanvasTenantConfigGet(w, r, tenantID)
	case http.MethodPatch:
		s.handleCanvasTenantConfigPatch(w, r, tenantID)
	default:
		canvasWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}
