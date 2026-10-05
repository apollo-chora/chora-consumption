// companion_memory_read_provenance_test.go — CHO-2185 (ADR-231 D4): a research
// note must reach the learner WITH the provenance CHO-2179 persisted for it.
//
// These tests pin the wire contract the FE depends on:
//
//   - researchNotes[] is its own list, read with a memory_type filter, so a
//     chatty learner's chat_turns cannot crowd research notes out of the
//     recency window (they would silently vanish from the panel).
//   - each note carries sourceMetadata{webSearchQueries, citations[domain,title,
//     snippet]}.
//   - sourceMetadata is present IFF there is provenance. A pre-0094 note (NULL)
//     and an empty husk both OMIT the key, so the FE has exactly one branch for
//     "we did not record this" and can never render an empty attribution block
//     implying a search that never happened.
//   - NO uri is ever emitted. The Vertex redirect expires (~30d); re-serving one
//     from a months-old note is the dead-link failure D4 exists to prevent.
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

// fmResearchNote builds a grounded note as CHO-2179's writer persists it.
func fmResearchNote(id string, p *companionmind.Provenance) companionmind.EpisodicMemory {
	return companionmind.EpisodicMemory{
		ID:         id,
		MemoryType: companionmind.MemoryTypeResearch,
		Content:    "The Earth is an oblate spheroid.",
		CreatedAt:  time.Now().UTC(),
		Provenance: p,
	}
}

func fmFullProvenance() *companionmind.Provenance {
	return &companionmind.Provenance{
		WebSearchQueries: []string{"shape of the earth", "oblate spheroid"},
		Citations: []companionmind.ProvenanceCitation{
			{Domain: "nasa.gov", Title: "Earth's true shape", Snippet: "…bulges at the equator…"},
		},
	}
}

// fmDoMemoryGet drives the handler and returns the decoded body + raw bytes.
func fmDoMemoryGet(t *testing.T, rd *fmStubMemoryReader) (map[string]any, string) {
	t.Helper()
	h := NewCompanionMemoryReadHandler(rd, &fmStubInstances{inst: fmOwnedInstance()}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/me/companions/"+fmCompanionID+"/memory", nil)
	req.Header.Set("X-Tenant-Id", fmTenantID)
	req.Header.Set("gcid", fmGCID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	raw := rec.Body.String()
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body, raw
}

// The whole point of the story: the persisted provenance reaches the learner.
func TestMemoryRead_ResearchNoteCarriesDurableProvenance(t *testing.T) {
	rd := &fmStubMemoryReader{research: []companionmind.EpisodicMemory{
		fmResearchNote("mem-1", fmFullProvenance()),
	}}

	body, _ := fmDoMemoryGet(t, rd)

	notes, ok := body["researchNotes"].([]any)
	if !ok {
		t.Fatalf("researchNotes missing from the wire: %v", body)
	}
	if len(notes) != 1 {
		t.Fatalf("researchNotes len = %d, want 1", len(notes))
	}
	note := notes[0].(map[string]any)

	sm, ok := note["sourceMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("sourceMetadata missing — the provenance is still unreadable: %v", note)
	}

	queries, ok := sm["webSearchQueries"].([]any)
	if !ok || len(queries) != 2 {
		t.Fatalf("webSearchQueries = %v, want the 2 issued queries", sm["webSearchQueries"])
	}
	if queries[0] != "shape of the earth" {
		t.Errorf("query[0] = %v", queries[0])
	}

	cits, ok := sm["citations"].([]any)
	if !ok || len(cits) != 1 {
		t.Fatalf("citations = %v, want 1", sm["citations"])
	}
	c := cits[0].(map[string]any)
	if c["domain"] != "nasa.gov" {
		t.Errorf("domain = %v, want nasa.gov (the DURABLE handle)", c["domain"])
	}
	if c["title"] != "Earth's true shape" {
		t.Errorf("title = %v, want the headline", c["title"])
	}
	if c["snippet"] == nil || c["snippet"] == "" {
		t.Error("snippet dropped — it is part of the durable record")
	}
}

// ⚠ D4. A persisted citation has NO uri by design. If the wire ever grows one it
// will be a ~30-day Google redirect that has almost certainly expired by the time
// the note is re-read — a dead link that still LOOKS like a working source.
func TestMemoryRead_NeverEmitsAnExpiringUri(t *testing.T) {
	rd := &fmStubMemoryReader{research: []companionmind.EpisodicMemory{
		fmResearchNote("mem-1", fmFullProvenance()),
	}}

	_, raw := fmDoMemoryGet(t, rd)

	for _, banned := range []string{`"url"`, `"uri"`, "grounding-api-redirect"} {
		if strings.Contains(raw, banned) {
			t.Errorf("wire carries %s — a persisted citation must never ship an expiring link: %s", banned, raw)
		}
	}
}

// Research notes are read as their OWN filtered list. Without the filter they
// share the 20-row recency window with chat_turns, so a learner who chats a lot
// would watch their research notes silently disappear from the panel.
func TestMemoryRead_ResearchIsReadWithAMemoryTypeFilter(t *testing.T) {
	rd := &fmStubMemoryReader{research: []companionmind.EpisodicMemory{
		fmResearchNote("mem-1", fmFullProvenance()),
	}}

	fmDoMemoryGet(t, rd)

	got := rd.gotResearch
	if len(got.MemoryTypes) != 1 || got.MemoryTypes[0] != companionmind.MemoryTypeResearch {
		t.Fatalf("research read MemoryTypes = %v, want [research]", got.MemoryTypes)
	}
	if got.TenantID != fmTenantID || got.LearnerGCID != fmGCID || got.CompanionID != fmCompanionID {
		t.Errorf("research read not learner-scoped: %+v", got)
	}
}

// A pre-0094 note's provenance is genuinely unrecoverable. OMIT the key — do not
// ship `sourceMetadata: {}`, which would render as an empty attribution block and
// imply a search that never happened.
func TestMemoryRead_PreMigrationNoteOmitsSourceMetadata(t *testing.T) {
	rd := &fmStubMemoryReader{research: []companionmind.EpisodicMemory{
		fmResearchNote("mem-old", nil),
	}}

	body, _ := fmDoMemoryGet(t, rd)

	notes := body["researchNotes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("the note itself must still reach the learner; got %d", len(notes))
	}
	note := notes[0].(map[string]any)
	if _, present := note["sourceMetadata"]; present {
		t.Errorf("sourceMetadata must be ABSENT for an unrecorded note, not empty: %v", note)
	}
	if note["content"] == "" {
		t.Error("content dropped — only the provenance is missing, not the note")
	}
}

// Same rule, defensively: an EMPTY husk must be indistinguishable from NULL on
// the wire. The writer guards against husks today; the reader must not depend on
// that staying true.
func TestMemoryRead_EmptyProvenanceHuskIsOmitted(t *testing.T) {
	rd := &fmStubMemoryReader{research: []companionmind.EpisodicMemory{
		fmResearchNote("mem-husk", &companionmind.Provenance{}),
	}}

	body, _ := fmDoMemoryGet(t, rd)

	note := body["researchNotes"].([]any)[0].(map[string]any)
	if _, present := note["sourceMetadata"]; present {
		t.Errorf("an empty husk must be omitted, exactly like NULL: %v", note)
	}
}

// No research notes ⇒ a stable empty list, never null: the FE must have exactly
// one shape to branch on.
func TestMemoryRead_NoResearchNotesIsAnEmptyList(t *testing.T) {
	rd := &fmStubMemoryReader{}

	body, _ := fmDoMemoryGet(t, rd)

	notes, ok := body["researchNotes"].([]any)
	if !ok {
		t.Fatalf("researchNotes must be [] not null: %v", body["researchNotes"])
	}
	if len(notes) != 0 {
		t.Errorf("want no notes; got %d", len(notes))
	}
}
