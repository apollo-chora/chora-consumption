// grounded_test.go — P5 Far Sight (CHO-2017): the gateway grounded-search
// RESULT value types + the CITATION MANDATE primitives (ADR-220 D3). A grounded
// result whose citations are empty must be discardable fail-loud — never
// presented as companion knowledge — so the mandate is a pure, exhaustively
// testable predicate over the result, independent of any gateway wiring.
package grounded

import "testing"

func TestHit_IsCitation(t *testing.T) {
	cases := []struct {
		name string
		hit  Hit
		want bool
	}{
		{"url+title", Hit{URL: "https://x.test/a", Title: "A"}, true},
		{"url+title+snippet", Hit{URL: "https://x.test/a", Title: "A", Snippet: "s"}, true},
		{"missing url", Hit{Title: "A"}, false},
		{"missing title", Hit{URL: "https://x.test/a"}, false},
		{"blank-space url", Hit{URL: "   ", Title: "A"}, false},
		{"blank-space title", Hit{URL: "https://x.test/a", Title: "  "}, false},
		{"empty", Hit{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hit.IsCitation(); got != tc.want {
				t.Errorf("IsCitation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResult_CitedHits_FiltersNonCitations(t *testing.T) {
	r := Result{Hits: []Hit{
		{URL: "https://x.test/a", Title: "A"},
		{Title: "no url — not a citation"},
		{URL: "https://x.test/b", Title: "B", Snippet: "s"},
		{URL: "https://x.test/c"}, // no title — not a citation
	}}
	cited := r.CitedHits()
	if len(cited) != 2 {
		t.Fatalf("CitedHits() = %d, want 2 (only url+title hits survive): %+v", len(cited), cited)
	}
	if cited[0].Title != "A" || cited[1].Title != "B" {
		t.Errorf("CitedHits order/content = %+v, want [A, B]", cited)
	}
}

func TestResult_CitedHits_DedupsByURL(t *testing.T) {
	r := Result{Hits: []Hit{
		{URL: "https://x.test/a", Title: "A"},
		{URL: " https://x.test/a ", Title: "A-dup (trimmed URL collides)"},
		{URL: "https://x.test/b", Title: "B"},
	}}
	cited := r.CitedHits()
	if len(cited) != 2 {
		t.Fatalf("CitedHits() = %d, want 2 (dedup by trimmed URL): %+v", len(cited), cited)
	}
	if cited[0].Title != "A" {
		t.Errorf("first-seen wins on dedup: got %q, want A", cited[0].Title)
	}
}

func TestResult_HasCitations(t *testing.T) {
	empty := Result{}
	if empty.HasCitations() {
		t.Error("empty result must have NO citations (mandate: discard)")
	}
	noValid := Result{Hits: []Hit{{Title: "no url"}, {URL: "https://x.test/z"}}}
	if noValid.HasCitations() {
		t.Error("a result with only invalid hits must have NO citations")
	}
	one := Result{Hits: []Hit{{URL: "https://x.test/a", Title: "A"}}}
	if !one.HasCitations() {
		t.Error("a result with one url+title hit HAS citations")
	}
}

func TestResult_CitedHits_TrimsFields(t *testing.T) {
	r := Result{Hits: []Hit{{URL: "  https://x.test/a  ", Title: "  A  ", Snippet: "  s  "}}}
	cited := r.CitedHits()
	if len(cited) != 1 {
		t.Fatalf("want 1 cited hit; got %d", len(cited))
	}
	if cited[0].URL != "https://x.test/a" || cited[0].Title != "A" || cited[0].Snippet != "s" {
		t.Errorf("CitedHits must trim fields: got %+v", cited[0])
	}
}

func TestResult_CitedHits_PreservesAndTrimsDomain(t *testing.T) {
	// ADR-231 D4: the redirect `uri` is a Google grounding-api-redirect link that
	// expires (~30d); the DURABLE citation surface is the publisher DOMAIN. The
	// mandate still hinges on url+title (IsCitation), but CitedHits must carry
	// Domain through — trimmed — so the web_research memory-note sink persists
	// domain+title+snippet (not the ephemeral uri).
	r := Result{Hits: []Hit{{
		URL:     "  https://vertexaisearch.cloud.google.com/grounding-api-redirect/x  ",
		Title:   "  Shape of the Earth  ",
		Snippet: "  near-spherical  ",
		Domain:  "  nature.com  ",
	}}}
	cited := r.CitedHits()
	if len(cited) != 1 {
		t.Fatalf("want 1 cited hit; got %d", len(cited))
	}
	if cited[0].Domain != "nature.com" {
		t.Errorf("CitedHits must carry the trimmed publisher domain (ADR-231 D4): got %q, want nature.com", cited[0].Domain)
	}
	// A hit with a domain but no title is still NOT a citation (the mandate is
	// url+title; domain never rescues an unnameable source).
	noTitle := Result{Hits: []Hit{{URL: "https://x.test/a", Domain: "x.test"}}}
	if noTitle.HasCitations() {
		t.Error("domain must not make a title-less hit a citation")
	}
}

func TestQuery_Validate(t *testing.T) {
	if err := (Query{Directive: "is the earth round?", MaxHits: 5}).Validate(); err != nil {
		t.Errorf("valid query errored: %v", err)
	}
	if err := (Query{Directive: "   ", MaxHits: 5}).Validate(); err == nil {
		t.Error("a blank directive must fail validation (never a directionless egress call)")
	}
	if err := (Query{Directive: "x", MaxHits: 0}).Validate(); err == nil {
		t.Error("MaxHits<=0 must fail validation (a grounded search must be bounded)")
	}
}
