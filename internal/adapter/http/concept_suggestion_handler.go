// concept_suggestion_handler.go — learner CURATION surface for Companion
// suggestions (ADR-212 WS-4, D4). The demoted "fog" proposes candidate concepts
// + edges (ingested by the WS-4 subscriber as pending suggestions); this surface
// lets the learner review + curate them:
//
//	GET  /v1/me/concept-graph/suggestions              list the learner's pending suggestions
//	POST /v1/me/concept-graph/suggestions/{id}/accept   mint the concept/edge (provenance=companion_suggested_accepted)
//	POST /v1/me/concept-graph/suggestions/{id}/dismiss  decline it (status flip, retained for audit)
//
// All mutations go through the Suggestion aggregate (Accept mints + flips;
// Dismiss flips) — no SQL in the handler. Accept is persisted atomically by the
// SuggestionAccepter (concept/edge insert + status flip in one tx). Learner-
// scoped via extRequireContext (X-Tenant-Id + gcid) + repo (tenant, learner) +
// RLS; GetByID → nil ⇒ 404. Routes nest under /concept-graph so the gateway
// subtree proxy + Istio authz wildcard already cover them (no new plumbing).
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
	"github.com/apollo-chora/chora-consumption/internal/domain"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

const (
	suggestionsByIDPrefix = "/v1/me/concept-graph/suggestions/"
)

// ---- DTO ----

type suggestionDTO struct {
	SuggestionID string   `json:"suggestionId"`
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	Title        string   `json:"title,omitempty"`
	AtomRefs     []string `json:"atomRefs,omitempty"`
	// FocalConceptID is the concept the fog generated this suggestion around
	// (WS-C7 §6): the canvas fans a fog-ghost around this node. Empty (omitted)
	// for a whole-map suggestion. NB: the Suggestion aggregate carries no goalId,
	// so goalId is intentionally absent from this wire (the FE fog filter keys
	// on focalConceptId ∈ map nodes).
	FocalConceptID  string    `json:"focalConceptId,omitempty"`
	SourceConceptID string    `json:"sourceConceptId,omitempty"`
	TargetConceptID string    `json:"targetConceptId,omitempty"`
	Class           string    `json:"class,omitempty"`
	Rationale       string    `json:"rationale,omitempty"`
	ModelID         string    `json:"modelId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

func toSuggestionDTO(s *conceptgraph.Suggestion) suggestionDTO {
	dto := suggestionDTO{
		SuggestionID:    s.SuggestionID,
		Kind:            string(s.Kind),
		Status:          string(s.Status),
		Title:           s.Title,
		FocalConceptID:  s.FocalConceptID,
		SourceConceptID: s.SourceConceptID,
		TargetConceptID: s.TargetConceptID,
		Class:           string(s.EdgeClass),
		Rationale:       s.Rationale,
		ModelID:         s.ModelID,
		CreatedAt:       s.CreatedAt,
	}
	if len(s.AtomRefs) > 0 {
		dto.AtomRefs = s.AtomRefs
	}
	return dto
}

// suggestionGenerateReq is the (optional) body for the generate trigger. Empty
// body ⇒ a whole-map suggestion (no focal). goalId identifies the MAP (WS-A3): the
// map's Companion is resolved from Goal.AttachedCompanionID (the theme-keyed
// CompanionMapBinding is retired).
type suggestionGenerateReq struct {
	FocalConceptID string `json:"focalConceptId,omitempty"`
	GoalID         string `json:"goalId,omitempty"`
}

// handleMeSuggestionGenerate — POST /v1/me/concept-graph/suggestions/generate.
// Publishes chora.consumption.concept_suggestion.requested.v1 (JSON, via the
// durable outbox) so the concept-shaped fog orchestrator proposes concepts/edges
// around the focal concept; the suggestions arrive asynchronously via
// concept_suggestion.emitted.v1. Returns 202 Accepted (fire-and-forget). No
// mana gate here (v1 — the fog call is metered at the model gateway).
//
// WS-C4 (CHO-2083, ADR-227 D2) — hard fog-of-war on goal maps: a focal inside
// any of the learner's goal subtrees is revealed only once that node is WON
// (refuseUnwonGoalReveal), and a whole-map request naming a goalId is refused
// outright. Non-goal territory keeps both request forms free (ADR-212
// sovereignty); manual node authoring is untouched — the gate governs the
// LLM's pen, never the learner's.
func (s *ExtServer) handleMeSuggestionGenerate(w http.ResponseWriter, r *http.Request) {
	if s.Concepts == nil || s.Publisher == nil {
		extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "concept repo / publisher not wired")
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

	var req suggestionGenerateReq
	if r.Body != nil {
		// Empty body is allowed (whole-map suggestion) — ignore EOF.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	focalID := strings.TrimSpace(req.FocalConceptID)

	// ADR-227 D2: a goal map's fog lifts only through wins — an unanchored
	// whole-map sweep against a named goal would reveal children of unwon
	// nodes, so it is refused regardless of whether the goalId resolves (the
	// request's intent to target a goal map is explicit).
	if focalID == "" && strings.TrimSpace(req.GoalID) != "" {
		extWriteError(w, http.StatusConflict, "CAMPAIGN_MAP_REQUIRES_FOCAL", "a goal-map reveal must anchor on a WON focal node (ADR-227 D2)")
		return
	}

	// Resolve the focal concept (if one was given) for its title + atom grounding.
	// The loaded node is retained (focalNode) so the ADR-247 Cap B enrichment can
	// read its sub-goal + concept_key without a second lookup.
	var focalTitle string
	var focalAtomRefs []string
	var focalNode *conceptgraph.ConceptNode
	if focalID != "" {
		focal, err := s.Concepts.GetByID(ctx, tenantID, gcid, focalID)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "CONCEPT_GET_FAILED", err.Error())
			return
		}
		if focal == nil {
			extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", "focal concept not found")
			return
		}
		focalNode = focal
		focalTitle = focal.Title
		focalAtomRefs = focal.AtomRefs
	}

	// The learner's existing concepts (bounded) so the fog doesn't re-propose them
	// and can reference them by id in proposed edges. Excludes the focal itself.
	// Loaded before the gate so the subtree walk reuses the same roster.
	existing := make([]events.ExistingConcept, 0)
	all, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_LIST_FAILED", err.Error())
		return
	}

	// WS-C4: the won-gate for goal-subtree focal nodes. The gate's subtree walk
	// also yields the CONTAINING goal (when the focal sits inside one) — the
	// CHO-2117 goal context for focal-only requests that name no goalId.
	var containingGoal *goal.Goal
	if focalID != "" {
		var handled bool
		containingGoal, handled = s.refuseUnwonGoalReveal(ctx, w, tenantID, gcid, focalID, all)
		if handled {
			return
		}
	}
	for _, c := range all {
		if c.ConceptID == focalID {
			continue
		}
		existing = append(existing, events.ExistingConcept{ConceptID: c.ConceptID, Title: c.Title})
		if len(existing) >= events.MaxExistingConceptsInSuggestionRequest {
			break
		}
	}

	// Resolve the map's designated Companion from its Goal (WS-A3, ADR-214 D1 —
	// the Goal is the single source of truth; the theme-keyed binding is retired).
	// Best-effort: an unattached map (or nil Goals repo / blank goalId) yields an
	// unattributed fog request (companion_id ""), and map_theme is derived from the
	// map's root concept so the fog orchestrator's payload contract is unchanged.
	// CHO-2117: a focal-only request (no goalId — the A+ discovery graph shape)
	// rides the CONTAINING goal the won-gate already resolved, so the goal theme
	// scopes the LLM generation on every goal-map reveal. Loose (non-goal)
	// territory stays unscoped by design (ADR-212 sovereignty).
	companionID := ""
	mapTheme := ""
	themeGoal := containingGoal
	if s.Goals != nil && strings.TrimSpace(req.GoalID) != "" {
		if g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, strings.TrimSpace(req.GoalID)); gerr == nil && g != nil && g.LearnerGCID == gcid && g.TenantID == tenantID {
			themeGoal = g
		}
	}
	if themeGoal != nil {
		if themeGoal.AttachedCompanionID != nil {
			companionID = *themeGoal.AttachedCompanionID
		}
		mapTheme = s.resolveMapTheme(ctx, tenantID, gcid, themeGoal)
	}

	// ADR-244 D2: the catalogue the Companion may cite in atom_refs. Resolved
	// through the SAME ADR-243 entitlement as the manual picker, so the
	// Companion can never propose an atom the learner would be refused on
	// accept. A resolution error degrades to an EMPTY catalogue rather than
	// failing the whole suggestion: the fog still proposes concepts, it just
	// cannot cite atoms, and that is strictly better than no suggestions.
	// ADR-245: the catalogue is ORDERED by topic affinity to this goal's theme
	// and the focal concept, and each entry is banded, so the fog can bind atom
	// choice to the theme instead of receiving a cross-domain menu. Ranking
	// never filters, so an unthemed or unmatched map still gets the full set.
	catalogue := s.ResolveSuggestionCatalogue(ctx, tenantID, gcid, mapTheme, focalTitle, focalAtomRefs)

	// ADR-247 Capability B (CHO-2328): frame the fog request with the focal node's
	// sub-goal + diagnosed weakness + goal/ancestor lineage. Best-effort and
	// nil-tolerant (see buildSuggestionEnrichment); a loose whole-map request has
	// no focal, so every piece degrades to empty and the fog stays curiosity-led.
	subGoal, weakness, goalTitle, ancestors := s.buildSuggestionEnrichment(ctx, tenantID, gcid, focalNode, themeGoal, all)

	requestID := domain.NewUUIDv7()
	tp := tracing.EnsureTraceparent(r.Header.Get("traceparent"))
	ts := r.Header.Get("tracestate")
	env := events.NewEnvelope(tenantID, gcid, tp, ts, "concept_suggestion.requested."+requestID)
	payload := events.SuggestionRequestPayload(events.SuggestionRequestInput{
		TenantID:       tenantID,
		LearnerGCID:    gcid,
		CompanionID:    companionID,
		MapTheme:       mapTheme,
		FocalConceptID: focalID,
		FocalTitle:     focalTitle,
		FocalAtomRefs:  focalAtomRefs,
		Existing:       existing,
		AtomCatalogue:  catalogue,
		RequestedAt:    time.Now().UTC(),
		RequestSource:  events.SuggestionSourceLearnerRequest,
		SubGoal:        subGoal,
		GoalTitle:      goalTitle,
		Ancestors:      ancestors,
		Weakness:       weakness,
	})
	if err := s.Publisher.Publish(events.TopicConceptSuggestionRequested, env, payload); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusAccepted, map[string]any{
		"status":         "requested",
		"requestId":      requestID,
		"focalConceptId": focalID,
	})
}

// buildSuggestionEnrichment assembles the ADR-247 Capability B fog inputs for a
// focal node: its sub-goal (F1), the diagnosed weakness for its concept_key (F2),
// and the goal + ancestor lineage up to the theme goal's root (F3). Mirrors the
// Cap A CampaignDose.buildCampaignPromptContext. Every piece is best-effort: a nil
// focal (a whole-map request), a nil loader / edge repo, or a load error degrades
// that piece to its empty form and the fog keeps today's curiosity/theme prompt.
// It never fails the request - the enrichment is additive on an already-durable
// publish. The wire builder normalises the empty forms (ancestors -> [], weakness
// -> JSON null).
func (s *ExtServer) buildSuggestionEnrichment(ctx context.Context, tenantID, gcid string, focal *conceptgraph.ConceptNode, themeGoal *goal.Goal, roster []*conceptgraph.ConceptNode) (subGoal string, weakness *events.WeaknessPayload, goalTitle string, ancestors []string) {
	if focal == nil {
		return "", nil, "", nil
	}
	subGoal = focal.SubGoal
	if s.WeaknessLoader != nil {
		if wc, err := s.WeaknessLoader.LoadWeaknessContextByConceptKey(ctx, tenantID, gcid, focal.ConceptKey); err == nil && wc != nil {
			weakness = &events.WeaknessPayload{
				Descriptor:     wc.Descriptor,
				Misconceptions: wc.Misconceptions,
				Evidence:       wc.Evidence,
			}
		}
	}
	if s.ConceptEdges != nil && themeGoal != nil {
		if root := derefString(themeGoal.RootConceptID); strings.TrimSpace(root) != "" {
			if edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid); err == nil {
				anc := conceptgraph.WalkAncestors(focal.ConceptID, root, roster, edges)
				goalTitle = anc.GoalTitle
				ancestors = anc.Ancestors
			}
		}
	}
	return subGoal, weakness, goalTitle, ancestors
}

// refuseUnwonGoalReveal enforces ADR-227 D2 (WS-C4, CHO-2083): when the focal
// concept sits inside ANY of the learner's goal subtrees, the LLM reveal of its
// children is earned by WINNING that node (campaign_node_progress.won_at).
// Membership is resolved server-side from the learner's own goals + live
// hierarchy edges (the same conceptgraph.SubtreeConceptIDs walk the frontier
// uses, so gate and frontier cannot disagree) — the request's goalId is never
// trusted for gating. Win state is per-(learner, concept), so the first
// containing goal decides. Loose (non-goal) territory is never gated.
//
// Fail-CLOSED: with a focal present and the campaign wiring absent the request
// is refused 503 — silently skipping the won-check would fail-open the fog.
// Returns (containingGoal, handled): handled=true when a refusal (or error) was
// written; on the WON pass-through, containingGoal is the goal whose subtree
// holds the focal — the CHO-2117 goal context (theme + companion attribution)
// for focal-only requests. nil for loose territory.
func (s *ExtServer) refuseUnwonGoalReveal(ctx context.Context, w http.ResponseWriter, tenantID, gcid, focalID string, roster []*conceptgraph.ConceptNode) (*goal.Goal, bool) {
	if s.Goals == nil || s.ConceptEdges == nil || s.CampaignProgress == nil {
		extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "campaign reveal gate not wired (fail-closed, ADR-227 D2)")
		return nil, true
	}
	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOAL_LIST_FAILED", err.Error())
		return nil, true
	}
	var rooted []*goal.Goal
	for _, g := range goals {
		if g != nil && g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
			rooted = append(rooted, g)
		}
	}
	if len(rooted) == 0 {
		return nil, false // no campaign territory exists — nothing to gate
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "EDGE_LIST_FAILED", err.Error())
		return nil, true
	}
	nodeVals := derefConcepts(roster)
	edgeVals := derefEdges(edges)
	for _, g := range rooted {
		if !conceptgraph.SubtreeConceptIDs(nodeVals, edgeVals, *g.RootConceptID)[focalID] {
			continue
		}
		p, err := s.CampaignProgress.GetByConcept(ctx, tenantID, gcid, focalID)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "CAMPAIGN_PROGRESS_GET_FAILED", err.Error())
			return nil, true
		}
		if p == nil || !p.Won() {
			extWriteError(w, http.StatusConflict, "CAMPAIGN_NODE_NOT_WON", "win this node to reveal its children (ADR-227 D2)")
			return nil, true
		}
		return g, false // won — the reveal is earned (and permanent)
	}
	return nil, false
}

// handleMeSuggestions — GET /v1/me/concept-graph/suggestions (list pending).
func (s *ExtServer) handleMeSuggestions(w http.ResponseWriter, r *http.Request) {
	if s.Suggestions == nil {
		extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "suggestion repo not wired")
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

	// Optional focal scoping (ADR-212 WS-4): with ?focalConceptId=<id> return only
	// that concept's suggestions + whole-map (NULL) ones, so a stale prior-generate
	// batch anchored on a DIFFERENT focal doesn't leak onto this node. No focal ⇒
	// the unfiltered whole-map inbox (back-compat).
	var list []*conceptgraph.Suggestion
	if focal := strings.TrimSpace(r.URL.Query().Get("focalConceptId")); focal != "" {
		if !conceptgraph.IsUUIDShaped(focal) {
			// Fail-loud visibility: a non-UUID focal is a client bug. The repo
			// degrades it to the whole-map inbox (never a 22P02 500), but log it so a
			// recurrence is diagnosable (the 2026-07-22 incident surfaced NO error log).
			log.Printf("consumption: suggestions read, malformed focalConceptId %q (not a UUID); serving whole-map inbox (tenant=%s)", focal, tenantID)
		}
		list, err = s.Suggestions.ListPendingForFocal(ctx, tenantID, gcid, focal)
	} else {
		list, err = s.Suggestions.ListPending(ctx, tenantID, gcid)
	}
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SUGGESTIONS_LIST_FAILED", err.Error())
		return
	}

	// Goal fence (bug #19): a suggestion belongs to exactly ONE map — the goal
	// whose concept subtree holds its focal. concept_suggestions carries no
	// goal_id, and ListPending[ForFocal] union in whole-map (NULL focal) + (when
	// unfocused) EVERY goal's rows, so a sibling goal's fog leaks across maps
	// (Photosynthesis "NEW LINK"s on the Scrum map). When the caller names its
	// map, hard-scope the inbox to that goal's LIVE subtree — the same
	// SubtreeConceptIDs walk refuseUnwonGoalReveal gates on, so the inbox and the
	// reveal-gate cannot disagree. Absent goalId (the unrooted discovery graph)
	// keeps the focal/whole-map behaviour.
	if goalID := strings.TrimSpace(r.URL.Query().Get("goalId")); goalID != "" {
		scoped, handled := s.scopeSuggestionsToGoal(ctx, w, tenantID, gcid, goalID, list)
		if handled {
			return
		}
		list = scoped
	}

	out := make([]suggestionDTO, 0, len(list))
	for _, sug := range list {
		out = append(out, toSuggestionDTO(sug))
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// scopeSuggestionsToGoal narrows a pending-suggestion list to the goal map that
// owns them: rows whose focal_concept_id sits in the goal's LIVE concept subtree
// (SubtreeConceptIDs over root_concept_id — the same walk refuseUnwonGoalReveal
// uses). This is the read-time goal fence for bug #19: concept_suggestions has
// no goal_id, so a flat per-learner read otherwise bleeds a sibling goal's fog
// across maps. A whole-map (empty focal) row is in NO goal subtree, so it is
// correctly dropped on a rooted goal map (the generate door already refuses
// whole-map requests on goal maps, ADR-227 D2).
//
// Fail-closed: goalId present but the scope ports unwired ⇒ refuse (503) rather
// than serve the unscoped list. A goalId that does not resolve to a rooted,
// learner-owned goal (the unrooted discovery graph, a foreign/blank id) is a
// no-op: the caller keeps the list it already holds. Returns (scoped, handled);
// handled=true when a response was already written.
func (s *ExtServer) scopeSuggestionsToGoal(ctx context.Context, w http.ResponseWriter, tenantID, gcid, goalID string, list []*conceptgraph.Suggestion) ([]*conceptgraph.Suggestion, bool) {
	if s.Goals == nil || s.Concepts == nil || s.ConceptEdges == nil {
		extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "goal-scope ports not wired (fail-closed, bug #19)")
		return nil, true
	}
	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOAL_GET_FAILED", err.Error())
		return nil, true
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID ||
		g.RootConceptID == nil || strings.TrimSpace(*g.RootConceptID) == "" {
		return list, false // unrooted discovery graph / foreign id — leave as-is
	}
	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "CONCEPT_LIST_FAILED", err.Error())
		return nil, true
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "EDGE_LIST_FAILED", err.Error())
		return nil, true
	}
	inGoal := conceptgraph.SubtreeConceptIDs(derefConcepts(nodes), derefEdges(edges), *g.RootConceptID)
	scoped := make([]*conceptgraph.Suggestion, 0, len(list))
	for _, sug := range list {
		if sug != nil && inGoal[sug.FocalConceptID] {
			scoped = append(scoped, sug)
		}
	}
	return scoped, false
}

// handleMeSuggestionByID — POST /v1/me/concept-graph/suggestions/{id}/accept|dismiss.
func (s *ExtServer) handleMeSuggestionByID(w http.ResponseWriter, r *http.Request) {
	if s.Suggestions == nil {
		extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "suggestion repo not wired")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, suggestionsByIDPrefix), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "expected {id}/accept|dismiss")
		return
	}
	id, action := parts[0], parts[1]
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

	sug, err := s.Suggestions.GetByID(ctx, tenantID, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SUGGESTION_GET_FAILED", err.Error())
		return
	}
	if sug == nil {
		extWriteError(w, http.StatusNotFound, "SUGGESTION_NOT_FOUND", "")
		return
	}
	now := time.Now().UTC()

	switch action {
	case "accept":
		if s.SuggestionAccept == nil {
			extWriteError(w, http.StatusServiceUnavailable, "SUGGESTIONS_UNAVAILABLE", "suggestion accepter not wired")
			return
		}
		node, edge, derr := sug.Accept(now)
		if derr != nil {
			writeSuggestionDomainError(w, derr)
			return
		}
		if err := s.SuggestionAccept.ApplyAccept(ctx, sug, node, edge); err != nil {
			extWriteError(w, http.StatusInternalServerError, "SUGGESTION_ACCEPT_FAILED", err.Error())
			return
		}
		// ADR-244 D4: the learner accepting a Companion proposal IS the binding
		// act (ADR-212 D9), so it emits like any other.
		s.emitConceptMinted(ctx, node, events.ChangeSourceSuggestionAccepted)
		extWriteJSON(w, http.StatusOK, toSuggestionDTO(sug))
	case "dismiss":
		if derr := sug.Dismiss(now); derr != nil {
			writeSuggestionDomainError(w, derr)
			return
		}
		if err := s.Suggestions.Update(ctx, sug); err != nil {
			extWriteError(w, http.StatusInternalServerError, "SUGGESTION_DISMISS_FAILED", err.Error())
			return
		}
		extWriteJSON(w, http.StatusOK, toSuggestionDTO(sug))
	default:
		extWriteError(w, http.StatusNotFound, "UNKNOWN_ACTION", "expected accept or dismiss")
	}
}

// writeSuggestionDomainError maps suggestion domain sentinels to HTTP codes.
func writeSuggestionDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conceptgraph.ErrSuggestionDecided):
		extWriteError(w, http.StatusConflict, "SUGGESTION_ALREADY_DECIDED", err.Error())
	case errors.Is(err, conceptgraph.ErrSelfLoop):
		extWriteError(w, http.StatusUnprocessableEntity, "EDGE_SELF_LOOP", err.Error())
	case errors.Is(err, conceptgraph.ErrInvalid):
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID", err.Error())
	default:
		extWriteError(w, http.StatusInternalServerError, "SUGGESTION_MUTATION_FAILED", err.Error())
	}
}
