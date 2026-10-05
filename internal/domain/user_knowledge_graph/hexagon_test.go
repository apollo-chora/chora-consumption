package userknowledgegraph

import (
	"testing"
)

const (
	testRunID    = "01970000-0000-7000-d000-000000000001"
	testModelID  = "gemini-2.5-flash"
	testFogLabel = "Daily standups, retros, and the planning cadence."
)

// makeValidNeighbors returns 6 valid distinct neighbors for tests.
// position 0 → atom_id ending in 02; position 5 → atom_id ending in 07.
func makeValidNeighbors() []HexagonNeighbor {
	atoms := []string{
		"01970000-0000-7000-a000-000000000002",
		"01970000-0000-7000-a000-000000000003",
		"01970000-0000-7000-a000-000000000004",
		"01970000-0000-7000-a000-000000000005",
		"01970000-0000-7000-a000-000000000006",
		"01970000-0000-7000-a000-000000000007",
	}
	relations := []NeighborRelation{
		NeighborRelationPrerequisiteOf,
		NeighborRelationExtends,
		NeighborRelationAnalogyOf,
		NeighborRelationContrastsWith,
		NeighborRelationAppliedIn,
		NeighborRelationCuriosityJump,
	}
	out := make([]HexagonNeighbor, 6)
	for i := range out {
		out[i] = HexagonNeighbor{
			AtomID:     atoms[i],
			Relation:   relations[i],
			Confidence: 0.7,
			FogLabel:   testFogLabel,
			IsJunction: false,
		}
	}
	return out
}

// TestNewHexagonNode_Success — fresh hexagon with 6 neighbors, fresh cache.
func TestNewHexagonNode_Success(t *testing.T) {
	neighbors := makeValidNeighbors()
	h, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.HexNodeID == "" {
		t.Error("expected HexNodeID to be set (UUIDv7)")
	}
	if h.FocalAtomID != testAtomID {
		t.Errorf("FocalAtomID = %q, want %q", h.FocalAtomID, testAtomID)
	}
	if len(h.Neighbors) != 6 {
		t.Errorf("len(Neighbors) = %d, want 6", len(h.Neighbors))
	}
	if h.GeneratedByRunID != testRunID {
		t.Errorf("GeneratedByRunID = %q, want %q", h.GeneratedByRunID, testRunID)
	}
	if h.GeneratedByModelID != testModelID {
		t.Errorf("GeneratedByModelID = %q, want %q", h.GeneratedByModelID, testModelID)
	}
	if h.GeneratedAt.IsZero() {
		t.Error("expected GeneratedAt to be set")
	}
	if h.InvalidatedAt != nil {
		t.Error("expected fresh hexagon to have InvalidatedAt nil")
	}
	if h.InvalidationReason != FogInvalidationReasonNeverGenerated {
		// fresh = never_generated meaning "this is the first generation; nothing invalidated"
		// (interpreted as "no invalidation reason currently")
		t.Errorf("InvalidationReason = %q, want %q", h.InvalidationReason, FogInvalidationReasonNeverGenerated)
	}
	if !h.IsCacheFresh() {
		t.Error("expected fresh hexagon to be cache-fresh")
	}
}

func TestNewHexagonNode_RequiresExactly6Neighbors(t *testing.T) {
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors()[:5], // 5 only
		testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when neighbors count != 6 (got 5)")
	}

	tooMany := append(makeValidNeighbors(), HexagonNeighbor{
		AtomID:     "01970000-0000-7000-a000-000000000099",
		Relation:   NeighborRelationExtends,
		Confidence: 0.5,
		FogLabel:   "x",
	})
	_, err = NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, tooMany, // 7
		testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when neighbors count != 6 (got 7)")
	}
}

func TestNewHexagonNode_RejectsEmptyNeighborAtomID(t *testing.T) {
	neighbors := makeValidNeighbors()
	neighbors[2].AtomID = ""
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when a neighbor has empty atom_id")
	}
}

func TestNewHexagonNode_RejectsConfidenceOutOfRange(t *testing.T) {
	neighbors := makeValidNeighbors()
	neighbors[0].Confidence = 1.5
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when confidence > 1.0")
	}

	neighbors[0].Confidence = -0.1
	_, err = NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when confidence < 0.0")
	}
}

func TestNewHexagonNode_RejectsDuplicateNeighborAtomIDs(t *testing.T) {
	neighbors := makeValidNeighbors()
	neighbors[1].AtomID = neighbors[0].AtomID
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when neighbors have duplicate atom_ids")
	}
}

func TestNewHexagonNode_RejectsFocalInNeighbors(t *testing.T) {
	neighbors := makeValidNeighbors()
	neighbors[3].AtomID = testAtomID // focal atom appears as neighbor
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when focal atom_id appears in neighbors")
	}
}

func TestNewHexagonNode_RequiresExplorationID(t *testing.T) {
	_, err := NewHexagonNode(
		testClusterID, "",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors(), testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when exploration_id is empty")
	}
}

func TestNewHexagonNode_RequiresClusterID(t *testing.T) {
	_, err := NewHexagonNode(
		"", "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors(), testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when cluster_id is empty")
	}
}

func TestNewHexagonNode_RequiresFocalAtomID(t *testing.T) {
	_, err := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		"", makeValidNeighbors(), testRunID, testModelID,
	)
	if err == nil {
		t.Error("expected error when focal_atom_id is empty")
	}
}

// TestInvalidate — happy path, marks the cache stale.
func TestInvalidate_Fresh(t *testing.T) {
	h, _ := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors(), testRunID, testModelID,
	)

	if err := h.Invalidate(FogInvalidationReasonAtomPublished); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.InvalidatedAt == nil {
		t.Error("expected InvalidatedAt to be set after Invalidate")
	}
	if h.InvalidationReason != FogInvalidationReasonAtomPublished {
		t.Errorf("InvalidationReason = %q, want %q",
			h.InvalidationReason, FogInvalidationReasonAtomPublished)
	}
	if h.IsCacheFresh() {
		t.Error("expected IsCacheFresh() to be false after Invalidate")
	}
}

// TestInvalidate_AlreadyInvalidated — second call with a HIGHER-priority
// reason wins; lower-priority is ignored. The priority order matches the
// canonical SQL enum order: atom_published > atom_revision_updated >
// user_retention_shifted > manual_admin > never_generated.
//
// (Practically — the sweeper invalidates by the strongest signal that has
// arrived; if a new event is weaker, we keep the original reason.)
func TestInvalidate_AlreadyInvalidated_KeepsPriority(t *testing.T) {
	h, _ := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors(), testRunID, testModelID,
	)
	_ = h.Invalidate(FogInvalidationReasonAtomPublished) // higher priority
	originalAt := h.InvalidatedAt
	originalReason := h.InvalidationReason

	if err := h.Invalidate(FogInvalidationReasonManualAdmin); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reason should not be downgraded.
	if h.InvalidationReason != originalReason {
		t.Errorf("InvalidationReason was downgraded: %q -> %q",
			originalReason, h.InvalidationReason)
	}
	// Timestamp should not advance backwards either.
	if !h.InvalidatedAt.Equal(*originalAt) {
		t.Errorf("InvalidatedAt unexpectedly changed: %v -> %v", originalAt, h.InvalidatedAt)
	}
}

func TestInvalidate_AlreadyInvalidated_HigherPriorityWins(t *testing.T) {
	h, _ := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, makeValidNeighbors(), testRunID, testModelID,
	)
	_ = h.Invalidate(FogInvalidationReasonManualAdmin) // lower priority
	if err := h.Invalidate(FogInvalidationReasonAtomPublished); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.InvalidationReason != FogInvalidationReasonAtomPublished {
		t.Errorf("InvalidationReason = %q, want atom_published (higher priority)",
			h.InvalidationReason)
	}
}

// JunctionAtomIDs — convenience: surface neighbor atom_ids flagged as junctions.
func TestJunctionAtomIDs(t *testing.T) {
	neighbors := makeValidNeighbors()
	neighbors[1].IsJunction = true
	neighbors[4].IsJunction = true

	h, _ := NewHexagonNode(
		testClusterID, "01970000-0000-7000-e000-000000000001",
		testTenantID, testUserGCID,
		testAtomID, neighbors, testRunID, testModelID,
	)

	jct := h.JunctionAtomIDs()
	if len(jct) != 2 {
		t.Errorf("len(JunctionAtomIDs) = %d, want 2", len(jct))
	}
	want := []string{neighbors[1].AtomID, neighbors[4].AtomID}
	for i, w := range want {
		if jct[i] != w {
			t.Errorf("jct[%d] = %q, want %q", i, jct[i], w)
		}
	}
}
