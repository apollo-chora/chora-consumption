// growth_edge_pending_reviews.go: the GET arm of /v1/me/growth-edges/uploads,
// serving the learner's diagnoses parked at the HITL review interrupt
// (UX refactor Phase B, package B6 item 1).
//
// Shares a mount with the POST producer in growth_edge_uploads_handler.go. The
// dispatch lives here and delegates POST back, so adding this read cannot turn
// the producer into a 405; a test pins exactly that.
//
// The status filter is REQUIRED on the GET. A bare GET on this path is a
// caller mistake, and answering it with the learner's whole upload history
// would be a much larger read that nobody asked for and that no card renders.
// Naming the mistake is cheaper for everyone than quietly widening the query.
package http

import (
	"net/http"
	"strconv"
	"strings"

	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// pendingReviewItem is one parked diagnosis on the wire.
//
// A trimmed projection rather than the whole aggregate: the home card needs
// enough to render and to deep-link, and the blob URI, the shred marker and
// the upserted edge ids are none of its business.
type pendingReviewItem struct {
	UploadID   string          `json:"upload_id"`
	UploadKind string          `json:"upload_kind"`
	Status     string          `json:"status"`
	GoalID     string          `json:"goal_id,omitempty"`
	CreatedAt  string          `json:"created_at"`
	Review     *wu.ReviewPanel `json:"review,omitempty"`
}

type pendingReviewsResponse struct {
	Items []pendingReviewItem `json:"items"`
}

// handleMeGrowthEdgeUploadsDispatch routes the shared mount by method.
func (s *ExtServer) handleMeGrowthEdgeUploadsDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleMePendingReviews(w, r)
		return
	}
	s.handleMeGrowthEdgeUploads(w, r)
}

// handleMePendingReviews serves GET /v1/me/growth-edges/uploads?status=awaiting_review.
func (s *ExtServer) handleMePendingReviews(w http.ResponseWriter, r *http.Request) {
	// Order matters. Validate the request BEFORE reporting the port unwired, so
	// a malformed call gets the same 400 whether or not the deployment happens
	// to have the read model wired; otherwise the error an operator sees would
	// depend on configuration rather than on the request.
	status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != string(wu.StatusAwaitingReview) {
		extWriteError(w, http.StatusBadRequest, "STATUS_FILTER_REQUIRED",
			"this collection is read only as ?status=awaiting_review")
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	_ = tenantID // the tenant rides the ctx into RLS; named here for symmetry with the sibling handlers

	if s.WeaknessPendingReviews == nil {
		extWriteError(w, http.StatusServiceUnavailable, "PENDING_REVIEWS_UNAVAILABLE",
			"pending-review read model not wired")
		return
	}

	// A garbage limit falls back to the repository default rather than 400ing.
	// This is the learner's own home card; refusing to render it over a bad
	// query string would be a worse answer than ignoring the query string.
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil {
			limit = n
		}
	}

	// The learner is the VERIFIED gcid header, never a query parameter. A
	// caller-supplied learner id here would let one learner read another's
	// parked diagnoses by guessing an id.
	uploads, err := s.WeaknessPendingReviews.ListAwaitingReview(r.Context(), gcid, limit)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PENDING_REVIEWS_FAILED", err.Error())
		return
	}

	items := make([]pendingReviewItem, 0, len(uploads))
	for _, u := range uploads {
		items = append(items, pendingReviewItem{
			UploadID:   u.UploadID,
			UploadKind: u.UploadKind,
			Status:     string(u.Status),
			GoalID:     u.GoalID,
			CreatedAt:  u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Review:     u.Review,
		})
	}
	extWriteJSON(w, http.StatusOK, pendingReviewsResponse{Items: items})
}
