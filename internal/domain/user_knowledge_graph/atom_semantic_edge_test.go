// atom_semantic_edge_test.go — RED-phase tests for the relocated
// authoring-time atom-to-atom semantic edge value object (relocated +
// renamed per ADR-143 §1 from chora_creation.knowledge_graph_edges to
// chora_consumption.atom_semantic_edges).
//
// Per ADR-143 §3:
//   - Tenant-scoped, NOT user-scoped (authoring-time data)
//   - Soft-delete via DeletedAt
//   - Self-loops forbidden (CHECK source <> target)
//   - Weight in [0, 1]
package userknowledgegraph

import (
	"testing"
	"time"
)

func TestNewAtomSemanticEdge_Success(t *testing.T) {
	now := time.Now().UTC()
	e, err := NewAtomSemanticEdge(testTenantID, testAtomID, testAtomIDAlt, AtomEdgeTypeExtends, 0.7, now)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if e.EdgeID == "" {
		t.Error("expected EdgeID to be set")
	}
	if e.TenantID != testTenantID {
		t.Errorf("TenantID = %q", e.TenantID)
	}
	if e.SourceAtomID != testAtomID || e.TargetAtomID != testAtomIDAlt {
		t.Errorf("source/target wrong: %q→%q", e.SourceAtomID, e.TargetAtomID)
	}
	if e.EdgeType != AtomEdgeTypeExtends {
		t.Errorf("EdgeType = %q", e.EdgeType)
	}
	if e.Weight != 0.7 {
		t.Errorf("Weight = %v", e.Weight)
	}
}

func TestNewAtomSemanticEdge_RejectsSelfLoop(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewAtomSemanticEdge(testTenantID, testAtomID, testAtomID, AtomEdgeTypeExtends, 0.5, now)
	if err == nil {
		t.Error("expected ErrAtomEdgeSelfLoop")
	}
}

func TestNewAtomSemanticEdge_RejectsBadWeight(t *testing.T) {
	cases := []float32{-0.1, 1.1, 2.0}
	for _, w := range cases {
		_, err := NewAtomSemanticEdge(testTenantID, testAtomID, testAtomIDAlt, AtomEdgeTypeExtends, w, time.Now().UTC())
		if err == nil {
			t.Errorf("weight %v should fail validation", w)
		}
	}
}

func TestNewAtomSemanticEdge_RejectsMissingTenant(t *testing.T) {
	_, err := NewAtomSemanticEdge("", testAtomID, testAtomIDAlt, AtomEdgeTypeExtends, 0.5, time.Now().UTC())
	if err == nil {
		t.Error("expected error on missing tenant")
	}
}

func TestNewAtomSemanticEdge_RejectsBadEdgeType(t *testing.T) {
	_, err := NewAtomSemanticEdge(testTenantID, testAtomID, testAtomIDAlt, AtomEdgeType(""), 0.5, time.Now().UTC())
	if err == nil {
		t.Error("expected error on empty edge type")
	}
	_, err = NewAtomSemanticEdge(testTenantID, testAtomID, testAtomIDAlt, AtomEdgeType("curiosity_jump"), 0.5, time.Now().UTC())
	if err == nil {
		t.Error("expected error on non-authoring edge type (curiosity_jump is fog-only)")
	}
}

func TestAtomSemanticEdge_SoftDelete(t *testing.T) {
	now := time.Now().UTC()
	e, _ := NewAtomSemanticEdge(testTenantID, testAtomID, testAtomIDAlt, AtomEdgeTypeExtends, 0.5, now)
	e.SoftDelete(now)
	if e.DeletedAt == nil {
		t.Error("DeletedAt was not set")
	}
}
