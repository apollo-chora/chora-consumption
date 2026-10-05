// concept_suggestion_test.go — RLS-contract + SQL-shape + scan-mapping
// verification for the Suggestion pg adapter (ADR-212 WS-4). Mirrors
// concept_graph_test.go: a stub Querier captures SQL so we assert the SET LOCAL
// chora.tenant_id pair lands BEFORE the mutation, and the shared goalFakeQuerier
// returns canned rows so the GetByID/GetBySourceEvent/ListPending scan→domain
// mapping is exercised. Schema mirrors migrations/0060_concept_suggestions.up.sql.
package pg

import (
	"context"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	csEvent = "01970000-0000-7000-e000-0000000000e9"
	csFam   = "01970000-0000-7000-f000-0000000000ea"
	csSugID = "01970000-0000-7000-5000-0000000000eb"
	csFocal = "01970000-0000-7000-c000-0000000000ec"
)

func sp(s string) *string { return &s }

func conceptSugFixture(t *testing.T) *conceptgraph.Suggestion {
	t.Helper()
	s, err := conceptgraph.NewConceptSuggestion(conceptgraph.NewConceptSuggestionInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Title: "Fibonacci sequence",
		AtomRefs: []string{cgAtom}, Rationale: "relates to story points",
		ModelID: "gemini-2.5-flash", RunID: "run-1", SourceCompanionID: csFam,
		SourceEventID: csEvent, Now: cgNow,
	})
	if err != nil {
		t.Fatalf("conceptSugFixture: %v", err)
	}
	return s
}

func edgeSugFixture(t *testing.T) *conceptgraph.Suggestion {
	t.Helper()
	s, err := conceptgraph.NewEdgeSuggestion(conceptgraph.NewEdgeSuggestionInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		SourceConceptID: cgSource, TargetConceptID: cgTarget, Class: conceptgraph.EdgeClassHierarchy,
		ModelID: "gemini-2.5-flash", RunID: "run-2", SourceEventID: csEvent, Now: cgNow,
	})
	if err != nil {
		t.Fatalf("edgeSugFixture: %v", err)
	}
	return s
}

func TestPGSuggestionRepo_CreateConceptAppliesRLSAndInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSuggestionRepo(tx)
	if err := repo.Create(withCtx(), conceptSugFixture(t)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + insert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") || !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS pair missing before insert: %v", calls)
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO concept_suggestions") {
		t.Errorf("last = %q; want INSERT INTO concept_suggestions", last)
	}
	if !contains(last, "kind") || !contains(last, "status") || !contains(last, "source_event_id") {
		t.Errorf("insert = %q; want kind + status + source_event_id columns", last)
	}
	if !contains(last, "focal_concept_id") {
		t.Errorf("insert = %q; want focal_concept_id column (focal scoping)", last)
	}
}

func TestPGSuggestionRepo_CreateEdgeInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSuggestionRepo(tx)
	if err := repo.Create(withCtx(), edgeSugFixture(t)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO concept_suggestions") || !contains(last, "source_concept_id") || !contains(last, "edge_class") {
		t.Errorf("insert = %q; want edge columns", last)
	}
}

func TestPGSuggestionRepo_CreateBatchOneTxAllInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSuggestionRepo(tx)
	if err := repo.CreateBatch(withCtx(), []*conceptgraph.Suggestion{conceptSugFixture(t), edgeSugFixture(t)}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	calls := tx.q.execCalls
	// One RLS pair (single RunInTx) + 2 inserts.
	if len(calls) != 4 {
		t.Fatalf("want RLS pair + 2 inserts in ONE tx; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") || !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS pair missing: %v", calls)
	}
	if !contains(calls[2], "INSERT INTO concept_suggestions") || !contains(calls[3], "INSERT INTO concept_suggestions") {
		t.Errorf("want 2 inserts; got %v", calls[2:])
	}
}

func TestPGSuggestionRepo_CreateBatchEmptyNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	if err := NewSuggestionRepo(tx).CreateBatch(withCtx(), nil); err != nil {
		t.Fatalf("CreateBatch(nil): %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("empty batch should not touch DB: %v", tx.q.execCalls)
	}
}

func TestPGSuggestionRepo_UpdateScopedToDecision(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewSuggestionRepo(tx)
	s := conceptSugFixture(t)
	if _, _, err := s.Accept(cgNow); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if err := repo.Update(withCtx(), s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "UPDATE concept_suggestions") {
		t.Errorf("last = %q; want UPDATE concept_suggestions", last)
	}
	if !contains(last, "status") || !contains(last, "decided_at") {
		t.Errorf("update = %q; want status + decided_at", last)
	}
	if !contains(last, "WHERE id") || !contains(last, "tenant_id") || !contains(last, "learner_gcid") {
		t.Errorf("update not scoped to id/tenant/learner: %q", last)
	}
}

// canned 20-col suggestion row (concept shape). focal_concept_id sits between
// source_event_id and created_at (mirrors suggestionColumns).
func conceptSugRow(status string) []any {
	return []any{
		csSugID, pgTenantID, pgUserGCID, "concept", status,
		sp("Fibonacci sequence"), []string{cgAtom}, nil, nil, nil,
		sp("why"), sp("gemini-2.5-flash"), sp("run-1"), sp(csFam), sp(csEvent),
		sp(csFocal),
		cgNow, cgNow, nil, nil,
	}
}

func TestPGSuggestionRepo_GetByIDScansRow(t *testing.T) {
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: conceptSugRow("pending")}}
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: fake})

	s, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, csSugID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if s == nil {
		t.Fatal("GetByID returned nil for an existing row")
	}
	if s.Kind != conceptgraph.SuggestionKindConcept || s.Status != conceptgraph.SuggestionStatusPending {
		t.Errorf("kind/status = %q/%q", s.Kind, s.Status)
	}
	if s.Title != "Fibonacci sequence" || len(s.AtomRefs) != 1 || s.AtomRefs[0] != cgAtom {
		t.Errorf("concept payload = %+v", s)
	}
	if s.ModelID != "gemini-2.5-flash" || s.SourceEventID != csEvent {
		t.Errorf("stamps not mapped: %+v", s)
	}
	if s.FocalConceptID != csFocal {
		t.Errorf("focal_concept_id not scanned: got %q want %q", s.FocalConceptID, csFocal)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_suggestions") || !contains(q, "WHERE id") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get SQL = %q; want by-id live select", q)
	}
	if !contains(q, "focal_concept_id") {
		t.Errorf("select columns = %q; want focal_concept_id", q)
	}
}

func TestPGSuggestionRepo_GetBySourceEventScansRow(t *testing.T) {
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: conceptSugRow("pending")}}
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: fake})

	s, err := repo.GetBySourceEvent(withCtx(), pgTenantID, pgUserGCID, csEvent)
	if err != nil || s == nil {
		t.Fatalf("GetBySourceEvent: (%v, %v)", s, err)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_suggestions") || !contains(q, "source_event_id") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get-by-event SQL = %q", q)
	}
}

func TestPGSuggestionRepo_ListPendingLiveNewestFirst(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: conceptSugRow("pending")},
	}}}
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListPending(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(out) != 1 || out[0].Status != conceptgraph.SuggestionStatusPending {
		t.Fatalf("out = %+v; want 1 pending", out)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_suggestions") || !contains(q, "status = 'pending'") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("list SQL = %q; want pending-only newest-first", q)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing before list: %v", fake.sqls)
	}
}

// ListPendingForFocal returns focal-matched OR whole-map (NULL) pending rows —
// so a stale batch from a DIFFERENT focal does not leak onto this node, while
// whole-map suggestions still show everywhere.
func TestPGSuggestionRepo_ListPendingForFocalFiltersFocalOrNull(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: conceptSugRow("pending")},
	}}}
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListPendingForFocal(withCtx(), pgTenantID, pgUserGCID, csFocal)
	if err != nil {
		t.Fatalf("ListPendingForFocal: %v", err)
	}
	if len(out) != 1 || out[0].Status != conceptgraph.SuggestionStatusPending {
		t.Fatalf("out = %+v; want 1 pending", out)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM concept_suggestions") || !contains(q, "status = 'pending'") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("focal list SQL = %q; want pending-only newest-first", q)
	}
	// The focal filter: match the requested focal OR whole-map (NULL) rows.
	if !contains(q, "focal_concept_id = $3") || !contains(q, "focal_concept_id IS NULL") {
		t.Errorf("focal list SQL = %q; want (focal = $3 OR focal IS NULL) filter", q)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing before focal list: %v", fake.sqls)
	}
}

// A focal that is NOT a canonical UUID can never equal any stored
// focal_concept_id (a uuid column), so it must resolve to the whole-map
// (NULL-focal) inbox (the SAME rows a valid-but-absent focal yields) and must
// NOT be bound into `focal_concept_id = $3`, which coerces it to uuid and raises
// 22P02 (SUGGESTIONS_LIST_FAILED, 500-ing the whole Suggestions tab). Regression
// for the 2026-07-22 live 500 (a malformed focal from the map-companion panel).
func TestPGSuggestionRepo_ListPendingForFocalNonUUIDFocalWholeMapOnly(t *testing.T) {
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: conceptSugRow("pending")},
	}}}
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListPendingForFocal(withCtx(), pgTenantID, pgUserGCID, "not-a-uuid")
	if err != nil {
		t.Fatalf("ListPendingForFocal(non-uuid): %v", err)
	}
	if len(out) != 1 || out[0].Status != conceptgraph.SuggestionStatusPending {
		t.Fatalf("out = %+v; want 1 pending (the whole-map row)", out)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if contains(q, "focal_concept_id = $3") {
		t.Errorf("non-uuid focal SQL = %q; must NOT bind a non-uuid into the $3 uuid cast (22P02)", q)
	}
	if !contains(q, "focal_concept_id IS NULL") || !contains(q, "status = 'pending'") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("non-uuid focal SQL = %q; want the whole-map (focal IS NULL) pending query", q)
	}
	if !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing before whole-map list: %v", fake.sqls)
	}
}

func TestPGSuggestionRepo_NilAndNotFoundAndBubble(t *testing.T) {
	// nil create → no DB.
	tx := &stubTxRunner{}
	if err := NewSuggestionRepo(tx).Create(withCtx(), nil); err != nil {
		t.Fatalf("Create(nil): %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil create should not touch DB: %v", tx.q.execCalls)
	}
	// not found → (nil, nil).
	g, err := NewSuggestionRepo(&stubTxRunner{}).GetByID(withCtx(), pgTenantID, pgUserGCID, "missing")
	if err != nil || g != nil {
		t.Errorf("missing row: got (%v, %v); want (nil, nil)", g, err)
	}
	// ErrNoRows scan → (nil, nil).
	fake := &goalFakeQuerier{row: &goalFakeRow{err: ErrNoRows}}
	g2, err := NewSuggestionRepo(&stubTxRunnerWith{q: fake}).GetBySourceEvent(withCtx(), pgTenantID, pgUserGCID, csEvent)
	if err != nil || g2 != nil {
		t.Errorf("ErrNoRows: got (%v, %v); want (nil, nil)", g2, err)
	}
	// exec error bubbles (fail-loud).
	repo := NewSuggestionRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: context.DeadlineExceeded}})
	if err := repo.Create(withCtx(), conceptSugFixture(t)); err == nil {
		t.Error("want create error to bubble")
	}
}

func TestPGSuggestionRepo_SatisfiesPort(t *testing.T) {
	var _ conceptgraph.SuggestionRepository = (*SuggestionRepo)(nil)
}
