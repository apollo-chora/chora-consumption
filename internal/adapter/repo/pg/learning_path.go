// learning_path.go — Postgres adapter for the LearningPath aggregate.
// Owns the chora_consumption.learning_paths table.
//
// Schema (migrations/0001_initial.sql + 0043_learning_path_course_binding):
//
//	learning_paths (path_id PK, tenant_id, owner_gcid, title, description,
//	                atom_ids UUID[], completed BOOL, completed_at,
//	                course_id, enrollment_id, current_index,
//	                created_at, updated_at, deleted_at)
//
// R3: this adapter replaces the in-memory LearningPathRepo in production so a
// bootstrapped path survives pod restart and is consistent across replicas.
// `completed`/`completed_at` carry binary completion; `current_index` carries
// the Straight-Up cursor (0..len(atom_ids)); course_id/enrollment_id bind the
// path to a chora-delivery enrolment (NULL for legacy non-course paths).
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ErrInvalidLearningPath is the sentinel for nil-input writes.
var ErrInvalidLearningPath = errors.New("pg: learning_path is nil")

// LearningPathRepo is the Postgres-backed LearningPath repo.
type LearningPathRepo struct {
	tx TxRunner
}

// NewLearningPathRepo constructs the repo.
func NewLearningPathRepo(tx TxRunner) *LearningPathRepo {
	return &LearningPathRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ learning_path.Repo = (*LearningPathRepo)(nil)

// nullableUUID maps an empty string to a SQL NULL (UUID columns reject "").
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Save upserts a LearningPath by path_id.
func (r *LearningPathRepo) Save(ctx context.Context, p *learning_path.LearningPath) error {
	if p == nil {
		return ErrInvalidLearningPath
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var completedAt any
		if p.CompletedAt != nil {
			completedAt = *p.CompletedAt
		}
		// ADR-233: source_type / traversal_mode are NOT NULL with CHECKs in the
		// DB (migration 0093). A zero-value struct (e.g. one hand-built in a
		// legacy code path that predates the provenance axis) would violate the
		// CHECK with an empty string, so normalise to the schema defaults here —
		// same values the column DEFAULTs carry.
		sourceType := p.SourceType
		if sourceType == "" {
			sourceType = learning_path.SourceTypeAdHoc
		}
		traversalMode := p.TraversalMode
		if traversalMode == "" {
			traversalMode = learning_path.TraversalModeLinear
		}
		_, err := q.Exec(ctx, upsertLearningPathSQL,
			p.PathID, p.TenantID, p.OwnerGCID, p.Title, "",
			p.AtomIDs, p.CompletedAt != nil, completedAt,
			nullableUUID(p.CourseID), nullableUUID(p.EnrollmentID), p.CurrentIndex,
			sourceType, nullableUUID(p.SourceID), traversalMode,
			nullableUUID(p.StudyListEventID),
		)
		return err
	})
}

// scanPath scans the canonical column projection (shared by Get / list / lookup).
func scanPath(row interface{ Scan(...any) error }) (*learning_path.LearningPath, error) {
	var (
		p                learning_path.LearningPath
		completedAt      *time.Time
		completed        bool
		courseID         *string
		enrollmentID     *string
		sourceID         *string
		studyListEventID *string
	)
	if err := row.Scan(
		&p.PathID, &p.TenantID, &p.OwnerGCID, &p.Title,
		&p.AtomIDs, &completed, &completedAt,
		&courseID, &enrollmentID, &p.CurrentIndex,
		&p.SourceType, &sourceID, &p.TraversalMode, &studyListEventID,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.CompletedAt = completedAt
	if courseID != nil {
		p.CourseID = *courseID
	}
	if enrollmentID != nil {
		p.EnrollmentID = *enrollmentID
	}
	if sourceID != nil {
		p.SourceID = *sourceID
	}
	if studyListEventID != nil {
		p.StudyListEventID = *studyListEventID
	}
	return &p, nil
}

// GetBySourceCollection returns the study-list path derived from one
// (tenant, owner, collection) trio, or ErrNoRows. Backed by the partial unique
// index idx_learning_paths_source_collection_owner (migration 0093). This is the
// subscriber's get-or-create probe for the additive re-sync (ADR-233 D4).
func (r *LearningPathRepo) GetBySourceCollection(ctx context.Context, tenantID, ownerGCID, collectionID string) (*learning_path.LearningPath, error) {
	var out *learning_path.LearningPath
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getLearningPathBySourceCollectionSQL, tenantID, ownerGCID, collectionID)
		if row == nil {
			return ErrNoRows
		}
		p, err := scanPath(row)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// GetByStudyListEventID returns the path anchored to one conversion event, or
// ErrNoRows. The DURABLE dedupe anchor for the converted_to_study_list
// subscriber — a redelivered push carries the same study_list_event_id.
//
// NOT filtered on deleted_at: a soft-deleted path still holds its anchor, so a
// redelivery after the learner deleted the study list is correctly seen as a
// duplicate rather than silently RESURRECTING the list as a fresh row.
func (r *LearningPathRepo) GetByStudyListEventID(ctx context.Context, studyListEventID string) (*learning_path.LearningPath, error) {
	var out *learning_path.LearningPath
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getLearningPathByStudyListEventSQL, studyListEventID)
		if row == nil {
			return ErrNoRows
		}
		p, err := scanPath(row)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// Get returns the path by id, or nil + ErrNoRows on not-found.
func (r *LearningPathRepo) Get(ctx context.Context, pathID string) (*learning_path.LearningPath, error) {
	var out *learning_path.LearningPath
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadLearningPathSQL, pathID)
		if row == nil {
			return ErrNoRows
		}
		p, err := scanPath(row)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// GetByCourseAndGCID returns the single course-bound path for one
// (tenant, course, learner) trio, or ErrNoRows. Backed by the partial unique
// index idx_learning_paths_course_owner.
func (r *LearningPathRepo) GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*learning_path.LearningPath, error) {
	var out *learning_path.LearningPath
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getLearningPathByCourseSQL, tenantID, courseID, gcid)
		if row == nil {
			return ErrNoRows
		}
		p, err := scanPath(row)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// ListByLearner returns all non-deleted paths for one (tenant, gcid).
// limit <= 0 = unbounded.
func (r *LearningPathRepo) ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*learning_path.LearningPath, error) {
	return r.queryList(ctx, listLearningPathsByLearnerSQL, tenantID, gcid, sqlLimit(limit))
}

// ListByLearnerWithAtom returns the learner's non-deleted paths that contain
// atomID (an atom may live in multiple paths).
func (r *LearningPathRepo) ListByLearnerWithAtom(ctx context.Context, tenantID, gcid, atomID string) ([]*learning_path.LearningPath, error) {
	return r.queryList(ctx, listLearningPathsByLearnerWithAtomSQL, tenantID, gcid, atomID)
}

// ListByCourse returns all non-deleted course-bound paths for a (tenant,
// course) pair across learners (R2 retroactive append).
func (r *LearningPathRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*learning_path.LearningPath, error) {
	return r.queryList(ctx, listLearningPathsByCourseSQL, tenantID, courseID)
}

// queryList runs a multi-row SELECT with the canonical projection.
func (r *LearningPathRepo) queryList(ctx context.Context, sql string, args ...any) ([]*learning_path.LearningPath, error) {
	out := make([]*learning_path.LearningPath, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPath(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

const (
	// ADR-233 (migration 0093): source_type / source_id / traversal_mode /
	// study_list_event_id ride the same upsert.
	//
	// source_type / source_id / traversal_mode are deliberately ABSENT from the
	// DO UPDATE set: a path's PROVENANCE and TRAVERSAL SEMANTICS are fixed at
	// construction. A re-convert re-syncs ATOMS (D4); it never re-parents the
	// path or flips it between linear and spaced.
	//
	// study_list_event_id IS updated: it re-anchors to the conversion event most
	// recently applied, so a redelivery of THAT event is caught by the durable
	// anchor (GetByStudyListEventID) with no TTL. Leaving it pinned to the first
	// conversion would leave every SUBSEQUENT re-convert dedupable only by the
	// in-process/TTL tracker.
	upsertLearningPathSQL = `
		INSERT INTO learning_paths (
			path_id, tenant_id, owner_gcid, title, description,
			atom_ids, completed, completed_at,
			course_id, enrollment_id, current_index,
			source_type, source_id, traversal_mode, study_list_event_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (path_id) DO UPDATE SET
			title               = EXCLUDED.title,
			atom_ids            = EXCLUDED.atom_ids,
			completed           = EXCLUDED.completed,
			completed_at        = EXCLUDED.completed_at,
			course_id           = EXCLUDED.course_id,
			enrollment_id       = EXCLUDED.enrollment_id,
			current_index       = EXCLUDED.current_index,
			study_list_event_id = EXCLUDED.study_list_event_id`

	learningPathCols = `path_id, tenant_id, owner_gcid, title, atom_ids,
		completed, completed_at, course_id, enrollment_id, current_index,
		source_type, source_id, traversal_mode, study_list_event_id,
		created_at, updated_at`

	loadLearningPathSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE path_id = $1 AND deleted_at IS NULL`

	getLearningPathByCourseSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE tenant_id = $1 AND course_id = $2 AND owner_gcid = $3
			AND deleted_at IS NULL
		LIMIT 1`

	listLearningPathsByLearnerSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE tenant_id = $1 AND owner_gcid = $2 AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT $3`

	listLearningPathsByLearnerWithAtomSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE tenant_id = $1 AND owner_gcid = $2 AND $3 = ANY(atom_ids)
			AND deleted_at IS NULL
		ORDER BY created_at ASC`

	listLearningPathsByCourseSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
		ORDER BY created_at ASC`

	// ADR-233 D2/D4 — the get-or-create probe for a collection-derived study
	// list. Backed by idx_learning_paths_source_collection_owner (0093).
	getLearningPathBySourceCollectionSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE tenant_id = $1 AND owner_gcid = $2 AND source_id = $3
			AND source_type = 'collection'
			AND deleted_at IS NULL
		LIMIT 1`

	// ADR-233 — the durable delivery-dedupe anchor. NOT filtered on deleted_at
	// (see GetByStudyListEventID doc: a soft-deleted path must still absorb a
	// redelivery rather than be resurrected as a new row).
	getLearningPathByStudyListEventSQL = `
		SELECT ` + learningPathCols + `
		FROM learning_paths
		WHERE study_list_event_id = $1
		LIMIT 1`
)
