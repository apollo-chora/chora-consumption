// companion_ceremony_edge_scout_handler.go — CHO-2040 (CR §8 R7-3): the
// ceremony edge-scout PROPOSE runner.
//
//	POST /v1/me/companions/{id}/ceremony/edge-scout   {"goal_id":"<uuid>"}
//
// At the Companion summon/binding ceremony (goal attach on /a/companion) the
// learner's Companion crawls their past learning signals and proposes a
// labelled checkbox list of growth edges — remediate (active weaknesses ∪
// graded-assessment overall comments) ∪ explore (goal-adjacent WS-4 fog
// suggestions). The FE later POSTs the ticked edges to the CHO-2038
// mint-by-title endpoint (/v1/me/goals/{id}/learning-edges) — that write side
// is NOT this runner's concern.
//
// CR R7-3 locked shape — a COMPOSED runner on the CHO-2013 invoke machinery,
// deliberately NOT a catalogue Skill row: a catalogue Skill demands
// grant+equip+stage gates (weakness_sight st3 / kg_explore st4) which would
// lock young companions out of the goal-attach UX; the ceremony IS the
// acquisition moment (precedent: sovereign acquire + WS-4 fog suggestions run
// grantless). Runner semantics mirror invokeCompanionSkill:
//
//  1. Gates — learner-authed, companion owned, goal owned (READ-ONLY via the
//     goal repo — this runner never mutates goals).
//  2. Affordability — read-only balance pre-check on the locked price key
//     `companion_ceremony_edge_scout`; the model-gateway is the SOLE debiter
//     (ADR-177): the runner stamps ManaActionCode on the turn and never
//     debits locally.
//  3. Compose — weaknesses (strength-desc top-N, pre-embedded) ∪ pending fog
//     concept suggestions scoped to the goal's root concept ∪ the delivery
//     graded-comment crawl (gRPC seam, locked ~20 window).
//  4. Virgin fallback — ZERO weaknesses AND ZERO comments ⇒ SKIP the engine
//     turn entirely (no LLM, no charge): explore-only candidates seeded from
//     the fog suggestions or, map-empty, the goal's ConceptSet titles — a
//     learner never gets a blank panel.
//  5. Rank — pgvector-cosine relevance to the goal anchor (root-concept title
//     + ConceptSet), computed via the SAME Vertex embedding client the
//     weakness store uses; weaknesses ride their STORED vectors. Embedder
//     unwired at boot ⇒ the documented deviation: source-order pool + the
//     turn's explicit top-8 selection (loud log, never a 5xx).
//  6. ONE extraction turn over the same engine client the skill-invoke runner
//     drives — fenced data in (defence-in-depth; Armor screens centrally at
//     the gateway per ADR-177/152), STRICT JSON out. Malformed reply ⇒
//     fail-loud 502 EDGE_SCOUT_BAD_REPLY — never a silent fallback.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// GoalReader is the runner's READ-ONLY slice of the learner-owned Goal
// aggregate (ADR-204). Satisfied by the pg goal.Repository — the same repo
// the /v1/me/goals CRUD uses. The runner never mutates goals.
type GoalReader interface {
	GetByID(ctx context.Context, tenantID, learnerGCID, goalID string) (*goal.Goal, error)
}

// FogSuggestionReader is the goal-adjacent fog read: the learner's PENDING
// WS-4 concept suggestions (the demoted fog's output store, ADR-212 D4).
// Satisfied by the pg conceptgraph.SuggestionRepository. Focal-scoped when
// the goal anchors a root concept; whole-map otherwise.
type FogSuggestionReader interface {
	ListPending(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Suggestion, error)
	ListPendingForFocal(ctx context.Context, tenantID, learnerGCID, focalConceptID string) ([]*conceptgraph.Suggestion, error)
}

// GradedCommentsReader is the delivery seam: the learner's released
// graded-assessment overall comments via Delivery.ListLearnerGradedSubmissions
// (the runner's ONLY cross-domain read; typed gRPC, cross-DB forbidden).
// Satisfied by clients.DeliveryGradedCommentsGRPCClient.
type GradedCommentsReader interface {
	ListLearnerGradedComments(ctx context.Context, tenantID, learnerGCID string, limit int) ([]edgescout.CommentSignal, error)
}

// ceremonyEdgeScoutReq is the inbound body. Unknown fields are ignored
// (mirrors the skill-invoke decode style); goal_id is REQUIRED + UUID-shaped.
type ceremonyEdgeScoutReq struct {
	GoalID string `json:"goal_id"`
}

// ceremonyCandidateResp is one proposed growth edge on the wire. Title +
// intent (+ atom_refs) are POSTable to the CHO-2038 learning-edges endpoint
// as-is.
type ceremonyCandidateResp struct {
	Title    string   `json:"title"`
	Intent   string   `json:"intent"`
	Source   string   `json:"source"`
	AtomRefs []string `json:"atom_refs,omitempty"`
	// NearDuplicateOf names the OWNED concept this candidate restates, "" when
	// the candidate is genuinely new. ALWAYS emitted (never omitempty) so the FE
	// can rely on the key existing. Near-duplicates ride the SAME candidates
	// array, sorted last, deliberately NOT a second array: a second array is
	// invisible to an un-updated frontend, which would recreate the very harm
	// this fixes (the learner shown a concept they already own, unmarked).
	NearDuplicateOf string `json:"near_duplicate_of"`
	Rationale       string `json:"rationale,omitempty"`
}

// ceremonyEdgeScoutResp is the wire response. mana_charged mirrors the
// invoke-runner projection (the gateway ledger is the truth); turn_id is
// absent on the no-LLM fallback path. first_run reports the R8-1 pricing
// applied: true ⇒ the free companion_ceremony_edge_scout_first code rode the
// turn (or, on the fallback path, the freebie is still intact).
type ceremonyEdgeScoutResp struct {
	Candidates  []ceremonyCandidateResp `json:"candidates"`
	Fallback    bool                    `json:"fallback"`
	ManaCharged int                     `json:"mana_charged"`
	TurnID      string                  `json:"turn_id,omitempty"`
	FirstRun    bool                    `json:"first_run"`
}

// ceremonyEdgeScoutMaxTokens gives the strict-JSON top-8 list headroom
// (mirrors invokeFullMaxTokens).
const ceremonyEdgeScoutMaxTokens = 1024

// ceremonyEdgeScout handles POST /v1/me/companions/{id}/ceremony/edge-scout.
func (s *Server) ceremonyEdgeScout(w http.ResponseWriter, r *http.Request, companionID string) {
	// Wiring gate (fail-soft at boot, fail-loud per request — the
	// SKILL_INVOKE_NOT_WIRED pattern). Every composed source must be real:
	// a missing port silently narrowing the crawl would fabricate a thinner
	// learner than reality. CeremonyRuns (the R8-1 first-run ledger) is part
	// of the gate — a nil ledger would silently misprice every run.
	if s.Goals == nil || s.LearnerWeakness == nil || s.FogSuggestions == nil || s.GradedComments == nil || s.CeremonyRuns == nil ||
		s.KgExploreMap == nil || s.KgExploreEdges == nil {
		writeError(w, http.StatusServiceUnavailable, "EDGE_SCOUT_NOT_WIRED",
			"ceremony edge-scout ports not wired (goal/weakness/fog/graded-comments/first-run-ledger/concept-map/concept-edges) — check cmd/server wiring + CHORA_DELIVERY_GRPC_BASE_URL + migration 0067")
		return
	}
	tenantID, gcid, err := requireContext(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "MISSING_CONTEXT", err.Error())
		return
	}
	var req ceremonyEdgeScoutReq
	if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil && !errors.Is(derr, io.EOF) {
		writeError(w, http.StatusBadRequest, "BAD_JSON", derr.Error())
		return
	}
	goalID := strings.TrimSpace(req.GoalID)
	if goalID == "" || !uuidShaped(goalID) {
		writeError(w, http.StatusBadRequest, "INVALID_GOAL_REF",
			"goal_id required and must be a UUID")
		return
	}

	ctx := repoCtx(r, tenantID, gcid)
	if _, err := s.loadOwnedInstance(ctx, tenantID, gcid, companionID); err != nil {
		s.writeSkillError(w, err)
		return
	}

	// Goal gate — owned by the caller, READ-ONLY. Mirrors the CHO-2038
	// handler's no-leak rule: any mismatch renders 404, never 403.
	g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "GOAL_READ_FAILED", gerr.Error())
		return
	}
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	// R8-1 first-run ledger probe (CHO-2040 Unit B): no run row ⇒ this run is
	// FREE (the zero-cost `_first` code rides the turn; the balance pre-check
	// is skipped). A ledger read failure is a LOUD 500 — guessing "free" is
	// un-metered abuse, guessing "paid" silently overrides the owner ruling.
	hasRun, frErr := s.CeremonyRuns.HasRun(ctx, tenantID, goalID, gcid)
	if frErr != nil {
		writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_RUN_READ_FAILED", frErr.Error())
		return
	}
	firstRun := !hasRun

	// ---- Source 1: active weaknesses (strength-desc top-N, pre-embedded). ----
	wres, werr := s.LearnerWeakness.List(ctx, lw.ListQuery{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Sort:        lw.SortStrengthDesc,
		PageSize:    edgescout.MaxWeaknessSignals,
	})
	if werr != nil {
		writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_WEAKNESS_READ_FAILED", werr.Error())
		return
	}
	weaknesses := make([]edgescout.WeaknessSignal, 0, len(wres.Items))
	for _, e := range wres.Items {
		weaknesses = append(weaknesses, edgescout.WeaknessSignal{
			Label:     e.ConceptLabel,
			Strength:  e.Strength,
			Summary:   e.Descriptor.Summary,
			AtomRefs:  e.CachedDrillAtomIDs,
			Embedding: e.Embedding,
		})
	}

	// ---- Source 2: goal-adjacent fog (pending WS-4 concept suggestions). ----
	rootConceptID := ""
	if g.RootConceptID != nil {
		rootConceptID = strings.TrimSpace(*g.RootConceptID)
	}
	// bug #19: read the WHOLE pending inbox, then fence to THIS goal by its LIVE
	// concept SUBTREE — not just the root. A fog anchored on a WON CHILD (a
	// descendant focal, revealed by winning that node) belongs to this goal's
	// map; the old ListPendingForFocal(root) + root-only fence dropped it in two
	// places (the SQL never returned child-focal rows, and the Go fence
	// re-excluded them). SubtreeConceptIDs over KgExploreMap+KgExploreEdges is the
	// SAME downward hierarchy walk the read-side scopeSuggestionsToGoal + the
	// kg_explore skill use, so the ceremony panel and the curation inbox can never
	// disagree. Won-ness needs no re-check here: a child-focal suggestion can
	// exist only because that node was won (refuseUnwonGoalReveal gates the pen).
	pending, ferr := s.FogSuggestions.ListPending(ctx, tenantID, gcid)
	if ferr != nil {
		writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_FOG_READ_FAILED", ferr.Error())
		return
	}
	// The learner's live concept map serves TWO purposes here: fencing fog to
	// this goal's subtree (below), and the already-conceptualised exclusion
	// applied after reconcile. Read once, unconditionally: a rootless goal
	// still needs the exclusion.
	nodes, nerr := s.KgExploreMap.ListByLearner(ctx, tenantID, gcid)
	if nerr != nil {
		writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_MAP_READ_FAILED", nerr.Error())
		return
	}
	alreadyConceptualised := ownedConceptIndex(nodes)

	var inGoal map[string]bool
	if rootConceptID != "" {
		edges, eerr := s.KgExploreEdges.ListByLearner(ctx, tenantID, gcid)
		if eerr != nil {
			writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_EDGE_READ_FAILED", eerr.Error())
			return
		}
		inGoal = conceptgraph.SubtreeConceptIDs(derefConcepts(nodes), derefEdges(edges), rootConceptID)
	}
	fogs := make([]edgescout.FogSignal, 0, len(pending))
	for _, sg := range pending {
		if sg == nil || sg.Kind != conceptgraph.SuggestionKindConcept || strings.TrimSpace(sg.Title) == "" {
			continue // edge suggestions carry no title — not candidates
		}
		// #19 fence: admit whole-map fog (focal-less: the learner's own un-goaled
		// exploration) OR any fog anchored anywhere in THIS goal's subtree (root
		// or a won child). A focal in a SIBLING goal's subtree is dropped. A
		// rootless goal has no subtree (inGoal nil) ⇒ only whole-map fog rides.
		if focal := strings.TrimSpace(sg.FocalConceptID); focal != "" && !inGoal[focal] {
			continue
		}
		fogs = append(fogs, edgescout.FogSignal{
			Title:     sg.Title,
			Rationale: sg.Rationale,
			AtomRefs:  sg.AtomRefs,
		})
		if len(fogs) == edgescout.MaxFogSignals {
			break // newest-first per the repo contract
		}
	}

	// ---- Source 3: past graded-assessment overall comments (delivery seam,
	// locked ~20-activity window). An unreachable delivery is a 502, NEVER
	// "no comments" — that would silently flip the learner into the virgin
	// fallback.
	comments, cerr := s.GradedComments.ListLearnerGradedComments(ctx, tenantID, gcid, edgescout.GradedCommentsWindow)
	if cerr != nil {
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_COMMENTS_READ_FAILED", cerr.Error())
		return
	}

	// ---- Virgin fallback: no remediation evidence anywhere ⇒ no LLM, no
	// charge; explore-only seeds (fog, else ConceptSet). Ordered BEFORE the
	// engine + mana gates on purpose — a brand-new broke learner on an
	// engine-less environment still gets a panel.
	//
	// R8-1: the fallback does NOT consume the freebie — no LLM turn ran, so
	// nothing is recorded in the ledger; the learner's first REAL scout (once
	// they have evidence) is still free. first_run is reported informationally.
	if edgescout.DecideFallback(len(weaknesses), len(comments)) {
		seeds := edgescout.FallbackCandidates(fogs, g.ConceptSet, edgescout.MaxCandidates)
		// The fallback used to return here BEFORE any filter ran, so it could
		// seed a concept the learner already owns. EXACT tier only: the
		// containment tier would routinely blank a virgin learner's panel
		// (the goal ROOT is itself a ConceptNode, and the ConceptSet seeds are
		// its own vocabulary), which the never-blank-panel contract forbids.
		seeds = dropOwnedFallbackSeeds(seeds, alreadyConceptualised, goalID)
		writeJSON(w, http.StatusOK, ceremonyEdgeScoutResp{
			Candidates:  toCeremonyCandidates(seeds),
			Fallback:    true,
			ManaCharged: 0,
			FirstRun:    firstRun,
		})
		return
	}

	if s.CompanionEngine == nil || s.CompanionEngineResource == "" {
		writeError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED",
			"companion turn lane not wired at chora-consumption boot")
		return
	}

	// Affordability pre-check (read-only; ADR-177 — the gateway debits once
	// via the stamped action code). R8-1: the FIRST run per goal stamps the
	// zero-cost `_first` code — cost resolves to 0, so the balance pre-check
	// below is SKIPPED (a broke learner still gets their free first scout)
	// while the turn stays fully metered at the gateway (IMDA D3). Re-runs
	// stamp the paid code; both codes are seeded in chora_identity
	// mana_action_pricing (migration 0032: 25 / 0) — the gateway
	// authoritative-resolves on debit.
	actionCode := companion.ActionCompanionCeremonyEdgeScout
	if firstRun {
		actionCode = companion.ActionCompanionCeremonyEdgeScoutFirst
	}
	cost, lerr := companion.LookupCost(actionCode)
	if lerr != nil {
		writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_PRICE_UNKNOWN",
			"no canonical cost for action code "+actionCode)
		return
	}
	if cost > 0 && s.ManaQuoter != nil {
		balCtx := clients.WithTraceparent(r.Context(), r.Header.Get("traceparent"))
		if snap, berr := s.ManaQuoter.GetBalance(balCtx, gcid); berr == nil {
			if snap.BalanceUnits < cost {
				writeInsufficientManaEnvelope(w, &companion.ErrInsufficientMana{
					RequiredUnits:  cost,
					CurrentBalance: snap.BalanceUnits,
				})
				return
			}
		} else {
			// Balance read failed — fail-open per ADR-142 §8 (the gateway
			// still gates + debits server-side). Mirrors the invoke runner.
			log.Printf("consumption: ceremony edge-scout balance pre-check non-fatal error: %v", berr)
		}
	}

	// ---- Goal anchor + deterministic cosine ranking (CR-locked default).
	rootTitle := ""
	if rootConceptID != "" && s.ConceptNodes != nil {
		node, nerr := s.ConceptNodes.GetByID(ctx, tenantID, gcid, rootConceptID)
		if nerr != nil {
			writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_CONCEPT_READ_FAILED", nerr.Error())
			return
		}
		if node != nil {
			rootTitle = node.Title
		}
	}
	pool := edgescout.BuildPool(weaknesses, fogs)
	ranked, rankErr := s.rankEdgeScoutPool(ctx, tenantID, rootTitle, g.ConceptSet, pool)
	if rankErr != nil {
		// Ranking is part of the locked runner when the embedder is wired —
		// an embed failure fails loud, never a silently unranked list.
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_EMBED_FAILED", rankErr.Error())
		return
	}
	ranked = edgescout.SelectTop(ranked, edgescout.MaxPoolTitled)

	// Session-bootstrap config (mesh-sidestep) — mirrors the invoke runner.
	var companionConfigJSON string
	if s.CompanionConfigJSONResolver != nil {
		cfgJSON, rerr := s.CompanionConfigJSONResolver(r.Context(), tenantID, companionID, gcid)
		if rerr != nil {
			writeError(w, http.StatusBadGateway, "COMPANION_CONFIG_RESOLVE_FAILED", rerr.Error())
			return
		}
		companionConfigJSON = cfgJSON
	}

	// ---- The ONE fenced extraction turn (gateway debits via the stamped
	// action code; the runner never debits).
	prompt := edgescout.BuildPrompt(edgescout.PromptInput{
		GoalRootTitle: rootTitle,
		ConceptSet:    g.ConceptSet,
		Pool:          ranked,
		Comments:      comments,
		Max:           edgescout.MaxCandidates,
	})
	turnID := domain.NewUUIDv7()
	frames, serr := s.CompanionEngine.StreamChat(r.Context(), clients.CompanionChatRequest{
		TenantID:            tenantID,
		UserGCID:            gcid,
		CompanionID:         companionID,
		ManaTier:            resolveManaTier(s, tenantID, gcid),
		Message:             prompt,
		MaxOutputTokens:     ceremonyEdgeScoutMaxTokens,
		TurnID:              turnID,
		CompanionConfigJSON: companionConfigJSON,
		ManaActionCode:      actionCode,
	})
	if serr != nil {
		if errors.Is(serr, agentengine.ErrEngineNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "ENGINE_NOT_CONFIGURED", serr.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "ENGINE_STREAM_FAILED", serr.Error())
		return
	}
	reply, engineErrMsg := drainInvokeTurn(frames)
	if engineErrMsg != "" {
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_ENGINE_ERROR", engineErrMsg)
		return
	}
	if reply == "" {
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_EMPTY_REPLY",
			"companion engine returned an empty reply for the edge-scout turn")
		return
	}

	// ---- STRICT parse + deterministic reconcile (pool truth wins; forged
	// provenance rejected). Malformed ⇒ fail-loud 502, never a fallback.
	parsed, perr := edgescout.ParseReply(reply)
	if perr != nil {
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_BAD_REPLY", perr.Error())
		return
	}
	final, rerr := edgescout.Reconcile(parsed, pool, edgescout.MaxCandidates)
	if rerr != nil {
		writeError(w, http.StatusBadGateway, "EDGE_SCOUT_BAD_REPLY", rerr.Error())
		return
	}
	// Never re-propose a concept the learner already holds (measured 2026-08-07:
	// an accepted edge came straight back on the next Look again). Two tiers,
	// deliberately different verdicts:
	//
	//   1. EXACT key ⇒ DROP, learner-wide. Certain, and measured 0 false
	//      positives in 245. Reconcile's own empty-check already guarded
	//      extraction quality above, so an empty list HERE means "nothing new to
	//      offer": an honest empty state, not an error.
	//   2. CONTIGUOUS TOKEN CONTAINMENT ⇒ DEMOTE, scoped to THIS goal's subtree.
	//      Catches the paraphrase tier 1 cannot ("Cycling safety" does not
	//      normalise to "cycling-safety-protocols"), but the predicate cannot
	//      tell a restatement from a genus, so it never deletes and never
	//      censors a sibling goal (see edgescout/nearduplicate.go).
	final = dropAlreadyConceptualised(final, alreadyConceptualised, goalID, turnID)
	final = demoteNearDuplicates(final, ownedInGoalConcepts(nodes, inGoal), goalID, turnID)

	// R8-1: burning the freebie is PART of the successful first-run flow -
	// the row lands only after the turn parsed + reconciled (a failed turn
	// keeps the freebie), and a row-write failure is a LOUD 500, never a
	// silent repeat-freebie. Semantics of that 500: the free turn DID run,
	// and a retry may run another free turn: the deliberate trade; the
	// UNIQUE(tenant, goal, learner) ledger row + ON CONFLICT DO NOTHING
	// absorb the concurrent double-tap, so the freebie burns exactly once.
	if firstRun {
		if recErr := s.CeremonyRuns.RecordRun(ctx, tenantID, goalID, gcid, time.Now().UTC()); recErr != nil {
			writeError(w, http.StatusInternalServerError, "EDGE_SCOUT_RUN_RECORD_FAILED",
				"free first run completed but the run ledger write failed (retry may re-run free): "+recErr.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, ceremonyEdgeScoutResp{
		Candidates:  toCeremonyCandidates(final),
		Fallback:    false,
		ManaCharged: int(cost),
		TurnID:      turnID,
		FirstRun:    firstRun,
	})
}

// rankEdgeScoutPool applies the CR-locked default ranking: cosine relevance
// of every titled candidate to the goal anchor (root-concept title +
// ConceptSet), embedded via the SAME Vertex client the weakness store uses
// (anchor = RETRIEVAL_QUERY; fog titles = RETRIEVAL_DOCUMENT; weaknesses ride
// their STORED document vectors: no re-embed). Deterministic: unrankable
// vectors sort last, ties break by title.
//
// Embedder unwired at boot (nil) ⇒ the DOCUMENTED deviation path: the pool
// keeps source order (weakness strength-desc, then fog newest-first) and the
// extraction turn selects under its explicit top-8 instruction: logged
// loudly, never a fabricated cosine. A wired embedder that ERRORS fails loud
// (the caller 502s): no silent unranked fallback.
func (s *Server) rankEdgeScoutPool(ctx context.Context, tenantID, rootTitle string, conceptSet []string, pool []edgescout.Candidate) ([]edgescout.Candidate, error) {
	if len(pool) == 0 {
		return pool, nil
	}
	if s.Embedder == nil {
		log.Printf("consumption: ceremony edge-scout ranking DEVIATION: embedder unwired (CHORA_MODEL_GATEWAY_GRPC_URL unset); pool stays source-ordered, the extraction turn selects top-%d", edgescout.MaxCandidates)
		return pool, nil
	}
	anchorParts := make([]string, 0, 1+len(conceptSet))
	if t := strings.TrimSpace(rootTitle); t != "" {
		anchorParts = append(anchorParts, t)
	}
	for _, c := range conceptSet {
		if c = strings.TrimSpace(c); c != "" {
			anchorParts = append(anchorParts, c)
		}
	}
	if len(anchorParts) == 0 {
		// Nothing goal-side to rank against (rootless goal, empty concept
		// set): source order stands; not an error.
		return pool, nil
	}
	anchorVec, aerr := s.Embedder.Embed(ctx, companion.EmbedInput{
		Text:     strings.Join(anchorParts, "; "),
		TaskType: companion.EmbedTaskQuery,
		TenantID: tenantID,
	})
	if aerr != nil {
		return nil, aerr
	}
	scored := make([]edgescout.ScoredCandidate, 0, len(pool))
	for _, c := range pool {
		vec := c.Embedding
		if len(vec) == 0 {
			var eerr error
			vec, eerr = s.Embedder.Embed(ctx, companion.EmbedInput{
				Text:     c.Title,
				TaskType: companion.EmbedTaskDocument,
				TenantID: tenantID,
			})
			if eerr != nil {
				return nil, eerr
			}
		}
		score, ok := edgescout.Cosine(anchorVec, vec)
		scored = append(scored, edgescout.ScoredCandidate{Candidate: c, Score: score, Scored: ok})
	}
	return edgescout.RankByScore(scored), nil
}

// ownedConceptIndex indexes the learner's live ConceptNodes by BOTH title and
// stored concept_key, normalised into the shared concept_key vocabulary (a
// rename keeps the key, so both must pin), mapping each key to the title the
// learner SEES. Mirrors buildKgExplorePool's already-conceptualised set: kept
// learner-wide on purpose: never re-suggest an owned concept, whichever map
// owns it. The title value is what a suppression log names, so the log speaks
// the learner's current vocabulary rather than a slug.
func ownedConceptIndex(nodes []*conceptgraph.ConceptNode) map[string]string {
	out := make(map[string]string, len(nodes)*2)
	for _, c := range nodes {
		if c == nil {
			continue
		}
		display := strings.TrimSpace(c.Title)
		if display == "" {
			display = strings.TrimSpace(c.ConceptKey)
		}
		if display == "" {
			continue
		}
		for _, raw := range [2]string{c.Title, c.ConceptKey} {
			if k := lw.NormalizeConceptKey(raw); k != "" {
				out[k] = display
			}
		}
	}
	return out
}

// ownedInGoalConcepts narrows the learner's owned concepts to THIS goal's live
// subtree, using the inGoal set the fog fence already computed via
// conceptgraph.SubtreeConceptIDs (zero extra I/O). This is the scope of the
// near-duplicate DEMOTION tier: unscoped, a concept accepted on one goal would
// censor an unrelated goal. A rootless goal has no subtree (inGoal nil) ⇒ no
// owned concepts ⇒ the containment tier contributes nothing, which is the
// correct conservative answer rather than falling back to the whole map.
func ownedInGoalConcepts(nodes []*conceptgraph.ConceptNode, inGoal map[string]bool) []edgescout.OwnedConcept {
	out := make([]edgescout.OwnedConcept, 0, len(inGoal))
	for _, c := range nodes {
		if c == nil || !inGoal[c.ConceptID] {
			continue
		}
		out = append(out, edgescout.OwnedConcept{Title: c.Title, Key: c.ConceptKey})
	}
	return out
}

// dropAlreadyConceptualised removes candidates the learner already holds on
// their map (TIER 1: exact key, learner-wide). It runs on the RECONCILED
// output, not the pre-turn pool, because comment-sourced candidates never enter
// the pool (edgescout.Reconcile's documented carve-out): filtering the pool
// alone would let exactly the re-proposal we measured slip through.
//
// An empty result is an HONEST EMPTY STATE, not an error: the panel renders its
// "nothing new" state with Look again, and the slice stays non-nil so the JSON
// encodes [] rather than null.
//
// Both sides of every suppression are logged (candidate title, matched owned
// title, goal, turn) so a re-proposal complaint can be settled from the record.
// Deliberately NOT wired to a log-based alert: a cost-pause log exclusion would
// silently disarm it ([[reusable_gotcha_a_cost_pause_silently_disarms_all_log_alerting]]).
func dropAlreadyConceptualised(cands []edgescout.Candidate, owned map[string]string, goalID, turnID string) []edgescout.Candidate {
	out := make([]edgescout.Candidate, 0, len(cands))
	for _, c := range cands {
		if ownedTitle, isOwned := owned[lw.NormalizeConceptKey(c.Title)]; isOwned {
			log.Printf("consumption: ceremony edge-scout exclusion: DROPPED candidate %q (exact concept-key match on owned concept %q) goal=%s turn=%q",
				c.Title, ownedTitle, goalID, turnID)
			continue
		}
		out = append(out, c)
	}
	return out
}

// demoteNearDuplicates applies TIER 2 (contiguous token containment, scoped to
// this goal's subtree) and logs both sides of every demotion. The REAL
// concept-key normaliser is injected here: the pure package stays stdlib-only,
// and an adapter test pins that this normaliser still yields one token per word
// so the 2-token floor cannot silently swallow the whole tier.
func demoteNearDuplicates(cands []edgescout.Candidate, ownedInGoal []edgescout.OwnedConcept, goalID, turnID string) []edgescout.Candidate {
	out := edgescout.DemoteNearDuplicates(cands, ownedInGoal, lw.NormalizeConceptKey)
	for _, c := range out {
		if c.NearDuplicateOf == "" {
			continue
		}
		log.Printf("consumption: ceremony edge-scout exclusion: DEMOTED candidate %q to the end of the list (near-duplicate of owned concept %q) goal=%s turn=%q",
			c.Title, c.NearDuplicateOf, goalID, turnID)
	}
	return out
}

// dropOwnedFallbackSeeds applies TIER 1 ONLY to the virgin-fallback seeds. That
// path returns EARLY, before the engine turn, so until now it could hand a
// brand-new learner a concept they already own.
//
// The never-blank-panel contract outranks the exclusion here: when every seed is
// already owned, the unfiltered panel stands and says so loudly. No mana is
// charged on this path (no LLM turn ran), so a stale suggestion costs the
// learner nothing, whereas a blank ceremony panel is the one outcome this
// runner exists to prevent.
func dropOwnedFallbackSeeds(seeds []edgescout.Candidate, owned map[string]string, goalID string) []edgescout.Candidate {
	filtered := dropAlreadyConceptualised(seeds, owned, goalID, "")
	if len(filtered) == 0 && len(seeds) > 0 {
		log.Printf("consumption: ceremony edge-scout fallback: ALL %d seed(s) are already on the learner's map (goal=%s); keeping the unfiltered panel - the never-blank-panel contract outranks the exclusion on this un-charged path",
			len(seeds), goalID)
		return seeds
	}
	return filtered
}

// toCeremonyCandidates maps domain candidates onto the wire shape (always a
// non-nil array: the FE never sees null).
func toCeremonyCandidates(cands []edgescout.Candidate) []ceremonyCandidateResp {
	out := make([]ceremonyCandidateResp, 0, len(cands))
	for _, c := range cands {
		out = append(out, ceremonyCandidateResp{
			Title:           c.Title,
			Intent:          string(c.Intent),
			Source:          string(c.Source),
			AtomRefs:        c.AtomRefs,
			NearDuplicateOf: c.NearDuplicateOf,
			Rationale:       c.Rationale,
		})
	}
	return out
}
