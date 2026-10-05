// transcript_handler.go — W6 Slice 1 (Four-Mode plan "Outcome spine") read
// API over the StudentTranscript projection:
//
//	GET /v1/me/transcript                                   self, gcid-scoped
//	GET /v1/transcript/by-assessments?assessment_ids=a,b,c   tenant-scoped,
//	                                                         instructor/admin
//	                                                         role-gated (the
//	                                                         R+ per-offering
//	                                                         gradebook join)
//
// Both routes 503 when s.Transcript is unwired (fail-loud, never a silently
// empty list) — see ExtServer.WireTranscriptPg.
package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/auth/servicemesh"
	"github.com/apollo-chora/chora-common/tracing"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// transcriptEntryResp is the wire shape for one TranscriptEntry. gcid is
// included on BOTH endpoints (redundant with the caller's own identity on
// GET /v1/me/transcript, but keeps one shared shape the FE can type once
// across the self view and the cross-learner gradebook-join view, where gcid
// is load-bearing). Score/passed/course_id render as explicit `null` (no
// omitempty) rather than being omitted, so the FE contract always carries
// the key.
type transcriptEntryResp struct {
	EntryID   string `json:"entry_id"`
	GCID      string `json:"gcid"`
	Kind      string `json:"kind"`
	SourceRef string `json:"source_ref"`
	Title     string `json:"title"`
	// DeliveryType is the MODE that delivered this outcome
	// ({graduate|short|async}), snapshotted at grade time (CHO-2224). It is
	// ORTHOGONAL to Kind: Kind says WHAT the outcome is, DeliveryType says which
	// mode delivered it, and §10.6 criterion 1 ("rolls up graded components from
	// >=2 MODES") is demonstrated over exactly this field — without it two
	// same-kind rows are indistinguishable to any reader.
	//
	// Explicit null (a pointer, no omitempty) when unattributable — a
	// freestanding assessment has no Offering, certification.issued.v1 carries no
	// mode, and pre-field-14 events carry nothing. Matches the score/passed/
	// course_id contract above: the key is ALWAYS present.
	DeliveryType  *string  `json:"delivery_type"`
	ScoreEarned   *float64 `json:"score_earned"`
	ScorePossible *float64 `json:"score_possible"`
	ScorePercent  *float64 `json:"score_percent"`
	// Unseen and SeenAt are the B6 item 2 read-receipt, learner-private.
	// Both keys are ALWAYS present, matching the score contract above: a
	// missing key and a false are different claims, and the home card ranks
	// on this. Populated ONLY on this per-learner read; the cross-learner
	// gradebook read deliberately carries neither.
	Unseen     bool    `json:"unseen"`
	SeenAt     *string `json:"seen_at"`
	Passed     *bool   `json:"passed"`
	CourseID   *string `json:"course_id"`
	OccurredAt string  `json:"occurred_at"`
}

func toTranscriptEntryResp(e *st.TranscriptEntry) transcriptEntryResp {
	var courseID *string
	if e.CourseID != "" {
		c := e.CourseID
		courseID = &c
	}
	var deliveryType *string
	if e.DeliveryType != "" {
		d := string(e.DeliveryType)
		deliveryType = &d
	}
	// The read-receipt travels as BOTH a derived boolean and the raw stamp:
	// the boolean is what the card ranks on, the stamp is what a reader needs
	// to explain why. A nil SeenAt renders an explicit null, never an omitted
	// key (B6 item 2).
	var seenAt *string
	if e.SeenAt != nil {
		iso := e.SeenAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		seenAt = &iso
	}
	return transcriptEntryResp{
		EntryID:       e.EntryID,
		GCID:          e.GCID,
		Kind:          string(e.Kind),
		SourceRef:     e.SourceRef,
		Title:         e.Title,
		DeliveryType:  deliveryType,
		Unseen:        e.Unseen(),
		SeenAt:        seenAt,
		ScoreEarned:   e.ScoreEarned,
		ScorePossible: e.ScorePossible,
		ScorePercent:  e.ScorePercent,
		Passed:        e.Passed,
		CourseID:      courseID,
		OccurredAt:    e.OccurredAt.Format(time.RFC3339Nano),
	}
}

// ---------- GET /v1/me/transcript ----------

func (s *ExtServer) handleMeTranscript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	if s.Transcript == nil {
		extWriteError(w, http.StatusServiceUnavailable, "TRANSCRIPT_NOT_WIRED", "")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	limit := parseLimit(r, 50)
	// The pg-backed Transcript repo derives its RLS session from the context
	// (rls.ApplySession → tracing.{TenantID,GCID}FromContext); without this
	// wrap the query runs with no tenant set and the RLS-bound role returns 0
	// rows (mirrors handleMeTopicRetention).
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	entries, err := s.Transcript.ListByGCID(ctx, tenantID, gcid, limit)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRANSCRIPT_READ_FAILED", err.Error())
		return
	}
	out := make([]transcriptEntryResp, 0, len(entries))
	for _, e := range entries {
		out = append(out, toTranscriptEntryResp(e))
	}
	// Resolve course-provenanced titles at read time (CHO-2242): a certification
	// row is projected with title=course_id (certification.issued.v1 carries no
	// course name — transcript_subscriber.HandleCertIssued), so without this the
	// cert row renders a raw UUID. One batched course_directory lookup for the
	// whole page; additive + non-fatal (a miss/error keeps the stored title).
	// Assessment rows carry no course_id and are left untouched. out[i] aligns
	// with entries[i] — the response is built in order above.
	courseIDs := make([]string, len(entries))
	for i, e := range entries {
		courseIDs[i] = e.CourseID
	}
	titles := s.lookupCourseTitles(ctx, courseIDs)
	for i, e := range entries {
		if real := strings.TrimSpace(titles[e.CourseID]); real != "" {
			out[i].Title = real
		}
	}
	// unseen_count is a roll-up over the learner's WHOLE transcript, counted in
	// the store, so a paged list cannot badge a number that only describes the
	// page it happened to fetch.
	//
	// It used to be st.CountUnseen(entries), over the slice the LIMIT-ed read
	// above had just returned, which made this comment's claim false for every
	// learner holding more results than the page: exactly the learner with a
	// backlog, who is the one the badge exists for.
	//
	// A count the store could not produce is a 500, never a zero, because zero
	// renders as "you are all caught up".
	unseen, err := s.Transcript.CountUnseenByGCID(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRANSCRIPT_READ_FAILED", err.Error())
		return
	}
	extWriteJSON(w, http.StatusOK, map[string]any{
		"items":        out,
		"unseen_count": unseen,
	})
}

// ---------- GET /v1/transcript/by-assessments ----------

func (s *ExtServer) handleTranscriptByAssessments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	if s.Transcript == nil {
		extWriteError(w, http.StatusServiceUnavailable, "TRANSCRIPT_NOT_WIRED", "")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		extWriteError(w, http.StatusBadRequest, "MISSING_TENANT", "X-Tenant-Id required")
		return
	}
	// Fail-closed instructor/admin role gate — this endpoint deliberately
	// returns OTHER learners' entries (the R+ per-offering gradebook join),
	// so a missing/insufficient role header is 403, never a silent narrow.
	// Mirrors chora-delivery's hasOfferingAdminRole (x-mesh-user-roles,
	// mesh-asserted) so the same role vocabulary gates the join on both
	// sides of the read.
	if !hasTranscriptAdminRole(r) {
		extWriteError(w, http.StatusForbidden, "FORBIDDEN", "caller lacks instructor/admin/training-admin role")
		return
	}
	ids := parseCSVParam(r, "assessment_ids")
	if len(ids) == 0 {
		extWriteError(w, http.StatusBadRequest, "MISSING_ASSESSMENT_IDS", "assessment_ids query param required")
		return
	}
	// Tenant-only RLS session (no gcid — this read intentionally spans every
	// learner in the tenant; rls.ApplySession treats a missing gcid as fine
	// for non-user-scoped reads).
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	entries, err := s.Transcript.ListByAssessmentIDs(ctx, tenantID, ids)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "TRANSCRIPT_READ_FAILED", err.Error())
		return
	}
	// The read-receipt is stripped here, deliberately and by construction.
	//
	// Two independent reasons, and the belt-and-braces is the point. First,
	// whether a learner has OPENED their own result is a fact about their
	// reading, not about the assessment, so handing every instructor a
	// per-student read-receipt would be a disclosure nobody asked for.
	// Second, and worse if this were missed: the cross-learner query does not
	// SELECT seen_at at all, so every entry arrives with a nil stamp and
	// Unseen() would report TRUE for every student on the roster. That is not
	// a leak, it is a confident lie, and it is exactly the shape a shared
	// mapper produces when one caller's projection is widened.
	out := make([]transcriptEntryResp, 0, len(entries))
	for _, e := range entries {
		row := toTranscriptEntryResp(e)
		row.Unseen, row.SeenAt = false, nil
		out = append(out, row)
	}
	extWriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// hasTranscriptAdminRole reports whether the caller holds an instructor-level
// role, per the mesh-asserted servicemesh.HeaderUserRoles (x-mesh-user-roles)
// header — mirrors chora-delivery's hasOfferingAdminRole (assessment_handler.go
// / offering_handler.go) so the same role vocabulary gates the R+ gradebook
// join identically on both sides.
func hasTranscriptAdminRole(r *http.Request) bool {
	roles := strings.ToLower(r.Header.Get(servicemesh.HeaderUserRoles))
	if roles == "" {
		return false
	}
	for _, role := range strings.Split(roles, ",") {
		switch strings.TrimSpace(role) {
		case "instructor", "admin", "training-admin", "training_admin", "tenant_admin":
			return true
		}
	}
	return false
}

// parseCSVParam splits a comma-separated query param into a trimmed,
// empty-filtered slice. Returns nil for an absent/blank param.
func parseCSVParam(r *http.Request, key string) []string {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
