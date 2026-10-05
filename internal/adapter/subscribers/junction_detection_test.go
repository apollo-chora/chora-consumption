// junction_detection_test.go — RED-phase tests for the junction
// detection application service per ADR-143 §6 + S5.2 mission deliverable 4.
//
// Junction detection runs when:
//  1. User creates a 2nd+ cluster (overlap with existing clusters)
//  2. Fog generation surfaces a neighbor that's also a focal in
//     another active cluster of this user
//
// When the overlap atom set crosses the threshold (default 3 atoms,
// configurable via KG_JUNCTION_THRESHOLD), a Junction is persisted with
// pending status + emit chora.consumption.kg_junction.detected.v1.
package subscribers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

const (
	jdTenant = "01970000-0000-7000-8000-000000000001"
	jdUserA  = "01970000-0000-7000-9000-000000000001"
)

// mkSixNeighborsInts is a small helper for tests — uses fixed atom IDs.
func mkSixNeighborsInts(focal string) []userknowledgegraph.HexagonNeighbor {
	pool := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	out := make([]userknowledgegraph.HexagonNeighbor, 0, 6)
	for _, p := range pool {
		if p == focal {
			continue
		}
		out = append(out, userknowledgegraph.HexagonNeighbor{
			AtomID:     p,
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.5,
		})
		if len(out) == 6 {
			break
		}
	}
	return out
}

func TestJunctionDetector_NoOverlapDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	clusters := inmem.NewMapClusterRepo()
	hexagons := inmem.NewHexagonRepo()
	junctions := inmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)
	// Hexagon for cluster A focal=atomA — neighbors include "a1..a6"
	hexA, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, "atomA", mkSixNeighborsInts("atomA"), "r", "m")
	_ = hexagons.Upsert(ctx, hexA)

	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	res, err := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, []string{"atomB"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.JunctionsDetected != 0 {
		t.Errorf("detected = %d, want 0 (no overlap)", res.JunctionsDetected)
	}
}

func TestJunctionDetector_BelowThresholdDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	clusters := inmem.NewMapClusterRepo()
	hexagons := inmem.NewHexagonRepo()
	junctions := inmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)
	// Hex A focal=atomA. Cluster B's atom set includes "atomA" — that's
	// 1 overlap, below threshold of 3.
	hexA, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, "atomA", mkSixNeighborsInts("atomA"), "r", "m")
	_ = hexagons.Upsert(ctx, hexA)

	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	res, err := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, []string{"atomA", "x", "y"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.JunctionsDetected != 0 {
		t.Errorf("detected = %d, want 0 (overlap below threshold)", res.JunctionsDetected)
	}
}

func TestJunctionDetector_AtThresholdEmitsEventAndPersists(t *testing.T) {
	ctx := context.Background()
	clusters := inmem.NewMapClusterRepo()
	hexagons := inmem.NewHexagonRepo()
	junctions := inmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)

	// Cluster A has 3 focals: atom1, atom2, atom3.
	for _, focal := range []string{"atom1", "atom2", "atom3"} {
		hex, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, focal, mkSixNeighborsInts(focal), "r", "m")
		_ = hexagons.Upsert(ctx, hex)
	}

	// New cluster B's atom set overlaps all 3.
	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	res, err := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, []string{"atom1", "atom2", "atom3", "atom4"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.JunctionsDetected != 1 {
		t.Errorf("detected = %d, want 1", res.JunctionsDetected)
	}
	if len(res.JunctionIDs) != 1 {
		t.Errorf("len JunctionIDs = %d, want 1", len(res.JunctionIDs))
	}
	// Verify the persistent record.
	persisted, err := junctions.ListPendingForUser(ctx, jdTenant, jdUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 {
		t.Errorf("persisted len = %d, want 1", len(persisted))
	}
	// Verify event emitted.
	gotEvents := pub.Events()
	hasDetected := false
	for _, ev := range gotEvents {
		if ev.Topic == events.TopicKGJunctionDetected {
			hasDetected = true
		}
	}
	if !hasDetected {
		t.Errorf("expected %s event; got %d events", events.TopicKGJunctionDetected, len(gotEvents))
	}
}

func TestJunctionDetector_IdempotentOnDuplicateDetection(t *testing.T) {
	ctx := context.Background()
	clusters := inmem.NewMapClusterRepo()
	hexagons := inmem.NewHexagonRepo()
	junctions := inmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)
	for _, focal := range []string{"atom1", "atom2", "atom3"} {
		hex, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, focal, mkSixNeighborsInts(focal), "r", "m")
		_ = hexagons.Upsert(ctx, hex)
	}

	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	atoms := []string{"atom1", "atom2", "atom3"}
	_, _ = det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, atoms)
	res2, _ := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, atoms)
	if res2.JunctionsDetected != 0 {
		t.Errorf("second detect = %d, want 0 (idempotent)", res2.JunctionsDetected)
	}
	persisted, _ := junctions.ListPendingForUser(ctx, jdTenant, jdUserA)
	if len(persisted) != 1 {
		t.Errorf("persisted len = %d, want 1 (no duplicate)", len(persisted))
	}
}

func TestJunctionDetector_FailsLoudOnMissingTenant(t *testing.T) {
	ctx := context.Background()
	det := NewJunctionDetector(inmem.NewMapClusterRepo(), inmem.NewHexagonRepo(), inmem.NewJunctionRepo(), events.NewKGPublisher(events.NewInMemoryPublisher()), 3)
	_, err := det.DetectForCluster(ctx, "", jdUserA, "c", []string{"a"})
	if err == nil {
		t.Error("expected error: missing tenant")
	}
}

func TestJunctionDetector_FailsLoudOnMissingUser(t *testing.T) {
	ctx := context.Background()
	det := NewJunctionDetector(inmem.NewMapClusterRepo(), inmem.NewHexagonRepo(), inmem.NewJunctionRepo(), events.NewKGPublisher(events.NewInMemoryPublisher()), 3)
	_, err := det.DetectForCluster(ctx, jdTenant, "", "c", []string{"a"})
	if err == nil {
		t.Error("expected error: missing user")
	}
	// The error message should mention user (sanity check we didn't
	// accidentally swap argument order).
	if err != nil && !strings.Contains(err.Error(), "user") {
		t.Errorf("err = %v, want user mention", err)
	}
}

func TestJunctionDetector_RespectsClusterBNotInOtherUsers(t *testing.T) {
	ctx := context.Background()
	clusters := inmem.NewMapClusterRepo()
	hexagons := inmem.NewHexagonRepo()
	junctions := inmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cOther, _ := userknowledgegraph.NewMapCluster(jdTenant, "other-user", "kanban", "atomZ", "")
	_ = clusters.Save(ctx, cOther)

	// Other user's cluster has 3 focals identical to new atoms — but
	// they're another user's; must NOT trigger junction.
	for _, focal := range []string{"atom1", "atom2", "atom3"} {
		hex, _ := userknowledgegraph.NewHexagonNode(cOther.ClusterID, "exp-Other", jdTenant, "other-user", focal, mkSixNeighborsInts(focal), "r", "m")
		_ = hexagons.Upsert(ctx, hex)
	}

	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)
	res, _ := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID, []string{"atom1", "atom2", "atom3"})
	if res.JunctionsDetected != 0 {
		t.Errorf("detected = %d, want 0 (cross-user must not trigger)", res.JunctionsDetected)
	}
	// Sanity — check the time check.
	_ = time.Now()
}
