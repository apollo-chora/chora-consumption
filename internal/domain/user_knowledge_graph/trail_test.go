package userknowledgegraph

import (
	"testing"
)

const testExplorationID = "01970000-0000-7000-e000-000000000001"

// TestNewTrailHop_Success — happy path, all fields populated, traversed_at set.
func TestNewTrailHop_Success(t *testing.T) {
	h, err := NewTrailHop(
		testExplorationID, testClusterID,
		testTenantID, testUserGCID,
		testAtomID, testAtomIDAlt,
		NeighborRelationExtends, 1,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.TrailID == "" {
		t.Error("expected TrailID to be set (UUIDv7)")
	}
	if h.ExplorationID != testExplorationID {
		t.Errorf("ExplorationID = %q, want %q", h.ExplorationID, testExplorationID)
	}
	if h.FromFocalAtomID != testAtomID {
		t.Errorf("FromFocalAtomID = %q, want %q", h.FromFocalAtomID, testAtomID)
	}
	if h.ToFocalAtomID != testAtomIDAlt {
		t.Errorf("ToFocalAtomID = %q, want %q", h.ToFocalAtomID, testAtomIDAlt)
	}
	if h.Relation != NeighborRelationExtends {
		t.Errorf("Relation = %q, want %q", h.Relation, NeighborRelationExtends)
	}
	if h.StepIndex != 1 {
		t.Errorf("StepIndex = %d, want 1", h.StepIndex)
	}
	if h.TraversedAt.IsZero() {
		t.Error("expected TraversedAt to be set")
	}
}

func TestNewTrailHop_RequiresExplorationID(t *testing.T) {
	_, err := NewTrailHop("", testClusterID, testTenantID, testUserGCID,
		testAtomID, testAtomIDAlt, NeighborRelationExtends, 1)
	if err == nil {
		t.Error("expected error when exploration_id is empty")
	}
}

func TestNewTrailHop_RequiresFromFocal(t *testing.T) {
	_, err := NewTrailHop(testExplorationID, testClusterID, testTenantID, testUserGCID,
		"", testAtomIDAlt, NeighborRelationExtends, 1)
	if err == nil {
		t.Error("expected error when from_focal_atom_id is empty")
	}
}

func TestNewTrailHop_RequiresToFocal(t *testing.T) {
	_, err := NewTrailHop(testExplorationID, testClusterID, testTenantID, testUserGCID,
		testAtomID, "", NeighborRelationExtends, 1)
	if err == nil {
		t.Error("expected error when to_focal_atom_id is empty")
	}
}

func TestNewTrailHop_RejectsSameFromAndTo(t *testing.T) {
	_, err := NewTrailHop(testExplorationID, testClusterID, testTenantID, testUserGCID,
		testAtomID, testAtomID, NeighborRelationExtends, 1)
	if err == nil {
		t.Error("expected error when from_focal_atom_id == to_focal_atom_id (matches schema CHECK)")
	}
}

func TestNewTrailHop_StepIndexNonNegative(t *testing.T) {
	_, err := NewTrailHop(testExplorationID, testClusterID, testTenantID, testUserGCID,
		testAtomID, testAtomIDAlt, NeighborRelationExtends, -1)
	if err == nil {
		t.Error("expected error when step_index < 0")
	}
}

func TestNewTrailHop_RequiresValidRelation(t *testing.T) {
	_, err := NewTrailHop(testExplorationID, testClusterID, testTenantID, testUserGCID,
		testAtomID, testAtomIDAlt, "", 1)
	if err == nil {
		t.Error("expected error when relation is empty (unspecified)")
	}
}

// JunctionOpportunity — transient struct, no domain methods, just smoke test
// the constructor for completeness + future extension.
func TestNewJunctionOpportunity_Success(t *testing.T) {
	otherCluster := "01970000-0000-7000-b000-000000000002"
	jo := NewJunctionOpportunity(
		testAtomID, testClusterID, otherCluster,
		"agile", "PMP",
	)
	if jo.ViaAtomID != testAtomID {
		t.Errorf("ViaAtomID = %q, want %q", jo.ViaAtomID, testAtomID)
	}
	if jo.CurrentClusterID != testClusterID {
		t.Errorf("CurrentClusterID = %q, want %q", jo.CurrentClusterID, testClusterID)
	}
	if jo.OtherClusterID != otherCluster {
		t.Errorf("OtherClusterID = %q, want %q", jo.OtherClusterID, otherCluster)
	}
	if jo.CurrentClusterDisplayName != "agile" {
		t.Errorf("CurrentClusterDisplayName = %q, want %q", jo.CurrentClusterDisplayName, "agile")
	}
	if jo.OtherClusterDisplayName != "PMP" {
		t.Errorf("OtherClusterDisplayName = %q, want %q", jo.OtherClusterDisplayName, "PMP")
	}
}
