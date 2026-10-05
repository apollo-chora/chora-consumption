// companion_memory_recall_provenance_test.go — CHO-2192 (ADR-231 D4).
//
// CHO-2179 WROTE source_metadata. CHO-2185 taught the VIEW path to read it back.
// The RECALL path — the one the chat handler actually calls, and therefore the
// only one that can attribute a claim the Companion is making RIGHT NOW — still
// selected five columns and dropped the sixth on the floor.
//
// ⚠ This is the CHO-2179 gotcha repeating in a second costume: THE READER NEVER
// SELECTED THE COLUMN. The INSERT was correct, the migration was applied, the
// data was on disk — and the feature was 100% dead, because nothing ever asked
// for it. Assert the projection, not just the scan.
//
// Reuses memStubQuerier / memStubRows / memStubTxRunner / withCtx / contains from
// companion_memory_recall_test.go + user_kg_test.go.
package pg

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

// provenanceJSON is what CHO-2179's writer actually marshals onto disk — the
// snake_case tags of companion.MemoryProvenance. Building the fixture by
// MARSHALLING the real struct (rather than hand-writing a JSON literal) means the
// round-trip cannot silently drift from the writer's shape.
func provenanceJSON(t *testing.T, p companion.MemoryProvenance) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal fixture provenance: %v", err)
	}
	return b
}

// THE headline defect. The recall SQL must PROJECT source_metadata; without this
// the column is unreachable no matter how correct every other layer is.
func TestCompanionMemoryRepo_Recall_ProjectsSourceMetadata(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	if _, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(tx.q.queryCalls) != 1 {
		t.Fatalf("expected one Query; got %d", len(tx.q.queryCalls))
	}
	if !contains(tx.q.queryCalls[0], "source_metadata") {
		t.Errorf("recall SQL does not project source_metadata — the provenance is unreachable:\n%s",
			tx.q.queryCalls[0])
	}
}

// The RLS pair must still land BEFORE the widened query. Exec ORDER, never exec
// COUNT: a count passes even when the GUCs are set after the read, which is
// exactly when the read returns nothing.
func TestCompanionMemoryRepo_Recall_Provenance_AppliesRLSBeforeQuery(t *testing.T) {
	stub := &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"01970000-0000-7000-d000-000000000001", companion.MemoryTypeResearch, "note", time.Now().UTC(), float64(0.1),
			provenanceJSON(t, companion.MemoryProvenance{Citations: []companion.MemoryCitation{{Domain: "cdc.gov"}}})},
	}}}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	if _, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(stub.execCalls) < 2 {
		t.Fatalf("expected the RLS SET LOCAL pair before the query; got %d execs", len(stub.execCalls))
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("exec[0] = %q; want the tenant GUC first", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("exec[1] = %q; want the gcid GUC second", stub.execCalls[1])
	}
	if len(stub.queryCalls) != 1 {
		t.Fatalf("expected the recall query AFTER the GUC pair; got %d queries", len(stub.queryCalls))
	}
}

// A grounded note's durable provenance must survive the scan intact.
func TestCompanionMemoryRepo_Recall_ScansProvenance(t *testing.T) {
	created := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)
	raw := provenanceJSON(t, companion.MemoryProvenance{
		WebSearchQueries: []string{"how warm should an egg be"},
		Citations: []companion.MemoryCitation{
			{Domain: "cdc.gov", Title: "Incubation basics", Snippet: "near 37.5C"},
		},
	})
	stub := &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"01970000-0000-7000-d000-000000000001", companion.MemoryTypeResearch, "eggs sit near 37.5C", created, float64(0.12), raw},
	}}}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	got, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows; want 1", len(got))
	}
	p := got[0].Provenance
	if p == nil {
		t.Fatal("Provenance = nil; the persisted source_metadata was dropped on the floor")
	}
	if len(p.Citations) != 1 || p.Citations[0].Domain != "cdc.gov" {
		t.Errorf("citations = %#v; want the durable cdc.gov domain", p.Citations)
	}
	if p.Citations[0].Title != "Incubation basics" {
		t.Errorf("title = %q; want the headline preserved", p.Citations[0].Title)
	}
	if len(p.WebSearchQueries) != 1 || p.WebSearchQueries[0] != "how warm should an egg be" {
		t.Errorf("queries = %#v; want the issued query preserved", p.WebSearchQueries)
	}
}

// A note written BEFORE mig 0094 has source_metadata NULL. That is an honest,
// recoverable state — not an error, and never an empty husk we invent.
func TestCompanionMemoryRepo_Recall_NullProvenance_IsNil_NotAnError(t *testing.T) {
	stub := &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		// nil cell models SQL NULL.
		{"01970000-0000-7000-d000-000000000001", companion.MemoryTypeResearch, "old note", time.Now().UTC(), float64(0.1), nil},
	}}}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	got, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err != nil {
		t.Fatalf("Recall returned an error for a pre-0094 NULL: %v — NULL is honest, not broken", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows; want 1", len(got))
	}
	if got[0].Provenance != nil {
		t.Errorf("Provenance = %#v; want nil — nothing may be fabricated for an unrecorded note", got[0].Provenance)
	}
	// The row is still GROUNDED — it is a research note. The learner must be told
	// its sources were not recorded, not left with silence.
	if !got[0].IsGrounded() {
		t.Error("IsGrounded() = false for a research note; a pre-0094 note is still grounded")
	}
}

// `{}` on disk says exactly what NULL says. Collapse it, so the wire has ONE
// shape for "nothing to attribute" and the FE can never render an empty block.
func TestCompanionMemoryRepo_Recall_EmptyHusk_CollapsesToNil(t *testing.T) {
	stub := &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"01970000-0000-7000-d000-000000000001", companion.MemoryTypeResearch, "note", time.Now().UTC(), float64(0.1), []byte(`{}`)},
	}}}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	got, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if got[0].Provenance != nil {
		t.Errorf("Provenance = %#v; want nil — an empty husk is the same fact as NULL", got[0].Provenance)
	}
}

// Malformed JSONB is LOUD. NULL means "never recorded" (honest); garbage means we
// wrote something we cannot read back — a real corruption we must not paper over.
func TestCompanionMemoryRepo_Recall_MalformedProvenance_FailsLoud(t *testing.T) {
	stub := &memStubQuerier{nextRows: &memStubRows{rows: [][]any{
		{"01970000-0000-7000-d000-000000000001", companion.MemoryTypeResearch, "note", time.Now().UTC(), float64(0.1), []byte(`{"citations":`)},
	}}}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	_, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err == nil {
		t.Fatal("Recall returned nil error on malformed source_metadata; corruption must fail LOUD")
	}
	if !contains(err.Error(), "source_metadata") {
		t.Errorf("error = %q; want it to name the column it could not decode", err.Error())
	}
}

// The recall row's memory_type const and the view model's MUST agree — they are
// the SAME string on the SAME wire, declared in two bounded packages that (by
// design) do not import each other. A silent divergence here would make the
// recall path stop recognising the very notes the view path renders.
func TestMemoryTypeResearch_AgreesAcrossBoundedPackages(t *testing.T) {
	if companion.MemoryTypeResearch != companionmind.MemoryTypeResearch {
		t.Fatalf("memory_type drift: companion=%q companionmind=%q — these name the same persisted value",
			companion.MemoryTypeResearch, companionmind.MemoryTypeResearch)
	}
	if companion.MemoryTypeResearch != "research" {
		t.Errorf("MemoryTypeResearch = %q; want the persisted literal %q",
			companion.MemoryTypeResearch, "research")
	}
}
