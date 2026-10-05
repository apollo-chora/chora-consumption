package course_content

import "context"

// ProjectionRepo persists the course-content read projection. The subscriber
// REPLACES the full item set for a course on every content_composed event
// (idempotent projection — last-writer-wins by composed_at ordering at the bus).
type ProjectionRepo interface {
	// ReplaceByCourse atomically replaces all items for (tenant, course) with
	// the supplied ordered set. An empty slice clears the course's projection.
	ReplaceByCourse(ctx context.Context, tenantID, courseID string, items []*Item) error
	// ListByCourse returns the ordered (by position) items for a course.
	ListByCourse(ctx context.Context, tenantID, courseID string) ([]*Item, error)
}
