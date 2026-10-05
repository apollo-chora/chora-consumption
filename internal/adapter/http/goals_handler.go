// goals_handler.go — learner-owned Goal CRUD API (ADR-204 §2).
//
//	POST   /v1/me/goals          create
//	GET    /v1/me/goals          list (+ derived primaryLens, ADR-204 §3)
//	PATCH  /v1/me/goals/{id}     update status / attach-detach Companion (1:1)
//
// Learner-scoped: the GCID is the validated session subject (from headers via
// extRequireContext), NEVER a body/query param — a learner only reads/writes
// their own goals. The repo's RLS + the by-id leak guard enforce isolation.
//
// Hand-written camelCase DTOs (no proto), mirroring the KG canvas FE shapes. The
// graduation state machine, goal.* events, and dose scoping are DEFERRED (this is
// the aggregate + persistence + basic CRUD slice).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companiongoalknowledge "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const goalsByIDPrefix = "/v1/me/goals/"

// GoalKnowledgeInvalidator stales the cached per-goal Companion reflections for
// one goal (CHO-2118). Declared here, at the consumer, because the goal PATCH is
// the ONE invalidation trigger with no event behind it: every other signal in
// the set arrives on the six-topic goal-knowledge inbox, but goal CRUD emits
// nothing, so a re-root would otherwise go unnoticed.
//
// Satisfied by *subscribers.GoalKnowledgeInvalidator.
type GoalKnowledgeInvalidator interface {
	InvalidateGoal(ctx context.Context, tenantID, learnerGCID, goalID string, reason companiongoalknowledge.InvalidationReason) (int, error)
}

// goalDTO is the wire shape of one Goal (camelCase). tenant + learner are implicit
// (the session subject) and never echoed.
type goalDTO struct {
	GoalID              string     `json:"goalId"`
	Kind                string     `json:"kind"`
	ChoraTargetRef      *string    `json:"choraTargetRef,omitempty"`
	ConceptSet          []string   `json:"conceptSet"`
	Status              string     `json:"status"`
	NorthStarNote       string     `json:"northStarNote"`
	AttachedCompanionID *string    `json:"attachedCompanionId,omitempty"`
	RootConceptID       *string    `json:"rootConceptId,omitempty"`
	PersonalCompletedAt *time.Time `json:"personalCompletedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	// Verified progress (CHO-1921): the share of ConceptSet the learner has
	// VERIFIABLY mastered (grown Growth Edges), computed intra-domain at read
	// time — never stored. Always present (a 0%-ring is meaningful), so the FE
	// %-ring can render unconditionally.
	ProgressPercent  int `json:"progressPercent"`
	MasteredConcepts int `json:"masteredConcepts"`
	TotalConcepts    int `json:"totalConcepts"`
}

// goalsListResp is the GET /v1/me/goals envelope. Beside the goals it carries
// the LEAD COUNTERS (UX Track U, master plan section 4.1, ruling R4): the raw
// signals the A+ home ranks on. `primaryLens` stays as their back-compat
// projection; the FE already defaults it to `curiosity` when absent, so every
// field here is additive and an older client is unaffected.
type goalsListResp struct {
	Items       []goalDTO `json:"items"`
	PrimaryLens string    `json:"primaryLens"`
	// ActiveGoals / ActiveCourseBoundPaths are the two lane counts. They are
	// emitted unconditionally (0 is a meaningful count) so the ranker never has
	// to distinguish absent from zero on a healthy response.
	ActiveGoals            int `json:"activeGoals"`
	ActiveCourseBoundPaths int `json:"activeCourseBoundPaths"`
	// LastCuriosityAt / LastCourseAt are the per-axis activity stamps, OMITTED
	// when the axis holds nothing. Never zero-valued: a zero time reads as
	// infinitely stale and would rank a brand-new learner as the most neglected
	// one on the platform.
	LastCuriosityAt *time.Time `json:"lastCuriosityAt,omitempty"`
	LastCourseAt    *time.Time `json:"lastCourseAt,omitempty"`
	// LeadPartial declares the COURSE axis unread this request (the learning-path
	// read failed). Fail-loud without breaking the page: the goals still render,
	// and the consumer knows the empty axis is a gap rather than a fact. Omitted
	// on a clean read.
	LeadPartial bool `json:"leadPartial,omitempty"`
}

type goalCreateReq struct {
	Kind           string   `json:"kind"`
	ChoraTargetRef *string  `json:"choraTargetRef,omitempty"`
	ConceptSet     []string `json:"conceptSet,omitempty"`
	NorthStarNote  string   `json:"northStarNote,omitempty"`
	// RootConceptID anchors the Goal to its evolving root ConceptNode (ADR-214 D1
	// — a map = a Goal rooted at this concept). Set by "＋New map" (WS-B) after the
	// root concept is minted. Optional (a plain goal may have none).
	RootConceptID *string `json:"rootConceptId,omitempty"`
}

type goalPatchReq struct {
	Status              *string `json:"status,omitempty"`
	AttachedCompanionID *string `json:"attachedCompanionId,omitempty"`
	DetachCompanion     bool    `json:"detachCompanion,omitempty"`
	// RootConceptID re-anchors the Goal to a new root ConceptNode — the WS-C
	// make-root operation (ADR-214 D1: "goal evolution = re-rooting"). Blank/absent
	// leaves the anchor unchanged.
	RootConceptID *string `json:"rootConceptId,omitempty"`
	// PersonalComplete is the learner's personal-axis "done for me" toggle (ADR-213
	// / WS-D Mastery lens): true marks personal completion, false reopens it. Absent
	// leaves it unchanged. Orthogonal to the verified Status — it NEVER touches a
	// credentialed value (the impermeable wall).
	PersonalComplete *bool `json:"personalComplete,omitempty"`
}

// goalToDTO maps a Goal to its wire shape with NO mastered concepts resolved
// (the POST create + PATCH responses): progressPercent + masteredConcepts are 0,
// totalConcepts still reflects the concept set. The GET list path enriches with
// verified progress via goalToDTOWithProgress.
func goalToDTO(g goal.Goal) goalDTO {
	return goalToDTOWithProgress(g, nil)
}

// goalToDTOWithProgress maps a Goal to its wire shape, computing verified
// progress (CHO-1921) against the learner's mastered (grown) Growth-Edge
// concept-key set. A nil/empty mastered set yields 0 mastered everywhere (the
// fail-soft default); totalConcepts always reflects the goal's concept set.
func goalToDTOWithProgress(g goal.Goal, mastered map[string]bool) goalDTO {
	cs := g.ConceptSet
	if cs == nil {
		cs = []string{}
	}
	// Same count the goal-graduation subscriber uses (lw.CountMastered) so the
	// ring's 100% and the subscriber's graduation fire on the identical share.
	masteredCount := lw.CountMastered(cs, mastered)
	total := len(cs)
	return goalDTO{
		GoalID:              g.GoalID,
		Kind:                string(g.Kind),
		ChoraTargetRef:      g.ChoraTargetRef,
		ConceptSet:          cs,
		Status:              string(g.Status),
		NorthStarNote:       g.NorthStarNote,
		AttachedCompanionID: g.AttachedCompanionID,
		RootConceptID:       g.RootConceptID,
		PersonalCompletedAt: g.PersonalCompletedAt,
		CreatedAt:           g.CreatedAt,
		UpdatedAt:           g.UpdatedAt,
		ProgressPercent:     goal.ProgressPercent(masteredCount, total),
		MasteredConcepts:    masteredCount,
		TotalConcepts:       total,
	}
}

// handleMeGoals — POST (create) + GET (list) /v1/me/goals.
func (s *ExtServer) handleMeGoals(w http.ResponseWriter, r *http.Request) {
	if s.Goals == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GOALS_UNAVAILABLE", "goal repo not wired")
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
		s.listGoals(w, ctx, tenantID, gcid)
	case http.MethodPost:
		s.createGoal(w, r, ctx, tenantID, gcid)
	default:
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	}
}

// listGoals — GET /v1/me/goals. Returns the learner's live goals + the DERIVED
// primaryLens (ADR-204 §3; never stored).
func (s *ExtServer) listGoals(w http.ResponseWriter, ctx context.Context, tenantID, gcid string) {
	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	// Load the learner's mastered (grown) concept keys ONCE per request to drive
	// each goal's verified-progress %-ring (CHO-1921). Fail-soft (see helper).
	mastered := s.masteredConceptKeys(ctx, tenantID, gcid)
	items := make([]goalDTO, 0, len(goals))
	vals := make([]goal.Goal, 0, len(goals))
	for _, g := range goals {
		if g == nil {
			continue
		}
		items = append(items, goalToDTOWithProgress(*g, mastered))
		vals = append(vals, *g)
	}
	lead, leadPartial := s.leadCounters(ctx, tenantID, gcid, vals)
	extWriteJSON(w, http.StatusOK, goalsListResp{
		Items:                  items,
		PrimaryLens:            string(lead.PrimaryLens()),
		ActiveGoals:            lead.ActiveGoals,
		ActiveCourseBoundPaths: lead.ActiveCourseBoundPaths,
		LastCuriosityAt:        lead.LastCuriosityAt,
		LastCourseAt:           lead.LastCourseAt,
		LeadPartial:            leadPartial,
	})
}

// leadCounters derives the home's lead signals (master plan section 4.1). The
// credential axis is the CHEAPEST predicate available in this request: one call
// to the learning-path repo the same struct already holds, in the same database,
// counting the learner's live course-bound paths (learning_paths.course_id,
// migration 0043). No new dependency, no new event, no cross-DB read.
//
// It returns the counters and whether the COURSE axis is degraded. A failed or
// unwired path read never 500s the goals list (the goals are the resource; the
// lead is the enrichment) but it is NEVER passed off as "no course-bound paths"
// either: the caller declares it on the wire as `leadPartial`.
func (s *ExtServer) leadCounters(
	ctx context.Context,
	tenantID, gcid string,
	goals []goal.Goal,
) (goal.Lead, bool) {
	if s.Paths == nil {
		return goal.DeriveLead(goals, nil), true
	}
	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		return goal.DeriveLead(goals, nil), true
	}
	leads := make([]goal.PathLead, 0, len(paths))
	for _, p := range paths {
		if p == nil {
			continue
		}
		leads = append(leads, goal.PathLead{
			CourseBound: strings.TrimSpace(p.CourseID) != "",
			UpdatedAt:   p.UpdatedAt,
		})
	}
	return goal.DeriveLead(goals, leads), false
}

// companionHolderGoalID returns the id of the learner's OTHER live Goal that
// currently holds this Companion, or "" when none does (CHO-2403).
//
// Reads the learner's own goal list, which already filters soft-deleted rows,
// so removing a map genuinely frees its Companion rather than holding it
// hostage. Fail-LOUD: a read error is returned, never swallowed into a
// permissive "nobody holds it", because swallowing it would let the very
// double-assignment this guard exists to stop through on a transient blip.
func (s *ExtServer) companionHolderGoalID(
	ctx context.Context,
	tenantID, gcid, companionID, exceptGoalID string,
) (string, error) {
	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		return "", err
	}
	for _, other := range goals {
		if other == nil || other.GoalID == exceptGoalID || other.AttachedCompanionID == nil {
			continue
		}
		if *other.AttachedCompanionID == companionID {
			return other.GoalID, nil
		}
	}
	return "", nil
}

// masteredConceptKeys loads the learner's MASTERED (grown) Growth-Edge concept
// keys into a normalised-slug set, for the verified-progress %-ring on each Goal
// (CHO-1921). Computed intra-domain at read time from the live weakness signal —
// no new state, no events, no cross-DB read.
//
// FAIL-SOFT (mirrors the KG growth overlay, kg_growth_overlay.go): a nil repo OR
// a read error yields an EMPTY set (progress 0 everywhere) and NEVER breaks the
// goals read — the progress ring is enrichment, not data. The request ctx already
// carries tenant + gcid (RLS), so it is threaded as-is (no new tenant plumbing).
func (s *ExtServer) masteredConceptKeys(ctx context.Context, tenantID, gcid string) map[string]bool {
	// Fail-soft wrapper around the shared derivation (lw.MasteredConceptKeys,
	// also used by the goal-graduation subscriber): a nil repo OR a read error
	// yields an EMPTY set (progress 0 everywhere) and NEVER breaks the goals
	// read — the ring is enrichment, not data. The subscriber shares this exact
	// derivation but propagates the error (fail-loud → NACK).
	mastered, err := lw.MasteredConceptKeys(ctx, s.LearnerWeakness, tenantID, gcid)
	if err != nil {
		return map[string]bool{}
	}
	return mastered
}

// createGoal — POST /v1/me/goals.
func (s *ExtServer) createGoal(w http.ResponseWriter, r *http.Request, ctx context.Context, tenantID, gcid string) {
	var req goalCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID:       tenantID,
		LearnerGCID:    gcid,
		Kind:           goal.Kind(strings.TrimSpace(req.Kind)),
		ChoraTargetRef: req.ChoraTargetRef,
		ConceptSet:     req.ConceptSet,
		NorthStarNote:  req.NorthStarNote,
		RootConceptID:  req.RootConceptID,
		Now:            time.Now().UTC(),
	})
	if err != nil {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_GOAL", err.Error())
		return
	}
	if err := s.Goals.Create(ctx, g); err != nil {
		extWriteError(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusCreated, goalToDTO(*g))
}

// deleteMeGoal — DELETE /v1/me/goals/{id}: soft-delete the whole map (Goal).
// Pseudonymise-not-delete (ddd §5): sets deleted_at so default reads (deleted_at
// IS NULL) omit it; the row + its concepts persist. Idempotent — an unknown,
// foreign, or already-deleted goal renders 404 (never reveal or delete another
// learner's map). 204 No Content on success. s.Goals nil is already guarded by
// the caller (handleMeGoalByID).
func (s *ExtServer) deleteMeGoal(w http.ResponseWriter, r *http.Request, id string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	g, err := s.Goals.GetByID(ctx, tenantID, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	// Not found OR cross-learner/tenant leak → 404 (never reveal another's goal).
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	g.SoftDelete(time.Now().UTC())
	if err := s.Goals.Update(ctx, g); err != nil {
		extWriteError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeGoalByID — PATCH /v1/me/goals/{id}.
func (s *ExtServer) handleMeGoalByID(w http.ResponseWriter, r *http.Request) {
	if s.Goals == nil {
		extWriteError(w, http.StatusServiceUnavailable, "GOALS_UNAVAILABLE", "goal repo not wired")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, goalsByIDPrefix), "/")
	// Sub-resource: POST /v1/me/goals/{id}/learning-edges (CHO-2038).
	if parts := strings.Split(rest, "/"); len(parts) == 2 && parts[1] == learningEdgesSuffix {
		s.handleMeGoalLearningEdges(w, r, parts[0])
		return
	}
	// Sub-resource: GET /v1/me/goals/{id}/knowledge (CHO-2118) — the Companion's
	// per-goal reflection (tier-1 block + the cached tier-2 synthesis). Nested
	// here, like the campaign doors, so the gateway prefix proxy + the Istio
	// /v1/me/goals/* allowlist admit it without new edge plumbing.
	if parts := strings.Split(rest, "/"); len(parts) == 2 && parts[1] == goalKnowledgeSuffix {
		s.handleMeGoalKnowledge(w, r, parts[0])
		return
	}
	// Sub-resource: POST /v1/me/goals/{id}/campaign/questions/answer (ADR-227
	// WS-C7, CHO-2086) — the hex-tap practice answers door. A 4-part campaign
	// path, checked before the 3-part switch below.
	if parts := strings.Split(rest, "/"); len(parts) == 4 && parts[1] == "campaign" && parts[2] == "questions" && parts[3] == "answer" {
		s.handleMeGoalCampaignAnswer(w, r, parts[0])
		return
	}
	// Sub-resources: POST /v1/me/goals/{id}/campaign/{focus|seal} (ADR-227
	// WS-C1, CHO-2080) — nested here so the gateway subtree proxy + Istio
	// authz wildcard admit them without new plumbing.
	if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[1] == "campaign" {
		switch parts[2] {
		case "focus":
			s.handleMeGoalCampaignFocus(w, r, parts[0])
		case "seal":
			s.handleMeGoalCampaignSeal(w, r, parts[0])
		case "questions":
			// ADR-227 D13 (WS-C3, CHO-2082) — the "generate once, retrieve
			// forever" question-serve read side (AC3).
			s.handleMeGoalCampaignQuestions(w, r, parts[0])
		default:
			extWriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown campaign action")
		}
		return
	}
	id := rest
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "")
		return
	}
	if strings.Contains(id, "/") {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown goal sub-resource")
		return
	}
	// DELETE /v1/me/goals/{id} — soft-delete the whole map (Goal). Handled
	// before the PATCH-only guard below.
	if r.Method == http.MethodDelete {
		s.deleteMeGoal(w, r, id)
		return
	}
	if r.Method != http.MethodPatch {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)

	var req goalPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if req.DetachCompanion && req.AttachedCompanionID != nil && strings.TrimSpace(*req.AttachedCompanionID) != "" {
		extWriteError(w, http.StatusBadRequest, "AMBIGUOUS_BOND", "cannot attach and detach a Companion in one request")
		return
	}

	g, err := s.Goals.GetByID(ctx, tenantID, gcid, id)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	// Not found OR cross-learner/tenant leak → 404 (never reveal another's goal).
	if g == nil || g.LearnerGCID != gcid || g.TenantID != tenantID {
		extWriteError(w, http.StatusNotFound, "GOAL_NOT_FOUND", "")
		return
	}

	now := time.Now().UTC()
	if req.Status != nil {
		if err := g.SetStatus(goal.Status(strings.TrimSpace(*req.Status)), now); err != nil {
			writeGoalDomainError(w, err)
			return
		}
	}
	summonCompanionID := ""
	switch {
	case req.DetachCompanion:
		if err := g.DetachCompanion(now); err != nil {
			writeGoalDomainError(w, err)
			return
		}
	case req.AttachedCompanionID != nil && strings.TrimSpace(*req.AttachedCompanionID) != "":
		// CHO-2013 P1 (R3-1): the attach IS the theme commitment — the
		// companion side (specialization derive + resonant-pick clear +
		// specialization_changed emit) rides the Summoner. Fail-loud when
		// unwired; validate ownership BEFORE the Goal mutates.
		fid := strings.TrimSpace(*req.AttachedCompanionID)
		if s.Summoner == nil {
			extWriteError(w, http.StatusServiceUnavailable, "SUMMON_NOT_WIRED", "companion summoning not wired")
			return
		}
		if _, err := s.Summoner.ValidateOwned(ctx, tenantID, gcid, fid); err != nil {
			if errors.Is(err, companion.ErrInstanceNotFound) {
				extWriteError(w, http.StatusNotFound, "COMPANION_NOT_FOUND", "")
				return
			}
			extWriteError(w, http.StatusInternalServerError, "COMPANION_LOOKUP_FAILED", err.Error())
			return
		}
		// CHO-2403: one Companion, one knowledge graph. Enforced here rather
		// than in the aggregate because the uniqueness spans Goal INSTANCES,
		// and an aggregate cannot see its siblings. Guaranteed underneath by
		// the partial unique index in migration 0109; this check exists so the
		// learner gets a 409 with a reason instead of a 23505 surfacing as 500.
		//
		// Skipped when THIS goal already holds the bond, so the idempotent
		// re-attach (double-tap, PATCH retry) keeps healing instead of 409ing.
		if g.AttachedCompanionID == nil || *g.AttachedCompanionID != fid {
			holder, herr := s.companionHolderGoalID(ctx, tenantID, gcid, fid, g.GoalID)
			if herr != nil {
				extWriteError(w, http.StatusInternalServerError, "GOAL_LIST_FAILED", herr.Error())
				return
			}
			if holder != "" {
				extWriteError(w, http.StatusConflict, "COMPANION_ALREADY_ASSIGNED",
					"that Companion is already assigned to another knowledge graph; detach it there first")
				return
			}
		}
		if err := g.AttachCompanion(fid, now); err != nil {
			writeGoalDomainError(w, err)
			return
		}
		summonCompanionID = fid
	}
	// WS-C make-root: re-anchor the Goal to a new root ConceptNode (ADR-214 D1).
	rerooted := false
	if req.RootConceptID != nil && strings.TrimSpace(*req.RootConceptID) != "" {
		before := derefString(g.RootConceptID)
		if err := g.AnchorToConcept(*req.RootConceptID, now); err != nil {
			writeGoalDomainError(w, err)
			return
		}
		// Only a root that ACTUALLY moved invalidates: re-anchoring to the same
		// concept describes the same subtree, so the reflection is still true and
		// staling it would burn a regen on every idempotent retry.
		rerooted = derefString(g.RootConceptID) != before
	}
	// WS-D Mastery lens: personal-axis "done for me" toggle (ADR-213). Never
	// touches the verified Status (the impermeable wall).
	if req.PersonalComplete != nil {
		var perr error
		if *req.PersonalComplete {
			perr = g.MarkPersonalComplete(now)
		} else {
			perr = g.ReopenPersonal(now)
		}
		if perr != nil {
			writeGoalDomainError(w, perr)
			return
		}
	}

	// CHO-2118: a re-rooted goal's cached reflection is about a DIFFERENT concept
	// subtree (ADR-214) — the strongest invalidation signal there is, and the only
	// one with no event behind it (goal CRUD emits nothing), so it is staled here.
	//
	// Deliberately BEFORE the Update, and fail-loud. If it ran after the write, a
	// failure would 500 the learner, and their retry would find the root already
	// moved — a no-op re-anchor that detects no change and never invalidates,
	// leaving a reflection about an abandoned subtree served as current forever.
	// Invalidate-then-write means a retry re-runs both. The inverse failure (the
	// Update below fails after a successful invalidation) merely costs one
	// regenerated reflection against the unchanged root — cheap, and correct.
	if rerooted && s.GoalKnowledge != nil {
		if _, err := s.GoalKnowledge.InvalidateGoal(ctx, tenantID, gcid, g.GoalID, companiongoalknowledge.ReasonGoalRerooted); err != nil {
			extWriteError(w, http.StatusInternalServerError, "GOAL_KNOWLEDGE_INVALIDATION_FAILED", err.Error())
			return
		}
	}

	if err := s.Goals.Update(ctx, g); err != nil {
		extWriteError(w, http.StatusInternalServerError, "UPDATE_FAILED", err.Error())
		return
	}
	// CHO-2013 P1 (R3-1): with the bond persisted, apply the companion-side
	// Summon effects. A failure here is a 5xx AND the bond stays — the PATCH
	// is retryable (re-attach is idempotent; the derive no-ops on same theme).
	if summonCompanionID != "" {
		traceparent, tracestate := traceFromHeaders(r)
		if _, err := s.Summoner.OnGoalBound(ctx, companion.SummonInput{
			TenantID:    tenantID,
			OwnerGCID:   gcid,
			CompanionID: summonCompanionID,
			Theme:       s.resolveMapTheme(ctx, tenantID, gcid, g),
			Traceparent: traceparent,
			Tracestate:  tracestate,
		}); err != nil {
			extWriteError(w, http.StatusInternalServerError, "SUMMON_FAILED", err.Error())
			return
		}
	}
	extWriteJSON(w, http.StatusOK, goalToDTO(*g))
}

// derefString reads a *string as a trimmed value ("" when nil), so a nil root
// and a blank root compare equal.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// writeGoalDomainError maps domain sentinels to HTTP status codes.
func writeGoalDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, goal.ErrAlreadyAttached):
		extWriteError(w, http.StatusConflict, "COMPANION_ALREADY_ATTACHED", err.Error())
	case errors.Is(err, goal.ErrNotAttached):
		extWriteError(w, http.StatusConflict, "COMPANION_NOT_ATTACHED", err.Error())
	case errors.Is(err, goal.ErrDeleted):
		extWriteError(w, http.StatusConflict, "GOAL_DELETED", err.Error())
	case errors.Is(err, goal.ErrInvalid):
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_GOAL", err.Error())
	default:
		extWriteError(w, http.StatusInternalServerError, "GOAL_MUTATION_FAILED", err.Error())
	}
}
