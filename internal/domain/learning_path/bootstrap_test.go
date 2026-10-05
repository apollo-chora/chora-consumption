// bootstrap_test.go — RED phase tests for course-bound LearningPath
// bootstrap (subscribed from `chora.delivery.enrollment.created.v1`) +
// advance/completion lifecycle.
//
// Per docs/m13/phyllis-mvp-2026-05-08.md §6 + audit-content-fillgaps.md §3.2:
//
//   - On `enrollment.created.v1`: chora-consumption bootstraps a
//     LearningPath for the (course_id, learner_gcid) pair using the
//     ordered atom list scoped to that course (queried from the local
//     atom_index projection, NEVER cross-DB).
//   - On `atom_session.completed.v1` with is_correct=true AND
//     atom_id ∈ path: advance current_atom_index by 1.
//   - When current_atom_index == len(atom_ids): emit
//     `learning_path.completed.v1` (idempotent — completion stamp once).
package learning_path

import (
	"errors"
	"testing"
	"time"
)

const (
	testCourseID = "01970000-0000-7000-b000-000000000001"
)

func TestBootstrapFromEnrollment_RequiresCourseID(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3}
	// Only the (tenant, learner, course) trio is mandatory. An empty atom
	// list is VALID — a PUBLISHED course with no atoms yet (e.g. test-set-
	// only) must still produce a path so the enrolled learner is not shown
	// "not enrolled" (OPEN-1 fix). Atoms fill in later via AppendAtom on
	// chora.creation.atom.created.v1.
	cases := []BootstrapParams{
		{TenantID: "", LearnerGCID: testGCID, CourseID: testCourseID, AtomIDs: atoms},
		{TenantID: testTenant, LearnerGCID: "", CourseID: testCourseID, AtomIDs: atoms},
		{TenantID: testTenant, LearnerGCID: testGCID, CourseID: "", AtomIDs: atoms},
	}
	for _, c := range cases {
		_, err := BootstrapFromEnrollment(c)
		if err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

// OPEN-1: a content-empty (0-atom) published course must bootstrap a valid
// path so the enrolled learner sees the course (not "not enrolled"). The
// path starts at CurrentIndex 0, 0 atoms, not completed; atoms are appended
// retroactively as chora.creation.atom.created.v1 events arrive.
func TestBootstrapFromEnrollment_AllowsEmptyAtomList(t *testing.T) {
	for _, atoms := range [][]string{nil, {}} {
		p, err := BootstrapFromEnrollment(BootstrapParams{
			TenantID:    testTenant,
			LearnerGCID: testGCID,
			CourseID:    testCourseID,
			AtomIDs:     atoms,
		})
		if err != nil {
			t.Fatalf("empty atom list should bootstrap: %v", err)
		}
		if p.PathID == "" {
			t.Errorf("PathID empty")
		}
		if len(p.AtomIDs) != 0 {
			t.Errorf("AtomIDs len = %d; want 0", len(p.AtomIDs))
		}
		if p.CurrentIndex != 0 {
			t.Errorf("CurrentIndex = %d; want 0", p.CurrentIndex)
		}
		if p.IsCompleted() {
			t.Errorf("0-atom path should not be completed on bootstrap")
		}
		if p.ProgressPercent() != 0.0 {
			t.Errorf("0-atom progress = %v; want 0.0", p.ProgressPercent())
		}
	}
}

// R2: atoms authored after enrollment are appended to an existing path so a
// content-empty path fills in over time.
func TestAppendAtom_AddsNewAtomIdempotently(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID: testTenant, LearnerGCID: testGCID, CourseID: testCourseID,
		AtomIDs: nil, Now: now,
	})
	if added := p.AppendAtom(testAtom1, now.Add(time.Minute)); !added {
		t.Errorf("AppendAtom(new) = false; want true")
	}
	if len(p.AtomIDs) != 1 || p.AtomIDs[0] != testAtom1 {
		t.Errorf("AtomIDs = %v; want [%s]", p.AtomIDs, testAtom1)
	}
	// Re-append same atom — idempotent no-op.
	if added := p.AppendAtom(testAtom1, now.Add(2*time.Minute)); added {
		t.Errorf("AppendAtom(dup) = true; want false")
	}
	if len(p.AtomIDs) != 1 {
		t.Errorf("AtomIDs len = %d after dup append; want 1", len(p.AtomIDs))
	}
	// Empty / blank atom id is ignored.
	if added := p.AppendAtom("  ", now); added {
		t.Errorf("AppendAtom(blank) = true; want false")
	}
}

// R2: appending new work to a previously-completed path reopens it.
func TestAppendAtom_ReopensCompletedPath(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID: testTenant, LearnerGCID: testGCID, CourseID: testCourseID,
		AtomIDs: []string{testAtom1}, Now: now,
	})
	if _, err := p.Advance(testAtom1, now); err != nil {
		t.Fatal(err)
	}
	if !p.IsCompleted() {
		t.Fatalf("precondition: path should be completed")
	}
	if added := p.AppendAtom(testAtom2, now.Add(time.Hour)); !added {
		t.Errorf("AppendAtom = false; want true")
	}
	if p.IsCompleted() {
		t.Errorf("path still completed after new atom appended; want reopened")
	}
	if p.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d; want 1 (cursor preserved)", p.CurrentIndex)
	}
}

func TestBootstrapFromEnrollment_BuildsCourseBoundPath(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3}
	enrollmentID := "01970000-0000-7000-c000-000000000001"
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	p, err := BootstrapFromEnrollment(BootstrapParams{
		TenantID:     testTenant,
		LearnerGCID:  testGCID,
		CourseID:     testCourseID,
		EnrollmentID: enrollmentID,
		AtomIDs:      atoms,
		Title:        "CSM-Prep",
		Now:          now,
	})
	if err != nil {
		t.Fatalf("BootstrapFromEnrollment: %v", err)
	}
	if p.PathID == "" {
		t.Errorf("PathID empty")
	}
	if p.CourseID != testCourseID {
		t.Errorf("CourseID = %q; want %q", p.CourseID, testCourseID)
	}
	if p.EnrollmentID != enrollmentID {
		t.Errorf("EnrollmentID = %q; want %q", p.EnrollmentID, enrollmentID)
	}
	if len(p.AtomIDs) != 3 {
		t.Errorf("AtomIDs len = %d; want 3", len(p.AtomIDs))
	}
	if p.CurrentIndex != 0 {
		t.Errorf("CurrentIndex = %d; want 0", p.CurrentIndex)
	}
	if p.IsCompleted() {
		t.Errorf("IsCompleted true on bootstrap; want false")
	}
}

func TestBootstrapFromEnrollment_IdempotentReturnsSameWhenCalledTwice(t *testing.T) {
	atoms := []string{testAtom1, testAtom2}
	p1, err := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same input creates a SEPARATE aggregate (handlers dedupe at repo).
	// We test only the validator here — bootstrap never errors on dup
	// inputs at the domain layer.
	p2, err := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if p1.PathID == p2.PathID {
		t.Errorf("expected different PathIDs (UUIDv7 monotonic)")
	}
}

func TestAdvance_OnAtomCompleted_MovesCurrentIndex(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	r, err := p.Advance(testAtom1, now)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !r.Advanced {
		t.Errorf("Advanced = false; want true")
	}
	if r.Completed {
		t.Errorf("Completed = true on first advance; want false")
	}
	if p.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d; want 1", p.CurrentIndex)
	}
}

func TestAdvance_OnFinalAtom_MarksCompleted(t *testing.T) {
	atoms := []string{testAtom1, testAtom2}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	_, err := p.Advance(testAtom1, now)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r, err := p.Advance(testAtom2, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !r.Completed {
		t.Errorf("Completed = false on final advance; want true")
	}
	if !p.IsCompleted() {
		t.Errorf("IsCompleted false after advancing past last atom")
	}
	if p.CompletedAt == nil {
		t.Errorf("CompletedAt nil after final advance")
	}
}

func TestAdvance_IdempotentOnAlreadyAdvancedAtom(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	if _, err := p.Advance(testAtom1, now); err != nil {
		t.Fatal(err)
	}
	// Replay the same advance — should be no-op.
	r, err := p.Advance(testAtom1, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if r.Advanced {
		t.Errorf("replay Advanced = true; want false (idempotent)")
	}
	if p.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d after replay; want 1", p.CurrentIndex)
	}
}

func TestAdvance_RejectsAtomNotInPath(t *testing.T) {
	atoms := []string{testAtom1, testAtom2}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	_, err := p.Advance("01970000-0000-7000-a000-000000000099", now)
	if err == nil {
		t.Errorf("expected error advancing atom not in path")
	}
	if !errors.Is(err, ErrAtomNotInPath) {
		t.Errorf("err = %v; want errors.Is(ErrAtomNotInPath)", err)
	}
}

func TestAdvance_RejectsOutOfOrderAtom(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	// Skip atom1, jump to atom3 — Straight-Up cert mode requires linear
	// advancement.
	r, err := p.Advance(testAtom3, now)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// Out-of-order = no advance, no error (idempotent — let the player
	// re-emit when they actually finish atom1).
	if r.Advanced {
		t.Errorf("Advanced = true on out-of-order; want false")
	}
	if p.CurrentIndex != 0 {
		t.Errorf("CurrentIndex = %d; want 0 (no advance)", p.CurrentIndex)
	}
}

func TestAdvance_AfterCompletionIsNoOp(t *testing.T) {
	atoms := []string{testAtom1}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	if _, err := p.Advance(testAtom1, now); err != nil {
		t.Fatal(err)
	}
	r, err := p.Advance(testAtom1, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("post-complete: %v", err)
	}
	if r.Advanced || r.Completed {
		t.Errorf("post-complete advance should be no-op (Advanced=false, Completed=false)")
	}
}

func TestProgressPercent(t *testing.T) {
	atoms := []string{testAtom1, testAtom2, testAtom3, "01970000-0000-7000-a000-000000000004"}
	p, _ := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     atoms,
	})
	if p.ProgressPercent() != 0.0 {
		t.Errorf("initial progress = %v; want 0.0", p.ProgressPercent())
	}
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	if _, err := p.Advance(testAtom1, now); err != nil {
		t.Fatal(err)
	}
	if p.ProgressPercent() != 0.25 {
		t.Errorf("after 1/4 progress = %v; want 0.25", p.ProgressPercent())
	}
}
