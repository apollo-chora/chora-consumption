// goal_knowledge_read_handler.go — CHO-2118 tier-2: the Companion's per-goal
// reflection, read + lazily regenerated.
//
//	GET /v1/me/goals/{id}/knowledge
//
// Serves the tier-1 deterministic block ALWAYS, plus the cached LLM reflection
// when there is one — and, when the policy says so, claims the row and emits a
// synthesis request for the next read to pick up. The learner never waits on a
// model: this endpoint does no LLM work, it only decides whether to ASK for some.
//
// Nested under the already-proxied /v1/me/goals/ subtree (dispatched from
// handleMeGoalByID), like the campaign sub-resources, so the gateway prefix proxy
// and the Istio path allowlist (/v1/me/goals/*, whose '*' crosses '/') admit it
// with no new edge plumbing.
//
// # This handler does not reason; it obeys
//
// The whole policy — when to serve, when to spend an LLM call, when to say
// honestly that there is nothing to reflect on — is companiongoalknowledge.
// DecideRead, a pure function with its own tests. Re-deriving any of it here
// would give the product decision two homes.
//
// # Claim-then-publish
//
// MarkRequested + Upsert (the CLAIM) happens BEFORE the publish, always. The
// claim is what stops N concurrent tab reads from each firing an LLM call, so a
// request must never escape without one. If the publish then fails, the read is
// still served: the claim self-expires via pendingTTL, so a learner is never
// stranded on "reflecting…" forever.
//
// # No English in here
//
// The response carries `status` (fresh / reflecting / none) and never prose. The
// disclosure line, the "reflecting…" marker and the honest "no memory yet" are
// i18n values in chora-web (en.json). A user-facing string in Go would be a
// second, untranslatable source of copy.
package http

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// goalKnowledgeSuffix is the sub-resource segment under /v1/me/goals/{id}/.
const goalKnowledgeSuffix = "knowledge"

// GoalKnowledgeCache is the narrow slice of companiongoalknowledge.Repository the
// read path needs: find the row, and claim/persist it. Narrow on purpose — the
// full Repository grows methods on other lanes, and this path has no business
// with the invalidation fan-outs or the closure tombstones.
type GoalKnowledgeCache interface {
	FindByGoal(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (*fgk.GoalKnowledge, error)
	Upsert(ctx context.Context, k *fgk.GoalKnowledge) error
}

// GoalScopedViewAssembler composes the tier-1 block for an already-loaded goal.
// *goalscopedview.Assembler satisfies it (and, by its other method, the
// completion subscriber's port — one assembly, two callers, so the served view
// and the drift-checked view can never disagree).
type GoalScopedViewAssembler interface {
	AssembleForGoal(ctx context.Context, tenantID, learnerGCID, companionID string, g *goal.Goal) (companionmind.GoalScopedView, error)
}

// GoalKnowledgeReadService is the read path's collaborators + the policy tunables
// (wired from env at cmd/server — no inline config).
type GoalKnowledgeReadService struct {
	Cache GoalKnowledgeCache
	Views GoalScopedViewAssembler

	Floor      time.Duration // min interval between two syntheses of one (companion, goal)
	MaxAge     time.Duration // refresh eventually even if nothing invalidated it
	PendingTTL time.Duration // how long an in-flight claim is trusted before re-issue

	// Now is a test seam; nil ⇒ time.Now().UTC.
	Now func() time.Time
}

func (s *GoalKnowledgeReadService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// wire DTOs — camelCase, learner-depth (ADR-215 D5): no vectors, no distances,
// no model ids, no prompt internals.

type shakyConceptDTO struct {
	ConceptKey   string  `json:"conceptKey"`
	ConceptLabel string  `json:"conceptLabel"`
	Strength     float64 `json:"strength"`
}

// goalKnowledgeReflectionDTO is the tier-2 panel. Status drives the render;
// chora-web owns every word the learner reads.
type goalKnowledgeReflectionDTO struct {
	Text   string `json:"text"`
	Status string `json:"status"` // fresh | reflecting | none
	// Claimless marks a read taken under ?generate=false: nothing was claimed
	// and no synthesis was asked for (C4, ADR-235). Present ONLY on such a read,
	// so the default wire is byte-identical to what it was.
	//
	// A caller MUST NOT read `reflecting` as progress its own request set in
	// motion when this is true. Nothing was bought, so a spinner drawn from it
	// would never resolve: that is the CHO-2180 failure in a new costume.
	Claimless bool `json:"claimless,omitempty"`
}

type goalKnowledgeResp struct {
	GoalID        string `json:"goalId"`
	GoalTitle     string `json:"goalTitle"`
	CompanionID   string `json:"companionId"`
	CompanionName string `json:"companionName"`

	ConceptsTotal    int               `json:"conceptsTotal"`
	ConceptsMastered int               `json:"conceptsMastered"`
	ShakyConcepts    []shakyConceptDTO `json:"shakyConcepts"`

	HasMemory bool                `json:"hasMemory"`
	Memories  []episodicMemoryDTO `json:"memories"`

	Reflection goalKnowledgeReflectionDTO `json:"reflection"`
}

// handleMeGoalKnowledge serves GET /v1/me/goals/{id}/knowledge. goalID is the
// already-parsed path segment (from handleMeGoalByID's dispatch); s.Goals is
// already nil-guarded by the caller.
func (s *ExtServer) handleMeGoalKnowledge(w http.ResponseWriter, r *http.Request, goalID string) {
	svc := s.GoalKnowledgeRead
	// Essential collaborators absent (no pgx pool at boot) → 503, never a panic.
	if svc == nil || svc.Cache == nil || svc.Views == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GOAL_KNOWLEDGE_UNAVAILABLE", "goal knowledge read not wired")
		return
	}
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	// C4 / ADR-235: ?generate=false is the CLAIMLESS read. Parsed before any
	// work, so a refused value cannot have already spent an LLM call.
	generate, ok := parseGenerateParam(r)
	if !ok {
		extWriteError(w, http.StatusBadRequest, "INVALID_GENERATE",
			"generate must be true or false")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	// Not found OR cross-learner/tenant leak → 404 (never reveal another's goal).
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	companionID := derefString(g.AttachedCompanionID)

	// Tier-1 ALWAYS. It is the response; the reflection is a panel on top of it.
	// A failure here is fail-loud (a half-view would produce a wrong content hash,
	// and the completion path would then refuse every synthesis against it).
	view, err := svc.Views.AssembleForGoal(ctx, tenantID, gcid, companionID, g)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOAL_VIEW_FAILED", err.Error())
		return
	}

	// No Companion bound ⇒ there is nobody to remember anything. Serve the
	// deterministic block and say so honestly. This short-circuits BEFORE
	// DecideRead deliberately: the learner may well have shaky concepts on this
	// goal (real signal), but the cache is keyed by companion_id and there is no
	// Companion to synthesise for — asking would be asking nobody.
	if companionID == "" {
		resp := toGoalKnowledgeResp(view, fgk.ReadDecision{Status: fgk.ReadStatusNone})
		resp.Reflection.Claimless = !generate
		extWriteJSON(w, http.StatusOK, resp)
		return
	}

	row, err := svc.Cache.FindByGoal(ctx, tenantID, gcid, companionID, g.GoalID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOAL_KNOWLEDGE_READ_FAILED", err.Error())
		return
	}

	now := svc.now()
	dec := fgk.DecideRead(row, view.HasSignal(), g.RootConceptID, now, svc.Floor, svc.MaxAge, svc.PendingTTL)

	// ADR-252 Q5 (ADR-254 D11): a CONTAINED Companion neither claims nor
	// publishes. The override sits here, after the pure policy and BEFORE the
	// claim, because the handler's own rule is "claim fails, do NOT publish":
	// expressing a pause as a claim failure would make an operator decision
	// look exactly like a broken database. Cached text still rides along; the
	// pause stops NEW synthesis, it does not retract what the learner has
	// already read.
	//
	// The reflection lane is mana-EXEMPT (no action_code on the wire), so it
	// asks the whole-Companion question: only an all-skills suspension in
	// scope covers it. Advisory: a read error is UNKNOWN, logged loud, and the
	// lane proceeds (the gateway still refuses the synthesis downstream).
	if s.CompanionSuspension != nil {
		if st, serr := s.CompanionSuspension.Status(ctx, tenantID, ""); serr != nil {
			log.Printf("consumption: goal_knowledge suspension advisory read FAILED tenant=%s gcid=%s companion=%s goal=%s: %v (proceeding; chora-model-gateway remains the control)",
				tenantID, gcid, companionID, g.GoalID, serr)
		} else if st.Paused {
			log.Printf("consumption: goal_knowledge synthesis SKIPPED, Companion contained tenant=%s gcid=%s companion=%s goal=%s scope=%s (ADR-252 Q5: no claim, no publish)",
				tenantID, gcid, companionID, g.GoalID, st.Scope)
			dec.RequestSynthesis = false
			dec.Status = fgk.ReadStatusPaused
		}
	}

	// The CLAIMLESS read (C4, ADR-235). It sits AFTER the pure policy and after
	// the containment override, in the same place and for the same reason: the
	// decision about what to SERVE is unchanged, and only the decision to SPEND
	// is withdrawn. Expressing it earlier would make a caller's thrift look like
	// a policy that had nothing to say.
	//
	// The rule this enforces is "never fetch a reflection for a panel the learner
	// is not looking at". Until now that lived entirely in the frontend caller,
	// so it was a convention no server could hold anyone to.
	if !generate {
		dec.RequestSynthesis = false
	}

	if dec.RequestSynthesis {
		s.requestGoalKnowledgeSynthesis(ctx, r, tenantID, gcid, companionID, g, view, row, now)
	}

	resp := toGoalKnowledgeResp(view, dec)
	// Say plainly that this read bought nothing, so a caller can never read
	// `reflecting` as progress its own request set in motion.
	resp.Reflection.Claimless = !generate
	extWriteJSON(w, http.StatusOK, resp)
}

// parseGenerateParam reads ?generate. Absent means true: the default behaviour
// is unchanged and the parameter is purely additive.
//
// An unparseable value is REFUSED rather than guessed, because guessing has no
// safe direction. Defaulting to generate would charge a caller that asked not to
// be charged; defaulting to claimless would silently stop generating
// reflections for a caller with a typo, which stays invisible until somebody
// notices the panel never fills.
func parseGenerateParam(r *http.Request) (generate bool, ok bool) {
	raw, present := r.URL.Query()["generate"]
	if !present || len(raw) == 0 {
		return true, true
	}
	switch raw[0] {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// requestGoalKnowledgeSynthesis claims the row and publishes the request — in
// that order, always.
//
// Neither failure blanks the learner's tab: the tier-1 block is real and already
// assembled, and the story's fail-soft AC is explicit that a synthesis problem
// renders the deterministic block, never an error card. Both failures are LOUD.
//
//   - Claim fails → do NOT publish. An unclaimed request is an unbounded LLM call:
//     every concurrent tab read would fire its own. The next read retries.
//   - Publish fails after the claim → keep serving. The claim self-expires via
//     pendingTTL, so the learner is never stranded on "reflecting…" forever.
func (s *ExtServer) requestGoalKnowledgeSynthesis(
	ctx context.Context,
	r *http.Request,
	tenantID, gcid, companionID string,
	g *goal.Goal,
	view companionmind.GoalScopedView,
	row *fgk.GoalKnowledge,
	now time.Time,
) {
	svc := s.GoalKnowledgeRead

	if row == nil {
		fresh, err := fgk.NewGoalKnowledge(tenantID, gcid, companionID, g.GoalID, g.RootConceptID)
		if err != nil {
			log.Printf("consumption: goal_knowledge claim REFUSED by the aggregate (%v) tenant=%s gcid=%s companion=%s goal=%s — serving tier-1 only",
				err, tenantID, gcid, companionID, g.GoalID)
			return
		}
		row = fresh
	}

	// --- the claim ---
	row.MarkRequested(now)
	if err := svc.Cache.Upsert(ctx, row); err != nil {
		log.Printf("consumption: goal_knowledge claim FAILED (%v) tenant=%s gcid=%s companion=%s goal=%s — NOT publishing (an unclaimed request is an unbounded LLM call); serving tier-1",
			err, tenantID, gcid, companionID, g.GoalID)
		return
	}

	if s.Publisher == nil {
		log.Printf("consumption: goal_knowledge synthesis NOT requested (no publisher wired) tenant=%s gcid=%s companion=%s goal=%s — the claim will self-expire",
			tenantID, gcid, companionID, g.GoalID)
		return
	}

	// --- the request ---
	//
	// Built from the SAME assembled view that was just served. Re-deriving it here
	// would make content_hash a description of something the model never saw, and
	// the completion's drift guard would then refuse every synthesis.
	payload := events.GoalKnowledgeRequestPayload(goalKnowledgeRequestInput(tenantID, gcid, companionID, g, view, now))

	// EnsureTraceparent, never the raw header: a blank traceparent fails envelope
	// validation downstream, silently.
	env := events.NewEnvelope(
		tenantID, gcid,
		tracing.EnsureTraceparent(r.Header.Get("traceparent")),
		r.Header.Get("tracestate"),
		goalKnowledgeRequestKey(companionID, g.GoalID, now),
	)

	if err := s.Publisher.Publish(events.TopicGoalKnowledgeSynthesisRequested, env, payload); err != nil {
		log.Printf("consumption: goal_knowledge synthesis request PUBLISH FAILED (%v) tenant=%s gcid=%s companion=%s goal=%s — the claim self-expires after the pending TTL and the next read re-requests; serving tier-1",
			err, tenantID, gcid, companionID, g.GoalID)
	}
}

// goalKnowledgeRequestInput lifts the served view onto the wire contract.
//
// PromptVersion is deliberately EMPTY: the synthesising orchestrator owns the
// prompt and its registration (ADR-197 — it self-registers its embedded default
// and stamps the authoritative version onto the completion, which is what lands
// on the cache row). Asserting a version here that consumption does not control
// would be a lie on the wire the moment the orchestrator bumps it.
func goalKnowledgeRequestInput(tenantID, gcid, companionID string, g *goal.Goal, view companionmind.GoalScopedView, now time.Time) events.GoalKnowledgeRequestInput {
	shaky := make([]events.GoalKnowledgeShakyConcept, 0, len(view.ShakyConcepts))
	for _, c := range view.ShakyConcepts {
		shaky = append(shaky, events.GoalKnowledgeShakyConcept{
			ConceptKey:   c.ConceptKey,
			ConceptLabel: c.ConceptLabel,
			Strength:     c.Strength,
		})
	}
	memories := make([]events.GoalKnowledgeMemory, 0, len(view.Memories))
	for _, m := range view.Memories {
		memories = append(memories, events.GoalKnowledgeMemory{
			MemoryID:  m.ID,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		})
	}
	return events.GoalKnowledgeRequestInput{
		TenantID:         tenantID,
		LearnerGCID:      gcid,
		CompanionID:      companionID,
		CompanionName:    view.CompanionName,
		GoalID:           g.GoalID,
		GoalTitle:        view.GoalTitle,
		RootConceptID:    derefString(g.RootConceptID),
		ConceptsTotal:    view.ConceptsTotal,
		ConceptsMastered: view.ConceptsMastered,
		ShakyConcepts:    shaky,
		Memories:         memories,
		ContentHash:      view.ContentHash(),
		RequestedAt:      now,
		RequestSource:    events.GoalKnowledgeSourceLazyRegen,
	}
}

// goalKnowledgeRequestKey identifies ONE claim.
//
// The orchestrator derives the completion's idempotency key from this one, and
// the completion inbox dedupes on it — so it must be stable across a Pub/Sub
// REDELIVERY of the same request (same claim ⇒ same key) and distinct across a
// genuine REGEN (new claim ⇒ new key, or the completion would be discarded as a
// duplicate and the reflection would never refresh). The claim instant is exactly
// that identity: MarkRequested stamps one per claim.
func goalKnowledgeRequestKey(companionID, goalID string, claimedAt time.Time) string {
	return "goal_knowledge.synthesis_requested:" + companionID + ":" + goalID + ":" +
		claimedAt.UTC().Format(time.RFC3339Nano)
}

// toGoalKnowledgeResp maps the tier-1 view + the read decision onto the wire.
// The tier-1 block is present unconditionally — it IS the fail-soft.
func toGoalKnowledgeResp(v companionmind.GoalScopedView, dec fgk.ReadDecision) goalKnowledgeResp {
	shaky := make([]shakyConceptDTO, 0, len(v.ShakyConcepts))
	for _, c := range v.ShakyConcepts {
		shaky = append(shaky, shakyConceptDTO{ConceptKey: c.ConceptKey, ConceptLabel: c.ConceptLabel, Strength: c.Strength})
	}
	mems := make([]episodicMemoryDTO, 0, len(v.Memories))
	for _, m := range v.Memories {
		mems = append(mems, episodicMemoryDTO{ID: m.ID, MemoryType: m.MemoryType, Content: m.Content, CreatedAt: m.CreatedAt})
	}
	return goalKnowledgeResp{
		GoalID:           v.GoalID,
		GoalTitle:        v.GoalTitle,
		CompanionID:      v.CompanionID,
		CompanionName:    v.CompanionName,
		ConceptsTotal:    v.ConceptsTotal,
		ConceptsMastered: v.ConceptsMastered,
		ShakyConcepts:    shaky,
		HasMemory:        v.HasMemory,
		Memories:         mems,
		Reflection: goalKnowledgeReflectionDTO{
			Text:   dec.Text,
			Status: dec.Status,
		},
	}
}
