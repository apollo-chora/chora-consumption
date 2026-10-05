// course_metadata_subscriber.go — projects the delivery domain's course
// metadata (chora.delivery.course.created.v1 + chora.delivery.course.updated.v1)
// into the local course_directory read-model (CHO-2059 follow-up).
//
// Both topics carry the same {course_id, title} shape and feed ONE Handle: the
// projection is a course_id → title map, and created vs updated is immaterial to
// it (an update is just a newer title for the same course). Idempotent +
// out-of-order safe by construction:
//   - duplicate event_ids are deduped by the idempotency tracker;
//   - the course_directory Upsert is last-writer-wins on updated_at (the pg
//     adapter's `WHERE updated_at < EXCLUDED.updated_at`), so a stale/redelivered
//     created cannot clobber a newer updated, regardless of arrival order.
//
// The table is tenant-agnostic (global course_id key, no RLS), so — unlike the
// LearnerProfile/course-content subscribers — this handler does NOT stamp a
// tenant onto the repo ctx; there is no tenant to scope by.
package subscribers

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
)

// CourseMetadataPayload mirrors the shared fields of
// chora.delivery.course.created.v1 + chora.delivery.course.updated.v1 the
// projection needs.
type CourseMetadataPayload struct {
	CourseID string
	Title    string
}

// CourseMetadataSubscriber upserts the course_directory projection.
type CourseMetadataSubscriber struct {
	dir     course_directory.CourseDirectoryPort
	tracker *idempotencyTracker
}

// NewCourseMetadataSubscriber constructs the subscriber.
func NewCourseMetadataSubscriber(dir course_directory.CourseDirectoryPort) *CourseMetadataSubscriber {
	return &CourseMetadataSubscriber{dir: dir, tracker: newIdempotencyTracker()}
}

// Handle upserts the course's title into the directory last-writer-wins. Same
// handler for both created + updated (see file doc).
//
// Process-then-mark (CHO-2130): seen() peek at entry, mark() only after the
// upsert landed — claim-first markSeen swallowed the redelivery after a
// transient Upsert failure. Errors return UNMARKED (NACK); the LWW upsert
// absorbs a duplicate.
func (s *CourseMetadataSubscriber) Handle(env events.Envelope, p CourseMetadataPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if s.tracker.seen(env.EventID) {
		return nil // duplicate — already projected
	}
	// Tenant-agnostic global table (no RLS) — a bare context is correct; there
	// is no tenant to stamp onto the repo ctx (unlike the LearnerProfile /
	// course-content subscribers, which write RLS-scoped tables).
	if err := s.dir.Upsert(context.Background(), course_directory.CourseDirectoryEntry{
		CourseID:  p.CourseID,
		Title:     p.Title,
		UpdatedAt: courseMetaUpdatedAt(env),
	}); err != nil {
		return fmt.Errorf("course_metadata subscriber: upsert course %q: %w", p.CourseID, err)
	}
	s.tracker.mark(env.EventID)
	return nil
}

// courseMetaUpdatedAt is the last-writer-wins clock for the upsert: the event's
// occurred_at, falling back to published_at only if occurred_at is zero (a valid
// envelope always carries occurred_at — this is defensive).
func courseMetaUpdatedAt(env events.Envelope) time.Time {
	if env.OccurredAt.IsZero() {
		return env.PublishedAt
	}
	return env.OccurredAt
}
