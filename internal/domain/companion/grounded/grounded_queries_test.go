// grounded_queries_test.go — CHO-2179: Result.SearchQueries(), the DURABLE
// half of grounded transparency (ADR-231 D4/D5).
//
// The Google Search-Suggestions chip is the ToS display obligation, but its
// links ride the SAME grounding-redirect infrastructure ADR-231 D4 says expires
// (~30 days) — so a chip persisted on a memory note rots to dead links. The
// issued web-search queries are plain strings that NEVER expire: they are what
// lets a note explain itself long after the chip is dead ("what I searched").
//
// RED-first: the wire value is vendor text (padded / duplicated / blank), so
// normalisation belongs HERE next to CitedHits — one rule, every adapter.
package grounded

import (
	"strings"
	"testing"
)

func TestResult_SearchQueries_TrimsDropsBlanksAndDeduplicates(t *testing.T) {
	r := Result{WebSearchQueries: []string{
		"  who is the prime minister of singapore  ", // padded
		"",                                       // blank
		"   ",                                    // whitespace-only
		"Lawrence Wong Prime Minister",           // distinct
		"who is the prime minister of singapore", // duplicate of #1 once trimmed
	}}

	got := r.SearchQueries()

	want := []string{
		"who is the prime minister of singapore",
		"Lawrence Wong Prime Minister",
	}
	if len(got) != len(want) {
		t.Fatalf("SearchQueries() = %#v (len %d), want %#v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SearchQueries()[%d] = %q, want %q (first-seen order preserved)", i, got[i], want[i])
		}
	}
}

func TestResult_SearchQueries_EmptyWhenVendorIssuedNone(t *testing.T) {
	// A grounded result with no issued queries must yield an EMPTY slice, never a
	// fabricated one — "no chip ⇒ no fabrication" applies to the queries too.
	for name, r := range map[string]Result{
		"nil slice":   {},
		"empty slice": {WebSearchQueries: []string{}},
		"blanks only": {WebSearchQueries: []string{"", "   "}},
	} {
		if got := r.SearchQueries(); len(got) != 0 {
			t.Errorf("%s: SearchQueries() = %#v, want empty", name, got)
		}
	}
}

// The queries are TRANSPARENCY METADATA, never knowledge (the proto field
// comment is explicit). This pins the one property that keeps them safe to
// render: SearchQueries never invents a query the vendor did not issue.
func TestResult_SearchQueries_NeverInventsAQuery(t *testing.T) {
	r := Result{
		Hits:                 []Hit{{URL: "https://nasa.gov/e", Title: "Earth", Domain: "nasa.gov"}},
		SearchEntryPointHTML: `<div class="chip">shape of the earth</div>`,
	}
	// Hits + a chip are present, but the vendor issued NO queries.
	if got := r.SearchQueries(); len(got) != 0 {
		t.Fatalf("SearchQueries() = %#v, want empty — queries must never be back-derived from hits or the chip", got)
	}
}

// Domain is the DURABLE citation surface (ADR-231 D4): CitedHits must carry it
// through trimmed, because the memory-note sink persists domain (never the
// ~30-day redirect uri) and the learner is shown "domain — title".
func TestResult_CitedHits_CarriesDurableDomain(t *testing.T) {
	r := Result{Hits: []Hit{{
		URL:    "https://vertexaisearch.cloud.google.com/grounding-api-redirect/AbC123",
		Title:  "  Shape of the Earth  ",
		Domain: "  nasa.gov  ",
	}}}

	cited := r.CitedHits()
	if len(cited) != 1 {
		t.Fatalf("CitedHits() = %d, want 1", len(cited))
	}
	if cited[0].Domain != "nasa.gov" {
		t.Errorf("CitedHits()[0].Domain = %q, want %q (trimmed, durable)", cited[0].Domain, "nasa.gov")
	}
	if strings.Contains(cited[0].Domain, "grounding-api-redirect") {
		t.Errorf("Domain must never be the ephemeral redirect uri: %q", cited[0].Domain)
	}
}
