// user_kg_test.go — RED-phase tests for the per-user Knowledge Graph
// in-memory adapters (ADR-143). Validates the MapClusterRepo,
// ExplorationRepo, HexagonRepo, AtomSemanticEdgesRepo, and JunctionRepo
// against the ports declared at
//
//	internal/domain/user_knowledge_graph/ports.go
//
// Per ddd-enforcement #5/#6: soft-deleted entities are excluded from
// default reads. Per ADR-143 §2: per-user RLS isolation must be honoured
// (cross-user attempts return ErrNotFound).
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

const (
	kgTenantA = "01970000-0000-7000-8000-00000000000a"
	kgTenantB = "01970000-0000-7000-8000-00000000000b"
	kgUserA   = "01970000-0000-7000-9000-00000000000a"
	kgUserB   = "01970000-0000-7000-9000-00000000000b"
	kgAtomA   = "01970000-0000-7000-a000-00000000000a"
	kgAtomB   = "01970000-0000-7000-a000-00000000000b"
	kgAtomC   = "01970000-0000-7000-a000-00000000000c"
	kgAtomD   = "01970000-0000-7000-a000-00000000000d"
	kgAtomE   = "01970000-0000-7000-a000-00000000000e"
	kgAtomF   = "01970000-0000-7000-a000-00000000000f"
	kgAtomG   = "01970000-0000-7000-a000-000000000010"
)

// ---------- MapClusterRepo ----------

func TestMapClusterRepo_SaveAndLoad(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()

	c, err := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.Load(ctx, c.ClusterID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ClusterID != c.ClusterID {
		t.Errorf("ClusterID = %q, want %q", got.ClusterID, c.ClusterID)
	}
	if got.SeedTopic != "agile" {
		t.Errorf("SeedTopic = %q, want %q", got.SeedTopic, "agile")
	}
}

func TestMapClusterRepo_LoadNotFoundReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	if _, err := repo.Load(ctx, "missing-cluster-id"); err != userknowledgegraph.ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMapClusterRepo_ListActiveByUser_FiltersByTenantAndUser(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	mine, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	otherUser, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserB, "scrum", kgAtomB, "")
	otherTenant, _ := userknowledgegraph.NewMapCluster(kgTenantB, kgUserA, "kanban", kgAtomC, "")
	_ = repo.Save(ctx, mine)
	_ = repo.Save(ctx, otherUser)
	_ = repo.Save(ctx, otherTenant)

	got, err := repo.ListActiveByUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ClusterID != mine.ClusterID {
		t.Errorf("ClusterID = %q, want %q", got[0].ClusterID, mine.ClusterID)
	}
}

func TestMapClusterRepo_ListActiveByUser_ExcludesArchived(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	_ = repo.Save(ctx, c)
	if err := c.Archive(); err != nil {
		t.Fatal(err)
	}
	_ = repo.Save(ctx, c)

	got, _ := repo.ListActiveByUser(ctx, kgTenantA, kgUserA)
	if len(got) != 0 {
		t.Errorf("archived cluster leaked into ListActiveByUser: %d items", len(got))
	}
}

func TestMapClusterRepo_CountActiveByUser(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	c1, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	c2, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "scrum", kgAtomB, "")
	_ = repo.Save(ctx, c1)
	_ = repo.Save(ctx, c2)

	n, err := repo.CountActiveByUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

func TestMapClusterRepo_FindBySurvivor(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	survivor, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	_ = repo.Save(ctx, survivor)
	merged, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "scrum", kgAtomB, "")
	if err := merged.MergeInto(survivor.ClusterID, kgAtomB); err != nil {
		t.Fatalf("merge: %v", err)
	}
	_ = repo.Save(ctx, merged)

	got, err := repo.FindBySurvivor(ctx, kgTenantA, survivor.ClusterID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ClusterID != merged.ClusterID {
		t.Errorf("got cluster = %q, want %q", got[0].ClusterID, merged.ClusterID)
	}
}

// ---------- ExplorationRepo ----------

func TestExplorationRepo_SaveAndLoadAndListByCluster(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	cluster, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")

	exp, err := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomA)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := repo.Save(ctx, exp); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.Load(ctx, exp.ExplorationID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ExplorationID != exp.ExplorationID {
		t.Errorf("got id = %q, want %q", got.ExplorationID, exp.ExplorationID)
	}

	list, err := repo.ListByCluster(ctx, cluster.ClusterID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
}

func TestExplorationRepo_AppendTrailHopAndLoadTrail(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	cluster, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	exp, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomA)

	if err := repo.Save(ctx, exp); err != nil {
		t.Fatal(err)
	}

	// Move focal to atom B and append the trail hop.
	if err := exp.MoveFocal(kgAtomB); err != nil {
		t.Fatal(err)
	}
	hop, err := userknowledgegraph.NewTrailHop(
		exp.ExplorationID, cluster.ClusterID, kgTenantA, kgUserA,
		kgAtomA, kgAtomB,
		userknowledgegraph.NeighborRelationExtends,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendTrailHop(ctx, exp, hop); err != nil {
		t.Fatalf("append: %v", err)
	}

	trail, err := repo.LoadTrail(ctx, exp.ExplorationID)
	if err != nil {
		t.Fatalf("load trail: %v", err)
	}
	if len(trail) != 1 {
		t.Fatalf("len = %d, want 1", len(trail))
	}
	if trail[0].FromFocalAtomID != kgAtomA || trail[0].ToFocalAtomID != kgAtomB {
		t.Errorf("hop = %+v", trail[0])
	}

	idx, err := repo.LatestStepIndex(ctx, exp.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("idx = %d, want 0", idx)
	}
}

func TestExplorationRepo_LatestStepIndex_EmptyReturnsMinusOne(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	idx, err := repo.LatestStepIndex(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Errorf("idx = %d, want -1", idx)
	}
}

// ---------- HexagonRepo ----------

// mkSixNeighbors returns 6 distinct neighbors that AVOID the focal atom
// (so caller can safely pass any of kgAtomA..kgAtomG as focal). Uses a
// pool of 8 atoms and skips the focal.
func mkSixNeighbors(focalAtom string) []userknowledgegraph.HexagonNeighbor {
	pool := []string{
		"01970000-0000-7000-a000-0000000000a1",
		"01970000-0000-7000-a000-0000000000a2",
		"01970000-0000-7000-a000-0000000000a3",
		"01970000-0000-7000-a000-0000000000a4",
		"01970000-0000-7000-a000-0000000000a5",
		"01970000-0000-7000-a000-0000000000a6",
		"01970000-0000-7000-a000-0000000000a7",
	}
	out := make([]userknowledgegraph.HexagonNeighbor, 0, 6)
	for _, atom := range pool {
		if atom == focalAtom {
			continue
		}
		out = append(out, userknowledgegraph.HexagonNeighbor{
			AtomID:     atom,
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.5,
			FogLabel:   "neighbor-" + atom,
		})
		if len(out) == 6 {
			break
		}
	}
	return out
}

func TestHexagonRepo_UpsertAndFindFresh(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, err := userknowledgegraph.NewHexagonNode(
		"cluster-1", "exp-1", kgTenantA, kgUserA, kgAtomA,
		mkSixNeighbors(kgAtomA),
		"run-1", "model-1",
	)
	if err != nil {
		t.Fatalf("new hex: %v", err)
	}
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.FindFresh(ctx, "exp-1", kgAtomA)
	if err != nil {
		t.Fatalf("findfresh: %v", err)
	}
	if got.HexNodeID != hex.HexNodeID {
		t.Errorf("HexNodeID = %q, want %q", got.HexNodeID, hex.HexNodeID)
	}
}

func TestHexagonRepo_FindFresh_StaleReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode(
		"cluster-1", "exp-1", kgTenantA, kgUserA, kgAtomA,
		mkSixNeighbors(kgAtomA),
		"run-1", "model-1",
	)
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}
	if err := hex.Invalidate(userknowledgegraph.FogInvalidationReasonAtomPublished); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindFresh(ctx, "exp-1", kgAtomA); err != userknowledgegraph.ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHexagonRepo_Find_StaleReturnsHexagon(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode(
		"cluster-1", "exp-1", kgTenantA, kgUserA, kgAtomA,
		mkSixNeighbors(kgAtomA),
		"run-1", "model-1",
	)
	_ = repo.Upsert(ctx, hex)
	_ = hex.Invalidate(userknowledgegraph.FogInvalidationReasonAtomPublished)
	_ = repo.Upsert(ctx, hex)

	got, err := repo.Find(ctx, "exp-1", kgAtomA)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.HexNodeID != hex.HexNodeID {
		t.Errorf("HexNodeID = %q, want %q", got.HexNodeID, hex.HexNodeID)
	}
}

// TestHexagonRepo_FindAllFocalsByUser_ExcludesCurrentClusterAndOtherUsers — the
// JUNCTION DETECTION key. Returns the distinct focal_atom_ids that are
// focal in any HexagonNode of the (tenant, user) — across ALL active
// clusters EXCEPT excludeClusterID.
func TestHexagonRepo_FindAllFocalsByUser_ExcludesCurrentClusterAndOtherUsers(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	// Cluster A focal atoms: A, B
	hexA1, _ := userknowledgegraph.NewHexagonNode("cluster-A", "exp-A", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r1", "m1")
	hexA2, _ := userknowledgegraph.NewHexagonNode("cluster-A", "exp-A", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r1", "m1")
	// Cluster B focal atoms: C
	hexB1, _ := userknowledgegraph.NewHexagonNode("cluster-B", "exp-B", kgTenantA, kgUserA, kgAtomC, mkSixNeighbors(kgAtomC), "r1", "m1")
	// Cluster C from another user — must NOT be returned
	hexC1, _ := userknowledgegraph.NewHexagonNode("cluster-C", "exp-C", kgTenantA, kgUserB, kgAtomD, mkSixNeighbors(kgAtomD), "r1", "m1")

	for _, h := range []*userknowledgegraph.HexagonNode{hexA1, hexA2, hexB1, hexC1} {
		if err := repo.Upsert(ctx, h); err != nil {
			t.Fatal(err)
		}
	}

	// When exploring cluster A, we want to see focals of clusters B onwards.
	got, err := repo.FindAllFocalsByUser(ctx, kgTenantA, kgUserA, "cluster-A")
	if err != nil {
		t.Fatalf("findfocals: %v", err)
	}
	want := map[string]bool{kgAtomC: true}
	if len(got) != len(want) {
		t.Errorf("len = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for _, atom := range got {
		if !want[atom] {
			t.Errorf("unexpected atom %q in result", atom)
		}
	}
}

func TestHexagonRepo_MarkInvalidatedByFocal(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode("c", "e", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	_ = repo.Upsert(ctx, hex)

	n, err := repo.MarkInvalidatedByFocal(ctx, kgAtomA, userknowledgegraph.FogInvalidationReasonAtomPublished)
	if err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	if _, err := repo.FindFresh(ctx, "e", kgAtomA); err != userknowledgegraph.ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound after invalidation", err)
	}
}

func TestHexagonRepo_MarkInvalidatedByUser(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex1, _ := userknowledgegraph.NewHexagonNode("c1", "e1", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	hex2, _ := userknowledgegraph.NewHexagonNode("c2", "e2", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r", "m")
	hex3, _ := userknowledgegraph.NewHexagonNode("c3", "e3", kgTenantA, kgUserB, kgAtomC, mkSixNeighbors(kgAtomC), "r", "m") // other user
	_ = repo.Upsert(ctx, hex1)
	_ = repo.Upsert(ctx, hex2)
	_ = repo.Upsert(ctx, hex3)

	n, err := repo.MarkInvalidatedByUser(ctx, kgTenantA, kgUserA, userknowledgegraph.FogInvalidationReasonUserRetentionShift)
	if err != nil {
		t.Fatalf("invalidate user: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	// other user untouched
	if _, err := repo.FindFresh(ctx, "e3", kgAtomC); err != nil {
		t.Errorf("other-user hexagon was invalidated: %v", err)
	}
}

func TestMapClusterRepo_ListAllByUser_IncludesArchived(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	active, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	archived, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "scrum", kgAtomB, "")
	_ = archived.Archive()
	_ = repo.Save(ctx, active)
	_ = repo.Save(ctx, archived)

	got, err := repo.ListAllByUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (active + archived)", len(got))
	}
}

func TestMapClusterRepo_SaveNilReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	if err := repo.Save(ctx, nil); err == nil {
		t.Error("expected error on nil cluster")
	}
}

func TestExplorationRepo_SaveNilReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	if err := repo.Save(ctx, nil); err == nil {
		t.Error("expected error on nil exploration")
	}
}

func TestExplorationRepo_AppendTrailHopNilFails(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	if err := repo.AppendTrailHop(ctx, nil, nil); err == nil {
		t.Error("expected error on nil exploration/hop")
	}
}

func TestHexagonRepo_FindInvalidatedOlderThan(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode("c", "e", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	_ = repo.Upsert(ctx, hex)
	_ = hex.Invalidate(userknowledgegraph.FogInvalidationReasonAtomPublished)
	_ = repo.Upsert(ctx, hex)

	cutoff := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	got, err := repo.FindInvalidatedOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

func TestHexagonRepo_FindInvalidatedOlderThan_BadFormat(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	if _, err := repo.FindInvalidatedOlderThan(ctx, "not-a-time"); err == nil {
		t.Error("expected error on bad time format")
	}
}

// ---------- AtomSemanticEdgesRepo ----------

func TestAtomSemanticEdgesRepo_ListByTarget(t *testing.T) {
	ctx := context.Background()
	repo := NewAtomSemanticEdgesRepo()
	now := time.Now().UTC()
	edge, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomA, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	_ = repo.Save(ctx, edge)

	got, err := repo.ListByTarget(ctx, kgTenantA, kgAtomB)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].SourceAtomID != kgAtomA {
		t.Errorf("source = %q, want %q", got[0].SourceAtomID, kgAtomA)
	}
}

func TestJunctionRepo_SaveNilFails(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	if err := repo.Save(ctx, nil); err == nil {
		t.Error("expected error on nil junction")
	}
}

func TestAtomSemanticEdgesRepo_SaveAndListBySource(t *testing.T) {
	ctx := context.Background()
	repo := NewAtomSemanticEdgesRepo()
	now := time.Now().UTC()
	edge := &userknowledgegraph.AtomSemanticEdge{
		EdgeID:       "01970000-0000-7000-c000-000000000001",
		TenantID:     kgTenantA,
		SourceAtomID: kgAtomA,
		TargetAtomID: kgAtomB,
		EdgeType:     userknowledgegraph.AtomEdgeTypeExtends,
		Weight:       0.7,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Save(ctx, edge); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.ListBySource(ctx, kgTenantA, kgAtomA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].TargetAtomID != kgAtomB {
		t.Errorf("target = %q, want %q", got[0].TargetAtomID, kgAtomB)
	}
}

func TestAtomSemanticEdgesRepo_FiltersByTenant(t *testing.T) {
	ctx := context.Background()
	repo := NewAtomSemanticEdgesRepo()
	now := time.Now().UTC()
	edgeA := &userknowledgegraph.AtomSemanticEdge{
		EdgeID:       "01970000-0000-7000-c000-000000000001",
		TenantID:     kgTenantA,
		SourceAtomID: kgAtomA, TargetAtomID: kgAtomB,
		EdgeType:  userknowledgegraph.AtomEdgeTypeExtends,
		CreatedAt: now, UpdatedAt: now,
	}
	edgeB := &userknowledgegraph.AtomSemanticEdge{
		EdgeID:       "01970000-0000-7000-c000-000000000002",
		TenantID:     kgTenantB,
		SourceAtomID: kgAtomA, TargetAtomID: kgAtomC,
		EdgeType:  userknowledgegraph.AtomEdgeTypeExtends,
		CreatedAt: now, UpdatedAt: now,
	}
	_ = repo.Save(ctx, edgeA)
	_ = repo.Save(ctx, edgeB)

	got, _ := repo.ListBySource(ctx, kgTenantA, kgAtomA)
	if len(got) != 1 {
		t.Fatalf("tenant cross-leak: len = %d, want 1", len(got))
	}
	if got[0].EdgeID != edgeA.EdgeID {
		t.Errorf("got edge = %q, want %q", got[0].EdgeID, edgeA.EdgeID)
	}
}

// ---------- JunctionRepo ----------

func TestJunctionRepo_SaveAndLoad(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()
	j := &userknowledgegraph.Junction{
		JunctionID:     "01970000-0000-7000-d000-000000000001",
		TenantID:       kgTenantA,
		UserGCID:       kgUserA,
		ClusterAID:     "cluster-a",
		ClusterBID:     "cluster-b",
		OverlapAtomIDs: []string{kgAtomA, kgAtomB, kgAtomC},
		Status:         userknowledgegraph.JunctionStatusPending,
		DetectedAt:     now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.Save(ctx, j); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.Load(ctx, j.JunctionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.JunctionID != j.JunctionID {
		t.Errorf("got = %q, want %q", got.JunctionID, j.JunctionID)
	}
	if got.Status != userknowledgegraph.JunctionStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestJunctionRepo_ListPendingForUser(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()
	mine := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000001",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	other := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000002",
		TenantID:   kgTenantA, UserGCID: kgUserB,
		ClusterAID: "cc", ClusterBID: "cd",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	mineAccepted := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000003",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ce", ClusterBID: "cf",
		Status:     userknowledgegraph.JunctionStatusAccepted,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	_ = repo.Save(ctx, mine)
	_ = repo.Save(ctx, other)
	_ = repo.Save(ctx, mineAccepted)

	got, err := repo.ListPendingForUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].JunctionID != mine.JunctionID {
		t.Errorf("got = %q, want %q", got[0].JunctionID, mine.JunctionID)
	}
}

func TestJunctionRepo_FindPendingByPair(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()
	j := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000001",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	_ = repo.Save(ctx, j)

	// Same pair (in either order) should return the existing junction.
	got, err := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "ca", "cb")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got == nil || got.JunctionID != j.JunctionID {
		t.Errorf("got = %v", got)
	}
	got, err = repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "cb", "ca")
	if err != nil {
		t.Fatalf("find swap: %v", err)
	}
	if got == nil || got.JunctionID != j.JunctionID {
		t.Errorf("got swap = %v", got)
	}
}

func TestJunctionRepo_FindPendingByPair_AbsentReturnsNil(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	got, err := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "ca", "cb")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got = %v", got)
	}
}
