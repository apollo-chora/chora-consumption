package learning_path

import (
	"testing"
)

// TestEnroll creates an enrollment for a learner against a path.
func TestEnroll(t *testing.T) {
	e, err := Enroll(testTenant, "path-1", testGCID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.EnrollmentID == "" {
		t.Error("expected EnrollmentID populated")
	}
	if e.PathID != "path-1" {
		t.Errorf("PathID = %q", e.PathID)
	}
	if e.LearnerGCID != testGCID {
		t.Errorf("LearnerGCID = %q", e.LearnerGCID)
	}
	if e.EnrolledAt.IsZero() {
		t.Error("expected EnrolledAt populated")
	}
	if len(e.CompletedAtomIDs) != 0 {
		t.Errorf("CompletedAtomIDs = %d items, want 0", len(e.CompletedAtomIDs))
	}
}

func TestEnroll_RequiresTenant(t *testing.T) {
	if _, err := Enroll("", "path-1", testGCID); err == nil {
		t.Error("expected error when tenant empty")
	}
}

func TestEnroll_RequiresPath(t *testing.T) {
	if _, err := Enroll(testTenant, "", testGCID); err == nil {
		t.Error("expected error when path empty")
	}
}

func TestEnroll_RequiresGCID(t *testing.T) {
	if _, err := Enroll(testTenant, "path-1", ""); err == nil {
		t.Error("expected error when gcid empty")
	}
}

// TestMarkAtomComplete adds an atom to CompletedAtomIDs (idempotent).
func TestMarkAtomComplete(t *testing.T) {
	e, _ := Enroll(testTenant, "path-1", testGCID)
	e.MarkAtomComplete(testAtom1)
	if len(e.CompletedAtomIDs) != 1 {
		t.Errorf("CompletedAtomIDs len = %d, want 1", len(e.CompletedAtomIDs))
	}
	// Idempotent — calling twice does not duplicate.
	e.MarkAtomComplete(testAtom1)
	if len(e.CompletedAtomIDs) != 1 {
		t.Errorf("after idempotent re-mark, CompletedAtomIDs len = %d, want 1", len(e.CompletedAtomIDs))
	}
}

// TestProgress computes percent-complete given path's full atom list.
func TestProgress(t *testing.T) {
	e, _ := Enroll(testTenant, "path-1", testGCID)
	pathAtoms := []string{testAtom1, testAtom2, testAtom3}

	if pct := e.Progress(pathAtoms); pct != 0.0 {
		t.Errorf("empty progress = %v, want 0.0", pct)
	}
	e.MarkAtomComplete(testAtom1)
	if pct := e.Progress(pathAtoms); pct != float32(1.0/3.0) {
		t.Errorf("1/3 progress = %v", pct)
	}
	e.MarkAtomComplete(testAtom2)
	e.MarkAtomComplete(testAtom3)
	if pct := e.Progress(pathAtoms); pct != 1.0 {
		t.Errorf("full progress = %v, want 1.0", pct)
	}
}

// TestProgress_EmptyPathReturnsZero handles divide-by-zero.
func TestProgress_EmptyPathReturnsZero(t *testing.T) {
	e, _ := Enroll(testTenant, "path-1", testGCID)
	if pct := e.Progress(nil); pct != 0.0 {
		t.Errorf("empty path progress = %v, want 0.0", pct)
	}
}

// TestProgress_IgnoresAtomsNotInPath filters spurious completions.
// Atoms completed but not in the path's atom list shouldn't inflate %.
func TestProgress_IgnoresAtomsNotInPath(t *testing.T) {
	e, _ := Enroll(testTenant, "path-1", testGCID)
	e.MarkAtomComplete("01970000-0000-7000-a000-000000009999")
	pathAtoms := []string{testAtom1, testAtom2}
	if pct := e.Progress(pathAtoms); pct != 0.0 {
		t.Errorf("non-path completion progress = %v, want 0.0", pct)
	}
}
