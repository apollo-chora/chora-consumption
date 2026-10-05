// kg_handler.go — S5.2 /v1/me/knowledge-graph/* endpoints (ADR-143).
//
// Routes:
//
//	GET    /v1/me/knowledge-graph/clusters
//	POST   /v1/me/knowledge-graph/clusters
//	GET    /v1/me/knowledge-graph/clusters/{cluster_id}
//	DELETE /v1/me/knowledge-graph/clusters/{cluster_id}
//	GET    /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/fog
//	POST   /v1/me/knowledge-graph/clusters/{cluster_id}/focal/{atom_id}/promote/{neighbor_id}
//	POST   /v1/me/knowledge-graph/junctions/{junction_id}/accept
//	POST   /v1/me/knowledge-graph/junctions/{junction_id}/reject
//
// Mandatory headers: X-Tenant-Id, gcid, traceparent.
//
// Per multi-tenant-rls skill: handler-side check is the FIRST line of
// RLS defence. Postgres RLS policies in migrations/0002 are the SECOND.
// On a mismatch, we return 404 (NEVER 403, never the row, never 200) to
// avoid information leakage.
//
// Per ADR-143 §6: Junctions detected during fog-gen are PERSISTED with
// pending status; the user accept/reject action transitions them.
// Detection of the SAME (cluster_a, cluster_b) pair while a pending
// junction exists is idempotent (FindPendingByPair upsert).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------- payloads ----------

type kgCreateClusterReq struct {
	SeedTopic   string `json:"seed_topic"`
	SeedAtomID  string `json:"seed_atom_id"`
	DisplayName string `json:"display_name"`

	// camelCase aliases — the A+ canvas shell (kg-fog.model.ts
	// ClusterCreateRequest) sends `{"seedTopic": "..."}`. normalise()
	// folds them onto the canonical snake_case fields.
	SeedTopicCamel   string `json:"seedTopic"`
	SeedAtomIDCamel  string `json:"seedAtomId"`
	DisplayNameCamel string `json:"displayName"`
}

// normalise folds the camelCase aliases onto the canonical fields
// (snake_case wins when both are present).
func (r *kgCreateClusterReq) normalise() {
	if strings.TrimSpace(r.SeedTopic) == "" {
		r.SeedTopic = r.SeedTopicCamel
	}
	if strings.TrimSpace(r.SeedAtomID) == "" {
		r.SeedAtomID = r.SeedAtomIDCamel
	}
	if strings.TrimSpace(r.DisplayName) == "" {
		r.DisplayName = r.DisplayNameCamel
	}
}

type kgClusterDTO struct {
	ClusterID           string    `json:"cluster_id"`
	TenantID            string    `json:"tenant_id"`
	UserGCID            string    `json:"user_gcid"`
	DisplayName         string    `json:"display_name"`
	SeedTopic           string    `json:"seed_topic"`
	SeedAtomID          string    `json:"seed_atom_id"`
	Status              string    `json:"status"`
	MergedIntoClusterID string    `json:"merged_into_cluster_id,omitempty"`
	MergedViaAtomID     string    `json:"merged_via_atom_id,omitempty"`
	NodeCount           int       `json:"node_count"`
	Version             int       `json:"version"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type kgExplorationDTO struct {
	ExplorationID      string    `json:"exploration_id"`
	ClusterID          string    `json:"cluster_id"`
	StartedAtAtomID    string    `json:"started_at_atom_id"`
	CurrentFocalAtomID string    `json:"current_focal_atom_id"`
	Status             string    `json:"status"`
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type kgClusterListResponse struct {
	Items                []kgClusterDTO `json:"items"`
	ActiveCount          int            `json:"active_count"`
	MaxConcurrentPerUser int            `json:"max_concurrent_per_user"`
}

// kgClusterListFEResponse is the wire shape consumed by chora-web's
// A+ dashboard panel per docs/m13/kg-fog-aplus-integration-backend-handoff-2026-05-13.md §1.1.
//
// camelCase keys + `{data, error?}` envelope is the Chora standard for
// the chora-gateway BFF surface. Distinct from the legacy
// kgClusterListResponse above which retains snake_case for back-compat.
type kgClusterListFEResponse struct {
	Data kgClusterListFEData `json:"data"`
}

type kgClusterListFEData struct {
	Clusters     []kgClusterFEDTO `json:"clusters"`
	CapRemaining int              `json:"capRemaining"`
	CapMax       int              `json:"capMax"`
}

// kgClusterFEDTO matches the FE TypeScript ClusterCard model.
type kgClusterFEDTO struct {
	ClusterID           string    `json:"clusterId"`
	SeedTopic           string    `json:"seedTopic"`
	ActiveExplorationID string    `json:"activeExplorationId,omitempty"`
	CurrentFocalAtomID  string    `json:"currentFocalAtomId,omitempty"`
	CurrentFocalTitle   string    `json:"currentFocalTitle,omitempty"`
	CurrentFocalTopic   string    `json:"currentFocalTopic,omitempty"`
	NeighborCount       int       `json:"neighborCount"`
	TrailDepth          int       `json:"trailDepth"`
	LastVisitedAt       time.Time `json:"lastVisitedAt"`
	CreatedAt           time.Time `json:"createdAt"`
	IsStale             bool      `json:"isStale"`
	JunctionPending     bool      `json:"junctionPending"`
	// GrowthEdge paints the W5 read-overlay when the cluster's seed topic
	// slug-matches one of the learner's active Growth Edges (Epic-1b).
	GrowthEdge *kgGrowthEdgeFEDTO `json:"growthEdge,omitempty"`
}

type kgClusterDetail struct {
	kgClusterDTO
	Explorations []kgExplorationDTO `json:"explorations"`
}

type kgCreateClusterResp struct {
	Cluster            kgClusterDTO     `json:"cluster"`
	InitialExploration kgExplorationDTO `json:"initial_exploration"`
}

type kgHexagonNeighborDTO struct {
	AtomID     string  `json:"atom_id"`
	Relation   string  `json:"relation"`
	Confidence float32 `json:"confidence"`
	FogLabel   string  `json:"fog_label"`
	IsJunction bool    `json:"is_junction"`
	// GrowthEdge paints the W5 read-overlay when the fog label slug-matches
	// one of the learner's active Growth Edges (Epic-1b).
	GrowthEdge *kgGrowthEdgeDTO `json:"growth_edge,omitempty"`
}

type kgHexagonDTO struct {
	HexNodeID          string                 `json:"hex_node_id"`
	ExplorationID      string                 `json:"exploration_id"`
	ClusterID          string                 `json:"cluster_id"`
	FocalAtomID        string                 `json:"focal_atom_id"`
	Neighbors          []kgHexagonNeighborDTO `json:"neighbors"`
	GeneratedByRunID   string                 `json:"generated_by_run_id"`
	GeneratedByModelID string                 `json:"generated_by_model_id"`
	GeneratedAt        time.Time              `json:"generated_at"`
	IsCacheFresh       bool                   `json:"is_cache_fresh"`
	// FocalGrowthEdge paints the W5 read-overlay for the focal atom (matched
	// via its atom_index topic tags; Epic-1b).
	FocalGrowthEdge *kgGrowthEdgeDTO `json:"focal_growth_edge,omitempty"`
}

type kgFogResponse struct {
	Hexagon   kgHexagonDTO          `json:"hexagon"`
	Junctions []kgJunctionOpportDTO `json:"junctions"`
}

type kgJunctionOpportDTO struct {
	ViaAtomID                 string `json:"via_atom_id"`
	CurrentClusterID          string `json:"current_cluster_id"`
	OtherClusterID            string `json:"other_cluster_id"`
	CurrentClusterDisplayName string `json:"current_cluster_display_name"`
	OtherClusterDisplayName   string `json:"other_cluster_display_name"`
}

type kgPromoteResp struct {
	Exploration kgExplorationDTO `json:"exploration"`
	TrailHop    kgTrailHopDTO    `json:"trail_hop"`
}

type kgTrailHopDTO struct {
	TrailID         string    `json:"trail_id"`
	ExplorationID   string    `json:"exploration_id"`
	FromFocalAtomID string    `json:"from_focal_atom_id"`
	ToFocalAtomID   string    `json:"to_focal_atom_id"`
	Relation        string    `json:"relation"`
	StepIndex       int       `json:"step_index"`
	TraversedAt     time.Time `json:"traversed_at"`
}

type kgJunctionAcceptReq struct {
	ViaAtomID string `json:"via_atom_id"`
}

type kgJunctionDTO struct {
	JunctionID        string    `json:"junction_id"`
	ClusterAID        string    `json:"cluster_a_id"`
	ClusterBID        string    `json:"cluster_b_id"`
	OverlapAtomIDs    []string  `json:"overlap_atom_ids"`
	Status            string    `json:"status"`
	ResolvedViaAtomID string    `json:"resolved_via_atom_id,omitempty"`
	DetectedAt        time.Time `json:"detected_at"`
}

type kgJunctionResolveResp struct {
	Junction         kgJunctionDTO `json:"junction"`
	SurvivingCluster *kgClusterDTO `json:"surviving_cluster,omitempty"`
	MergedCluster    *kgClusterDTO `json:"merged_cluster,omitempty"`
}

// ---------- mappers ----------

func toKGClusterDTO(c *userknowledgegraph.MapCluster) kgClusterDTO {
	return kgClusterDTO{
		ClusterID:           c.ClusterID,
		TenantID:            c.TenantID,
		UserGCID:            c.UserGCID,
		DisplayName:         c.DisplayName,
		SeedTopic:           c.SeedTopic,
		SeedAtomID:          c.SeedAtomID,
		Status:              string(c.Status),
		MergedIntoClusterID: c.MergedIntoClusterID,
		MergedViaAtomID:     c.MergedViaAtomID,
		NodeCount:           c.NodeCount,
		Version:             c.Version,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
	}
}

func toKGExplorationDTO(e *userknowledgegraph.Exploration) kgExplorationDTO {
	return kgExplorationDTO{
		ExplorationID:      e.ExplorationID,
		ClusterID:          e.ClusterID,
		StartedAtAtomID:    e.StartedAtAtomID,
		CurrentFocalAtomID: e.CurrentFocalAtomID,
		Status:             string(e.Status),
		Version:            e.Version,
		CreatedAt:          e.CreatedAt,
		UpdatedAt:          e.UpdatedAt,
	}
}

func toKGHexagonDTO(h *userknowledgegraph.HexagonNode) kgHexagonDTO {
	neighbors := make([]kgHexagonNeighborDTO, 0, len(h.Neighbors))
	for _, n := range h.Neighbors {
		neighbors = append(neighbors, kgHexagonNeighborDTO{
			AtomID:     n.AtomID,
			Relation:   string(n.Relation),
			Confidence: n.Confidence,
			FogLabel:   n.FogLabel,
			IsJunction: n.IsJunction,
		})
	}
	return kgHexagonDTO{
		HexNodeID:          h.HexNodeID,
		ExplorationID:      h.ExplorationID,
		ClusterID:          h.ClusterID,
		FocalAtomID:        h.FocalAtomID,
		Neighbors:          neighbors,
		GeneratedByRunID:   h.GeneratedByRunID,
		GeneratedByModelID: h.GeneratedByModelID,
		GeneratedAt:        h.GeneratedAt,
		IsCacheFresh:       h.IsCacheFresh(),
	}
}

func toKGJunctionDTO(j *userknowledgegraph.Junction) kgJunctionDTO {
	return kgJunctionDTO{
		JunctionID:        j.JunctionID,
		ClusterAID:        j.ClusterAID,
		ClusterBID:        j.ClusterBID,
		OverlapAtomIDs:    append([]string(nil), j.OverlapAtomIDs...),
		Status:            string(j.Status),
		ResolvedViaAtomID: j.ResolvedViaAtomID,
		DetectedAt:        j.DetectedAt,
	}
}

// ---------- /v1/me/knowledge-graph/clusters ----------

func (s *ExtServer) handleKGClusters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listKGClusters(w, r)
	case http.MethodPost:
		s.createKGCluster(w, r)
	default:
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

func (s *ExtServer) listKGClusters(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	cfg, err := s.KGConfig.ReadKnowledgeGraphConfig(r.Context(), tenantID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TENANT_CONFIG_FAILED", err.Error())
		return
	}
	clusters, err := s.KGClusters.ListAllByUser(r.Context(), tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	// Emit the FE-compatible shape per docs/m13/kg-fog-aplus-integration-backend-handoff-2026-05-13.md §1.1.
	// Only ACTIVE clusters surface to the dashboard panel; archived +
	// merged_into clusters are filtered out per FE empty-state contract.
	// W5 (Epic-1b): paint the Growth-Edge read-overlay on each card whose
	// seed topic matches an active edge — fail-soft, DTO-only. ADR-204 P2: the
	// paint also carries the orthogonal due-⏰ flag from the topic's retention.
	paint := s.learnerGrowthPaint(r.Context(), tenantID, gcid)
	feClusters := make([]kgClusterFEDTO, 0, len(clusters))
	active := 0
	for _, c := range clusters {
		if c.Status != userknowledgegraph.ClusterStatusActive {
			continue
		}
		active++
		dto := s.toKGClusterFEDTO(r.Context(), c)
		dto.GrowthEdge = paint.matchFE(c.SeedTopic, c.DisplayName)
		feClusters = append(feClusters, dto)
	}
	capMax := cfg.MaxConcurrentClustersPerUser
	capRemaining := capMax - active
	if capRemaining < 0 {
		capRemaining = 0
	}
	extWriteJSON(w, http.StatusOK, kgClusterListFEResponse{
		Data: kgClusterListFEData{
			Clusters:     feClusters,
			CapRemaining: capRemaining,
			CapMax:       capMax,
		},
	})
}

// toKGClusterFEDTO assembles the FE-shaped cluster card from the cluster
// aggregate + its active exploration + cached hexagon (when available).
//
// neighborCount, trailDepth, currentFocal* + isStale, junctionPending are
// best-effort enrichment fields; on data-access errors we leave them as
// zero values and let the FE render a graceful card. The §7 step 2 FE
// unblock only requires the clusters[] list to be present; enrichment
// quality improves over §7 step 3 (fog-orchestrator) + step 6 (junction
// decide).
func (s *ExtServer) toKGClusterFEDTO(ctx context.Context, c *userknowledgegraph.MapCluster) kgClusterFEDTO {
	dto := kgClusterFEDTO{
		ClusterID:     c.ClusterID,
		SeedTopic:     c.SeedTopic,
		CreatedAt:     c.CreatedAt,
		LastVisitedAt: c.UpdatedAt, // M14: derived from cluster.UpdatedAt; future migration adds a dedicated column
	}
	if s.KGExplorations != nil {
		exps, _ := s.KGExplorations.ListByCluster(ctx, c.ClusterID)
		for _, e := range exps {
			if e.Status != userknowledgegraph.ExplorationStatusActive {
				continue
			}
			dto.ActiveExplorationID = e.ExplorationID
			dto.CurrentFocalAtomID = e.CurrentFocalAtomID
			// Resolve the focal atom's human title from the IN-DOMAIN
			// atom_index projection (chora_consumption, RLS-scoped) — never a
			// cross-DB read to chora_creation. Best-effort like the rest of
			// this enrichment: a cache miss / not-yet-projected atom leaves
			// CurrentFocalTitle empty and the FE falls back to the seed topic.
			if s.AtomIndex != nil && e.CurrentFocalAtomID != "" {
				if ai, aerr := s.AtomIndex.Get(ctx, e.CurrentFocalAtomID); aerr == nil && ai != nil {
					dto.CurrentFocalTitle = ai.Title
				}
			}
			if idx, ierr := s.KGExplorations.LatestStepIndex(ctx, e.ExplorationID); ierr == nil {
				dto.TrailDepth = idx
			}
			if e.UpdatedAt.After(dto.LastVisitedAt) {
				dto.LastVisitedAt = e.UpdatedAt
			}
			if s.KGHexagons != nil {
				if hex, herr := s.KGHexagons.FindFresh(ctx, e.ExplorationID, e.CurrentFocalAtomID); herr == nil {
					dto.NeighborCount = len(hex.Neighbors)
					dto.IsStale = !hex.IsCacheFresh()
				} else {
					dto.IsStale = true // cache miss surfaces as stale
				}
			}
			break // FE shows the first active exploration as the cluster's "current"
		}
	}
	return dto
}

func (s *ExtServer) createKGCluster(w http.ResponseWriter, r *http.Request) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req kgCreateClusterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	req.normalise()
	if strings.TrimSpace(req.SeedTopic) == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_SEED_TOPIC", "seed_topic required")
		return
	}
	// Resolve seed_atom_id when the caller omits it (the A+ canvas shell
	// sends only seedTopic): interim S5.3 resolver over the learner's
	// REAL atom universe — LearningPath.AtomIDs → atom_index, substring
	// match on title/topic. Honest outcomes only: a match, a loud 422
	// (FE maps it to invalid_seed), or a loud 500 on storage failure.
	// The Meilisearch + LLM resolver supersedes this matcher when S5.3
	// proper lands.
	if strings.TrimSpace(req.SeedAtomID) == "" {
		seedAtom, rerr := s.resolveSeedAtom(r.Context(), tenantID, gcid, req.SeedTopic)
		switch {
		case rerr == nil && seedAtom != "":
			req.SeedAtomID = seedAtom
		case rerr == nil:
			extWriteError(w, http.StatusUnprocessableEntity, "INVALID_SEED",
				"no atom in your learning universe matches the seed topic — try a topic from your enrolled courses")
			return
		default:
			extWriteError(w, http.StatusInternalServerError, "SEED_RESOLVE_FAILED", rerr.Error())
			return
		}
	}

	// Cap enforcement.
	cfg, err := s.KGConfig.ReadKnowledgeGraphConfig(r.Context(), tenantID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TENANT_CONFIG_FAILED", err.Error())
		return
	}
	count, err := s.KGClusters.CountActiveByUser(r.Context(), tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "COUNT_FAILED", err.Error())
		return
	}
	if count >= cfg.MaxConcurrentClustersPerUser {
		extWriteJSON(w, http.StatusTooManyRequests, map[string]any{
			"code":                    "CLUSTER_CAP_REACHED",
			"message":                 "max_concurrent_kg_clusters_per_user reached",
			"active_count":            count,
			"max_concurrent_per_user": cfg.MaxConcurrentClustersPerUser,
		})
		return
	}

	c, err := userknowledgegraph.NewMapCluster(tenantID, gcid, req.SeedTopic, req.SeedAtomID, req.DisplayName)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "INVALID_CLUSTER", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	exp, err := userknowledgegraph.NewExploration(c.ClusterID, tenantID, gcid, c.SeedAtomID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "EXPLORATION_FAILED", err.Error())
		return
	}
	if err := s.KGExplorations.Save(r.Context(), exp); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_EXPLORATION_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishMapClusterCreated(ctx, c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	if err := s.KGEvents.PublishExplorationCreated(ctx, exp); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	// Junction detection across the user's other active clusters: if any
	// of the new cluster's atoms-of-interest (currently just seed_atom_id)
	// match an existing focal atom in another cluster of this user, emit
	// a persistent Junction record. Errors here are non-fatal — the
	// cluster is created either way.
	s.wireKGDetector()
	if s.KGJunctionDetector != nil {
		_, _ = s.KGJunctionDetector.DetectForCluster(
			ctx, tenantID, gcid, c.ClusterID, []string{c.SeedAtomID},
		)
	}

	extWriteJSON(w, http.StatusCreated, kgCreateClusterResp{
		Cluster:            toKGClusterDTO(c),
		InitialExploration: toKGExplorationDTO(exp),
	})
}

// resolveSeedAtom maps a seed topic onto an atom from the learner's
// universe (LearningPath.AtomIDs → atom_index — the daily-dose seam).
// Match: case-insensitive substring against title and topic tags, first
// hit in path order wins (deterministic). Returns ("", nil) when nothing
// matches — the caller renders the honest 422.
func (s *ExtServer) resolveSeedAtom(ctx context.Context, tenantID, gcid, seedTopic string) (string, error) {
	needle := strings.ToLower(strings.TrimSpace(seedTopic))
	if needle == "" {
		return "", nil
	}
	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, 100)
	if err != nil {
		return "", fmt.Errorf("seed resolver: list paths: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		for _, atomID := range p.AtomIDs {
			if seen[atomID] {
				continue
			}
			seen[atomID] = true
			entry, aerr := s.AtomIndex.Get(ctx, atomID)
			if aerr != nil || entry == nil {
				// Unprojected atoms are an honest omission (the projection
				// lands event-driven); storage failures surface via the
				// entries that DO resolve — a fully failing index yields
				// the 422 path, never fabricated matches.
				continue
			}
			if strings.Contains(strings.ToLower(entry.Title), needle) {
				return atomID, nil
			}
			for _, tag := range entry.TopicTags {
				if strings.Contains(strings.ToLower(tag), needle) {
					return atomID, nil
				}
			}
		}
	}
	return "", nil
}

func (s *ExtServer) handleKGClusterByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/knowledge-graph/clusters/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_CLUSTER_ID", "")
		return
	}
	clusterID := parts[0]

	switch len(parts) {
	case 1:
		switch r.Method {
		case http.MethodGet:
			s.getKGClusterByID(w, r, clusterID)
		case http.MethodDelete:
			s.archiveKGCluster(w, r, clusterID)
		default:
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		}
		return
	case 4:
		// .../clusters/{cid}/focal/{atom_id}/fog
		if parts[1] == "focal" && parts[3] == "fog" {
			if r.Method != http.MethodGet {
				extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
				return
			}
			s.getKGFog(w, r, clusterID, parts[2])
			return
		}
	case 5:
		// .../clusters/{cid}/focal/{atom_id}/promote/{neighbor_id}
		if parts[1] == "focal" && parts[3] == "promote" {
			if r.Method != http.MethodPost {
				extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
				return
			}
			s.promoteFocal(w, r, clusterID, parts[2], parts[4])
			return
		}
	}
	extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
}

func (s *ExtServer) getKGClusterByID(w http.ResponseWriter, r *http.Request, clusterID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	// RLS leak guard.
	if c.TenantID != tenantID || c.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	exps, _ := s.KGExplorations.ListByCluster(r.Context(), clusterID)
	expDTOs := make([]kgExplorationDTO, 0, len(exps))
	for _, e := range exps {
		expDTOs = append(expDTOs, toKGExplorationDTO(e))
	}
	extWriteJSON(w, http.StatusOK, kgClusterDetail{
		kgClusterDTO: toKGClusterDTO(c),
		Explorations: expDTOs,
	})
}

func (s *ExtServer) archiveKGCluster(w http.ResponseWriter, r *http.Request, clusterID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if c.TenantID != tenantID || c.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if err := c.Archive(); err != nil {
		if errors.Is(err, userknowledgegraph.ErrAlreadyArchived) {
			// Idempotent — return the current state.
			extWriteJSON(w, http.StatusOK, toKGClusterDTO(c))
			return
		}
		extWriteError(w, http.StatusConflict, "ARCHIVE_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishMapClusterArchived(ctx, c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, toKGClusterDTO(c))
}

func (s *ExtServer) getKGFog(w http.ResponseWriter, r *http.Request, clusterID, atomID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	// Verify cluster ownership.
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	// Find the cached hexagon. The fog is keyed by (exploration_id,
	// focal_atom_id); we need the active exploration for this cluster.
	exps, _ := s.KGExplorations.ListByCluster(r.Context(), clusterID)
	var hex *userknowledgegraph.HexagonNode
	for _, e := range exps {
		h, err := s.KGHexagons.FindFresh(r.Context(), e.ExplorationID, atomID)
		if err == nil {
			hex = h
			break
		}
	}
	if hex == nil {
		// Cache miss / stale — A-KG-FogOrch's responsibility to
		// regenerate (out of S5.2 scope). Return 404 with hint.
		extWriteError(w, http.StatusNotFound, "FOG_CACHE_MISS",
			"hexagon fog not cached; trigger fog generation via FogOrchestrator")
		return
	}
	// Surface JunctionOpportunity entries based on the hex's IsJunction
	// flag set by the FogOrchestrator (out-of-scope for S5.2 — left as
	// empty list when none flagged).
	junctions := make([]kgJunctionOpportDTO, 0)
	for _, n := range hex.Neighbors {
		if n.IsJunction {
			junctions = append(junctions, kgJunctionOpportDTO{
				ViaAtomID:                 n.AtomID,
				CurrentClusterID:          hex.ClusterID,
				CurrentClusterDisplayName: c.DisplayName,
			})
		}
	}
	// W5 (Epic-1b): paint the Growth-Edge read-overlay (focal via atom_index
	// topic tags; neighbors via fog labels). DTO-only — the cached hexagon
	// node is never mutated.
	hexDTO := toKGHexagonDTO(hex)
	s.annotateFogOverlay(r.Context(), &hexDTO, s.learnerGrowthPaint(r.Context(), tenantID, gcid))
	extWriteJSON(w, http.StatusOK, kgFogResponse{
		Hexagon:   hexDTO,
		Junctions: junctions,
	})
}

func (s *ExtServer) promoteFocal(w http.ResponseWriter, r *http.Request, clusterID, atomID, neighborID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	c, err := s.KGClusters.Load(r.Context(), clusterID)
	if err != nil || c.TenantID != tenantID || c.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", "")
		return
	}
	if c.Status != userknowledgegraph.ClusterStatusActive {
		extWriteError(w, http.StatusConflict, "CLUSTER_INACTIVE", "")
		return
	}
	// Find the active exploration whose current focal == atomID.
	exps, _ := s.KGExplorations.ListByCluster(r.Context(), clusterID)
	var exp *userknowledgegraph.Exploration
	for _, e := range exps {
		if e.CurrentFocalAtomID == atomID && e.Status == userknowledgegraph.ExplorationStatusActive {
			exp = e
			break
		}
	}
	if exp == nil {
		extWriteError(w, http.StatusNotFound, "EXPLORATION_NOT_FOUND",
			"no active exploration with current focal = atom_id")
		return
	}
	// Verify the neighbor is in the cached hexagon's 6 candidates.
	hex, err := s.KGHexagons.FindFresh(r.Context(), exp.ExplorationID, atomID)
	if err != nil {
		extWriteError(w, http.StatusUnprocessableEntity, "FOG_NOT_CACHED",
			"hexagon for focal not in cache; cannot validate neighbor")
		return
	}
	relation := userknowledgegraph.NeighborRelationCuriosityJump
	matched := false
	for _, n := range hex.Neighbors {
		if n.AtomID == neighborID {
			matched = true
			relation = n.Relation
			break
		}
	}
	if !matched {
		extWriteError(w, http.StatusUnprocessableEntity, "NEIGHBOR_NOT_IN_HEX",
			"target atom is not one of the cached 6 neighbors")
		return
	}
	// Move focal + append trail hop atomically.
	if err := exp.MoveFocal(neighborID); err != nil {
		extWriteError(w, http.StatusConflict, "MOVE_FAILED", err.Error())
		return
	}
	idx, _ := s.KGExplorations.LatestStepIndex(r.Context(), exp.ExplorationID)
	hop, err := userknowledgegraph.NewTrailHop(
		exp.ExplorationID, c.ClusterID, tenantID, gcid,
		atomID, neighborID,
		relation,
		idx+1,
	)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRAIL_FAILED", err.Error())
		return
	}
	if err := s.KGExplorations.AppendTrailHop(r.Context(), exp, hop); err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRAIL_SAVE_FAILED", err.Error())
		return
	}
	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishExplorationFocalChanged(ctx, exp, hop); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, kgPromoteResp{
		Exploration: toKGExplorationDTO(exp),
		TrailHop: kgTrailHopDTO{
			TrailID:         hop.TrailID,
			ExplorationID:   hop.ExplorationID,
			FromFocalAtomID: hop.FromFocalAtomID,
			ToFocalAtomID:   hop.ToFocalAtomID,
			Relation:        string(hop.Relation),
			StepIndex:       hop.StepIndex,
			TraversedAt:     hop.TraversedAt,
		},
	})
}

// ---------- /v1/me/knowledge-graph/junctions/{junction_id}/(accept|reject) ----------

func (s *ExtServer) handleKGJunctionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/knowledge-graph/junctions/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_JUNCTION_ID", "")
		return
	}
	jID := parts[0]
	switch parts[1] {
	case "accept":
		if r.Method != http.MethodPost {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.acceptKGJunction(w, r, jID)
	case "reject":
		if r.Method != http.MethodPost {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.rejectKGJunction(w, r, jID)
	default:
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

func (s *ExtServer) acceptKGJunction(w http.ResponseWriter, r *http.Request, junctionID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	j, err := s.KGJunctions.Load(r.Context(), junctionID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	if j.TenantID != tenantID || j.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	var req kgJunctionAcceptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	now := time.Now().UTC()
	if err := j.Accept(req.ViaAtomID, now); err != nil {
		extWriteError(w, http.StatusConflict, "ACCEPT_FAILED", err.Error())
		return
	}
	if err := s.KGJunctions.Save(r.Context(), j); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	// Trigger the cluster merge — the smaller cluster (by node_count)
	// is marked merged_into the larger.
	cA, errA := s.KGClusters.Load(r.Context(), j.ClusterAID)
	cB, errB := s.KGClusters.Load(r.Context(), j.ClusterBID)
	if errA != nil || errB != nil {
		extWriteError(w, http.StatusInternalServerError, "CLUSTER_LOAD_FAILED", "")
		return
	}
	survivor, merged := cA, cB
	if cB.NodeCount > cA.NodeCount {
		survivor, merged = cB, cA
	}
	if err := merged.MergeInto(survivor.ClusterID, req.ViaAtomID); err != nil {
		extWriteError(w, http.StatusConflict, "MERGE_FAILED", err.Error())
		return
	}
	if err := survivor.AbsorbMerged(merged, req.ViaAtomID); err != nil {
		extWriteError(w, http.StatusConflict, "ABSORB_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), survivor); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_SURVIVOR_FAILED", err.Error())
		return
	}
	if err := s.KGClusters.Save(r.Context(), merged); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_MERGED_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishJunctionAccepted(ctx, j); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	if err := s.KGEvents.PublishMapClusterMerged(ctx, survivor, merged, req.ViaAtomID); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	survivorDTO := toKGClusterDTO(survivor)
	mergedDTO := toKGClusterDTO(merged)
	extWriteJSON(w, http.StatusOK, kgJunctionResolveResp{
		Junction:         toKGJunctionDTO(j),
		SurvivingCluster: &survivorDTO,
		MergedCluster:    &mergedDTO,
	})
}

func (s *ExtServer) rejectKGJunction(w http.ResponseWriter, r *http.Request, junctionID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	j, err := s.KGJunctions.Load(r.Context(), junctionID)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	if j.TenantID != tenantID || j.UserGCID != gcid {
		extWriteError(w, http.StatusNotFound, "JUNCTION_NOT_FOUND", "")
		return
	}
	if err := j.Reject(time.Now().UTC()); err != nil {
		extWriteError(w, http.StatusConflict, "REJECT_FAILED", err.Error())
		return
	}
	if err := s.KGJunctions.Save(r.Context(), j); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	traceparent, tracestate := extTraceFromHeaders(r)
	ctx := events.WithTrace(r.Context(), traceparent, tracestate)
	if err := s.KGEvents.PublishJunctionRejected(ctx, j); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, kgJunctionResolveResp{
		Junction: toKGJunctionDTO(j),
	})
}
