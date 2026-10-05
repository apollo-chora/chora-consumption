// concept_graph_handler.go — learner-sovereign Discovery concept-graph HTTP
// surface (ADR-212 WS-2 re-root, CHO-1995 wire-up).
//
//	GET  /v1/me/concept-graph          list the learner's concepts + edges
//	POST /v1/me/concept-graph/reroot   re-parent the hierarchy so a chosen
//	                                   concept becomes the apex (append-only)
//
// Learner-scoped: the GCID is the validated session subject (headers via
// extRequireContext), NEVER a body/query param — a learner only reads/re-roots
// their own graph (RLS + the by-(tenant,learner) repo scoping enforce isolation).
//
// Re-root is the pure append-only engine (conceptgraph.ReRoot): it loads the
// learner's live concepts + edges, computes the flip plan (tombstone old
// parent->child hierarchy edges, create the reversed child->parent ones), and
// applies it in one transaction via the ReRootApplier. An already-apex target
// is an honest no-op (Apply skipped, changed=false).
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// wire DTOs — camelCase, mirroring the KG canvas FE shapes.

type conceptNodeDTO struct {
	ConceptID string   `json:"conceptId"`
	Title     string   `json:"title"`
	AtomRefs  []string `json:"atomRefs"`
	// GrowthEdge is the read-time weakness overlay (shaky strength + orthogonal
	// due-⏰), painted when the concept's title slug-matches one of the learner's
	// ACTIVE Growth Edges. Omitted when the concept has no shaky frontier
	// (WS-A1, My Knowledge unification — feeds the FE Shaky lens).
	GrowthEdge *kgGrowthEdgeFEDTO `json:"growthEdge,omitempty"`
	// Mastered flags a concept the learner has mastered (a grown Growth Edge —
	// ADR-213 personal axis). Grown edges are excluded from the shaky overlay, so
	// a mastered concept reads mastered:true with no growthEdge (feeds the
	// Mastery lens).
	Mastered bool `json:"mastered,omitempty"`
	// Provenance records HOW the concept entered the map (learner_authored /
	// companion_suggested_accepted / system_derived) — the WS-C canvas paints a
	// ● you / ✨ Companion badge from it (ADR-212 D4).
	Provenance string `json:"provenance,omitempty"`
	// Intent labels a ceremony learning-edge (CHO-2038): remediate (a weakness to
	// shore up) or explore (an adjacent curiosity). Empty for a plain concept. Feeds
	// the "Both, labelled" render + is the effect-a target_growth_edges filter
	// (remediate) the next-assessment generation reads off this map.
	Intent string `json:"intent,omitempty"`
	// SubGoal is the learner's per-node objective (ADR-247 D1). The drawer renders
	// + inline-edits it; empty when unset. SubGoalProvenance carries who authored
	// it (learner_authored primary) for the drawer provenance pill.
	SubGoal           string `json:"subGoal,omitempty"`
	SubGoalProvenance string `json:"subGoalProvenance,omitempty"`
	// Roads names the course-bound LearningPaths whose atoms overlap this
	// concept's AtomRefs (C4, plan §8: the drawer's "also on the certificate
	// path, module N"). Painted only by the MAP read, which carries the
	// accompanying roadsPartial flag; the flat /v1/me/concept-graph read has no
	// such flag and so never paints it. See maps_roads.go.
	Roads []conceptRoadDTO `json:"roads,omitempty"`
}

type conceptEdgeDTO struct {
	EdgeID          string `json:"edgeId"`
	SourceConceptID string `json:"sourceConceptId"`
	TargetConceptID string `json:"targetConceptId"`
	Class           string `json:"class"`
	Provenance      string `json:"provenance"`
}

type conceptGraphResp struct {
	Concepts []conceptNodeDTO `json:"concepts"`
	Edges    []conceptEdgeDTO `json:"edges"`
}

type rerootReq struct {
	NewRootID string `json:"newRootId"`
}

type rerootResp struct {
	NewRootID    string `json:"newRootId"`
	Changed      bool   `json:"changed"`
	EdgesFlipped int    `json:"edgesFlipped"`
}

// handleMeConceptGraph — GET /v1/me/concept-graph. Lists the learner's live
// concepts + edges (the graph the FE re-roots against).
func (s *ExtServer) handleMeConceptGraph(w http.ResponseWriter, r *http.Request) {
	if s.Concepts == nil || s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "concept graph repos not wired")
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
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	// Read-time overlays (WS-A1): paint each concept with the learner's Growth-Edge
	// shaky/due frontier (kg_growth_overlay engine) + a mastered (grown) flag. Both
	// are fail-soft enrichment — a nil/erroring repo yields an unpainted graph, the
	// read never breaks.
	paint := s.learnerGrowthPaint(ctx, tenantID, gcid)
	mastered := s.masteredConceptKeys(ctx, tenantID, gcid)
	ancestors := s.learnerLineageKeys(ctx, tenantID, gcid)
	extWriteJSON(w, http.StatusOK, toConceptGraphRespPainted(nodes, edges, paint, mastered, ancestors))
}

// handleMeConceptGraphReroot — POST /v1/me/concept-graph/reroot. Body:
// {newRootId}. Loads the learner's concepts + edges, computes the append-only
// re-root plan, and applies it. Already-apex ⇒ 200 changed=false (no write).
func (s *ExtServer) handleMeConceptGraphReroot(w http.ResponseWriter, r *http.Request) {
	if s.Concepts == nil || s.ConceptEdges == nil || s.ConceptReRoot == nil {
		extWriteError(w, http.StatusServiceUnavailable, "CONCEPT_GRAPH_UNAVAILABLE", "re-root repos not wired")
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

	var req rerootReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}

	plan, err := conceptgraph.ReRoot(derefConcepts(nodes), derefEdges(edges), req.NewRootID, time.Now().UTC())
	if err != nil {
		writeReRootError(w, err)
		return
	}
	if !plan.IsNoOp() {
		if err := s.ConceptReRoot.Apply(ctx, plan); err != nil {
			extWriteError(w, http.StatusInternalServerError, "REROOT_APPLY_FAILED", err.Error())
			return
		}
	}
	extWriteJSON(w, http.StatusOK, rerootResp{
		NewRootID:    plan.NewRootID,
		Changed:      !plan.IsNoOp(),
		EdgesFlipped: len(plan.ToCreate),
	})
}

// writeReRootError maps the re-root domain sentinels to HTTP status codes.
func writeReRootError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conceptgraph.ErrReRootUnknownRoot):
		extWriteError(w, http.StatusNotFound, "ROOT_NOT_FOUND", err.Error())
	case errors.Is(err, conceptgraph.ErrReRootMultipleParents):
		extWriteError(w, http.StatusConflict, "REROOT_AMBIGUOUS", err.Error())
	case errors.Is(err, conceptgraph.ErrReRootCycle):
		extWriteError(w, http.StatusConflict, "REROOT_CYCLE", err.Error())
	case errors.Is(err, conceptgraph.ErrConceptDeleted):
		extWriteError(w, http.StatusUnprocessableEntity, "ROOT_DELETED", err.Error())
	case errors.Is(err, conceptgraph.ErrInvalid):
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_REROOT", err.Error())
	default:
		extWriteError(w, http.StatusInternalServerError, "REROOT_FAILED", err.Error())
	}
}

// derefConcepts / derefEdges collapse the repo's []*T into the []T value slices
// the pure ReRoot engine reads (it treats them as read-only). Nils are skipped.
func derefConcepts(in []*conceptgraph.ConceptNode) []conceptgraph.ConceptNode {
	out := make([]conceptgraph.ConceptNode, 0, len(in))
	for _, n := range in {
		if n != nil {
			out = append(out, *n)
		}
	}
	return out
}

func derefEdges(in []*conceptgraph.Edge) []conceptgraph.Edge {
	out := make([]conceptgraph.Edge, 0, len(in))
	for _, e := range in {
		if e != nil {
			out = append(out, *e)
		}
	}
	return out
}

func toConceptGraphResp(nodes []*conceptgraph.ConceptNode, edges []*conceptgraph.Edge) conceptGraphResp {
	return toConceptGraphRespPainted(nodes, edges, nil, nil, nil)
}

// toConceptGraphRespPainted projects the graph and paints each concept with the
// learner's Growth-Edge overlay (shaky strength + due-⏰) and the mastered
// (grown) flag. Paint keys on the STORED concept_key + lineage ancestor keys
// (WS-C6, ADR-227 addendum #7) — never a slug re-derived from the live title,
// so a rename or a merge/split re-keying cannot silently un-paint. Every input
// is enrichment: nil paint / mastered / ancestors yield an unpainted graph
// (fail-soft — indexing a nil map returns false, matchFE is nil-safe).
func toConceptGraphRespPainted(nodes []*conceptgraph.ConceptNode, edges []*conceptgraph.Edge, paint *growthPaint, mastered map[string]bool, ancestorKeys map[string][]string) conceptGraphResp {
	cs := make([]conceptNodeDTO, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		refs := n.AtomRefs
		if refs == nil {
			refs = []string{}
		}
		dto := conceptNodeDTO{ConceptID: n.ConceptID, Title: n.Title, AtomRefs: refs, Provenance: string(n.Provenance), Intent: string(n.Intent), SubGoal: n.SubGoal, SubGoalProvenance: string(n.SubGoalProvenance)}
		labels := conceptPaintLabels(n, ancestorKeys)
		// ADR-238 M-D3: id-join first (edge.TargetConceptID == this node's id) so a
		// concept_key-slug drift can't un-paint the node; slug-join is the fallback.
		dto.GrowthEdge = paint.matchFEByNode(n.ConceptID, labels...) // nil-safe; nil when no ACTIVE edge matches
		if masteredByLabels(labels, mastered) {
			dto.Mastered = true
		}
		cs = append(cs, dto)
	}
	es := make([]conceptEdgeDTO, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		es = append(es, conceptEdgeDTO{
			EdgeID:          e.EdgeID,
			SourceConceptID: e.SourceConceptID,
			TargetConceptID: e.TargetConceptID,
			Class:           string(e.Class),
			Provenance:      string(e.Provenance),
		})
	}
	return conceptGraphResp{Concepts: cs, Edges: es}
}
