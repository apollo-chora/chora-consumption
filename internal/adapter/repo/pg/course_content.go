// course_content.go — Postgres adapter for the course-content read projection
// (hydrated from chora.delivery.course.content_composed.v1, CHO-1612).
//
// Durability (CHO-1612 D2): the in-memory CourseContentRepo loses the
// projection on pod restart and splits it across replicas, causing transient
// empty reads during deploy churn. This pg adapter persists it so the
// open-course learn page renders consistently regardless of which replica
// serves the read.
//
// Replace-by-course semantics: each content_composed event is a FULL ordered
// snapshot, so ReplaceByCourse swaps the whole set for a course inside one
// transaction. Per ddd-enforcement (soft delete only): it TOMBSTONES the
// course's current live rows (UPDATE ... SET deleted_at = NOW()) then
// upsert-REVIVES the new set (INSERT ... ON CONFLICT DO UPDATE SET
// deleted_at = NULL). Items present in the new snapshot end live; items no
// longer present stay tombstoned. Reads filter deleted_at IS NULL.
//
// Mirrors atom_index.go: RunInTx + rls.ApplySession before every read/write so
// the tenant_isolation RLS policy on course_content is enforced.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

// CourseContentRepo is the Postgres-backed projection repo.
type CourseContentRepo struct {
	tx TxRunner
}

// NewCourseContentRepo constructs a course_content repo.
func NewCourseContentRepo(tx TxRunner) *CourseContentRepo {
	return &CourseContentRepo{tx: tx}
}

// Compile-time check: pg adapter satisfies the same port the inmem adapter
// implements, so cmd/server can swap it in transparently.
var _ course_content.ProjectionRepo = (*CourseContentRepo)(nil)

// ReplaceByCourse atomically swaps the full ordered item set for a course:
// tombstone the course's current live rows, then upsert-revive the supplied
// items. An empty slice clears the course (tombstone only — the "content
// removed" snapshot).
func (r *CourseContentRepo) ReplaceByCourse(ctx context.Context, tenantID, courseID string, items []*course_content.Item) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, tombstoneCourseContentSQL, tenantID, courseID); err != nil {
			return err
		}
		for _, it := range items {
			if it == nil {
				continue
			}
			if _, err := q.Exec(ctx, upsertCourseContentSQL,
				it.ItemID, it.TenantID, it.CourseID, string(it.Kind), it.Ref, it.Title, it.Position,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListByCourse returns the ordered live items for a course (nil if none).
//
// SQL: SELECT ... FROM course_content WHERE tenant_id = $1 AND course_id = $2
//
//	AND deleted_at IS NULL ORDER BY position ASC;
func (r *CourseContentRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*course_content.Item, error) {
	out := make([]*course_content.Item, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listCourseContentByCourseSQL, tenantID, courseID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier returns nil — treat as empty
		}
		defer rows.Close()
		for rows.Next() {
			var (
				it   course_content.Item
				kind string
			)
			if err := rows.Scan(
				&it.ItemID, &it.TenantID, &it.CourseID, &kind, &it.Ref, &it.Title, &it.Position,
			); err != nil {
				return err
			}
			it.Kind = course_content.Kind(kind)
			out = append(out, &it)
		}
		return rows.Err()
	})
	return out, err
}

const (
	// tombstoneCourseContentSQL soft-deletes the course's current live rows.
	tombstoneCourseContentSQL = `
		UPDATE course_content SET deleted_at = NOW()
		WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL`

	// upsertCourseContentSQL revives/updates an item (deleted_at = NULL) so the
	// new snapshot's items end live regardless of a prior tombstone.
	upsertCourseContentSQL = `
		INSERT INTO course_content (
			item_id, tenant_id, course_id, kind, ref, title, position
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (item_id) DO UPDATE SET
			course_id  = EXCLUDED.course_id,
			kind       = EXCLUDED.kind,
			ref        = EXCLUDED.ref,
			title      = EXCLUDED.title,
			position   = EXCLUDED.position,
			deleted_at = NULL`

	listCourseContentByCourseSQL = `
		SELECT item_id, tenant_id, course_id, kind, ref, title, position
		FROM course_content
		WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
		ORDER BY position ASC`
)
