// companion_skill_invoke_seeker_provenance_test.go — CHO-2179 (ADR-231 D4/D5):
// the DURABLE grounded provenance a Seeker turn owes the learner.
//
// grounded.Hit's own doc comment states the invariant:
//
//	"Domain is the DURABLE citation surface: the web_research memory-note sink
//	 persists domain+title+snippet (never the ephemeral redirect uri), and the
//	 learner sees 'domain — title'."
//
// NEITHER HALF WAS IMPLEMENTED. `citationsFromHits` dropped Hit.Domain, so the
// A+ source list fell back to `c.domain || c.url` and headlined every source
// with the ~200-char Vertex grounding-api-redirect URL — the ONE identifier
// ADR-231 D4 says expires (~30 days) — while the durable publisher domain was
// discarded. And the memory note persisted only the LLM's prose: no citations,
// no queries, no way to ever explain where it came from.
//
// RED-first. Three properties, all previously false:
//  1. a citation carries its DURABLE domain to the learner (D4);
//  2. the issued web_search_queries reach the response — the transparency
//     surface that outlives the chip's dead links (D5) — and are never invented;
//  3. a research note PERSISTS that provenance (domain+title+snippet + queries),
//     and deliberately NOT the expiring redirect uri.
package http

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

// ---------------------------------------------------------------------------
// fixtures + helpers
// ---------------------------------------------------------------------------

// redirectURI mimics the real Vertex grounding-api-redirect link: opaque, huge,
// and EPHEMERAL (~30d). It is exactly what must never be the durable record.
const redirectURI = "https://vertexaisearch.cloud.google.com/grounding-api-redirect/AUZIYQG20JMqJ0eVYI5jY0WrNQYySp1SuR-EoGAu3Oqn0XCs9igSz8s24Yq2TBq6rLZfFRQry8U"

// twoCitedGrounded is a realistic gateway result: redirect URIs (ephemeral) +
// publisher domains (durable) + the chip + the queries the model actually issued.
func twoCitedGrounded() grounded.Result {
	return grounded.Result{
		Hits: []grounded.Hit{
			{URL: redirectURI + "1", Title: "Earth is an oblate spheroid", Snippet: "Measurements confirm Earth is round.", Domain: "nasa.gov"},
			{URL: redirectURI + "2", Title: "Shape of the Earth", Snippet: "Satellite geodesy shows a near-spherical planet.", Domain: "noaa.gov"},
		},
		SearchEntryPointHTML: `<div class="gsc-chip">shape of the earth</div>`,
		WebSearchQueries:     []string{"shape of the earth", "is the earth an oblate spheroid"},
	}
}

// respStrings pulls a string-array channel (web_search_queries) off a decoded
// response. A missing key returns nil — the honest "vendor issued none" shape.
func respStrings(t *testing.T, resp map[string]any, key string) []string {
	t.Helper()
	raw, ok := resp[key]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s is %T, want array", key, raw)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s entry is %T, want string", key, e)
		}
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// (1) ADR-231 D4 — the citation carries its DURABLE domain
// ---------------------------------------------------------------------------

func TestInvokeFactCheck_CitationsCarryDurableDomain(t *testing.T) {
	srv, id, _, _ := seedFactCheckServer(t, "Supported.", twoCitedGrounded())
	equipForInvoke(t, srv, id, "fact_check")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "fact_check",
		map[string]string{"claim": "The Earth is round."}))

	cites := respCitations(t, resp)
	if len(cites) != 2 {
		t.Fatalf("citations = %d, want 2", len(cites))
	}
	// The DURABLE surface. Without it A+ renders `c.domain || c.url` and the
	// learner is shown a 200-char expiring redirect URL as the source's name.
	if got := cites[0]["domain"]; got != "nasa.gov" {
		t.Errorf("citations[0].domain = %v, want nasa.gov (ADR-231 D4: the durable citation surface)", got)
	}
	if got := cites[1]["domain"]; got != "noaa.gov" {
		t.Errorf("citations[1].domain = %v, want noaa.gov", got)
	}
	// The uri still rides — it is the click-through, just not the identity.
	if got, _ := cites[0]["url"].(string); !strings.HasPrefix(got, redirectURI) {
		t.Errorf("citations[0].url = %q, want the redirect uri (the ephemeral click-through)", got)
	}
}

// ---------------------------------------------------------------------------
// (2) ADR-231 D5 — the issued queries reach the learner, and are never invented
// ---------------------------------------------------------------------------

func TestInvokeFactCheck_EmitsWebSearchQueries(t *testing.T) {
	srv, id, _, _ := seedFactCheckServer(t, "Supported.", twoCitedGrounded())
	equipForInvoke(t, srv, id, "fact_check")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "fact_check",
		map[string]string{"claim": "The Earth is round."}))

	got := respStrings(t, resp, "web_search_queries")
	want := []string{"shape of the earth", "is the earth an oblate spheroid"}
	if len(got) != len(want) {
		t.Fatalf("web_search_queries = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("web_search_queries[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A vendor result with NO issued queries must OMIT the key — never fabricate one
// (the same "no chip ⇒ no fabrication" rule the chip already obeys).
func TestInvokeFactCheck_NoQueriesOmitsTheKey(t *testing.T) {
	res := twoCitedGrounded()
	res.WebSearchQueries = nil
	srv, id, _, _ := seedFactCheckServer(t, "Supported.", res)
	equipForInvoke(t, srv, id, "fact_check")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "fact_check",
		map[string]string{"claim": "The Earth is round."}))

	if raw, present := resp["web_search_queries"]; present {
		t.Errorf("web_search_queries = %#v; want the key OMITTED when the vendor issued none", raw)
	}
}

// The no-citation HEDGE asserts no verdict and shows no sources and no chip
// (nothing grounded was shown ⇒ nothing to attribute). But it DID search — and
// the hedge prompt itself invites the learner to "rephrase the claim". Showing
// what was actually searched is what makes that invitation actionable, and it is
// transparency metadata, never knowledge. So the queries SURVIVE the hedge.
func TestInvokeFactCheck_HedgeShowsWhatWasSearchedButNoSourcesOrChip(t *testing.T) {
	res := grounded.Result{
		Hits:             nil, // zero citations ⇒ citation mandate ⇒ hedge
		WebSearchQueries: []string{"flat earth proof"},
	}
	srv, id, _, _ := seedFactCheckServer(t, "I could not find grounded sources.", res)
	equipForInvoke(t, srv, id, "fact_check")

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "fact_check",
		map[string]string{"claim": "The Earth is flat."}))

	if cites := respCitations(t, resp); cites != nil {
		t.Errorf("citations = %#v; the hedge must cite nothing", cites)
	}
	if _, present := resp["search_entry_point_html"]; present {
		t.Errorf("hedge must carry no chip (nothing grounded was shown ⇒ nothing to attribute)")
	}
	got := respStrings(t, resp, "web_search_queries")
	if len(got) != 1 || got[0] != "flat earth proof" {
		t.Errorf("web_search_queries = %#v, want [flat earth proof] — the hedge must still show what it searched", got)
	}
}

// ---------------------------------------------------------------------------
// (3) ADR-231 D4 — the research note PERSISTS the durable provenance
// ---------------------------------------------------------------------------

func TestInvokeWebResearch_NotePersistsDurableProvenance(t *testing.T) {
	srv, id, _, _, mem, _ := seedWebResearchServer(t,
		"NASA and NOAA agree the Earth is an oblate spheroid.", twoCitedGrounded(), nil)

	resp := decodeInvokeResp(t, invokeSkill(t, srv, id, "web_research",
		map[string]string{"direction": "shape of the Earth"}))

	if resp["recorded"] != true {
		t.Fatalf("recorded = %v, want true", resp["recorded"])
	}
	if len(mem.recorded) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(mem.recorded))
	}
	rec := mem.recorded[0]

	prov := rec.Provenance
	if prov == nil {
		t.Fatalf("RecordMemoryInput.Provenance is nil — a grounded research note MUST persist where it came from (ADR-231 D4); without it the note can never explain itself once the chip's links die")
	}

	// The queries: plain strings, never expire. This is the note's durable
	// "what I searched" line, long after every redirect uri is dead.
	if len(prov.WebSearchQueries) != 2 || prov.WebSearchQueries[0] != "shape of the earth" {
		t.Errorf("Provenance.WebSearchQueries = %#v, want the issued queries", prov.WebSearchQueries)
	}

	// The citations: domain + title + snippet — the DURABLE fields.
	if len(prov.Citations) != 2 {
		t.Fatalf("Provenance.Citations = %d, want 2", len(prov.Citations))
	}
	c0 := prov.Citations[0]
	if c0.Domain != "nasa.gov" || c0.Title != "Earth is an oblate spheroid" {
		t.Errorf("Provenance.Citations[0] = %+v, want the durable nasa.gov source", c0)
	}
	if !strings.Contains(c0.Snippet, "Earth is round") {
		t.Errorf("Provenance.Citations[0].Snippet = %q, want the grounded evidence", c0.Snippet)
	}

	// ⚠ THE POINT OF D4: the ephemeral redirect uri must NOT be persisted. A note
	// that stored it would rot to dead links in ~30 days — worse than storing
	// nothing, because it looks like a working source.
	for i, c := range prov.Citations {
		if strings.Contains(c.Domain+c.Title+c.Snippet, "grounding-api-redirect") {
			t.Errorf("Provenance.Citations[%d] leaks the EPHEMERAL redirect uri: %+v", i, c)
		}
	}

	// And it never pollutes the embedded prose — content_text is what gets
	// vector-embedded, so provenance riding in it would poison recall.
	if strings.Contains(rec.ContentText, "grounding-api-redirect") ||
		strings.Contains(rec.ContentText, "shape of the earth\n") {
		t.Errorf("ContentText = %q; provenance must NOT be smuggled into the embedded prose", rec.ContentText)
	}
}

// A non-Seeker memory_note skill (recap_scribe) is not grounded — it must record
// NO provenance, so the column stays NULL rather than carrying an empty husk.
func TestInvokeRecapScribe_NonGroundedNoteHasNoProvenance(t *testing.T) {
	engine := &fakeChatEngine{frames: chatFramesWithReply("You covered fractions today.")}
	// A recap over ZERO session rows deliberately mints no note (recordNote:false),
	// so seed one real turn — the point of this test is the note's NULL provenance.
	rows := []companionmind.EpisodicMemory{
		{ID: "m0", MemoryType: "chat_turn", Content: "Learner: what is 1/2 + 1/4?\nCompanion: three quarters",
			CreatedAt: time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)},
	}
	srv, id, mem, _ := recapScribeServer(t, engine, rows)

	_ = decodeInvokeResp(t, invokeSkill(t, srv, id, "recap_scribe", nil))

	if len(mem.recorded) != 1 {
		t.Fatalf("recorded %d rows, want 1 recap note", len(mem.recorded))
	}
	if prov := mem.recorded[0].Provenance; prov != nil {
		t.Errorf("recap note Provenance = %+v, want nil (not grounded ⇒ source_metadata stays NULL)", prov)
	}
}

// Compile-time pin: the domain provenance type is what the port carries, so the
// pg adapter can marshal it straight to the source_metadata JSONB column.
var _ = companion.RecordMemoryInput{Provenance: &companion.MemoryProvenance{
	WebSearchQueries: []string{"q"},
	Citations:        []companion.MemoryCitation{{Domain: "d", Title: "t", Snippet: "s"}},
}}
