// user_kg_cover_test.go — coverage for the per-user Knowledge Graph
// in-memory adapter branches not exercised by user_kg_test.go: the
// soft-deleted / wrong-tenant / wrong-status negative-filter paths and
// HasFocalInCluster (the in-memory junction-attribution probe).
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// softDelete stamps DeletedAt so a row is excluded from default reads.
func softDelete(deletedAt **time.Time) {
	now := time.Now().UTC()
	*deletedAt = &now
}

// ---------- nil-guard defensive contracts ----------

// TestRepos_NilArgReturnsErrNotFound covers the `if x == nil` guard on the
// write methods whose contract is to reject a nil aggregate with
// ErrNotFound (rather than panic on the dereference that follows).
func TestRepos_NilArgReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	if err := NewHexagonRepo().Upsert(ctx, nil); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Hexagon.Upsert(nil) = %v, want ErrNotFound", err)
	}
	if err := NewAtomSemanticEdgesRepo().Save(ctx, nil); err != userknowledgegraph.ErrNotFound {
		t.Errorf("AtomSemanticEdges.Save(nil) = %v, want ErrNotFound", err)
	}
	if err := NewJunctionRepo().Save(ctx, nil); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Junction.Save(nil) = %v, want ErrNotFound", err)
	}
	if err := NewExplorationRepo().Save(ctx, nil); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Exploration.Save(nil) = %v, want ErrNotFound", err)
	}
	if err := NewMapClusterRepo().Save(ctx, nil); err != userknowledgegraph.ErrNotFound {
		t.Errorf("MapCluster.Save(nil) = %v, want ErrNotFound", err)
	}
}

// ---------- HasFocalInCluster (0% → covered) ----------

func TestHexagonRepo_HasFocalInCluster(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode(
		"cluster-A", "exp-A", kgTenantA, kgUserA, kgAtomA,
		mkSixNeighbors(kgAtomA), "r", "m")
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}

	if ok, _ := repo.HasFocalInCluster(ctx, "cluster-A", kgAtomA); !ok {
		t.Error("HasFocalInCluster(cluster-A, atomA) = false, want true")
	}
	// Wrong cluster → false.
	if ok, _ := repo.HasFocalInCluster(ctx, "cluster-B", kgAtomA); ok {
		t.Error("HasFocalInCluster(cluster-B, atomA) = true, want false")
	}
	// Wrong focal → false.
	if ok, _ := repo.HasFocalInCluster(ctx, "cluster-A", kgAtomB); ok {
		t.Error("HasFocalInCluster(cluster-A, atomB) = true, want false")
	}
}

func TestHexagonRepo_HasFocalInCluster_ExcludesSoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode(
		"cluster-A", "exp-A", kgTenantA, kgUserA, kgAtomA,
		mkSixNeighbors(kgAtomA), "r", "m")
	softDelete(&hex.DeletedAt)
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}
	if ok, _ := repo.HasFocalInCluster(ctx, "cluster-A", kgAtomA); ok {
		t.Error("soft-deleted hexagon counted by HasFocalInCluster")
	}
}

// ---------- MapClusterRepo negative-filter branches ----------

// TestMapClusterRepo_QueriesExcludeSoftDeleted covers the DeletedAt != nil
// continue branch across ListActiveByUser / ListAllByUser / FindBySurvivor
// / Load (a soft-deleted cluster is invisible to every read).
func TestMapClusterRepo_QueriesExcludeSoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	c, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "agile", kgAtomA, "")
	softDelete(&c.DeletedAt)
	if err := repo.Save(ctx, c); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Load(ctx, c.ClusterID); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Load err = %v, want ErrNotFound", err)
	}
	if got, _ := repo.ListActiveByUser(ctx, kgTenantA, kgUserA); len(got) != 0 {
		t.Errorf("ListActiveByUser leaked soft-deleted: %d", len(got))
	}
	if got, _ := repo.ListAllByUser(ctx, kgTenantA, kgUserA); len(got) != 0 {
		t.Errorf("ListAllByUser leaked soft-deleted: %d", len(got))
	}
	if got, _ := repo.FindBySurvivor(ctx, kgTenantA, c.ClusterID); len(got) != 0 {
		t.Errorf("FindBySurvivor leaked soft-deleted: %d", len(got))
	}
}

// TestMapClusterRepo_ListAllByUser_FiltersTenantAndUser covers the
// (TenantID/UserGCID mismatch) continue in ListAllByUser.
func TestMapClusterRepo_ListAllByUser_FiltersTenantAndUser(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	mine, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "a", kgAtomA, "")
	otherUser, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserB, "b", kgAtomB, "")
	otherTenant, _ := userknowledgegraph.NewMapCluster(kgTenantB, kgUserA, "c", kgAtomC, "")
	_ = repo.Save(ctx, mine)
	_ = repo.Save(ctx, otherUser)
	_ = repo.Save(ctx, otherTenant)

	got, err := repo.ListAllByUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ClusterID != mine.ClusterID {
		t.Errorf("ListAllByUser = %d items, want only %q", len(got), mine.ClusterID)
	}
}

// TestMapClusterRepo_CountActiveByUser_Zero covers the count path with no
// active clusters (the early-return-on-empty branch).
func TestMapClusterRepo_CountActiveByUser_Zero(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	n, err := repo.CountActiveByUser(ctx, kgTenantA, kgUserA)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

// TestMapClusterRepo_FindBySurvivor_WrongTenant covers the tenant-mismatch
// continue in FindBySurvivor.
func TestMapClusterRepo_FindBySurvivor_WrongTenant(t *testing.T) {
	ctx := context.Background()
	repo := NewMapClusterRepo()
	survivor, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "a", kgAtomA, "")
	_ = repo.Save(ctx, survivor)
	merged, _ := userknowledgegraph.NewMapCluster(kgTenantB, kgUserA, "b", kgAtomB, "")
	_ = merged.MergeInto(survivor.ClusterID, kgAtomB)
	_ = repo.Save(ctx, merged)

	// Query in tenant A — the merged cluster lives in tenant B, so it is
	// filtered out by the tenant predicate.
	got, err := repo.FindBySurvivor(ctx, kgTenantA, survivor.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("FindBySurvivor crossed tenant: %d items", len(got))
	}
}

// ---------- ExplorationRepo negative-filter branches ----------

func TestExplorationRepo_LoadAndListExcludeSoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	cluster, _ := userknowledgegraph.NewMapCluster(kgTenantA, kgUserA, "a", kgAtomA, "")
	exp, _ := userknowledgegraph.NewExploration(cluster.ClusterID, kgTenantA, kgUserA, kgAtomA)
	softDelete(&exp.DeletedAt)
	if err := repo.Save(ctx, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load(ctx, exp.ExplorationID); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Load err = %v, want ErrNotFound", err)
	}
	if got, _ := repo.ListByCluster(ctx, cluster.ClusterID); len(got) != 0 {
		t.Errorf("ListByCluster leaked soft-deleted: %d", len(got))
	}
}

// TestExplorationRepo_ListByCluster_WrongCluster covers the ClusterID
// mismatch continue.
func TestExplorationRepo_ListByCluster_WrongCluster(t *testing.T) {
	ctx := context.Background()
	repo := NewExplorationRepo()
	exp, _ := userknowledgegraph.NewExploration("cluster-X", kgTenantA, kgUserA, kgAtomA)
	_ = repo.Save(ctx, exp)
	if got, _ := repo.ListByCluster(ctx, "cluster-Y"); len(got) != 0 {
		t.Errorf("ListByCluster(other) = %d, want 0", len(got))
	}
}

// ---------- HexagonRepo negative-filter branches ----------

func TestHexagonRepo_FindAndFindFresh_SoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	hex, _ := userknowledgegraph.NewHexagonNode("c", "e", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	softDelete(&hex.DeletedAt)
	if err := repo.Upsert(ctx, hex); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Find(ctx, "e", kgAtomA); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Find soft-deleted err = %v, want ErrNotFound", err)
	}
	if _, err := repo.FindFresh(ctx, "e", kgAtomA); err != userknowledgegraph.ErrNotFound {
		t.Errorf("FindFresh soft-deleted err = %v, want ErrNotFound", err)
	}
}

// TestHexagonRepo_FindAllFocalsByUser_SkipsSoftDeletedAndOtherTenant covers
// the DeletedAt + tenant/user mismatch continues in the junction-detection
// key. Invalidated rows still count (per ADR-143 §6 in the source).
func TestHexagonRepo_FindAllFocalsByUser_SkipsSoftDeletedAndOtherTenant(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	live, _ := userknowledgegraph.NewHexagonNode("cluster-B", "expB", kgTenantA, kgUserA, kgAtomC, mkSixNeighbors(kgAtomC), "r", "m")
	// Invalidated but live — still counted.
	invalidated, _ := userknowledgegraph.NewHexagonNode("cluster-B", "expB", kgTenantA, kgUserA, kgAtomD, mkSixNeighbors(kgAtomD), "r", "m")
	_ = invalidated.Invalidate(userknowledgegraph.FogInvalidationReasonAtomPublished)
	// Soft-deleted → skipped.
	deleted, _ := userknowledgegraph.NewHexagonNode("cluster-B", "expB", kgTenantA, kgUserA, kgAtomE, mkSixNeighbors(kgAtomE), "r", "m")
	softDelete(&deleted.DeletedAt)
	// Other tenant → skipped.
	otherTenant, _ := userknowledgegraph.NewHexagonNode("cluster-B", "expB", kgTenantB, kgUserA, kgAtomF, mkSixNeighbors(kgAtomF), "r", "m")
	for _, h := range []*userknowledgegraph.HexagonNode{live, invalidated, deleted, otherTenant} {
		if err := repo.Upsert(ctx, h); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.FindAllFocalsByUser(ctx, kgTenantA, kgUserA, "cluster-A")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{kgAtomC: true, kgAtomD: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want focals %v", got, want)
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected focal %q", a)
		}
	}
}

// TestHexagonRepo_MarkInvalidatedByFocal_MatchesNeighbor covers the
// neighbor-scan match branch + the no-match continue + soft-deleted skip.
func TestHexagonRepo_MarkInvalidatedByFocal_MatchesNeighbor(t *testing.T) {
	ctx := context.Background()
	repo := NewHexagonRepo()
	// hexNeighbor's focal is kgAtomA, but one of its neighbors is the pool
	// atom a1; invalidating by that neighbor atom must hit this hex.
	const neighborAtom = "01970000-0000-7000-a000-0000000000a1"
	hexNeighbor, _ := userknowledgegraph.NewHexagonNode("c1", "e1", kgTenantA, kgUserA, kgAtomA, mkSixNeighbors(kgAtomA), "r", "m")
	// hexUnrelated has neither focal nor neighbor == neighborAtom target.
	hexUnrelated, _ := userknowledgegraph.NewHexagonNode("c2", "e2", kgTenantA, kgUserA, kgAtomB, mkSixNeighbors(kgAtomB), "r", "m")
	// soft-deleted — must be skipped.
	hexDeleted, _ := userknowledgegraph.NewHexagonNode("c3", "e3", kgTenantA, kgUserA, neighborAtom, mkSixNeighbors(neighborAtom), "r", "m")
	softDelete(&hexDeleted.DeletedAt)
	for _, h := range []*userknowledgegraph.HexagonNode{hexNeighbor, hexUnrelated, hexDeleted} {
		_ = repo.Upsert(ctx, h)
	}

	// mkSixNeighbors for kgAtomA includes neighborAtom (a1); for kgAtomB it
	// also includes a1 (the pool starts at a1 and B != a1). So both
	// hexNeighbor and hexUnrelated carry a1 as a neighbor → 2 invalidated.
	n, err := repo.MarkInvalidatedByFocal(ctx, neighborAtom, userknowledgegraph.FogInvalidationReasonAtomPublished)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("invalidated count = %d, want 2 (both carry a1 as neighbor)", n)
	}
}

// ---------- AtomSemanticEdgesRepo negative-filter branches ----------

func TestAtomSemanticEdgesRepo_QueriesExcludeSoftDeletedAndWrongAtom(t *testing.T) {
	ctx := context.Background()
	repo := NewAtomSemanticEdgesRepo()
	now := time.Now().UTC()
	// Live edge A→B.
	live, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomA, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	// Soft-deleted edge A→C.
	deleted, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomA, kgAtomC, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	deleted.SoftDelete(now)
	// Different source D→B (so ListBySource(A) skips it; ListByTarget(B) includes it as live).
	otherSource, _ := userknowledgegraph.NewAtomSemanticEdge(kgTenantA, kgAtomD, kgAtomB, userknowledgegraph.AtomEdgeTypeExtends, 0.5, now)
	for _, e := range []*userknowledgegraph.AtomSemanticEdge{live, deleted, otherSource} {
		if err := repo.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	// ListBySource(A): live only (deleted skipped, otherSource has source D).
	bySrc, _ := repo.ListBySource(ctx, kgTenantA, kgAtomA)
	if len(bySrc) != 1 || bySrc[0].TargetAtomID != kgAtomB {
		t.Errorf("ListBySource(A) = %d items, want 1 (A→B)", len(bySrc))
	}

	// ListByTarget(B): live (A→B) + otherSource (D→B) = 2.
	byTgt, _ := repo.ListByTarget(ctx, kgTenantA, kgAtomB)
	if len(byTgt) != 2 {
		t.Errorf("ListByTarget(B) = %d items, want 2", len(byTgt))
	}

	// ListByTarget for an atom with no inbound edges → empty.
	none, _ := repo.ListByTarget(ctx, kgTenantA, kgAtomG)
	if len(none) != 0 {
		t.Errorf("ListByTarget(G) = %d, want 0", len(none))
	}
}

// ---------- JunctionRepo negative-filter branches ----------

func TestJunctionRepo_Load_SoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()
	j := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-000000000009",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	softDelete(&j.DeletedAt)
	_ = repo.Save(ctx, j)
	if _, err := repo.Load(ctx, j.JunctionID); err != userknowledgegraph.ErrNotFound {
		t.Errorf("Load soft-deleted err = %v, want ErrNotFound", err)
	}
}

// TestJunctionRepo_FindPendingByPair_NegativeFilters covers the
// negative-filter branches not exercised by the existing happy-path test:
// wrong-status skip, wrong-user skip, soft-deleted skip, plus the forward
// + reverse match and the no-match (nil, nil) return.
func TestJunctionRepo_FindPendingByPair_NegativeFilters(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()

	pending := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-00000000000a",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	accepted := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-00000000000b",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "cx", ClusterBID: "cy",
		Status:     userknowledgegraph.JunctionStatusAccepted,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	otherUser := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-00000000000c",
		TenantID:   kgTenantA, UserGCID: kgUserB,
		ClusterAID: "cm", ClusterBID: "cn",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	deleted := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-00000000000d",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "cp", ClusterBID: "cq",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	softDelete(&deleted.DeletedAt)
	for _, j := range []*userknowledgegraph.Junction{pending, accepted, otherUser, deleted} {
		_ = repo.Save(ctx, j)
	}

	// Forward order.
	got, err := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "ca", "cb")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.JunctionID != pending.JunctionID {
		t.Fatalf("forward match = %v, want %q", got, pending.JunctionID)
	}
	// Reverse order — pair order does NOT matter.
	rev, err := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "cb", "ca")
	if err != nil {
		t.Fatal(err)
	}
	if rev == nil || rev.JunctionID != pending.JunctionID {
		t.Fatalf("reverse match = %v, want %q", rev, pending.JunctionID)
	}
	// Accepted pair → not found (status skip).
	if nf, _ := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "cx", "cy"); nf != nil {
		t.Errorf("accepted pair returned %v, want nil", nf)
	}
	// Other user's pending pair → not found for this user.
	if nf, _ := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "cm", "cn"); nf != nil {
		t.Errorf("other-user pair returned %v, want nil", nf)
	}
	// Soft-deleted pair → not found.
	if nf, _ := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "cp", "cq"); nf != nil {
		t.Errorf("soft-deleted pair returned %v, want nil", nf)
	}
	// Entirely unknown pair → (nil, nil).
	if nf, err := repo.FindPendingByPair(ctx, kgTenantA, kgUserA, "zz", "zz"); err != nil || nf != nil {
		t.Errorf("unknown pair = (%v, %v), want (nil, nil)", nf, err)
	}
}

// TestJunctionRepo_ListPendingForUser_ExcludesSoftDeleted covers the
// DeletedAt continue in ListPendingForUser.
func TestJunctionRepo_ListPendingForUser_ExcludesSoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := NewJunctionRepo()
	now := time.Now().UTC()
	j := &userknowledgegraph.Junction{
		JunctionID: "01970000-0000-7000-d000-00000000000e",
		TenantID:   kgTenantA, UserGCID: kgUserA,
		ClusterAID: "ca", ClusterBID: "cb",
		Status:     userknowledgegraph.JunctionStatusPending,
		DetectedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	softDelete(&j.DeletedAt)
	_ = repo.Save(ctx, j)
	if got, _ := repo.ListPendingForUser(ctx, kgTenantA, kgUserA); len(got) != 0 {
		t.Errorf("ListPendingForUser leaked soft-deleted: %d", len(got))
	}
}
