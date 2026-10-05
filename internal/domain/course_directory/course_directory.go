// Package course_directory holds the chora-consumption course-directory
// projection — a tenant-agnostic course_id → title read-model kept fresh from
// the delivery domain's chora.delivery.course.{created,updated}.v1 events. It
// exists so a learner-facing Companion can name a real course ("Algebra I")
// instead of the type-generic noun ("a course") when reflecting the learner's
// verified LearnerProfile facts (the CHO-2059 follow-up).
//
// Why a local read-model (not a JOIN to chora_delivery): cross-DB queries are
// FORBIDDEN across the 13-DB topology (.claude/rules/ddd-enforcement.md HARD
// RULE #1). chora-consumption owns no courses, so it keeps its own copy of the
// titles it needs to render, updated over Pub/Sub rather than read cross-domain.
// It mirrors chora-delivery's user_directory (the Q3 GCID → display-name
// projection) exactly, one domain over.
//
// Tenant-agnostic by design: the directory is keyed on the GLOBAL course_id
// (opaque UUIDv7, no tenant context embedded) — a course title is not
// tenant-scoped. One row per course_id. Titles are only ever surfaced by a
// stitch against the learner's OWN RLS-scoped LearnerProfile facts
// (learner_profile presenter), so a caller resolves a title only for a
// course_id already visible in their own tenant's facts. Isolation holds at the
// JOIN boundary; the table itself carries no tenant column and no RLS policy.
//
// Hexagonal: pure domain, stdlib-only, NO infra imports. CourseDirectoryPort is
// the port; pg.CourseDirectoryRepo (prod, chora_consumption.course_directory) +
// InMemCourseDirectory (dev/tests) are the adapters.
package course_directory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrCourseIDRequired is returned by Upsert when the entry carries no course_id
// — the directory is keyed on it, so an empty course_id is a fail-loud
// programming error per feedback_no_stubs_real_wiring.
var ErrCourseIDRequired = errors.New("course_directory: entry requires a non-empty course_id")

// CourseDirectoryEntry is one projected row: a global course_id → title,
// stamped with the source updated_at that drives last-writer-wins
// reconciliation across out-of-order Pub/Sub redeliveries.
type CourseDirectoryEntry struct {
	CourseID  string
	Title     string
	UpdatedAt time.Time
}

// CourseDirectoryPort is the persistence port for the course-directory
// projection.
type CourseDirectoryPort interface {
	// Upsert applies the entry last-writer-wins on UpdatedAt: an entry whose
	// UpdatedAt is NOT strictly newer than the stored row is ignored (so a
	// stale redelivery cannot clobber a newer title, and an equal-timestamp
	// replay is a no-op). Fails loud on an empty course_id.
	Upsert(ctx context.Context, entry CourseDirectoryEntry) error
	// LookupTitles returns a course_id → title map for the supplied course_ids
	// that have a directory row. Absent course_ids are simply not in the map.
	// Rows with an empty title ARE returned (as ""); the caller (the presenter
	// stitch) applies the generic-noun fallback for empty/absent titles —
	// keeping the empty→generic policy in one place rather than baked into the
	// lookup. Empty input short-circuits before any query.
	LookupTitles(ctx context.Context, courseIDs []string) (map[string]string, error)
}

// InMemCourseDirectory is the in-memory CourseDirectoryPort for dev + unit
// tests. Safe for concurrent use.
type InMemCourseDirectory struct {
	mu    sync.RWMutex
	byCID map[string]CourseDirectoryEntry
}

// NewInMemCourseDirectory returns an empty in-memory directory.
func NewInMemCourseDirectory() *InMemCourseDirectory {
	return &InMemCourseDirectory{byCID: make(map[string]CourseDirectoryEntry)}
}

// Upsert applies last-writer-wins on UpdatedAt. Mirrors the pg adapter's
// `INSERT ... ON CONFLICT (course_id) DO UPDATE ... WHERE
// course_directory.updated_at < EXCLUDED.updated_at` (strict `<`, so
// equal-timestamp replays no-op).
func (d *InMemCourseDirectory) Upsert(_ context.Context, entry CourseDirectoryEntry) error {
	if strings.TrimSpace(entry.CourseID) == "" {
		return ErrCourseIDRequired
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.byCID[entry.CourseID]; ok && !existing.UpdatedAt.Before(entry.UpdatedAt) {
		// Stored row is same-age-or-newer — ignore (LWW + idempotent replay).
		return nil
	}
	d.byCID[entry.CourseID] = entry
	return nil
}

// LookupTitles returns titles for the found course_ids (see port doc). Empty
// input short-circuits before touching the map.
func (d *InMemCourseDirectory) LookupTitles(_ context.Context, courseIDs []string) (map[string]string, error) {
	if len(courseIDs) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(courseIDs))
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, id := range courseIDs {
		if e, ok := d.byCID[id]; ok {
			out[id] = e.Title
		}
	}
	return out, nil
}

// Compile-time assertion: InMemCourseDirectory must satisfy CourseDirectoryPort.
var _ CourseDirectoryPort = (*InMemCourseDirectory)(nil)
