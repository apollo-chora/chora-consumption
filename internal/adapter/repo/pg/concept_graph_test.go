// concept_graph_test.go — RLS-contract + SQL-shape + scan-mapping verification
// for the ConceptNode + Edge pg adapters (ADR-212 WS-1). Mirrors goal_test.go:
// a stub Querier captures Exec SQL so we assert the SET LOCAL chora.tenant_id
// pair lands BEFORE the mutation; the shared goalFakeQuerier returns canned rows
// so the GetByID/List scan→domain mapping is exercised. Schema mirrors
// migrations/0056_discovery_concept_graph.up.sql.
package pg

import (
	"context"
	"testing"
	"time"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	cgAtom    = "01970000-0000-7000-a000-0000000000f1"
	cgConcept = "01970000-0000-7000-c000-0000000000f1"
	cgSource  = "01970000-0000-7000-c000-0000000000f2"
	cgTarget  = "01970000-0000-7000-c000-0000000000f3"
)

var cgNow = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func conceptFixture(t *testing.T) *conceptgraph.ConceptNode {
	t.Helper()
	c, err := conceptgraph.NewConceptNode(conceptgraph.NewConceptNodeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Title: "story-point calculation",
		AtomRefs: []string{cgAtom}, Now: cgNow,
	})
	if err != nil {
		t.Fatalf("conceptFixture: %v", err)
	}
	return c
}

func edgeFixture(t *testing.T, class conceptgraph.EdgeClass) *conceptgraph.Edge {
	t.Helper()
	e, err := conceptgraph.NewEdge(conceptgraph.NewEdgeInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		SourceConceptID: cgSource, TargetConceptID: cgTarget, Class: class, Now: cgNow,
	})
	if err != nil {
		t.Fatalf("edgeFixture: %v", err)
	}
	return e
}

// ---- ConceptNodeRepo ----

func TestPGConceptNodeRepo_CreateAppliesRLSAndInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewConceptNodeRepo(tx)
	if err := repo.Create(withCtx(), conceptFixture(t)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + insert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", calls[0])
	}
	if !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q; want SET LOCAL gcid", calls[1])
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO concept_nodes") {
		t.Errorf("last = %q; want INSERT INTO concept_nodes", last)
	}
	if !contains(last, "atom_refs") || !contains(last, "provenance") {
		t.Errorf("insert = %q; want atom_refs + provenance columns", last)
	}
	if !contains(last, "sub_goal") {
		t.Errorf("insert = %q; want sub_goal column (ADR-247 D1)", last)
	}
}

func TestPGConceptNodeRepo_UpdateScoped(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewConceptNodeRepo(tx)
	if err := repo.Update(withCtx(), conceptFixture(t)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "UPDATE concept_nodes") {
		t.Errorf("last = %q; want UPDATE concept_nodes", last)
	}
	// Defence-in-depth: scoped by id AND tenant AND learner.
	if !contains(last, "WHERE id") || !contains(last, "tenant_id") || !contains(last, "learner_gcid") {
		t.Errorf("update not scoped to id/tenant/learner: %q", last)
	}
}

func TestPGConceptNodeRepo_GetByIDScansRow(t *testing.T) {
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		cgConcept, pgTenantID, pgUserGCID, "Scrum",
		[]string{cgAtom}, "learner_authored", "remediate", cgNow, cgNow, nil, "scrum",
		"explain relative sizing, not hours", "learner_authored",
	}}}
	repo := NewConceptNodeRepo(&stubTxRunnerWith{q: fake})

	c, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, cgConcept)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if c == nil {
		t.Fatal("GetByID returned nil for an existing row")
	}
	if c.Title != "Scrum" || c.Provenance != conceptgraph.ProvenanceLearnerAuthored {
		t.Errorf("title/provenance = %q/%q", c.Title, c.Provenance)
	}
	if c.Intent != conceptgraph.IntentRemediate {
		t.Errorf("intent = %q; want remediate (scanned from the intent column)", c.Intent)
	}
	// ADR-247 D1: sub_goal + provenance scan from the two trailing columns.
	if c.SubGoal != "explain relative sizing, not hours" {
		t.Errorf("sub_goal = %q; want the scanned objective", c.SubGoal)
	}
	if c.SubGoalProvenance != conceptgraph.ProvenanceLearnerAuthored {
		t.Errorf("sub_goal_provenance = %q; want learner_authored", c.SubGoalProvenance)
	}
	if len(c.AtomRefs) != 1 || c.AtomRefs[0] != cgAtom {
		t.Errorf("atom_refs = %v; want [%s]", c.AtomRefs, cgAtom)
	}
	if c.DeletedAt != nil {
		t.Error("live row must have nil deleted_at")
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_nodes") || !contains(q, "WHERE id") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get SQL = %q; want by-id live select", q)
	}
}

func TestPGConceptNodeRepo_ListByLearnerLiveNewestFirst(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: []any{cgConcept, pgTenantID, pgUserGCID, "Scrum", []string{cgAtom}, "learner_authored", "", cgNow, cgNow, nil, "scrum", "", ""}},
		{cols: []any{cgSource, pgTenantID, pgUserGCID, "Agile", []string{}, "companion_suggested_accepted", "explore", cgNow, cgNow, nil, "agile", "", ""}},
	}}}
	repo := NewConceptNodeRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d; want 2", len(out))
	}
	if out[1].Provenance != conceptgraph.ProvenanceCompanionSuggestedAccepted || len(out[1].AtomRefs) != 0 {
		t.Errorf("row1 = %+v; want companion-suggested empty concept", out[1])
	}
	if out[1].Intent != conceptgraph.IntentExplore {
		t.Errorf("row1 intent = %q; want explore", out[1].Intent)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_nodes") || !contains(q, "deleted_at IS NULL") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("list SQL = %q; want live-only newest-first", q)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing before list: %v", fake.sqls)
	}
}

func TestPGConceptNodeRepo_NilAndNotFoundAndBubble(t *testing.T) {
	// nil create → no DB.
	tx := &stubTxRunner{}
	if err := NewConceptNodeRepo(tx).Create(withCtx(), nil); err != nil {
		t.Fatalf("Create(nil): %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil create should not touch DB: %v", tx.q.execCalls)
	}
	// not found → (nil, nil).
	g, err := NewConceptNodeRepo(&stubTxRunner{}).GetByID(withCtx(), pgTenantID, pgUserGCID, "missing")
	if err != nil || g != nil {
		t.Errorf("missing row: got (%v, %v); want (nil, nil)", g, err)
	}
	// ErrNoRows scan → (nil, nil).
	fake := &goalFakeQuerier{row: &goalFakeRow{err: ErrNoRows}}
	g2, err := NewConceptNodeRepo(&stubTxRunnerWith{q: fake}).GetByID(withCtx(), pgTenantID, pgUserGCID, "x")
	if err != nil || g2 != nil {
		t.Errorf("ErrNoRows: got (%v, %v); want (nil, nil)", g2, err)
	}
	// exec error bubbles (fail-loud).
	repo := NewConceptNodeRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: context.DeadlineExceeded}})
	if err := repo.Create(withCtx(), conceptFixture(t)); err == nil {
		t.Error("want create error to bubble")
	}
}

func TestPGConceptNodeRepo_SatisfiesPort(t *testing.T) {
	var _ conceptgraph.ConceptNodeRepository = (*ConceptNodeRepo)(nil)
}

// ---- EdgeRepo ----

func TestPGEdgeRepo_CreateAppliesRLSAndInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewEdgeRepo(tx)
	if err := repo.Create(withCtx(), edgeFixture(t, conceptgraph.EdgeClassHierarchy)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 || !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS pair missing: %v", calls)
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO concept_edges") {
		t.Errorf("last = %q; want INSERT INTO concept_edges", last)
	}
	if !contains(last, "edge_class") || !contains(last, "source_concept_id") {
		t.Errorf("insert = %q; want edge_class + source_concept_id columns", last)
	}
}

func TestPGEdgeRepo_GetByIDScansRow(t *testing.T) {
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		"01970000-0000-7000-e000-000000000001", pgTenantID, pgUserGCID,
		cgSource, cgTarget, "hierarchy", "learner_authored", cgNow, cgNow, nil,
	}}}
	repo := NewEdgeRepo(&stubTxRunnerWith{q: fake})

	e, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-e000-000000000001")
	if err != nil || e == nil {
		t.Fatalf("GetByID: (%v, %v)", e, err)
	}
	if e.Class != conceptgraph.EdgeClassHierarchy || e.SourceConceptID != cgSource || e.TargetConceptID != cgTarget {
		t.Errorf("edge = %+v", e)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_edges") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get SQL = %q", q)
	}
}

func TestPGEdgeRepo_ListByConceptCoversBothEndpoints(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: []any{"01970000-0000-7000-e000-000000000001", pgTenantID, pgUserGCID, cgSource, cgTarget, "hierarchy", "learner_authored", cgNow, cgNow, nil}},
	}}}
	repo := NewEdgeRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListByConcept(withCtx(), pgTenantID, pgUserGCID, cgSource)
	if err != nil {
		t.Fatalf("ListByConcept: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d; want 1", len(out))
	}
	q := fake.sqls[len(fake.sqls)-1]
	// A concept's relations = edges where it is source OR target.
	if !contains(q, "source_concept_id") || !contains(q, "target_concept_id") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("list-by-concept SQL = %q; want source OR target, live-only", q)
	}
}

func TestPGEdgeRepo_ListByLearnerLiveNewestFirst(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: []any{"01970000-0000-7000-e000-000000000001", pgTenantID, pgUserGCID, cgSource, cgTarget, "lateral", "learner_authored", cgNow, cgNow, nil}},
	}}}
	repo := NewEdgeRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(out) != 1 || out[0].Class != conceptgraph.EdgeClassLateral {
		t.Fatalf("out = %+v; want 1 lateral edge", out)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_edges") || !contains(q, "deleted_at IS NULL") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("list SQL = %q; want live-only newest-first", q)
	}
}

func TestPGEdgeRepo_UpdateScopedAndNilNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewEdgeRepo(tx)
	if err := repo.Update(withCtx(), edgeFixture(t, conceptgraph.EdgeClassLateral)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "UPDATE concept_edges") || !contains(last, "WHERE id") || !contains(last, "learner_gcid") {
		t.Errorf("update = %q; want scoped UPDATE concept_edges", last)
	}
	// nil update → no DB.
	tx2 := &stubTxRunner{}
	if err := NewEdgeRepo(tx2).Update(withCtx(), nil); err != nil {
		t.Fatalf("Update(nil): %v", err)
	}
	if tx2.q != nil && len(tx2.q.execCalls) != 0 {
		t.Errorf("nil update should not touch DB: %v", tx2.q.execCalls)
	}
}

// SoftDeleteByConcept is the concept.deleted.v1 cascade primitive: it soft-deletes
// every LIVE edge incident to the concept (source OR target), tenant+learner
// scoped, so a removed concept leaves no dangling relations (CHO-2324).
func TestPGEdgeRepo_SoftDeleteByConceptScopedBothEndpoints(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewEdgeRepo(tx)
	if _, err := repo.SoftDeleteByConcept(withCtx(), pgTenantID, pgUserGCID, cgConcept, cgNow); err != nil {
		t.Fatalf("SoftDeleteByConcept: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 ||
		!contains(calls[0], "SET LOCAL chora.tenant_id") ||
		!contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Fatalf("want RLS SET LOCAL pair BEFORE the mutation; got %v", calls)
	}
	last := calls[len(calls)-1]
	if !contains(last, "UPDATE concept_edges") || !contains(last, "deleted_at") {
		t.Errorf("want a soft-delete UPDATE; got %q", last)
	}
	// An edge is incident if the concept is the source OR the target.
	if !contains(last, "source_concept_id") || !contains(last, "target_concept_id") {
		t.Errorf("want source OR target match; got %q", last)
	}
	// Tenant+learner scoped, live-only (a redelivery then touches 0 rows).
	if !contains(last, "tenant_id") || !contains(last, "learner_gcid") || !contains(last, "deleted_at IS NULL") {
		t.Errorf("want tenant+learner scope, live-only; got %q", last)
	}
}

func TestPGEdgeRepo_SatisfiesPort(t *testing.T) {
	var _ conceptgraph.EdgeRepository = (*EdgeRepo)(nil)
}
