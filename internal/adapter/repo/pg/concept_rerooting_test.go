// concept_rerooting_test.go — RLS-contract + SQL-shape verification for the
// ConceptReRootRepo applier (ADR-212 WS-2). Reuses the shared pg stub harness
// (stubTxRunner / stubTxRunnerWith / goalFakeQuerier / withCtx / contains /
// pgTenantID / pgUserGCID). Asserts a ReRootPlan is applied atomically: RLS pair
// first, then Update (soft-delete) the superseded edges, then Insert the flipped
// ones — reusing the WS-1 concept_edges SQL.
package pg

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

func rerootPlanFixture() conceptgraph.ReRootPlan {
	now := time.Date(2026, 7, 1, 15, 4, 5, 0, time.UTC)
	del := now
	// Superseded parent->child edge (already tombstoned by the domain plan).
	superseded := conceptgraph.Edge{
		EdgeID:          "01970000-0000-7000-e000-000000000001",
		TenantID:        pgTenantID,
		LearnerGCID:     pgUserGCID,
		SourceConceptID: "01970000-0000-7000-c000-000000000001", // parent
		TargetConceptID: "01970000-0000-7000-c000-000000000002", // child
		Class:           conceptgraph.EdgeClassHierarchy,
		Provenance:      conceptgraph.ProvenanceLearnerAuthored,
		CreatedAt:       now,
		UpdatedAt:       now,
		DeletedAt:       &del,
	}
	// Flipped child->parent edge (fresh row).
	flipped := conceptgraph.Edge{
		EdgeID:          "01970000-0000-7000-e000-000000000002",
		TenantID:        pgTenantID,
		LearnerGCID:     pgUserGCID,
		SourceConceptID: "01970000-0000-7000-c000-000000000002", // child now parent
		TargetConceptID: "01970000-0000-7000-c000-000000000001",
		Class:           conceptgraph.EdgeClassHierarchy,
		Provenance:      conceptgraph.ProvenanceLearnerAuthored,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return conceptgraph.ReRootPlan{
		NewRootID:    "01970000-0000-7000-c000-000000000002",
		ToSoftDelete: []conceptgraph.Edge{superseded},
		ToCreate:     []conceptgraph.Edge{flipped},
	}
}

func TestPGConceptReRoot_AppliesRLSThenUpdateThenInsert(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewConceptReRootRepo(tx)
	if err := repo.Apply(withCtx(), rerootPlanFixture()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 4 {
		t.Fatalf("want RLS pair + update + insert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", calls[0])
	}
	if !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q; want SET LOCAL gcid", calls[1])
	}
	// Soft-delete (UPDATE) must precede create (INSERT), and both after RLS.
	upIdx, inIdx := -1, -1
	for i, c := range calls {
		if upIdx == -1 && contains(c, "UPDATE concept_edges") {
			upIdx = i
		}
		if inIdx == -1 && contains(c, "INSERT INTO concept_edges") {
			inIdx = i
		}
	}
	if upIdx < 2 {
		t.Errorf("UPDATE concept_edges not found after RLS: %v", calls)
	}
	if inIdx < 2 {
		t.Errorf("INSERT INTO concept_edges not found after RLS: %v", calls)
	}
	if upIdx >= 0 && inIdx >= 0 && upIdx > inIdx {
		t.Errorf("soft-delete must precede create; update@%d insert@%d", upIdx, inIdx)
	}
}

func TestPGConceptReRoot_NoOpTouchesNothing(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewConceptReRootRepo(tx)
	if err := repo.Apply(withCtx(), conceptgraph.ReRootPlan{NewRootID: "x"}); err != nil {
		t.Fatalf("Apply(no-op): %v", err)
	}
	// A no-op plan short-circuits before RunInTx — the DB is never touched.
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("no-op plan should not touch the DB: %v", tx.q.execCalls)
	}
}

func TestPGConceptReRoot_MissingTenantContextFailsLoud(t *testing.T) {
	repo := NewConceptReRootRepo(&stubTxRunner{})
	err := repo.Apply(context.Background(), rerootPlanFixture())
	if err == nil {
		t.Fatal("want ErrNoTenantContext; got nil")
	}
	if err != rls.ErrNoTenantContext {
		t.Errorf("err = %v; want rls.ErrNoTenantContext", err)
	}
}

func TestPGConceptReRoot_ErrorBubbles(t *testing.T) {
	boom := context.DeadlineExceeded
	// goalFakeQuerier lets the RLS SET LOCALs through, then fails the mutation.
	repo := NewConceptReRootRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: boom}})
	if err := repo.Apply(withCtx(), rerootPlanFixture()); err == nil {
		t.Fatal("want the update/insert error to bubble")
	}
}

func TestPGConceptReRoot_InsertErrorBubbles(t *testing.T) {
	boom := context.DeadlineExceeded
	// Create-only plan (no soft-deletes) so the INSERT is the first mutation to
	// fail — exercises the create-branch error return.
	plan := rerootPlanFixture()
	plan.ToSoftDelete = nil
	repo := NewConceptReRootRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: boom}})
	if err := repo.Apply(withCtx(), plan); err == nil {
		t.Fatal("want the insert error to bubble")
	}
}

func TestPGConceptReRoot_SatisfiesPort(t *testing.T) {
	var _ conceptgraph.ReRootApplier = (*ConceptReRootRepo)(nil)
}
