package userknowledgegraph

import (
	"testing"
)

const (
	testClusterID = "01970000-0000-7000-b000-000000000001"
)

// TestNewExploration_Success — fresh exploration starts at current_focal =
// started_at_atom and is in ACTIVE status.
func TestNewExploration_Success(t *testing.T) {
	e, err := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.ExplorationID == "" {
		t.Error("expected ExplorationID to be set (UUIDv7)")
	}
	if e.ClusterID != testClusterID {
		t.Errorf("ClusterID = %q, want %q", e.ClusterID, testClusterID)
	}
	if e.TenantID != testTenantID {
		t.Errorf("TenantID = %q, want %q", e.TenantID, testTenantID)
	}
	if e.UserGCID != testUserGCID {
		t.Errorf("UserGCID = %q, want %q", e.UserGCID, testUserGCID)
	}
	if e.StartedAtAtomID != testAtomID {
		t.Errorf("StartedAtAtomID = %q, want %q", e.StartedAtAtomID, testAtomID)
	}
	if e.CurrentFocalAtomID != testAtomID {
		t.Errorf("CurrentFocalAtomID = %q, want %q (defaults to started_at)", e.CurrentFocalAtomID, testAtomID)
	}
	if e.Status != ExplorationStatusActive {
		t.Errorf("Status = %q, want %q", e.Status, ExplorationStatusActive)
	}
	if e.Version != 0 {
		t.Errorf("Version = %d, want 0", e.Version)
	}
	if e.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
}

func TestNewExploration_RequiresClusterID(t *testing.T) {
	_, err := NewExploration("", testTenantID, testUserGCID, testAtomID)
	if err == nil {
		t.Error("expected error when cluster_id is empty")
	}
}

func TestNewExploration_RequiresTenantID(t *testing.T) {
	_, err := NewExploration(testClusterID, "", testUserGCID, testAtomID)
	if err == nil {
		t.Error("expected error when tenant_id is empty")
	}
}

func TestNewExploration_RequiresUserGCID(t *testing.T) {
	_, err := NewExploration(testClusterID, testTenantID, "", testAtomID)
	if err == nil {
		t.Error("expected error when user_gcid is empty")
	}
}

func TestNewExploration_RequiresStartedAtAtomID(t *testing.T) {
	_, err := NewExploration(testClusterID, testTenantID, testUserGCID, "")
	if err == nil {
		t.Error("expected error when started_at_atom_id is empty")
	}
}

// TestArchiveExploration_FromActive — happy path archive.
func TestArchiveExploration_FromActive(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	originalVersion := e.Version

	if err := e.Archive(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Status != ExplorationStatusArchived {
		t.Errorf("Status = %q, want %q", e.Status, ExplorationStatusArchived)
	}
	if e.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, e.Version)
	}
}

func TestArchiveExploration_AlreadyArchived(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	_ = e.Archive()
	if err := e.Archive(); err == nil {
		t.Error("expected error when archiving an already-archived exploration")
	}
}

// TestMoveFocalNode_Success — happy path: focal updates, version bumps.
func TestMoveFocalNode_Success(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	originalVersion := e.Version

	if err := e.MoveFocal(testAtomIDAlt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.CurrentFocalAtomID != testAtomIDAlt {
		t.Errorf("CurrentFocalAtomID = %q, want %q", e.CurrentFocalAtomID, testAtomIDAlt)
	}
	if e.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, e.Version)
	}
	// StartedAtAtomID is immutable
	if e.StartedAtAtomID != testAtomID {
		t.Errorf("StartedAtAtomID changed unexpectedly: %q", e.StartedAtAtomID)
	}
}

func TestMoveFocalNode_RequiresTargetAtomID(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	if err := e.MoveFocal(""); err == nil {
		t.Error("expected error when target_atom_id is empty")
	}
}

// MoveFocal to the same atom is a no-op (no version bump). Distinct from
// rejection — the click was idempotent and we don't punish stale clients.
func TestMoveFocalNode_SameAsCurrent_IsNoOp(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	originalVersion := e.Version

	if err := e.MoveFocal(testAtomID); err != nil {
		t.Fatalf("expected no-op (no error), got %v", err)
	}
	if e.Version != originalVersion {
		t.Errorf("Version unexpectedly advanced on no-op: %d -> %d", originalVersion, e.Version)
	}
}

func TestMoveFocalNode_RejectsArchived(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	_ = e.Archive()
	if err := e.MoveFocal(testAtomIDAlt); err == nil {
		t.Error("expected error when moving focal in an archived exploration")
	}
}

// IsActive helper.
func TestExploration_IsActive(t *testing.T) {
	e, _ := NewExploration(testClusterID, testTenantID, testUserGCID, testAtomID)
	if !e.IsActive() {
		t.Error("expected fresh exploration to be active")
	}
	_ = e.Archive()
	if e.IsActive() {
		t.Error("expected archived exploration to NOT be active")
	}
}
