package learning_path

import (
	"testing"
)

const (
	testTenant = "01970000-0000-7000-8000-000000000001"
	testGCID   = "01970000-0000-7000-9000-000000000001"
	testAtom1  = "01970000-0000-7000-a000-000000000001"
	testAtom2  = "01970000-0000-7000-a000-000000000002"
	testAtom3  = "01970000-0000-7000-a000-000000000003"
)

// TestNew creates a LearningPath with title + atom IDs + owner gcid.
// All required invariants validated.
func TestNew(t *testing.T) {
	atomIDs := []string{testAtom1, testAtom2}
	p, err := New(testTenant, testGCID, "Algebra Foundations", atomIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PathID == "" {
		t.Error("expected PathID populated (UUIDv7)")
	}
	if p.OwnerGCID != testGCID {
		t.Errorf("OwnerGCID = %q, want %q", p.OwnerGCID, testGCID)
	}
	if p.Title != "Algebra Foundations" {
		t.Errorf("Title = %q", p.Title)
	}
	if len(p.AtomIDs) != 2 {
		t.Errorf("AtomIDs len = %d, want 2", len(p.AtomIDs))
	}
	if p.CreatedAt.IsZero() {
		t.Error("expected CreatedAt populated")
	}
	if p.DeletedAt != nil {
		t.Error("expected DeletedAt nil for new path")
	}
}

// TestNew_RejectsEmptyTenant enforces multi-tenant isolation.
func TestNew_RejectsEmptyTenant(t *testing.T) {
	if _, err := New("", testGCID, "T", []string{testAtom1}); err == nil {
		t.Error("expected error when tenant_id empty")
	}
}

// TestNew_RejectsEmptyOwner enforces GCID requirement.
func TestNew_RejectsEmptyOwner(t *testing.T) {
	if _, err := New(testTenant, "", "T", []string{testAtom1}); err == nil {
		t.Error("expected error when owner_gcid empty")
	}
}

// TestNew_RejectsEmptyTitle enforces title presence.
func TestNew_RejectsEmptyTitle(t *testing.T) {
	if _, err := New(testTenant, testGCID, "", []string{testAtom1}); err == nil {
		t.Error("expected error when title empty")
	}
}

// TestNew_RejectsEmptyAtomList enforces non-empty atom_ids — a path
// without atoms is meaningless per ddd-aggregate.
func TestNew_RejectsEmptyAtomList(t *testing.T) {
	if _, err := New(testTenant, testGCID, "T", nil); err == nil {
		t.Error("expected error when atom_ids empty")
	}
	if _, err := New(testTenant, testGCID, "T", []string{}); err == nil {
		t.Error("expected error when atom_ids empty slice")
	}
}

// TestSoftDelete sets DeletedAt without removing the row.
func TestSoftDelete(t *testing.T) {
	p, _ := New(testTenant, testGCID, "T", []string{testAtom1})
	p.SoftDelete()
	if p.DeletedAt == nil {
		t.Error("expected DeletedAt populated after SoftDelete")
	}
}
