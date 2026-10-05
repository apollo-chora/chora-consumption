// concept_authoring_handler.go — learner authoring surface for the sovereign
// Discovery graph (ADR-212 D1/D2, CJ buildout).
//
//	POST   /v1/me/concept-graph/concepts        create a learner-named concept
//	PATCH  /v1/me/concept-graph/concepts/{id}    rename / attach / detach atoms
//	DELETE /v1/me/concept-graph/concepts/{id}    soft-delete (never hard-delete)
//	POST   /v1/me/concept-graph/edges           author a typed edge (hierarchy|lateral)
//	DELETE /v1/me/concept-graph/edges/{id}       soft-delete an edge
//
// This is the authoring HEAD of the journey: WS-1 built the ConceptNode + Edge
// aggregates + repos; the round-3 wire-up exposed read + re-root; this exposes
// the CREATE/UPDATE/DELETE surface so a learner can actually BUILD their map.
// All mutations go through the domain aggregate methods (NewConceptNode / Rename
// / AttachAtom / DetachAtom / SoftDelete / NewEdge / SoftDelete) — no SQL in the
// handler. Learner-scoped: GCID is the validated session subject; the repo's
// (tenant, learner) scoping + RLS enforce isolation, and GetByID → nil ⇒ 404.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	conceptsByIDPrefix = "/v1/me/concept-graph/concepts/"
	edgesByIDPrefix    = "/v1/me/concept-graph/edges/"
)

// ---- DTOs ----

type conceptCreateReq struct {
	Title    string   `json:"title"`
	AtomRefs []string `json:"atomRefs,omitempty"`
}

type conceptPatchReq struct {
	Title          *string  `json:"title,omitempty"`
	AddAtomRefs    []string `json:"addAtomRefs,omitempty"`
	RemoveAtomRefs []string `json:"removeAtomRefs,omitempty"`
	// SubGoal (ADR-247 D1) sets or clears the learner's per-node objective. A
	// pointer distinguishes "not supplied" (nil, leave unchanged) from "clear"
	// (a supplied empty string). Authored by the learner (learner_authored).
	SubGoal *string `json:"subGoal,omitempty"`
}

type conceptDetailDTO struct {
	ConceptID         string    `json:"conceptId"`
	Title             string    `json:"title"`
	AtomRefs          []string  `json:"atomRefs"`
	Provenance        string    `json:"provenance"`
	SubGoal           string    `json:"subGoal,omitempty"`
	SubGoalProvenance string    `json:"subGoalProvenance,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type edgeCreateReq struct {
	SourceConceptID string `json:"sourceConceptId"`
	TargetConceptID string `json:"targetConceptId"`
	Class           string `json:"class"`
}

type edgeDetailDTO struct {
	EdgeID          string    `json:"edgeId"`
	SourceConceptID string    `json:"sourceConceptId"`
	TargetConceptID string    `json:"targetConceptId"`
	Class           string    `json:"class"`
	Provenance      string    `json:"provenance"`
	CreatedAt       time.Time `json:"createdAt"`
}

func toConceptDetailDTO(c *conceptgraph.ConceptNode) conceptDetailDTO {
	refs := c.AtomRefs
	if refs == nil {
		refs = []string{}
	}
	return conceptDetailDTO{
		ConceptID:         c.ConceptID,
		Title:             c.Title,
		AtomRefs:          refs,
		Provenance:        string(c.Provenance),
		SubGoal:           c.SubGoal,
		SubGoalProvenance: string(c.SubGoalProvenance),
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
	}
}

func toEdgeDetailDTO(e *conceptgraph.Edge) edgeDetailDTO {
	return edgeDetailDTO{
		EdgeID:          e.EdgeID,
		SourceConceptID: e.SourceConceptID,
		TargetConceptID: e.TargetConceptID,
		Class:           string(e.Class),
		Provenance:      string(e.Provenance),
		CreatedAt:       e.CreatedAt,
	}
}

// ---- concept handlers ----

// handleMeConceptCreate — POST /v1/me/concept-graph/concepts.
func (s *ExtServer) handleMeConceptCreate(w http.ResponseWriter, r *http.Request) {
	if s.Concepts == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "concept repo not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	var req conceptCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	c, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Title:       req.Title,
		AtomRefs:    req.AtomRefs,
		Now:         time.Now().UTC(),
	})
	if err != nil {
		writeConceptGraphDomainError(w, err)
		return
	}
	if err := s.Concepts.Create(ctx, c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_CREATE_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusCreated, toConceptDetailDTO(c))
}

// handleMeConceptByID — PATCH (rename/attach/detach) + DELETE (soft) on a concept.
func (s *ExtServer) handleMeConceptByID(w http.ResponseWriter, r *http.Request) {
	if s.Concepts == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "concept repo not wired")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, conceptsByIDPrefix), "/")
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "concept_id required")
		return
	}
	// Sub-resource dispatch (WS-C6, CHO-2085): {id}/merge + {id}/split are the
	// sovereign restructure doors, nested here so the gateway subtree proxy +
	// Istio authz wildcard admit them without new plumbing.
	if base, action, isSub := strings.Cut(id, "/"); isSub {
		switch action {
		case "merge":
			s.handleMeConceptMerge(w, r, base)
		case "split":
			s.handleMeConceptSplit(w, r, base)
		default:
			extWriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown concept sub-resource")
		}
		return
	}
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	c, err := s.Concepts.GetByID(ctx, tenantID, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_GET_FAILED", err.Error())
		return
	}
	if c == nil {
		extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", "")
		return
	}

	if r.Method == http.MethodDelete {
		now := time.Now().UTC()
		c.SoftDelete(now)
		if err := s.Concepts.Update(ctx, c); err != nil {
			extWriteError(w, http.StatusInternalServerError, "CONCEPT_DELETE_FAILED", err.Error())
			return
		}
		// CHO-2324: emit the cascade trigger AFTER the durable soft-delete
		// (persist-then-emit, like the atoms_bound path). Best-effort: an unwired
		// downstream lane must not fail a learner's successful delete.
		s.emitConceptDeleted(ctx, c, now)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// PATCH
	before := append([]string(nil), c.AtomRefs...)
	var req conceptPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	now := time.Now().UTC()
	if req.Title != nil {
		if err := c.Rename(*req.Title, now); err != nil {
			writeConceptGraphDomainError(w, err)
			return
		}
	}
	for _, a := range req.AddAtomRefs {
		if err := c.AttachAtom(a, now); err != nil {
			writeConceptGraphDomainError(w, err)
			return
		}
	}
	for _, a := range req.RemoveAtomRefs {
		if err := c.DetachAtom(a, now); err != nil {
			writeConceptGraphDomainError(w, err)
			return
		}
	}
	// ADR-247 D1: set/clear the learner's per-node objective. A supplied empty
	// string clears it; a nil pointer leaves it unchanged. Learner-authored.
	if req.SubGoal != nil {
		if err := c.SetSubGoal(*req.SubGoal, conceptgraph.ProvenanceLearnerAuthored, now); err != nil {
			writeConceptGraphDomainError(w, err)
			return
		}
	}
	if err := s.Concepts.Update(ctx, c); err != nil {
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_UPDATE_FAILED", err.Error())
		return
	}
	// ADR-244 D4: the binding is a domain event, not a silent side effect.
	// Emitted AFTER the durable write (persist-then-emit) and derived from the
	// ACTUAL before/after sets rather than the request, so an idempotent
	// re-attach reports nothing and a partial change reports only what moved.
	s.emitConceptAtomsBound(ctx, c, before, events.ChangeSourceManualAttach)
	extWriteJSON(w, http.StatusOK, toConceptDetailDTO(c))
}

// ---- edge handlers ----

// handleMeEdgeCreate — POST /v1/me/concept-graph/edges.
func (s *ExtServer) handleMeEdgeCreate(w http.ResponseWriter, r *http.Request) {
	if s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "edge repo not wired")
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	var req edgeCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	class := conceptgraph.EdgeClass(strings.TrimSpace(req.Class))
	if class != conceptgraph.EdgeClassHierarchy && class != conceptgraph.EdgeClassLateral {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_EDGE_CLASS", "class must be hierarchy or lateral")
		return
	}
	e, err := conceptgraph.NewEdge(conceptgraph.NewEdgeInput{
		TenantID:        tenantID,
		LearnerGCID:     gcid,
		SourceConceptID: req.SourceConceptID,
		TargetConceptID: req.TargetConceptID,
		Class:           class,
		Now:             time.Now().UTC(),
	})
	if err != nil {
		writeConceptGraphDomainError(w, err)
		return
	}
	if err := s.ConceptEdges.Create(ctx, e); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EDGE_CREATE_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusCreated, toEdgeDetailDTO(e))
}

// handleMeEdgeByID — DELETE /v1/me/concept-graph/edges/{id} (soft-delete).
func (s *ExtServer) handleMeEdgeByID(w http.ResponseWriter, r *http.Request) {
	if s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "edge repo not wired")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, edgesByIDPrefix), "/")
	if id == "" || strings.Contains(id, "/") {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "edge_id required")
		return
	}
	if r.Method != http.MethodDelete {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	e, err := s.ConceptEdges.GetByID(ctx, tenantID, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "EDGE_GET_FAILED", err.Error())
		return
	}
	if e == nil {
		extWriteError(w, http.StatusNotFound, "EDGE_NOT_FOUND", "")
		return
	}
	e.SoftDelete(time.Now().UTC())
	if err := s.ConceptEdges.Update(ctx, e); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EDGE_DELETE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeConceptGraphDomainError maps concept/edge domain sentinels to HTTP codes.
func writeConceptGraphDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conceptgraph.ErrConceptDeleted), errors.Is(err, conceptgraph.ErrEdgeDeleted):
		extWriteError(w, http.StatusConflict, "ALREADY_DELETED", err.Error())
	case errors.Is(err, conceptgraph.ErrSelfLoop):
		extWriteError(w, http.StatusUnprocessableEntity, "EDGE_SELF_LOOP", err.Error())
	case errors.Is(err, conceptgraph.ErrInvalid):
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID", err.Error())
	default:
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_GRAPH_MUTATION_FAILED", err.Error())
	}
}

// ConceptEvents is the narrow surface the concept doors need to make a binding
// observable (ADR-244 D4). Nil is tolerated: emission is additive telemetry on
// an already-durable write, so a service booted without it still serves the
// learner correctly. It logs loudly rather than failing the request, because
// refusing a learner's successful attach because a downstream lane is unwired
// would be the worse failure.
type ConceptEvents interface {
	ConceptAtomsBound(ctx context.Context, in events.ConceptAtomsBoundInput) error
	// ConceptDeleted reports a concept soft-delete (CHO-2324) so the edge-cleanup
	// subscriber can cascade the incident-edge removal.
	ConceptDeleted(ctx context.Context, in events.ConceptDeletedInput) error
}

// emitConceptAtomsBound diffs before/after and emits the resulting delta.
func (s *ExtServer) emitConceptAtomsBound(ctx context.Context, c *conceptgraph.ConceptNode, before []string, source string) {
	if s.ConceptEvents == nil || c == nil {
		return
	}
	attached, detached := diffAtomRefs(before, c.AtomRefs)
	if len(attached) == 0 && len(detached) == 0 {
		return // idempotent no-op; the emitter refuses these anyway
	}
	tenantID := tracing.TenantIDFromContext(ctx)
	in := events.ConceptAtomsBoundInput{
		TenantID:          tenantID,
		LearnerGCID:       c.LearnerGCID,
		ConceptID:         c.ConceptID,
		AttachedAtomIDs:   attached,
		DetachedAtomIDs:   detached,
		ResultingAtomRefs: append([]string(nil), c.AtomRefs...),
		ChangeSource:      source,
		Provenance:        string(c.Provenance),
		OccurredAt:        c.UpdatedAt,
		Traceparent:       tracing.TraceparentFromContext(ctx),
	}
	if in.TenantID == "" {
		in.TenantID = c.TenantID
	}
	if err := s.ConceptEvents.ConceptAtomsBound(ctx, in); err != nil {
		log.Printf("consumption: concept.atoms_bound emit FAILED concept=%s: %v", c.ConceptID, err)
	}
}

// emitConceptMinted reports a freshly-created concept's bindings (ADR-244 D4).
// A new node has no prior state, so its whole atom_refs set is the attach delta.
// Nodes that are born EMPTY emit nothing: the emitter refuses a no-op, and an
// empty concept is a first-class state rather than a binding event.
func (s *ExtServer) emitConceptMinted(ctx context.Context, c *conceptgraph.ConceptNode, source string) {
	s.emitConceptAtomsBound(ctx, c, nil, source)
}

// emitConceptDeleted reports a concept soft-delete so the edge-cleanup subscriber
// can cascade the incident-edge removal (CHO-2324). Best-effort like
// emitConceptAtomsBound: it logs loudly rather than failing an already-durable
// delete — refusing a learner's successful removal because a downstream lane is
// unwired would be the worse failure.
func (s *ExtServer) emitConceptDeleted(ctx context.Context, c *conceptgraph.ConceptNode, at time.Time) {
	if s.ConceptEvents == nil || c == nil {
		return
	}
	tenantID := tracing.TenantIDFromContext(ctx)
	if tenantID == "" {
		tenantID = c.TenantID
	}
	in := events.ConceptDeletedInput{
		TenantID:    tenantID,
		LearnerGCID: c.LearnerGCID,
		ConceptID:   c.ConceptID,
		OccurredAt:  at,
		Traceparent: tracing.TraceparentFromContext(ctx),
	}
	if err := s.ConceptEvents.ConceptDeleted(ctx, in); err != nil {
		log.Printf("consumption: concept.deleted emit FAILED concept=%s: %v", c.ConceptID, err)
	}
}

// diffAtomRefs returns (added, removed) between two ref sets, order-preserving.
func diffAtomRefs(before, after []string) (added, removed []string) {
	inBefore := make(map[string]bool, len(before))
	for _, r := range before {
		inBefore[r] = true
	}
	inAfter := make(map[string]bool, len(after))
	for _, r := range after {
		inAfter[r] = true
	}
	for _, r := range after {
		if !inBefore[r] {
			added = append(added, r)
		}
	}
	for _, r := range before {
		if !inAfter[r] {
			removed = append(removed, r)
		}
	}
	return added, removed
}
