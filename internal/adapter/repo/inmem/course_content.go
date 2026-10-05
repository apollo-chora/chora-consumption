// course_content.go — in-memory store for the course-content read projection
// (hydrated from chora.delivery.course.content_composed.v1, CHO-1612).
//
// Replace-by-course semantics: each content_composed event is a full snapshot,
// so ReplaceByCourse swaps the whole ordered set for a course atomically.
// Production swaps in the pg adapter; the subscriber + read layers are unchanged.
package inmem

import (
	"context"
	"sort"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

// CourseContentRepo is an in-memory projection store keyed by (tenant, course).
type CourseContentRepo struct {
	mu sync.RWMutex
	by map[string][]*course_content.Item
}

// NewCourseContentRepo returns an empty projection store.
func NewCourseContentRepo() *CourseContentRepo {
	return &CourseContentRepo{by: make(map[string][]*course_content.Item)}
}

func ccKey(tenantID, courseID string) string { return tenantID + "/" + courseID }

// ReplaceByCourse swaps the full ordered item set for a course.
func (r *CourseContentRepo) ReplaceByCourse(_ context.Context, tenantID, courseID string, items []*course_content.Item) error {
	cp := make([]*course_content.Item, len(items))
	copy(cp, items)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Position < cp[j].Position })
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(cp) == 0 {
		delete(r.by, ccKey(tenantID, courseID))
		return nil
	}
	r.by[ccKey(tenantID, courseID)] = cp
	return nil
}

// ListByCourse returns the ordered items for a course (nil if none).
func (r *CourseContentRepo) ListByCourse(_ context.Context, tenantID, courseID string) ([]*course_content.Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := r.by[ccKey(tenantID, courseID)]
	out := make([]*course_content.Item, len(items))
	copy(out, items)
	return out, nil
}
