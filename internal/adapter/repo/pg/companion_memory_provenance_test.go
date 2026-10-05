// companion_memory_provenance_test.go — CHO-2179 (ADR-231 D4): the memory-note
// sink persists a grounded note's DURABLE provenance.
//
// Before this, a `memory_type='research'` row carried the LLM's prose and
// NOTHING else — no citations, no queries. Once the Google grounding-redirect
// links expired (~30 days, D4) the note could never explain where it came from,
// and neither could an auditor. grounded.Hit's doc comment already ASSERTED the
// opposite ("the web_research memory-note sink persists domain+title+snippet").
//
// RED-first: Record must bind a source_metadata JSONB payload carrying the
// durable fields — and must bind NULL for a non-grounded note, so a recap row
// never carries an empty husk.
package pg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// insertProvenanceArgIdx is the source_metadata bind position in the INSERT
// (0-based over the bound args; the column is appended last).
const insertProvenanceArgIdx = 12

func groundedProvenanceFixture() *companion.MemoryProvenance {
	return &companion.MemoryProvenance{
		WebSearchQueries: []string{"shape of the earth", "oblate spheroid"},
		Citations: []companion.MemoryCitation{
			{Domain: "nasa.gov", Title: "Earth is an oblate spheroid", Snippet: "Measurements confirm it."},
			{Domain: "noaa.gov", Title: "Shape of the Earth", Snippet: "Satellite geodesy agrees."},
		},
	}
}

func TestCompanionMemoryRepo_Record_BindsDurableProvenanceJSON(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	err := repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		MemoryType:  "research",
		ContentText: "NASA and NOAA agree the Earth is an oblate spheroid.",
		Embedding:   sampleEmbedding(),
		ModelID:     "text-embedding-004",
		Now:         time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		Provenance:  groundedProvenanceFixture(),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The SQL must actually name the column, or the bind is a silent no-op.
	if !strings.Contains(insertCompanionMemoryRecallSQL, "source_metadata") {
		t.Fatalf("insertCompanionMemoryRecallSQL does not write source_metadata:\n%s", insertCompanionMemoryRecallSQL)
	}

	args := tx.q.execArgs[2] // [0]+[1] are the RLS SET LOCALs
	if len(args) <= insertProvenanceArgIdx {
		t.Fatalf("INSERT bound %d args, want > %d (source_metadata missing)", len(args), insertProvenanceArgIdx)
	}
	raw := args[insertProvenanceArgIdx]
	if raw == nil {
		t.Fatalf("source_metadata bound NULL for a GROUNDED note — the provenance was dropped")
	}

	var payload companion.MemoryProvenance
	switch v := raw.(type) {
	case []byte:
		if err := json.Unmarshal(v, &payload); err != nil {
			t.Fatalf("source_metadata is not valid JSON: %v (%s)", err, v)
		}
	case string:
		if err := json.Unmarshal([]byte(v), &payload); err != nil {
			t.Fatalf("source_metadata is not valid JSON: %v (%s)", err, v)
		}
	default:
		t.Fatalf("source_metadata bound as %T, want JSON bytes/string for a JSONB column", raw)
	}

	if len(payload.WebSearchQueries) != 2 || payload.WebSearchQueries[0] != "shape of the earth" {
		t.Errorf("persisted WebSearchQueries = %#v, want the issued queries", payload.WebSearchQueries)
	}
	if len(payload.Citations) != 2 || payload.Citations[0].Domain != "nasa.gov" {
		t.Errorf("persisted Citations = %#v, want the durable domain+title+snippet", payload.Citations)
	}
	// ⚠ ADR-231 D4: the redirect uri EXPIRES — it must never be the durable record.
	// MemoryCitation deliberately has no URL field; this pins that it stays that way.
	if strings.Contains(string(mustJSON(t, payload)), "grounding-api-redirect") {
		t.Errorf("persisted provenance leaks the ephemeral redirect uri: %s", mustJSON(t, payload))
	}
}

func TestCompanionMemoryRepo_Record_NonGroundedNoteBindsNullProvenance(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	err := repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		MemoryType:  "recap",
		ContentText: "You covered fractions today.",
		Embedding:   sampleEmbedding(),
		ModelID:     "text-embedding-004",
		Now:         time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		// Provenance deliberately nil — a recap is not grounded.
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	args := tx.q.execArgs[2]
	if len(args) <= insertProvenanceArgIdx {
		t.Fatalf("INSERT bound %d args, want > %d", len(args), insertProvenanceArgIdx)
	}
	if got := args[insertProvenanceArgIdx]; got != nil {
		t.Errorf("source_metadata = %#v for a non-grounded note, want NULL (no empty husk)", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
