// learner_weakness_context_test.go: CHO-2328 / ADR-247 F2 weakness-load
// read helper (LoadWeaknessContextByConceptKey). Verifies the descriptor ->
// WeaknessContext distillation, the EXPLICIT-source + soft-delete + learner
// scoping, and the (nil, nil) no-match degrade. Reuses the lwStub* harness +
// lwFullRow/lwNow from learner_weakness_test.go and withCtx/pgTenantID/
// pgUserGCID/contains from user_kg_test.go (no live pgvector).
package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-common/rls"
)

// ---- populated context: an explicit diagnosed weakness exists ----

func TestLearnerWeaknessRepo_LoadWeaknessContextByConceptKey_ReturnsPopulated(t *testing.T) {
	desc := `{"summary":"confuses fluvial vs pluvial flooding",` +
		`"misconceptions":["all flooding is rainfall","dams stop all floods"],` +
		`"sample_wrong":[{"prompt":"What causes fluvial flooding?",` +
		`"chosen":"Heavy local rain","why_wrong":"river channel overflow, not local rain"}]}`
	stub := &lwStubQuerier{row: &lwStubRow{vals: lwFullRow(
		"edge-1", "riverine-flooding", "Riverine Flooding", "physical-geography", "",
		[]string{"flooding"}, 0.8, []string{"explicit", "derived"}, desc, []string{}, "active",
		lwNow(), lwNow(),
	)}}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	got, err := repo.LoadWeaknessContextByConceptKey(withCtx(), pgTenantID, pgUserGCID, "Riverine Flooding")
	if err != nil {
		t.Fatalf("LoadWeaknessContextByConceptKey: %v", err)
	}
	if got == nil {
		t.Fatal("got nil; want a populated WeaknessContext")
	}
	if got.Descriptor != "confuses fluvial vs pluvial flooding" {
		t.Errorf("Descriptor = %q; want the descriptor summary", got.Descriptor)
	}
	if len(got.Misconceptions) != 2 || got.Misconceptions[0] != "all flooding is rainfall" {
		t.Errorf("Misconceptions = %v; want the two decoded misconceptions", got.Misconceptions)
	}
	if len(got.Evidence) != 1 || !contains(got.Evidence[0], "river channel overflow") {
		t.Errorf("Evidence = %v; want the flattened sample_wrong exemplar", got.Evidence)
	}
	// RLS SET LOCAL pair must run BEFORE the load (multi-tenant-rls contract).
	if len(stub.execCalls) < 2 ||
		!contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") ||
		!contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS SET LOCAL pair missing/misordered; execs=%v", stub.execCalls)
	}
	// Exactly one concept-key QueryRow issued.
	if len(stub.rowCalls) != 1 || !contains(stub.rowCalls[0], "concept_key = $2") {
		t.Errorf("expected one concept_key QueryRow; rowCalls=%v", stub.rowCalls)
	}
}

// ---- no explicit weakness for that concept_key -> (nil, nil) ----

func TestLearnerWeaknessRepo_LoadWeaknessContextByConceptKey_NotFound(t *testing.T) {
	tx := &lwStubTxRunner{} // default QueryRow -> ErrNoRows
	repo := NewLearnerWeaknessRepo(tx)

	got, err := repo.LoadWeaknessContextByConceptKey(withCtx(), pgTenantID, pgUserGCID, "unseen-concept")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v; want nil (no explicit weakness => clean degrade)", got)
	}
}

// ---- scoping: learner + soft-delete + EXPLICIT source (SQL contract) +
//      tenant (RLS fail-loud) ----

func TestLearnerWeaknessRepo_LoadWeaknessContextByConceptKey_ScopesLearnerSoftDeleteExplicitTenant(t *testing.T) {
	// A different learner, a soft-deleted row, or a non-explicit (derived /
	// classroom / ceremony-only) edge must NOT match, enforced in the SQL.
	for _, frag := range []string{
		"learner_gcid = $1", "concept_key = $2", "deleted_at IS NULL", "'explicit' = ANY(sources)",
	} {
		if !contains(getExplicitWeaknessByConceptKeySQL, frag) {
			t.Errorf("scoping predicate %q missing from:\n%s", frag, getExplicitWeaknessByConceptKeySQL)
		}
	}
	// Tenant isolation is RLS-enforced: with no tenant on ctx the read fails loud.
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{})
	if _, err := repo.LoadWeaknessContextByConceptKey(
		context.Background(), pgTenantID, pgUserGCID, "riverine-flooding",
	); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext (RLS tenant scoping)", err)
	}
}

// ---- blank/degenerate concept_key degrades to (nil, nil) without a tx ----

func TestLearnerWeaknessRepo_LoadWeaknessContextByConceptKey_BlankKey(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)

	got, err := repo.LoadWeaknessContextByConceptKey(withCtx(), pgTenantID, pgUserGCID, "  !!  ")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Errorf("blank concept_key -> %+v; want nil", got)
	}
	if tx.q != nil {
		t.Error("blank concept_key must not open a tx")
	}
}

// ---- a real (non-ErrNoRows) scan error propagates ----

func TestLearnerWeaknessRepo_LoadWeaknessContextByConceptKey_PropagatesScanError(t *testing.T) {
	stub := &lwStubQuerier{row: &lwStubRow{err: errors.New("scan boom")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})

	if _, err := repo.LoadWeaknessContextByConceptKey(
		withCtx(), pgTenantID, pgUserGCID, "riverine-flooding",
	); err == nil {
		t.Error("expected non-ErrNoRows scan error to propagate")
	}
}
