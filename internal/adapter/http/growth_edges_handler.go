// growth_edges_handler.go — A+ Growth-Edges read API (Epic-1b W8a).
//
// Learner-facing REST over the LearnerWeakness aggregate, per the FROZEN
// chora-contracts/openapi/consumption-growth-edges.yaml:
//
//	GET    /v1/me/growth-edges              list (filter + sort + paginate)
//	GET    /v1/me/growth-edges/{id}         read one (descriptor + cached drills)
//	DELETE /v1/me/growth-edges/{id}         soft-delete (dismiss)
//
// Learner-scoped: the GCID is the validated session subject (from headers via
// extRequireContext), NEVER a query param — a learner only ever reads/writes
// their own edges. The repo's RLS + the by-id leak guard enforce isolation.
//
// The upload producer + poll (POST/GET .../uploads) are W8b.
package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const growthEdgesByIDPrefix = "/v1/me/growth-edges/"

// growthEdgeDTO is the wire shape of one Growth Edge (contract: GrowthEdge).
// The learner-facing term is "Growth Edge"; the data is the internal weakness.
type growthEdgeDTO struct {
	ID                 string        `json:"id"`
	ConceptKey         string        `json:"concept_key"`
	ConceptLabel       string        `json:"concept_label"`
	Category           string        `json:"category,omitempty"`
	Tags               []string      `json:"tags"`
	TopicID            string        `json:"topic_id,omitempty"`
	Strength           float64       `json:"strength"`
	Sources            []string      `json:"sources"`
	Descriptor         lw.Descriptor `json:"descriptor"`
	CachedDrillAtomIDs []string      `json:"cached_drill_atom_ids"`
	Status             string        `json:"status"`
	FirstSeenAt        time.Time     `json:"first_seen_at"`
	LastEvidencedAt    time.Time     `json:"last_evidenced_at"`
}

type growthEdgesListResp struct {
	Items         []growthEdgeDTO `json:"items"`
	NextPageToken string          `json:"next_page_token,omitempty"`
}

func growthEdgeToDTO(e lw.LearnerWeakness) growthEdgeDTO {
	sources := make([]string, 0, len(e.Sources))
	for _, s := range e.Sources {
		sources = append(sources, string(s))
	}
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	drills := e.CachedDrillAtomIDs
	if drills == nil {
		drills = []string{}
	}
	return growthEdgeDTO{
		ID:                 e.ID,
		ConceptKey:         e.ConceptKey,
		ConceptLabel:       e.ConceptLabel,
		Category:           e.Category,
		Tags:               tags,
		TopicID:            e.TopicID,
		Strength:           e.Strength,
		Sources:            sources,
		Descriptor:         e.Descriptor,
		CachedDrillAtomIDs: drills,
		Status:             string(e.Status),
		FirstSeenAt:        e.FirstSeenAt,
		LastEvidencedAt:    e.LastEvidencedAt,
	}
}

// handleMeGrowthEdges — GET /v1/me/growth-edges (list).
func (s *ExtServer) handleMeGrowthEdges(w http.ResponseWriter, r *http.Request) {
	if s.LearnerWeakness == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GROWTH_EDGES_UNAVAILABLE", "learner weakness repo not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	q := parseGrowthEdgeListQuery(r, tenantID, gcid)
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	// Read the FULL filtered set, then read-time roll up near-duplicate edges on
	// the same category/topic before paginating — the rollup the contract +
	// migration 0046 promised but never implemented. Rolling up over the whole
	// set (not a raw page) keeps each bucket's representative on a single page,
	// so next_page_token walks distinct rolled-up edges. List() stays raw for the
	// dose / KG-paint / chat overlays, which drill individual concepts.
	all, err := s.LearnerWeakness.ListAll(ctx, q)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	res := lw.RollupPage(all, q.Sort, q.PageToken, q.PageSize)
	items := make([]growthEdgeDTO, 0, len(res.Items))
	for _, e := range res.Items {
		items = append(items, growthEdgeToDTO(e))
	}
	extWriteJSON(w, http.StatusOK, growthEdgesListResp{Items: items, NextPageToken: res.NextPageToken})
}

// handleMeGrowthEdgeByID — GET / DELETE /v1/me/growth-edges/{id}.
func (s *ExtServer) handleMeGrowthEdgeByID(w http.ResponseWriter, r *http.Request) {
	if s.LearnerWeakness == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GROWTH_EDGES_UNAVAILABLE", "learner weakness repo not wired")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, growthEdgesByIDPrefix), "/")
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	switch r.Method {
	case http.MethodGet:
		e, err := s.LearnerWeakness.Get(ctx, gcid, id)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
			return
		}
		// Not found OR cross-learner/tenant leak → 404 (never reveal another's edge).
		if e == nil || e.LearnerGCID != gcid || e.TenantID != tenantID {
			extWriteError(w, http.StatusNotFound, "GROWTH_EDGE_NOT_FOUND", "")
			return
		}
		extWriteJSON(w, http.StatusOK, growthEdgeToDTO(*e))
	case http.MethodDelete:
		if err := s.LearnerWeakness.SoftDelete(ctx, gcid, id, time.Now().UTC()); err != nil {
			extWriteError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

// parseGrowthEdgeListQuery builds the repo ListQuery from the request, scoping to
// the session learner (TenantID + LearnerGCID are NEVER taken from the query).
func parseGrowthEdgeListQuery(r *http.Request, tenantID, gcid string) lw.ListQuery {
	qs := r.URL.Query()
	q := lw.ListQuery{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Category:    strings.TrimSpace(qs.Get("category")),
		TopicID:     strings.TrimSpace(qs.Get("topic_id")),
		Sort:        lw.SortStrengthDesc,
	}
	for _, t := range qs["tag"] {
		if t = strings.TrimSpace(t); t != "" {
			q.Tags = append(q.Tags, t)
		}
	}
	if v := strings.TrimSpace(qs.Get("min_strength")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			q.MinStrength = f
		}
	}
	if v := strings.TrimSpace(qs.Get("include_grown")); v != "" {
		q.IncludeGrown, _ = strconv.ParseBool(v)
	}
	switch lw.ListSort(strings.TrimSpace(qs.Get("sort"))) {
	case lw.SortLastEvidencedDesc:
		q.Sort = lw.SortLastEvidencedDesc
	case lw.SortFirstSeenDesc:
		q.Sort = lw.SortFirstSeenDesc
	case lw.SortStrengthDesc:
		q.Sort = lw.SortStrengthDesc
	}
	if v := strings.TrimSpace(qs.Get("page_size")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.PageSize = n // repo clamps to the accepted set
		}
	}
	q.PageToken = strings.TrimSpace(qs.Get("page_token"))
	return q
}
