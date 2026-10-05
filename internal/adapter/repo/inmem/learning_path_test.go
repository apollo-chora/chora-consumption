// Package inmem - extension-scope LearningPath + AtomAttempt in-memory
// repos. Distinct from the older `internal/adapter/inmem` skeleton repos
// that back the Phyllis-MVP Companion handlers.
//
// This package backs the EXT scope endpoints:
//
//	POST /learning-paths
//	POST /learning-paths/{id}/enrollments
//	GET  /learning-paths/{id}/progress
//	POST /sessions
//	PATCH /sessions/{id}/answer
//	POST /sessions/{id}:abandon
//	GET  /sessions/{id}
//
// Production (M12+): replaced with PostgreSQL via pgx + RLS.
package inmem

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	tenantID = "01970000-0000-7000-8000-000000000001"
	gcid     = "01970000-0000-7000-9000-000000000001"
	atom1    = "01970000-0000-7000-a000-000000000001"
	atom2    = "01970000-0000-7000-a000-000000000002"
)

// TestLearningPathRepo_SaveAndGet round-trips a path through the repo.
func TestLearningPathRepo_SaveAndGet(t *testing.T) {
	r := NewLearningPathRepo()
	p, _ := learning_path.New(tenantID, gcid, "Algebra", []string{atom1, atom2})
	r.Save(context.Background(), p)

	got, err := r.Get(context.Background(), p.PathID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PathID != p.PathID {
		t.Errorf("PathID = %q, want %q", got.PathID, p.PathID)
	}
}

// TestLearningPathRepo_Get_NotFound returns ErrNotFound.
func TestLearningPathRepo_Get_NotFound(t *testing.T) {
	r := NewLearningPathRepo()
	if _, err := r.Get(context.Background(), "nonexistent"); err == nil {
		t.Error("expected error for missing path")
	}
}

// TestLearningPathRepo_SoftDeletedExcluded — soft-deleted paths are
// excluded from default Get per ddd-enforcement #6.
func TestLearningPathRepo_SoftDeletedExcluded(t *testing.T) {
	r := NewLearningPathRepo()
	p, _ := learning_path.New(tenantID, gcid, "X", []string{atom1})
	r.Save(context.Background(), p)
	p.SoftDelete()
	r.Save(context.Background(), p)
	if _, err := r.Get(context.Background(), p.PathID); err == nil {
		t.Error("expected ErrNotFound for soft-deleted path")
	}
}

// TestEnrollmentRepo_SaveAndGet round-trips an enrollment.
func TestEnrollmentRepo_SaveAndGet(t *testing.T) {
	r := NewEnrollmentRepo()
	e, _ := learning_path.Enroll(tenantID, "path-1", gcid)
	r.Save(e)

	got, err := r.Get(e.EnrollmentID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LearnerGCID != gcid {
		t.Errorf("LearnerGCID = %q", got.LearnerGCID)
	}
}

// TestEnrollmentRepo_GetByPathAndGCID returns matched enrollment.
func TestEnrollmentRepo_GetByPathAndGCID(t *testing.T) {
	r := NewEnrollmentRepo()
	e, _ := learning_path.Enroll(tenantID, "path-1", gcid)
	r.Save(e)

	got, err := r.GetByPathAndGCID("path-1", gcid)
	if err != nil {
		t.Fatalf("GetByPathAndGCID: %v", err)
	}
	if got.EnrollmentID != e.EnrollmentID {
		t.Errorf("EnrollmentID mismatch")
	}
}

// TestEnrollmentRepo_GetByPathAndGCID_NotFound.
func TestEnrollmentRepo_GetByPathAndGCID_NotFound(t *testing.T) {
	r := NewEnrollmentRepo()
	if _, err := r.GetByPathAndGCID("missing", gcid); err == nil {
		t.Error("expected ErrNotFound")
	}
}
