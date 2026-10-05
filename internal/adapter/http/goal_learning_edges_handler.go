// goal_learning_edges_handler.go — the unified ceremony "learning-edges" write
// surface (CHO-2038, ADR-212/214). POST /v1/me/goals/{id}/learning-edges: the
// learner confirms the edges their Companion's crawl proposed at the summon
// ceremony; each ticked edge mints a ConceptNode + a hierarchy edge under the
// goal's root concept (so it lands in the goal's subtree and paints on the map),
// carrying a remediate|explore intent ("Both, labelled").
//
// Learner-scoped: GCID is the validated session subject (extRequireContext),
// never a body param; the goal is ownership-checked (404 on any leak). The mint +
// hierarchy-linking are one atomic batch (LearningEdgeApplier). Dispatched from
// handleMeGoalByID when the path carries the learning-edges sub-resource.
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
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// learningEdgesSuffix is the ceremony learning-edges sub-resource path segment.
const learningEdgesSuffix = "learning-edges"

// learningEdgeSelReq is one ticked ceremony learning-edge in the request.
type learningEdgeSelReq struct {
	Title    string   `json:"title"`
	Intent   string   `json:"intent"` // "remediate" | "explore"
	AtomRefs []string `json:"atomRefs,omitempty"`
}

// learningEdgesReq is the ceremony's confirmed selection for one goal.
type learningEdgesReq struct {
	Edges []learningEdgeSelReq `json:"edges"`
}

// mintedEdgeResp is one minted concept + its hierarchy edge (the FE paints these).
type mintedEdgeResp struct {
	ConceptID string `json:"conceptId"`
	Title     string `json:"title"`
	Intent    string `json:"intent"`
	EdgeID    string `json:"edgeId"`
}

// handleMeGoalLearningEdges — POST /v1/me/goals/{id}/learning-edges (CHO-2038).
// goalID is the already-parsed path segment (from handleMeGoalByID's dispatch).
func (s *ExtServer) handleMeGoalLearningEdges(w http.ResponseWriter, r *http.Request, goalID string) {
	if s.Goals == nil || s.LearningEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "LEARNING_EDGES_UNAVAILABLE", "goal repo / learning-edge applier not wired")
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

	var req learningEdgesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	// Resolve + own the goal (never reveal another learner's goal → 404).
	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	sels := make([]conceptgraph.LearningEdgeSelection, 0, len(req.Edges))
	for _, e := range req.Edges {
		sels = append(sels, conceptgraph.LearningEdgeSelection{
			Title:    e.Title,
			Intent:   conceptgraph.Intent(strings.TrimSpace(e.Intent)),
			AtomRefs: e.AtomRefs,
		})
	}

	root := ""
	if g.RootConceptID != nil {
		root = *g.RootConceptID
	}
	minted, err := conceptgraph.SelectLearningEdges(conceptgraph.SelectLearningEdgesInput{
		TenantID:      tenantID,
		LearnerGCID:   gcid,
		RootConceptID: root,
		Selections:    sels,
		Now:           time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, conceptgraph.ErrInvalid) {
			// Blank root / unlabelled intent / blank title / empty batch.
			extWriteError(w, http.StatusUnprocessableEntity, "INVALID_LEARNING_EDGES", err.Error())
			return
		}
		extWriteError(w, http.StatusInternalServerError, "SELECT_FAILED", err.Error())
		return
	}
	if err := s.LearningEdges.ApplyLearningEdges(ctx, minted); err != nil {
		extWriteError(w, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	// ADR-244 D4: a ceremony-minted concept can carry drill atoms.
	for i := range minted {
		s.emitConceptMinted(ctx, minted[i].Node, events.ChangeSourceCeremonyEdge)
	}

	// CHO-2043 — map↔list consistency. The map paints a concept shaky from
	// concept_nodes.intent='remediate', but the growth-edges list + drawer
	// diagnosis are backed only by learner_weakness. A remediate learning-edge
	// IS the learner declaring a weakness to shore up, so mint an unevidenced
	// backing edge for each — the map paint, the list, and the diagnosis then
	// share one backing. Explore edges are curiosity, not gaps → no weakness.
	// Best-effort after the concept mint (the primary artifact): a failure is
	// loud, never fails the whole ceremony (concepts already landed).
	s.mintCeremonyWeaknesses(ctx, tenantID, gcid, minted)

	out := make([]mintedEdgeResp, 0, len(minted))
	for _, m := range minted {
		out = append(out, mintedEdgeResp{
			ConceptID: m.Node.ConceptID,
			Title:     m.Node.Title,
			Intent:    string(m.Intent),
			EdgeID:    m.Edge.EdgeID,
		})
	}
	extWriteJSON(w, http.StatusCreated, map[string]any{"learningEdges": out})
}

// mintCeremonyWeaknesses seeds an unevidenced learner_weakness for every
// REMEDIATE minted edge (CHO-2043), so the growth-edges list + drawer diagnosis
// can join what the map paints shaky from concept_nodes.intent. The seed is a
// real (embedded) but declared-not-evidenced edge: source=ceremony, empty
// descriptor, no cached drills → it stays out of the daily-dose/Ebbinghaus math
// until a real diagnosis enriches strength + descriptor. The repo's Upsert folds
// a re-tick into the existing edge (embedding-cosine dedup), so this is
// idempotent. Best-effort + fail-loud: a per-edge failure is logged (the concept
// is already minted + shaky on the map) but never fails the ceremony.
func (s *ExtServer) mintCeremonyWeaknesses(ctx context.Context, tenantID, gcid string, minted []conceptgraph.MintedLearningEdge) {
	if s.LearnerWeakness == nil || s.WeaknessEmbedder == nil {
		for _, m := range minted {
			if m.Intent == conceptgraph.IntentRemediate {
				log.Printf("consumption: ceremony weakness backing SKIPPED (LearnerWeakness/WeaknessEmbedder not wired) — concept %q will read shaky on the map with no growth-edges list row", m.Node.Title)
				break
			}
		}
		return
	}
	now := time.Now().UTC()
	for _, m := range minted {
		if m.Intent != conceptgraph.IntentRemediate {
			continue
		}
		title := m.Node.Title
		emb, err := s.WeaknessEmbedder.Embed(ctx, title, tenantID)
		if err != nil {
			log.Printf("consumption: ceremony weakness embed FAILED for %q: %v (map shaky, list row missing until re-tick/diagnosis)", title, err)
			continue
		}
		if _, err := s.LearnerWeakness.Upsert(ctx, lw.UpsertInput{
			TenantID:     tenantID,
			LearnerGCID:  gcid,
			ConceptLabel: title,
			Embedding:    emb,
			Strength:     lw.CeremonySeedStrength,
			Source:       lw.SourceCeremony,
			Now:          now,
		}); err != nil {
			log.Printf("consumption: ceremony weakness upsert FAILED for %q: %v (map shaky, list row missing until re-tick/diagnosis)", title, err)
			continue
		}
	}
}
