// me_handlers.go — S4.2 /v1/me/* endpoints for the Phyllis MVP.
//
// Endpoints:
//
//	POST /v1/me/atom-sessions                    start AtomAttempt
//	POST /v1/me/atom-sessions/{id}/answers       submit answer (server-side MCQ grade)
//	GET  /v1/me/learning-paths                   list learner's paths
//	GET  /v1/me/learning-paths/{id}              path detail
//	GET  /v1/me/topic-retention                  paginated retention scores
//
// Mandatory request headers:
//
//	X-Tenant-Id  tenant UUIDv7
//	gcid         learner UUIDv7
//	traceparent  W3C trace context (mandatory per CLAUDE.md §6)
//
// Per audit-content-fillgaps.md §3.2: server-side MCQ grading replaces
// the caller-supplied `correct: bool` from the legacy endpoint. The
// answer key comes from the local atom_index projection (cross-DB-
// forbidden — chora-consumption never queries chora_creation).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

// ---------- payloads ----------

type meStartSessionReq struct {
	AtomID string `json:"atom_id"`
}

type meSubmitAnswerReq struct {
	AnswerID         string `json:"answer_id"`          // S4.2 idempotency key
	SelectedOptionID string `json:"selected_option_id"` // MCQ — server-side identity grading (CHO-1627)
	HintUsed         bool   `json:"hint_used"`
}

type meSubmitAnswerResp struct {
	SessionID      string `json:"session_id"`
	Status         string `json:"status"`
	IsCorrect      bool   `json:"is_correct"`
	Duplicate      bool   `json:"duplicate"`
	HintsUsed      int    `json:"hints_used"`
	AnswerCount    int    `json:"answer_count"`
	PathsAdvanced  int    `json:"paths_advanced"`
	PathsCompleted int    `json:"paths_completed"`
	IMDAEvidenceID string `json:"imda_evidence_id,omitempty"`
	// Campaign carries the ladder outcome when this dose answer folded into
	// today's campaign hex (CHO-2315): the dose lane renders the same won / cleared /
	// paced / counted feedback as the hex-tap practice lane. Absent when the
	// answer folded into no campaign (not wired, no march today, not material).
	Campaign *doseCampaignOutcomeResp `json:"campaign,omitempty"`
}

type mePathSummary struct {
	PathID       string `json:"path_id"`
	CourseID     string `json:"course_id,omitempty"`
	EnrollmentID string `json:"enrollment_id,omitempty"`

	// SourceType — ADR-233 D2 provenance. What this path was DERIVED from.
	//
	// 🔴 A study list is `source_type == "collection"`. It is NEVER
	// `!= "course"`. The column is NOT NULL and DEFAULTS to 'ad_hoc', and
	// migration 0093's backfill only stamped 'course' where a course_id already
	// existed — so all three values are live on this wire, and the negative form
	// sweeps every legacy ad_hoc path in as a study list. Guarded by
	// TestGetMeLearningPaths_StudyListDiscriminatorIsPositive_ADR233_D2.
	//
	// Without this field a study list reached A+ as a course with an empty
	// course_id: the wire did not carry the distinction, so the frontend could
	// not make it.
	SourceType string `json:"source_type,omitempty"`
	// SourceID — course_id | collection_id | "" (ad_hoc). Distinct from
	// CourseID above, which is the delivery BINDING (an enrollment reference),
	// not provenance; a course-sourced path carries both.
	SourceID string `json:"source_id,omitempty"`

	Title           string   `json:"title"`
	AtomIDs         []string `json:"atom_ids"`
	CurrentIndex    int      `json:"current_index"`
	TotalAtoms      int      `json:"total_atoms"`
	ProgressPercent float32  `json:"progress_percent"`
	Completed       bool     `json:"completed"`
}

type meTopicScore struct {
	TopicID        string  `json:"topic_id"`
	Strength       float64 `json:"strength_days"`
	RetentionScore float64 `json:"retention_score"`
	RetentionAtNow float64 `json:"retention_at_now"`
	ReviewCount    int     `json:"review_count"`
	LastReviewedAt string  `json:"last_reviewed_at"`
}

// ---------- POST /v1/me/atom-sessions ----------

func (s *ExtServer) handleMeAtomSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	var req meStartSessionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if strings.TrimSpace(req.AtomID) == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_ATOM_ID", "")
		return
	}
	// CHO-2273 (SECURITY) — a learner may only take a PUBLISHED atom. A draft is
	// author WIP; taking it would grade against a projection that is not a
	// learner answer key (and, post-CHO-2272, against a stale key). Gate on
	// Playable(), mirroring the dose/companion/KG precedent. A MISSING atom_index
	// row means the projection has not caught up (a published-but-unprojected
	// atom) — that must NOT be blocked; only a row we can see to be a draft is
	// refused. RLS-bound Get needs the tenant/gcid on ctx.
	startCtx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	if ai, aerr := s.AtomIndex.Get(startCtx, req.AtomID); aerr == nil {
		if !ai.Playable() {
			extWriteError(w, http.StatusUnprocessableEntity, "ATOM_NOT_PUBLISHED",
				"atom is not published — drafts are not takeable")
			return
		}
	} else if !errors.Is(aerr, atom_index.ErrNotFound) {
		extWriteError(w, http.StatusInternalServerError, "ATOM_LOOKUP_FAILED", aerr.Error())
		return
	}
	sess, err := atom_attempt.Start(tenantID, gcid, req.AtomID, atom_attempt.SystemClock)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "INVALID_START", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, sess.SessionID+"|started")
	if err := s.Publisher.Publish(events.TopicAtomSessionStarted, env, map[string]any{
		"session_id":   sess.SessionID,
		"learner_gcid": gcid,
		"atom_id":      sess.AtomID,
		"started_at":   sess.StartedAt,
		"source":       "v1_me_api",
	}); err != nil {
		extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
		return
	}

	extWriteJSON(w, http.StatusCreated, toExtSessionResp(sess))
}

// ---------- POST /v1/me/atom-sessions/{id}/answers ----------

func (s *ExtServer) handleMeAtomSessionsByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/atom-sessions/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_SESSION_ID", "")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "answers" {
		if r.Method != http.MethodPost {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.submitMeAnswer(w, r, id)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
			return
		}
		s.getSession(w, r, id)
		return
	}
	extWriteError(w, http.StatusNotFound, "NOT_FOUND", "")
}

func (s *ExtServer) submitMeAnswer(w http.ResponseWriter, r *http.Request, sessionID string) {
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	sess, err := s.Sessions.Get(r.Context(), sessionID)
	if errors.Is(err, atom_attempt.ErrNotFound) {
		extWriteError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_READ_FAILED", err.Error())
		return
	}
	// RLS leak guard: cross-tenant or cross-learner attempts return 404.
	if sess.TenantID != tenantID || sess.LearnerGCID != gcid {
		extWriteError(w, http.StatusNotFound, "SESSION_NOT_FOUND", "")
		return
	}
	var req meSubmitAnswerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	// Server-side MCQ grading: look up atom_index entry; if MCQ, grade by
	// stable option-id identity (CHO-1627: selected_option_id == correct
	// option id), NOT positional index. pg atom_index is RLS-bound
	// (rls.ApplySession reads tenant/gcid from ctx); wrap r.Context() so the
	// MCQ grade lookup isn't filtered to 0 rows.
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	correct := false
	answerText := ""
	gradedTopic := ""
	if req.SelectedOptionID != "" {
		// CHO-2030 buildout (2026-07-03, P1.D walk finding #5): grading
		// gaps must surface, never silently mark the learner wrong. A
		// projection row without an answer key (pre-dose-era atoms) or a
		// missing row is 422 ATOM_NOT_GRADEABLE (the attempt is NOT
		// consumed); a repo failure is 500 GRADING_UNAVAILABLE.
		atom, atomErr := s.AtomIndex.Get(ctx, sess.AtomID)
		switch {
		case errors.Is(atomErr, atom_index.ErrNotFound):
			extWriteError(w, http.StatusUnprocessableEntity, "ATOM_NOT_GRADEABLE",
				"atom has no server answer key (not indexed) — cannot grade this submission")
			return
		case atomErr != nil:
			extWriteError(w, http.StatusInternalServerError, "GRADING_UNAVAILABLE", atomErr.Error())
			return
		}
		// CHO-2273 (SECURITY) — never score a DRAFT (author WIP). A row we can see
		// to be a draft is refused; a published-but-unprojected atom already 422'd
		// as ATOM_NOT_GRADEABLE via the ErrNotFound arm above. This also catches a
		// session started while the atom was published then unpublished mid-take.
		if !atom.Playable() {
			extWriteError(w, http.StatusUnprocessableEntity, "ATOM_NOT_PUBLISHED",
				"atom is not published — drafts are not gradeable")
			return
		}
		ok, gerr := atom.Grade(req.SelectedOptionID)
		if gerr != nil {
			extWriteError(w, http.StatusUnprocessableEntity, "ATOM_NOT_GRADEABLE",
				"atom has no server answer key — cannot grade this submission")
			return
		}
		correct = ok
		answerText = req.SelectedOptionID
		gradedTopic = atom.PrimaryTopic()
	}

	res, err := sess.SubmitAnswerWithID(req.AnswerID, answerText, req.HintUsed, correct, atom_attempt.SystemClock)
	if err != nil {
		if errors.Is(err, atom_attempt.ErrInvalidTransition) || errors.Is(err, atom_attempt.ErrHintsExceeded) {
			extWriteError(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
			return
		}
		extWriteError(w, http.StatusBadRequest, "SUBMIT_FAILED", err.Error())
		return
	}
	if err := s.Sessions.Save(r.Context(), sess); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SESSION_PERSIST_FAILED", err.Error())
		return
	}

	traceparent, tracestate := extTraceFromHeaders(r)
	resp := meSubmitAnswerResp{
		SessionID:   sess.SessionID,
		Status:      string(sess.Status),
		IsCorrect:   res.Correct,
		Duplicate:   res.Duplicate,
		HintsUsed:   sess.HintsUsed,
		AnswerCount: sess.AnswerCount,
	}

	// Skip publish + side-effect on idempotent replay (atomic guarantee).
	if res.Duplicate {
		extWriteJSON(w, http.StatusOK, resp)
		return
	}

	// ADR-227 D6/D10 (CHO-2081): fold a SERVER-graded answer on today's
	// campaign material into the campaign ladder (rungs clear on
	// server-graded correct answers only; self-graded feedback never
	// counts). Fail-loud — verified ladder progress must never be silently
	// lost (the SessionCompletion posture below); a won/deleted node is a
	// benign no-op inside recordCampaignAnswer.
	if req.SelectedOptionID != "" {
		campOut, cerr := s.recordCampaignAnswer(ctx, tenantID, gcid, sess.AtomID, gradedTopic, correct, time.Now().UTC())
		if cerr != nil {
			extWriteError(w, http.StatusInternalServerError, "CAMPAIGN_FOLD_FAILED", cerr.Error())
			return
		}
		// Surface the fold so the dose lane shows won/cleared/paced feedback
		// (CHO-2315); nil (omitempty) when the answer folded into no campaign.
		resp.Campaign = campOut
	}

	// Publish completed event when transition lands on completed.
	if sess.Status == atom_attempt.StatusCompleted {
		env := events.NewEnvelope(tenantID, gcid, traceparent, tracestate, sess.SessionID+"|completed")
		if err := s.Publisher.Publish(events.TopicAtomSessionCompletedV1, env, map[string]any{
			"session_id":     sess.SessionID,
			"learner_gcid":   gcid,
			"atom_id":        sess.AtomID,
			"answer_correct": res.Correct,
			"hints_used":     sess.HintsUsed,
			"answer_count":   sess.AnswerCount,
			"completed_at":   sess.CompletedAt,
			"source_action":  "v1_me_api",
		}); err != nil {
			extWriteError(w, http.StatusInternalServerError, "EVENT_PUBLISH_FAILED", err.Error())
			return
		}

		// In-process side-effect fan-out (LearningPath.Advance +
		// TopicRetention.Review + IMDA D1 evidence emit). Must run under the
		// RLS-wrapped ctx from the grading read — the pg paths/atom_index
		// repos reject a bare request ctx (rls: tenant_id missing).
		side, err := s.SessionCompletion.Handle(ctx, subscribers.SessionCompletedInput{
			TenantID:    tenantID,
			LearnerGCID: gcid,
			AtomID:      sess.AtomID,
			IsCorrect:   res.Correct,
			SessionID:   sess.SessionID,
			Traceparent: traceparent,
			Tracestate:  tracestate,
			OccurredAt:  time.Now().UTC(),
		})
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "SIDE_EFFECT_FAILED", err.Error())
			return
		}
		resp.PathsAdvanced = side.PathsAdvanced
		resp.PathsCompleted = side.PathsCompleted
		resp.IMDAEvidenceID = side.IMDAEvidenceID
	}

	extWriteJSON(w, http.StatusOK, resp)
}

// ---------- GET /v1/me/learning-paths ----------

func (s *ExtServer) handleMeLearningPaths(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	// Course-bound detail lookup — single LearningPath for (tenant, course,
	// learner). Returns 404 (fail-loud) when the bootstrap event from
	// chora-delivery has not yet landed; the FE poller treats 404 as
	// "not-yet-ready, retry".
	if courseID := strings.TrimSpace(r.URL.Query().Get("course_id")); courseID != "" {
		s.getMeLearningPathByCourse(w, r, tenantID, gcid, courseID)
		return
	}
	limit := parseLimit(r, 50)
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	paths, err := s.Paths.ListByLearner(ctx, tenantID, gcid, limit)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PATHS_LIST_FAILED", err.Error())
		return
	}
	out := make([]mePathSummary, 0, len(paths))
	for _, p := range paths {
		out = append(out, mePathSummary{
			PathID:          p.PathID,
			CourseID:        p.CourseID,
			EnrollmentID:    p.EnrollmentID,
			SourceType:      p.SourceType,
			SourceID:        p.SourceID,
			Title:           p.Title,
			AtomIDs:         p.AtomIDs,
			CurrentIndex:    p.CurrentIndex,
			TotalAtoms:      len(p.AtomIDs),
			ProgressPercent: p.ProgressPercent(),
			Completed:       p.IsCompleted(),
		})
	}
	// Resolve course-provenanced titles at read time (CHO-2242): an
	// enrollment-bootstrapped path carries a "Course <uuid>" placeholder
	// (chora_delivery owns the name; enrollment.created carries none), so the
	// Continue-learning heading would render a raw UUID. One batched
	// course_directory lookup for the whole page; additive + non-fatal (a
	// miss/error keeps the placeholder). Study-list / ad_hoc paths carry no
	// course_id and are left untouched.
	courseIDs := make([]string, len(out))
	for i := range out {
		courseIDs[i] = out[i].CourseID
	}
	titles := s.lookupCourseTitles(ctx, courseIDs)
	for i := range out {
		if real := strings.TrimSpace(titles[out[i].CourseID]); real != "" {
			out[i].Title = real
		}
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---------- GET /v1/me/learning-paths?course_id={id} (course-learn page) ----------

// meCourseLearnResp is the hydrated single-path shape consumed by the A+
// course-learn page (open-course flow). Distinct from mePathSummary
// because: (a) atom titles are hydrated from the local atom_index
// projection (cross-DB-forbidden per ddd-enforcement HARD RULE); (b)
// per-atom state is derived from path.CurrentIndex; (c) mode exposes the
// Straight-Up / Discovery distinction (V1 = Straight-Up only).
type meCourseLearnResp struct {
	LearningPathID string `json:"learning_path_id"`
	CourseID       string `json:"course_id"`
	Title          string `json:"title"`
	Mode           string `json:"mode"`
	BootstrappedAt string `json:"bootstrapped_at"`
	CurrentIndex   int    `json:"current_index"`
	TotalAtoms     int    `json:"total_atoms"`
	// CompletedAtoms — how MANY atoms the learner has finished.
	//
	// 🔴 CHO-2169. A+ renders a "{completed} / {total} completed" line and divides
	// one by the other for the progress bar. It read `completed` — which is the
	// path-level BOOLEAN below. So the learner was shown a literal
	// "false / 5 completed", and completionPct computed `false / 5`.
	//
	// The two fields have almost the same name and answer entirely different
	// questions ("how many are done?" vs "is the whole thing done?"). Nothing
	// caught it: the FE model DECLARED `completed: number`, and TypeScript cannot
	// check a JSON body at runtime. A key-presence contract test would have passed
	// too — the key was there, it just meant something else.
	CompletedAtoms int                 `json:"completed_atoms"`
	Completed      bool                `json:"completed"`
	Atoms          []meCourseLearnAtom `json:"atoms"`
}

type meCourseLearnAtom struct {
	AtomID      string  `json:"atom_id"`
	Title       string  `json:"title"`
	Order       int     `json:"order"`
	State       string  `json:"state"` // completed | in_progress | not_started
	CompletedAt *string `json:"completed_at,omitempty"`
}

// lookupCourseTitles batch-resolves the distinct non-empty course IDs among ids
// to their real titles through the optional course_directory projection. It is
// the single shared posture behind every read that swaps a course-provenanced
// placeholder title for the real name — the course-learn detail, the
// learning-paths list, and the transcript. Additive + non-fatal: a nil reader, a
// lookup error, or no course refs all yield an empty map, so each caller keeps
// its own placeholder title. Dedup lives here, so a caller makes exactly one
// LookupTitles call per request regardless of how many rows repeat a course.
func (s *ExtServer) lookupCourseTitles(ctx context.Context, ids []string) map[string]string {
	if s.CourseDirectory == nil {
		return map[string]string{}
	}
	distinct := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		distinct = append(distinct, id)
	}
	if len(distinct) == 0 {
		return map[string]string{}
	}
	titles, err := s.CourseDirectory.LookupTitles(ctx, distinct)
	if err != nil {
		log.Printf("course-directory: title lookup failed for %d course id(s) (fail-soft, placeholders kept): %v", len(distinct), err)
		return map[string]string{}
	}
	// CHO-2247 Guard 2: a course id with no directory row means this request
	// renders a raw-UUID placeholder to a learner. That was previously SILENT —
	// only a lookup ERROR spoke — which is how the projection rotted to 5 rows
	// against 19 courses behind a green suite. Report the misses; never fail the
	// read (the enrichment stays additive).
	if missing := unresolvedIDs(distinct, titles); len(missing) > 0 {
		obs := s.OnUnresolvedCourseTitles
		if obs == nil {
			obs = defaultUnresolvedCourseTitles
		}
		obs(ctx, missing)
	}
	return titles
}

// unresolvedIDs returns the ids that came back without a non-empty title. An
// empty-string title counts as unresolved: the row exists but names nothing, so
// the caller still renders its placeholder.
func unresolvedIDs(requested []string, titles map[string]string) []string {
	var missing []string
	for _, id := range requested {
		if strings.TrimSpace(titles[id]) == "" {
			missing = append(missing, id)
		}
	}
	return missing
}

// defaultUnresolvedCourseTitles is the fallback observer: a loud log naming the
// courses served as placeholders. See ExtServer.OnUnresolvedCourseTitles for why
// this is a log and not a metric today (this platform has no metrics lane), and
// why a log alone cannot page anyone (the cost-pause sink exclusion).
func defaultUnresolvedCourseTitles(_ context.Context, courseIDs []string) {
	log.Printf("course-directory: %d course id(s) have NO directory row — a raw-UUID placeholder is being served to a learner: %v",
		len(courseIDs), courseIDs)
}

// LookupCourseTitlesForTest exposes the unexported batch resolver to the
// package's external test binary (package http_test). The resolver is the single
// shared posture behind every placeholder swap, so its miss-detection deserves
// direct tests rather than only incidental coverage through three handlers.
func (s *ExtServer) LookupCourseTitlesForTest(ctx context.Context, ids []string) map[string]string {
	return s.lookupCourseTitles(ctx, ids)
}

func (s *ExtServer) getMeLearningPathByCourse(w http.ResponseWriter, r *http.Request, tenantID, gcid, courseID string) {
	// The pg repos derive their RLS session from the context (rls.ApplySession
	// → tracing.{TenantID,GCID}FromContext); without this wrap the query runs
	// with no tenant set and the RLS-bound role returns 0 rows → spurious 404.
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	p, err := s.Paths.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "PATH_NOT_BOOTSTRAPPED",
			"no learning path yet for this course — retry after enrollment subscriber lands")
		return
	}
	atoms := make([]meCourseLearnAtom, 0, len(p.AtomIDs))
	completedAtoms := 0
	for i, atomID := range p.AtomIDs {
		title := ""
		if ai, aerr := s.AtomIndex.Get(ctx, atomID); aerr == nil {
			title = ai.Title
		}
		state := atomStateForIndex(i, p.CurrentIndex, p.IsCompleted())
		if state == "completed" {
			completedAtoms++
		}
		atoms = append(atoms, meCourseLearnAtom{
			AtomID: atomID,
			Title:  title,
			Order:  i + 1,
			State:  state,
		})
	}
	// The enrollment-bootstrapped path title is a "Course <uuid>" placeholder
	// (chora_delivery owns the course name; enrollment.created carries none —
	// see learning_path.BootstrapFromEnrollment). Resolve the real name from
	// the local course_directory projection (CHO-2059) so the A+ course-learn
	// heading shows a name, not a raw UUID. Additive + non-fatal: a nil reader,
	// lookup error, or directory miss keeps the path's own title.
	title := p.Title
	if real := strings.TrimSpace(s.lookupCourseTitles(ctx, []string{courseID})[courseID]); real != "" {
		title = real
	}
	extWriteJSON(w, http.StatusOK, meCourseLearnResp{
		LearningPathID: p.PathID,
		CourseID:       p.CourseID,
		Title:          title,
		Mode:           "straight_up",
		BootstrappedAt: p.CreatedAt.Format(time.RFC3339Nano),
		CurrentIndex:   p.CurrentIndex,
		TotalAtoms:     len(p.AtomIDs),
		CompletedAtoms: completedAtoms,
		Completed:      p.IsCompleted(),
		Atoms:          atoms,
	})
}

// atomStateForIndex maps (atom-position, path-progress) onto the FE's
// 3-state vocabulary. A completed path wraps the last atom as completed
// (Straight-Up mode never re-opens a finished atom).
func atomStateForIndex(i, current int, pathCompleted bool) string {
	if i < current {
		return "completed"
	}
	if i == current {
		if pathCompleted {
			return "completed"
		}
		return "in_progress"
	}
	return "not_started"
}

// ---------- GET /v1/me/learning-paths/{id} ----------

func (s *ExtServer) handleMeLearningPathByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/me/learning-paths/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_PATH_ID", "")
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
	p, err := s.Paths.Get(ctx, id)
	if err != nil {
		extWriteError(w, http.StatusNotFound, "PATH_NOT_FOUND", "")
		return
	}
	// RLS leak guard.
	if p.TenantID != tenantID || p.OwnerGCID != gcid {
		extWriteError(w, http.StatusNotFound, "PATH_NOT_FOUND", "")
		return
	}
	extWriteJSON(w, http.StatusOK, mePathSummary{
		PathID:          p.PathID,
		CourseID:        p.CourseID,
		EnrollmentID:    p.EnrollmentID,
		SourceType:      p.SourceType,
		SourceID:        p.SourceID,
		Title:           p.Title,
		AtomIDs:         p.AtomIDs,
		CurrentIndex:    p.CurrentIndex,
		TotalAtoms:      len(p.AtomIDs),
		ProgressPercent: p.ProgressPercent(),
		Completed:       p.IsCompleted(),
	})
}

// ---------- GET /v1/me/topic-retention ----------

func (s *ExtServer) handleMeTopicRetention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	limit := parseLimit(r, 50)
	// The pg-backed Retention repo derives its RLS session from the context
	// (rls.ApplySession → tracing.{TenantID,GCID}FromContext); without this wrap
	// the query runs with no tenant set and the RLS-bound role returns 0 rows.
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	scores, err := s.Retention.ListByLearner(ctx, tenantID, gcid, limit)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "RETENTION_READ_FAILED", err.Error())
		return
	}
	now := time.Now().UTC()
	out := make([]meTopicScore, 0, len(scores))
	for _, sc := range scores {
		out = append(out, meTopicScore{
			TopicID:        sc.TopicID,
			Strength:       sc.Strength,
			RetentionScore: sc.RetentionScore,
			RetentionAtNow: sc.RetentionAt(now),
			ReviewCount:    sc.ReviewCount,
			LastReviewedAt: sc.LastReviewedAt.Format(time.RFC3339Nano),
		})
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func parseLimit(r *http.Request, def int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > 200 {
		return 200
	}
	return n
}
