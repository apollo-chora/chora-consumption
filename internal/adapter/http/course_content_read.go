// course_content_read.go — GET /v1/me/courses/{course_id}/content (CHO-1612).
//
// Returns the ordered, heterogeneous curriculum for a course from the local
// course-content projection (hydrated from chora.delivery.course.content_composed.v1).
// The curriculum is course-level (not per-learner); per-learner progress lives
// in the LearningPath read. Cross-DB-forbidden: served entirely from the local
// projection.
package http

import (
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
)

type courseContentItemResp struct {
	ItemID   string `json:"item_id"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Position int    `json:"position"`
}

type courseContentResp struct {
	CourseID string                  `json:"course_id"`
	Items    []courseContentItemResp `json:"items"`
}

// handleMeCourseContent serves GET /v1/me/courses/{course_id}/content.
func (s *ExtServer) handleMeCourseContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	tenantID, _, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	// Path: /v1/me/courses/{course_id}/content
	rest := strings.TrimPrefix(r.URL.Path, "/v1/me/courses/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "content" || parts[0] == "" {
		extWriteError(w, http.StatusNotFound, "NOT_FOUND", "expected /v1/me/courses/{course_id}/content")
		return
	}
	courseID := parts[0]

	// pg course_content projection is RLS-bound; course content is tenant-scoped
	// (not per-learner), so tenant-only ctx suffices for rls.ApplySession.
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, err := s.CourseContent.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PROJECTION_ERROR", err.Error())
		return
	}
	out := courseContentResp{CourseID: courseID, Items: make([]courseContentItemResp, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, courseContentItemResp{
			ItemID:   it.ItemID,
			Kind:     string(it.Kind),
			Ref:      it.Ref,
			Title:    it.Title,
			Position: it.Position,
		})
	}
	extWriteJSON(w, http.StatusOK, out)
}
