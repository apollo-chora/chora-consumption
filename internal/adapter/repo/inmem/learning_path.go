// Package inmem (EXT scope) — in-memory repos for the EXT-scope
// LearningPath + AtomAttempt aggregates. See package doc in
// learning_path_test.go for context.
//
// Soft-deleted entities are retained but excluded from default reads
// (per ddd-enforcement #6). Production (M12+) replaces with PostgreSQL
// via pgx + RLS (database-postgresql + multi-tenant-rls skills).
package inmem

import (
	"context"
	"errors"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ErrNotFound is returned when an entity is not present (or soft-deleted).
var ErrNotFound = errors.New("inmem: not found")

// LearningPathRepo is an in-memory store for LearningPath.
//
// R3: methods take a context for parity with the pg-backed adapter (which
// uses it for the RLS tenant session); the in-memory store ignores ctx. Both
// satisfy learning_path.Repo so the composition root can swap durability in.
type LearningPathRepo struct {
	mu    sync.RWMutex
	store map[string]*learning_path.LearningPath
	// byCourseGCID indexes course-bound paths for the
	// `enrollment.created.v1` subscriber's dedupe lookup. Key:
	// tenant|course|gcid → path_id.
	byCourseGCID map[string]string
}

// NewLearningPathRepo constructs an empty repo.
func NewLearningPathRepo() *LearningPathRepo {
	return &LearningPathRepo{
		store:        make(map[string]*learning_path.LearningPath),
		byCourseGCID: make(map[string]string),
	}
}

// Compile-time check: the in-memory adapter satisfies the domain port.
var _ learning_path.Repo = (*LearningPathRepo)(nil)

func courseGCIDKey(tenantID, courseID, gcid string) string {
	return tenantID + "|" + courseID + "|" + gcid
}

// Save inserts or updates a LearningPath by PathID.
func (r *LearningPathRepo) Save(_ context.Context, p *learning_path.LearningPath) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[p.PathID] = p
	if p.CourseID != "" {
		r.byCourseGCID[courseGCIDKey(p.TenantID, p.CourseID, p.OwnerGCID)] = p.PathID
	}
	return nil
}

// Get returns the path or ErrNotFound (soft-deleted excluded).
func (r *LearningPathRepo) Get(_ context.Context, id string) (*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.store[id]
	if !ok || p.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// GetByCourseAndGCID returns the LearningPath bootstrapped for one
// (tenant, course, learner) trio, or ErrNotFound. Used by the
// `enrollment.created.v1` subscriber for idempotent bootstrap and the
// `atom_session.completed.v1` subscriber to find a path to advance.
func (r *LearningPathRepo) GetByCourseAndGCID(_ context.Context, tenantID, courseID, gcid string) (*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byCourseGCID[courseGCIDKey(tenantID, courseID, gcid)]
	if !ok {
		return nil, ErrNotFound
	}
	p, ok := r.store[id]
	if !ok || p.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// ListByCourse returns all non-deleted course-bound LearningPaths for a
// (tenant, course) pair, across learners. Used by the AtomCreatedSubscriber
// to retroactively append a newly-authored atom to every enrolled learner's
// path (R2). Order is non-deterministic.
func (r *LearningPathRepo) ListByCourse(_ context.Context, tenantID, courseID string) ([]*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*learning_path.LearningPath, 0)
	for _, p := range r.store {
		if p.DeletedAt != nil {
			continue
		}
		if p.TenantID != tenantID || p.CourseID != courseID {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// ListByLearner returns all non-deleted LearningPaths owned by one learner
// (across courses). limit <= 0 = unbounded.
func (r *LearningPathRepo) ListByLearner(_ context.Context, tenantID, gcid string, limit int) ([]*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*learning_path.LearningPath, 0)
	for _, p := range r.store {
		if p.DeletedAt != nil {
			continue
		}
		if p.TenantID != tenantID || p.OwnerGCID != gcid {
			continue
		}
		out = append(out, p)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetBySourceCollection returns the study-list path derived from one
// (tenant, owner, collection) trio, or ErrNotFound (ADR-233 D2/D4 — the
// get-or-create probe for the additive re-sync).
func (r *LearningPathRepo) GetBySourceCollection(_ context.Context, tenantID, ownerGCID, collectionID string) (*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.store {
		if p.DeletedAt != nil {
			continue
		}
		if p.SourceType != learning_path.SourceTypeCollection {
			continue
		}
		if p.TenantID == tenantID && p.OwnerGCID == ownerGCID && p.SourceID == collectionID {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

// GetByStudyListEventID returns the path anchored to one conversion event, or
// ErrNotFound (ADR-233 — the durable delivery-dedupe anchor).
//
// Soft-deleted paths are INTENTIONALLY still matched: the anchor survives a
// delete, so a redelivered push is absorbed as a duplicate rather than
// resurrecting the study list as a fresh row. Mirrors the pg adapter, whose
// lookup SQL likewise omits the deleted_at filter.
func (r *LearningPathRepo) GetByStudyListEventID(_ context.Context, studyListEventID string) (*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if studyListEventID == "" {
		return nil, ErrNotFound
	}
	for _, p := range r.store {
		if p.StudyListEventID == studyListEventID {
			return p, nil
		}
	}
	return nil, ErrNotFound
}

// ListByLearnerWithAtom returns all non-deleted LearningPaths owned by one
// learner that contain the supplied atom_id. Used by the
// `atom_session.completed.v1` subscriber to advance every relevant path
// (an atom may live in multiple paths).
func (r *LearningPathRepo) ListByLearnerWithAtom(_ context.Context, tenantID, gcid, atomID string) ([]*learning_path.LearningPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*learning_path.LearningPath, 0)
	for _, p := range r.store {
		if p.DeletedAt != nil {
			continue
		}
		if p.TenantID != tenantID || p.OwnerGCID != gcid {
			continue
		}
		for _, a := range p.AtomIDs {
			if a == atomID {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

// EnrollmentRepo is an in-memory store for Enrollment.
type EnrollmentRepo struct {
	mu       sync.RWMutex
	byID     map[string]*learning_path.Enrollment
	byPathFK map[string]string // key: pathID|gcid → enrollmentID
}

// NewEnrollmentRepo constructs an empty repo.
func NewEnrollmentRepo() *EnrollmentRepo {
	return &EnrollmentRepo{
		byID:     make(map[string]*learning_path.Enrollment),
		byPathFK: make(map[string]string),
	}
}

// Save inserts or updates the enrollment.
func (r *EnrollmentRepo) Save(e *learning_path.Enrollment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[e.EnrollmentID] = e
	r.byPathFK[enrollKey(e.PathID, e.LearnerGCID)] = e.EnrollmentID
}

// Get returns the enrollment by ID, excluding soft-deleted.
func (r *EnrollmentRepo) Get(id string) (*learning_path.Enrollment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[id]
	if !ok || e.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return e, nil
}

// GetByPathAndGCID returns the matched enrollment for (pathID, gcid).
// Returns ErrNotFound when no enrollment exists.
func (r *EnrollmentRepo) GetByPathAndGCID(pathID, learnerGCID string) (*learning_path.Enrollment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byPathFK[enrollKey(pathID, learnerGCID)]
	if !ok {
		return nil, ErrNotFound
	}
	e, ok := r.byID[id]
	if !ok || e.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return e, nil
}

func enrollKey(pathID, gcid string) string {
	return pathID + "|" + gcid
}
