// learning_path_cover_test.go — coverage for the course-bound query
// methods on LearningPathRepo (GetByCourseAndGCID / ListByCourse /
// ListByLearner / ListByLearnerWithAtom) and the soft-delete /
// not-found exclusion branches. These characterize the actual filtering
// behavior so a regression in the index keying or the soft-delete /
// tenant / owner predicates is caught.
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	lpCourse1 = "01970000-0000-7000-b000-000000000001"
	lpCourse2 = "01970000-0000-7000-b000-000000000002"
	lpGCID2   = "01970000-0000-7000-9000-000000000002"
	lpTenant2 = "01970000-0000-7000-8000-000000000002"
	lpEnroll1 = "01970000-0000-7000-c000-000000000001"
)

// mkCoursePath bootstraps a course-bound LearningPath (populates CourseID
// so it lands in the byCourseGCID index) for the given trio + atoms.
func mkCoursePath(t *testing.T, tenant, gc, course string, atoms []string) *learning_path.LearningPath {
	t.Helper()
	p, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:     tenant,
		LearnerGCID:  gc,
		CourseID:     course,
		EnrollmentID: lpEnroll1,
		AtomIDs:      atoms,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return p
}

// TestLearningPathRepo_GetByCourseAndGCID covers the happy path, the
// index-miss path, and the soft-deleted exclusion path.
func TestLearningPathRepo_GetByCourseAndGCID(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	p := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1, atom2})
	if err := r.Save(ctx, p); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := r.GetByCourseAndGCID(ctx, tenantID, lpCourse1, gcid)
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v", err)
	}
	if got.PathID != p.PathID {
		t.Errorf("PathID = %q, want %q", got.PathID, p.PathID)
	}

	// Index miss: wrong course → ErrNotFound.
	if _, err := r.GetByCourseAndGCID(ctx, tenantID, lpCourse2, gcid); err != ErrNotFound {
		t.Errorf("wrong course err = %v, want ErrNotFound", err)
	}

	// Soft-deleted: index entry still present, but row is excluded.
	p.SoftDelete()
	if err := r.Save(ctx, p); err != nil {
		t.Fatalf("save soft-deleted: %v", err)
	}
	if _, err := r.GetByCourseAndGCID(ctx, tenantID, lpCourse1, gcid); err != ErrNotFound {
		t.Errorf("soft-deleted err = %v, want ErrNotFound", err)
	}
}

// TestLearningPathRepo_Save_NoCourseIDNotIndexed verifies that a path with
// an empty CourseID (the New() default) is NOT added to byCourseGCID, so a
// course lookup misses even though Get(pathID) succeeds.
func TestLearningPathRepo_Save_NoCourseIDNotIndexed(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	p, _ := learning_path.New(tenantID, gcid, "Self-made", []string{atom1})
	if p.CourseID != "" {
		t.Fatalf("precondition: New() set CourseID = %q", p.CourseID)
	}
	if err := r.Save(ctx, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := r.Get(ctx, p.PathID); err != nil {
		t.Fatalf("Get by id: %v", err)
	}
	// No CourseID was set → no course index entry was written.
	if _, err := r.GetByCourseAndGCID(ctx, tenantID, "", gcid); err != ErrNotFound {
		t.Errorf("course lookup err = %v, want ErrNotFound", err)
	}
}

// TestLearningPathRepo_ListByCourse filters by (tenant, course), excludes
// soft-deleted, and excludes paths from other tenants/courses.
func TestLearningPathRepo_ListByCourse(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	// Two learners on course1, tenant A.
	p1 := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1})
	p2 := mkCoursePath(t, tenantID, lpGCID2, lpCourse1, []string{atom1})
	// Different course, same tenant → excluded.
	pOtherCourse := mkCoursePath(t, tenantID, gcid, lpCourse2, []string{atom2})
	// Different tenant, same course → excluded.
	pOtherTenant := mkCoursePath(t, lpTenant2, gcid, lpCourse1, []string{atom1})
	// Soft-deleted on course1 → excluded.
	pDeleted := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom2})
	pDeleted.SoftDelete()
	for _, p := range []*learning_path.LearningPath{p1, p2, pOtherCourse, pOtherTenant, pDeleted} {
		if err := r.Save(ctx, p); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	got, err := r.ListByCourse(ctx, tenantID, lpCourse1)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (got=%v)", len(got), idsOf(got))
	}
	ids := map[string]bool{}
	for _, p := range got {
		ids[p.PathID] = true
	}
	if !ids[p1.PathID] || !ids[p2.PathID] {
		t.Errorf("missing expected paths; got %v", idsOf(got))
	}
}

// TestLearningPathRepo_ListByLearner filters by (tenant, owner), excludes
// soft-deleted + foreign tenants, and honours the limit.
func TestLearningPathRepo_ListByLearner(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	a := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1})
	b := mkCoursePath(t, tenantID, gcid, lpCourse2, []string{atom2})
	otherOwner := mkCoursePath(t, tenantID, lpGCID2, lpCourse1, []string{atom1})
	otherTenant := mkCoursePath(t, lpTenant2, gcid, lpCourse1, []string{atom1})
	deleted := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom2})
	deleted.SoftDelete()
	for _, p := range []*learning_path.LearningPath{a, b, otherOwner, otherTenant, deleted} {
		if err := r.Save(ctx, p); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// Unbounded (limit <= 0).
	got, err := r.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("unbounded len = %d, want 2 (got=%v)", len(got), idsOf(got))
	}

	// Bounded limit stops early.
	got2, err := r.ListByLearner(ctx, tenantID, gcid, 1)
	if err != nil {
		t.Fatalf("ListByLearner limited: %v", err)
	}
	if len(got2) != 1 {
		t.Errorf("limited len = %d, want 1", len(got2))
	}
}

// TestLearningPathRepo_ListByLearnerWithAtom returns only the learner's
// non-deleted paths that contain the atom; exercises the inner atom-match
// loop including the no-match path.
func TestLearningPathRepo_ListByLearnerWithAtom(t *testing.T) {
	ctx := context.Background()
	r := NewLearningPathRepo()
	withAtom1 := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1, atom2})
	withoutAtom1 := mkCoursePath(t, tenantID, gcid, lpCourse2, []string{atom2})
	otherOwnerWithAtom1 := mkCoursePath(t, tenantID, lpGCID2, lpCourse1, []string{atom1})
	deletedWithAtom1 := mkCoursePath(t, tenantID, gcid, lpCourse1, []string{atom1})
	deletedWithAtom1.SoftDelete()
	for _, p := range []*learning_path.LearningPath{withAtom1, withoutAtom1, otherOwnerWithAtom1, deletedWithAtom1} {
		if err := r.Save(ctx, p); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	got, err := r.ListByLearnerWithAtom(ctx, tenantID, gcid, atom1)
	if err != nil {
		t.Fatalf("ListByLearnerWithAtom: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (got=%v)", len(got), idsOf(got))
	}
	if got[0].PathID != withAtom1.PathID {
		t.Errorf("got %q, want %q", got[0].PathID, withAtom1.PathID)
	}

	// An atom that is in NO path returns an empty (non-nil) slice.
	none, err := r.ListByLearnerWithAtom(ctx, tenantID, gcid, "no-such-atom")
	if err != nil {
		t.Fatalf("ListByLearnerWithAtom(no-such): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("no-match = %v, want empty non-nil slice", none)
	}
}

// TestEnrollmentRepo_Get_SoftDeletedExcluded covers the soft-deleted
// branch of EnrollmentRepo.Get and GetByPathAndGCID (DeletedAt != nil).
func TestEnrollmentRepo_Get_SoftDeletedExcluded(t *testing.T) {
	r := NewEnrollmentRepo()
	e, _ := learning_path.Enroll(tenantID, "path-1", gcid)
	r.Save(e)
	now := time.Now().UTC()
	e.DeletedAt = &now
	r.Save(e)

	if _, err := r.Get(e.EnrollmentID); err != ErrNotFound {
		t.Errorf("Get soft-deleted err = %v, want ErrNotFound", err)
	}
	if _, err := r.GetByPathAndGCID("path-1", gcid); err != ErrNotFound {
		t.Errorf("GetByPathAndGCID soft-deleted err = %v, want ErrNotFound", err)
	}
}

func idsOf(ps []*learning_path.LearningPath) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.PathID)
	}
	return out
}
