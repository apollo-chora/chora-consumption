package userknowledgegraph

import (
	"testing"
)

const (
	testTenantID  = "01970000-0000-7000-8000-000000000001"
	testUserGCID  = "01970000-0000-7000-9000-000000000001"
	testAtomID    = "01970000-0000-7000-a000-000000000001"
	testAtomIDAlt = "01970000-0000-7000-a000-000000000002"
	testSeed      = "agile"
)

// TestNewMapCluster_Success — happy path constructs an active cluster
// with the expected defaults.
func TestNewMapCluster_Success(t *testing.T) {
	c, err := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ClusterID == "" {
		t.Error("expected ClusterID to be set (UUIDv7)")
	}
	if c.TenantID != testTenantID {
		t.Errorf("TenantID = %q, want %q", c.TenantID, testTenantID)
	}
	if c.UserGCID != testUserGCID {
		t.Errorf("UserGCID = %q, want %q", c.UserGCID, testUserGCID)
	}
	if c.SeedTopic != testSeed {
		t.Errorf("SeedTopic = %q, want %q", c.SeedTopic, testSeed)
	}
	if c.SeedAtomID != testAtomID {
		t.Errorf("SeedAtomID = %q, want %q", c.SeedAtomID, testAtomID)
	}
	if c.Status != ClusterStatusActive {
		t.Errorf("Status = %q, want %q", c.Status, ClusterStatusActive)
	}
	if c.NodeCount != 1 {
		t.Errorf("NodeCount = %d, want 1", c.NodeCount)
	}
	if c.Version != 0 {
		t.Errorf("Version = %d, want 0", c.Version)
	}
	if c.MergedIntoClusterID != "" {
		t.Errorf("MergedIntoClusterID should be empty, got %q", c.MergedIntoClusterID)
	}
	if c.MergedViaAtomID != "" {
		t.Errorf("MergedViaAtomID should be empty, got %q", c.MergedViaAtomID)
	}
	if c.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if c.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
	if c.DeletedAt != nil {
		t.Error("expected DeletedAt to be nil for a fresh cluster")
	}
}

func TestNewMapCluster_DisplayNameDefaultsToSeedTopic(t *testing.T) {
	c, err := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DisplayName != testSeed {
		t.Errorf("DisplayName = %q, want %q (defaulted to seed_topic)", c.DisplayName, testSeed)
	}
}

func TestNewMapCluster_DisplayNameOverride(t *testing.T) {
	c, err := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "My Agile Map")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DisplayName != "My Agile Map" {
		t.Errorf("DisplayName = %q, want %q", c.DisplayName, "My Agile Map")
	}
}

func TestNewMapCluster_RequiresTenantID(t *testing.T) {
	_, err := NewMapCluster("", testUserGCID, testSeed, testAtomID, "")
	if err == nil {
		t.Error("expected error when tenant_id is empty")
	}
}

func TestNewMapCluster_RequiresUserGCID(t *testing.T) {
	_, err := NewMapCluster(testTenantID, "", testSeed, testAtomID, "")
	if err == nil {
		t.Error("expected error when user_gcid is empty")
	}
}

func TestNewMapCluster_RequiresSeedTopic(t *testing.T) {
	_, err := NewMapCluster(testTenantID, testUserGCID, "", testAtomID, "")
	if err == nil {
		t.Error("expected error when seed_topic is empty")
	}
}

func TestNewMapCluster_RequiresSeedAtomID(t *testing.T) {
	_, err := NewMapCluster(testTenantID, testUserGCID, testSeed, "", "")
	if err == nil {
		t.Error("expected error when seed_atom_id is empty")
	}
}

func TestNewMapCluster_SeedTopicMaxLength(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	_, err := NewMapCluster(testTenantID, testUserGCID, string(long), testAtomID, "")
	if err == nil {
		t.Error("expected error when seed_topic exceeds 128 chars")
	}
}

func TestNewMapCluster_DisplayNameMaxLength(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	_, err := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, string(long))
	if err == nil {
		t.Error("expected error when display_name exceeds 128 chars")
	}
}

// TestArchive — archive transitions ACTIVE -> ARCHIVED, sets UpdatedAt
// and bumps Version.
func TestArchive_FromActive(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	originalVersion := c.Version

	if err := c.Archive(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Status != ClusterStatusArchived {
		t.Errorf("Status = %q, want %q", c.Status, ClusterStatusArchived)
	}
	if c.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, c.Version)
	}
}

// TestArchive_AlreadyArchived returns ErrAlreadyArchived (idempotency
// requires explicit handling at adapter layer; domain rejects double-archive).
func TestArchive_AlreadyArchived(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.Archive()
	if err := c.Archive(); err == nil {
		t.Error("expected error when archiving an already-archived cluster")
	}
}

// TestArchive_AlreadyMerged returns ErrInvalidStatusTransition.
func TestArchive_AlreadyMerged(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	survivorID := "01970000-0000-7000-c000-000000000001"
	_ = c.MergeInto(survivorID, testAtomIDAlt)
	if err := c.Archive(); err == nil {
		t.Error("expected error when archiving a merged cluster")
	}
}

const testGoalID = "01970000-0000-7000-d000-000000000001"

// TestProject_FromActive — active -> projected records the Goal it became
// (ADR-223) and bumps Version.
func TestProject_FromActive(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	originalVersion := c.Version
	if err := c.Project(testGoalID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Status != ClusterStatusProjected {
		t.Errorf("Status = %q, want %q", c.Status, ClusterStatusProjected)
	}
	if c.ProjectedIntoGoalID != testGoalID {
		t.Errorf("ProjectedIntoGoalID = %q, want %q", c.ProjectedIntoGoalID, testGoalID)
	}
	if c.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, c.Version)
	}
}

// TestProject_FromArchived — an archived (set-aside) cluster may still be
// projected into a sovereign map.
func TestProject_FromArchived(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.Archive()
	if err := c.Project(testGoalID); err != nil {
		t.Fatalf("unexpected error projecting an archived cluster: %v", err)
	}
	if c.Status != ClusterStatusProjected {
		t.Errorf("Status = %q, want projected", c.Status)
	}
}

// TestProject_RequiresGoal — a blank goal id is rejected (the projection
// must record the Goal it became).
func TestProject_RequiresGoal(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err := c.Project("   "); err == nil {
		t.Error("expected error projecting without a goal id")
	}
}

// TestProject_AlreadyProjected — projection is terminal; a second call is
// rejected (adapter maps to idempotent 200 with the recorded goal).
func TestProject_AlreadyProjected(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.Project(testGoalID)
	if err := c.Project("01970000-0000-7000-d000-000000000002"); err == nil {
		t.Error("expected error re-projecting an already-projected cluster")
	}
}

// TestProject_AlreadyMerged — a merged cluster cannot be projected.
func TestProject_AlreadyMerged(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.MergeInto("01970000-0000-7000-c000-000000000001", testAtomIDAlt)
	if err := c.Project(testGoalID); err == nil {
		t.Error("expected error projecting a merged cluster")
	}
}

// TestMergeInto — happy path transitions ACTIVE -> MERGED_INTO with
// surviving cluster + via_atom recorded.
func TestMergeInto_FromActive(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	originalVersion := c.Version
	survivorID := "01970000-0000-7000-c000-000000000001"

	if err := c.MergeInto(survivorID, testAtomIDAlt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Status != ClusterStatusMergedInto {
		t.Errorf("Status = %q, want %q", c.Status, ClusterStatusMergedInto)
	}
	if c.MergedIntoClusterID != survivorID {
		t.Errorf("MergedIntoClusterID = %q, want %q", c.MergedIntoClusterID, survivorID)
	}
	if c.MergedViaAtomID != testAtomIDAlt {
		t.Errorf("MergedViaAtomID = %q, want %q", c.MergedViaAtomID, testAtomIDAlt)
	}
	if c.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, c.Version)
	}
}

func TestMergeInto_RequiresSurvivingClusterID(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err := c.MergeInto("", testAtomIDAlt); err == nil {
		t.Error("expected error when surviving_cluster_id is empty")
	}
}

func TestMergeInto_RequiresViaAtomID(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	survivorID := "01970000-0000-7000-c000-000000000001"
	if err := c.MergeInto(survivorID, ""); err == nil {
		t.Error("expected error when via_atom_id is empty")
	}
}

func TestMergeInto_AlreadyArchived(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.Archive()
	survivorID := "01970000-0000-7000-c000-000000000001"
	if err := c.MergeInto(survivorID, testAtomIDAlt); err == nil {
		t.Error("expected error when merging an archived cluster")
	}
}

func TestMergeInto_SelfMerge(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err := c.MergeInto(c.ClusterID, testAtomIDAlt); err == nil {
		t.Error("expected error when merging a cluster into itself")
	}
}

// TestRename — happy path updates display_name + bumps Version.
func TestRename_Success(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	originalVersion := c.Version

	if err := c.Rename("My Renamed Map"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DisplayName != "My Renamed Map" {
		t.Errorf("DisplayName = %q, want %q", c.DisplayName, "My Renamed Map")
	}
	if c.Version <= originalVersion {
		t.Errorf("Version did not advance: %d -> %d", originalVersion, c.Version)
	}
}

func TestRename_RequiresNonEmpty(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	if err := c.Rename(""); err == nil {
		t.Error("expected error when new display_name is empty")
	}
}

func TestRename_MaxLength(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := c.Rename(string(long)); err == nil {
		t.Error("expected error when new display_name exceeds 128 chars")
	}
}

func TestRename_RejectsArchived(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	_ = c.Archive()
	if err := c.Rename("nope"); err == nil {
		t.Error("expected error when renaming an archived cluster")
	}
}

// TestIncrementNodeCount — used by Exploration commands when a new focal
// is added to the cluster's distinct atom set.
func TestIncrementNodeCount_Success(t *testing.T) {
	c, _ := NewMapCluster(testTenantID, testUserGCID, testSeed, testAtomID, "")
	original := c.NodeCount

	c.IncrementNodeCount()
	if c.NodeCount != original+1 {
		t.Errorf("NodeCount = %d, want %d", c.NodeCount, original+1)
	}
}

func TestAbsorbMerged_AddsNodeCounts(t *testing.T) {
	survivor, _ := NewMapCluster(testTenantID, testUserGCID, "agile", testAtomID, "")
	survivor.NodeCount = 5
	merged, _ := NewMapCluster(testTenantID, testUserGCID, "PMP", testAtomIDAlt, "")
	merged.NodeCount = 3

	survivor.AbsorbMerged(merged, testAtomIDAlt) // via_atom counted once

	// 5 + 3 - 1 (shared via_atom counted in both before merge)
	if survivor.NodeCount != 7 {
		t.Errorf("NodeCount after merge = %d, want 7", survivor.NodeCount)
	}
	// display_name defaults to "{seed_a} + {seed_b}"
	if survivor.DisplayName != "agile + PMP" {
		t.Errorf("DisplayName after merge = %q, want %q", survivor.DisplayName, "agile + PMP")
	}
}

func TestAbsorbMerged_RejectsArchived(t *testing.T) {
	survivor, _ := NewMapCluster(testTenantID, testUserGCID, "agile", testAtomID, "")
	_ = survivor.Archive()
	merged, _ := NewMapCluster(testTenantID, testUserGCID, "PMP", testAtomIDAlt, "")
	if err := survivor.AbsorbMerged(merged, testAtomIDAlt); err == nil {
		t.Error("expected error when survivor is archived")
	}
}
