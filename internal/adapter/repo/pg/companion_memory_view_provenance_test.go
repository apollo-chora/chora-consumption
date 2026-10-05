// companion_memory_view_provenance_test.go — CHO-2185 (ADR-231 D4): the learner
// read path must SELECT and decode companion_memory_recall.source_metadata.
//
// CHO-2179 added the column and wrote it on every grounded note; this read was
// still projecting `id, memory_type, content_text, created_at` and dropping the
// provenance on the floor. These tests pin the column into the projection, the
// type filter into the WHERE, and the JSONB decode into the scan.
//
// Reuses the shared memStub* / pgTenantID helpers from companion_memory_recall_test.go.
package pg

import (
	"testing"
	"time"

	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

// The projection must carry source_metadata. Without it the durable provenance
// is unreadable no matter what the rest of the stack does.
func TestRecentMemories_SQLSelectsSourceMetadata(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)

	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}

	q := tx.q.queryCalls[0]
	if !contains(q, "source_metadata") {
		t.Errorf("projection is missing source_metadata — the provenance stays unreadable: %q", q)
	}
	// D5 still holds: no vector machinery on the learner read.
	if contains(q, "embedding") || contains(q, "<=>") {
		t.Errorf("learner read must not project vector internals: %q", q)
	}
}

// A memory_type filter must reach the WHERE clause and bind as $3, so research
// notes can be read as their own list. Without it they are crowded out of the
// recency window by ordinary chat turns and the learner never sees them.
func TestRecentMemories_MemoryTypeFilterBindsAndFiltersInSQL(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)

	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
		MemoryLimit: 7,
		MemoryTypes: []string{companionmind.MemoryTypeResearch},
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}

	q := tx.q.queryCalls[0]
	if !contains(q, "memory_type = ANY") {
		t.Errorf("SQL missing the memory_type filter: %q", q)
	}

	args := tx.q.queryArgs[0]
	if len(args) != 3 {
		t.Fatalf("want 3 bound args (companion_id, limit, types); got %d: %v", len(args), args)
	}
	if args[0] != memCompanionID {
		t.Errorf("$1 = %v, want the companion id", args[0])
	}
	if args[1] != 7 {
		t.Errorf("$2 = %v, want the clamped limit 7", args[1])
	}
	types, ok := args[2].([]string)
	if !ok {
		t.Fatalf("$3 = %T, want []string bound as a text[]", args[2])
	}
	if len(types) != 1 || types[0] != "research" {
		t.Errorf("$3 = %v, want [research]", types)
	}
}

// An unfiltered read must bind an EMPTY type array (not nil, not a sentinel) —
// the SQL's cardinality($3)=0 branch is what makes "all types" work, and it is
// the existing panel contract. A nil here would silently change the pgx encoding.
func TestRecentMemories_NoFilterBindsEmptyTypeArray(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)

	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}

	types, ok := tx.q.queryArgs[0][2].([]string)
	if !ok {
		t.Fatalf("$3 = %T, want []string", tx.q.queryArgs[0][2])
	}
	if types == nil {
		t.Fatal("$3 is nil — bind an empty []string so the cardinality()=0 branch fires")
	}
	if len(types) != 0 {
		t.Errorf("$3 = %v, want empty (all types)", types)
	}
}

// The JSONB decodes into the read model, whole. This is the assertion CHO-2179
// never had: the persisted bytes must come back as domain/title/snippet + queries.
func TestRecentMemories_ScansSourceMetadataIntoProvenance(t *testing.T) {
	now := time.Now().UTC()
	raw := []byte(`{"web_search_queries":["shape of the earth"],` +
		`"citations":[{"domain":"nasa.gov","title":"Earth's true shape","snippet":"…bulges…"}]}`)

	tx := &memStubTxRunner{q: &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"mem-1", "research", "The Earth is an oblate spheroid.", now, raw},
	}}}}
	repo := NewCompanionMemoryViewRepo(tx)

	out, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
	})
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rows = %d, want 1", len(out))
	}
	p := out[0].Provenance
	if p == nil {
		t.Fatal("source_metadata decoded to nil — the provenance is still unreadable")
	}
	if len(p.WebSearchQueries) != 1 || p.WebSearchQueries[0] != "shape of the earth" {
		t.Errorf("WebSearchQueries = %v, want [shape of the earth]", p.WebSearchQueries)
	}
	if len(p.Citations) != 1 {
		t.Fatalf("Citations = %d, want 1", len(p.Citations))
	}
	c := p.Citations[0]
	if c.Domain != "nasa.gov" {
		t.Errorf("Domain = %q, want nasa.gov", c.Domain)
	}
	if c.Title != "Earth's true shape" {
		t.Errorf("Title = %q — the headline is part of the durable record", c.Title)
	}
	if c.Snippet == "" {
		t.Error("Snippet dropped in the scan")
	}
}

// A NULL source_metadata (every pre-0094 note) must scan to nil provenance and
// NOT error. NULL is the honest "we did not record it" state, not a failure.
func TestRecentMemories_NullSourceMetadataScansToNilProvenance(t *testing.T) {
	tx := &memStubTxRunner{q: &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"mem-old", "research", "A note from before mig 0094.", time.Now().UTC(), nil},
	}}}}
	repo := NewCompanionMemoryViewRepo(tx)

	out, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
	})
	if err != nil {
		t.Fatalf("a NULL source_metadata is not an error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rows = %d, want 1 — the note is still readable", len(out))
	}
	if out[0].Provenance != nil {
		t.Error("NULL must decode to nil provenance, never an invented husk")
	}
	if out[0].Content == "" {
		t.Error("content dropped — only the provenance is missing, not the note")
	}
}

// The RLS session must still be applied on the FILTERED read — the new code path.
// companion_memory_recall is FORCE-RLS, so a read that skipped the GUCs would not
// error, it would just quietly return nothing (CHO-2170).
//
// Assert the ORDER of the execs, never their COUNT: a count assertion is exactly
// what let the CHO-2170 silent no-op ship, because it REQUIRED the RLS pair to be
// absent. The tenant itself comes from the ctx (the validated session), which is
// why this drives the repo the way the handler does.
func TestRecentMemories_FilteredReadStillAppliesRLS(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)

	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
		MemoryTypes: []string{companionmind.MemoryTypeResearch},
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}

	if len(tx.q.execCalls) < 2 {
		t.Fatalf("RLS session never applied on the filtered read: %v", tx.q.execCalls)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("exec[0] = %q, want the tenant SET LOCAL", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("exec[1] = %q, want the gcid SET LOCAL", tx.q.execCalls[1])
	}
	if len(tx.q.queryCalls) != 1 {
		t.Fatalf("expected the read to run; got %d queries", len(tx.q.queryCalls))
	}
}
