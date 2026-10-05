// course_directory.go — Postgres adapter for chora_consumption.course_directory
// (the course_id → title projection; CHO-2059 follow-up).
//
// SCHEMA: migrations/0072_course_directory.up.sql.
//
// The course_directory table is a TENANT-AGNOSTIC, RLS-FREE global read-model:
// one row per global course_id (opaque UUIDv7, no tenant embedded). Titles
// arrive from chora.delivery.course.{created,updated}.v1 and are only ever
// surfaced by a stitch against the learner's OWN RLS-scoped LearnerProfile facts
// (the learner_profile presenter). Because the table carries no RLS policy,
// these methods deliberately do NOT call rls.ApplySession — SET LOCAL
// chora.tenant_id would be a pointless no-op, and Upsert has no tenant to scope
// by. This is the one consumption pg adapter that omits ApplySession, and the
// reason is the no-RLS global-table design (mirrors chora-delivery's
// user_directory.go one domain over).
//
// Cross-DB queries forbidden — this reads/writes only chora_consumption.
package pg

import (
	"context"
	"errors"
	"strings"

	domain "github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
)

// ErrCourseDirectoryMissingCourseID is returned when the entry has no course_id
// — the directory is keyed on it. Fail loud per feedback_no_stubs_real_wiring.
var ErrCourseDirectoryMissingCourseID = errors.New("pg: course_directory entry requires a non-empty course_id")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertCourseDirectory idempotently upserts one directory row last-writer-
// wins: the DO UPDATE only fires when the stored row is strictly older than the
// incoming updated_at, so a stale/out-of-order redelivery can never clobber a
// newer title and an equal-timestamp replay is a no-op. Args: $1 course_id, $2
// title, $3 updated_at.
const SQLUpsertCourseDirectory = `
INSERT INTO course_directory (course_id, title, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (course_id) DO UPDATE SET
    title      = EXCLUDED.title,
    updated_at = EXCLUDED.updated_at
WHERE course_directory.updated_at < EXCLUDED.updated_at
`

// SQLLookupCourseDirectoryTitles returns (course_id, title) for every supplied
// course_id that has a row. $1 is a text[] of course_ids cast to uuid[] so the
// PK index is used. Rows with an empty title are returned as-is; the caller (the
// presenter) applies the generic-noun fallback.
const SQLLookupCourseDirectoryTitles = `
SELECT course_id, title
FROM course_directory
WHERE course_id = ANY($1::uuid[])
`

// -----------------------------------------------------------------------------
// CourseDirectoryRepo
// -----------------------------------------------------------------------------

// CourseDirectoryRepo is the Postgres-backed course_directory.CourseDirectoryPort
// impl.
type CourseDirectoryRepo struct {
	tx TxRunner
}

// NewCourseDirectoryRepo constructs a CourseDirectoryRepo around a TxRunner.
// Passing nil yields a Repo that returns ErrNotImplemented from every method
// (matches the sibling scaffold-era repos). Production wires this behind the
// same pool gate as the other pg repos.
func NewCourseDirectoryRepo(tx TxRunner) *CourseDirectoryRepo {
	return &CourseDirectoryRepo{tx: tx}
}

// Upsert applies the entry last-writer-wins on updated_at (see SQL). No RLS
// (global tenant-agnostic table) — no rls.ApplySession.
func (r *CourseDirectoryRepo) Upsert(ctx context.Context, entry domain.CourseDirectoryEntry) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(entry.CourseID) == "" {
		return ErrCourseDirectoryMissingCourseID
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		_, err := q.Exec(ctx, SQLUpsertCourseDirectory,
			entry.CourseID, entry.Title, entry.UpdatedAt,
		)
		return err
	})
}

// LookupTitles returns a course_id → title map for the supplied course_ids that
// have a directory row. Empty input short-circuits before any query. No RLS.
func (r *CourseDirectoryRepo) LookupTitles(ctx context.Context, courseIDs []string) (map[string]string, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if len(courseIDs) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(courseIDs))
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		rs, qErr := q.Query(ctx, SQLLookupCourseDirectoryTitles, courseIDs)
		if qErr != nil {
			return qErr
		}
		if rs == nil {
			return nil // stub Querier returns nil — treat as empty (matches course_content.go)
		}
		defer rs.Close()
		for rs.Next() {
			var id, title string
			if scanErr := rs.Scan(&id, &title); scanErr != nil {
				return scanErr
			}
			out[id] = title
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Compile-time assertion: CourseDirectoryRepo must satisfy the domain port.
var _ domain.CourseDirectoryPort = (*CourseDirectoryRepo)(nil)
