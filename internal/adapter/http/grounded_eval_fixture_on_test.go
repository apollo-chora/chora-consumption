//go:build eval_fixture

// grounded_eval_fixture_on_test.go — unit tests for the EVAL-ONLY (build tag
// `eval_fixture`) test-double GroundedSearchPort. These compile + run ONLY under
// `go test -tags eval_fixture` — the production build (no tag) has neither the
// fixture nor these tests, so a stub can never leak into a prod binary.
//
// The fixture backs the ADR-174 §8 groundedness gate for the Seekers
// (fact_check / web_research): their grounding is gateway-sourced (not a DB
// pool), so the gate needs a PINNED source set per golden/adversarial row. This
// fixture returns each row's stated sources keyed on the (trimmed) directive.
package http

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFixture writes a fixture JSON to a temp file and returns its path.
func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "grounded_fixture.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

const sampleFixture = `{
  "rows": [
    {
      "directive": "The Earth is an oblate spheroid",
      "hits": [
        {"url": "https://nasa.gov/earth", "title": "Earth is an oblate spheroid", "snippet": "Measurements confirm Earth is round, slightly flattened at the poles.", "domain": "nasa.gov"},
        {"url": "https://noaa.gov/shape", "title": "Shape of the Earth", "snippet": "Satellite geodesy shows a near-spherical planet.", "domain": "noaa.gov"}
      ]
    },
    {
      "directive": "Aliens secretly built the Eiffel Tower in 1889",
      "hits": []
    }
  ]
}`

func TestGroundedEvalFixture_ReturnsPinnedHits(t *testing.T) {
	fx, err := loadGroundedFixture(writeFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := fx.SearchGround(context.Background(), groundedFixtureQuery("The Earth is an oblate spheroid"))
	if err != nil {
		t.Fatalf("SearchGround: %v", err)
	}
	cited := res.CitedHits()
	if len(cited) != 2 {
		t.Fatalf("cited hits = %d, want 2", len(cited))
	}
	if cited[0].URL != "https://nasa.gov/earth" || cited[0].Domain != "nasa.gov" {
		t.Fatalf("hit[0] = %+v, want NASA earth + domain", cited[0])
	}
	if cited[1].Title != "Shape of the Earth" {
		t.Fatalf("hit[1].Title = %q, want NOAA shape", cited[1].Title)
	}
}

// A whitespace-padded directive must match its trimmed fixture key (the runner
// trims claim/direction before building the Query, but be robust).
func TestGroundedEvalFixture_TrimsDirective(t *testing.T) {
	fx, err := loadGroundedFixture(writeFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := fx.SearchGround(context.Background(), groundedFixtureQuery("  The Earth is an oblate spheroid  "))
	if err != nil {
		t.Fatalf("SearchGround (padded): %v", err)
	}
	if len(res.CitedHits()) != 2 {
		t.Fatalf("padded directive did not match trimmed key")
	}
}

// A zero-hit row is the CITATION MANDATE case: an empty result whose
// HasCitations is false (⇒ the runner hedges, no verdict/note).
func TestGroundedEvalFixture_ZeroHitsRow(t *testing.T) {
	fx, err := loadGroundedFixture(writeFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res, err := fx.SearchGround(context.Background(), groundedFixtureQuery("Aliens secretly built the Eiffel Tower in 1889"))
	if err != nil {
		t.Fatalf("SearchGround (zero): %v", err)
	}
	if res.HasCitations() {
		t.Fatalf("zero-hit row must have no citations (hedge case)")
	}
}

// An unknown directive is fail-loud: a golden/fixture drift must surface as an
// error (which the runner turns into a 502), NEVER a silent empty result that
// would masquerade as an honest hedge and mask the mismatch.
func TestGroundedEvalFixture_UnknownDirectiveFailsLoud(t *testing.T) {
	fx, err := loadGroundedFixture(writeFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := fx.SearchGround(context.Background(), groundedFixtureQuery("an unmapped claim")); err == nil {
		t.Fatalf("unknown directive must fail loud, got nil error")
	}
}

func TestGroundedEvalFixture_MalformedJSONFailsLoud(t *testing.T) {
	if _, err := loadGroundedFixture(writeFixture(t, "{ not json")); err == nil {
		t.Fatalf("malformed fixture must fail loud")
	}
}

func TestGroundedEvalFixture_MissingFileFailsLoud(t *testing.T) {
	if _, err := loadGroundedFixture(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatalf("missing fixture file must fail loud")
	}
}

// The override seam: unset env ⇒ (nil, nil) so the caller falls through to the
// real gateway client; set env ⇒ a non-nil fixture port.
func TestEvalGroundedSearchOverride_EnvGated(t *testing.T) {
	getenvEmpty := func(string) string { return "" }
	port, err := evalGroundedSearchOverride(getenvEmpty)
	if err != nil || port != nil {
		t.Fatalf("unset env: want (nil,nil), got (%v,%v)", port, err)
	}

	path := writeFixture(t, sampleFixture)
	getenvSet := func(k string) string {
		if k == envGroundedEvalFixturePath {
			return path
		}
		return ""
	}
	port, err = evalGroundedSearchOverride(getenvSet)
	if err != nil {
		t.Fatalf("set env: unexpected err %v", err)
	}
	if port == nil {
		t.Fatalf("set env: want non-nil fixture port")
	}
}

func TestEvalGroundedSearchOverride_BadPathFailsLoud(t *testing.T) {
	getenvBad := func(k string) string {
		if k == envGroundedEvalFixturePath {
			return "/no/such/fixture.json"
		}
		return ""
	}
	if _, err := evalGroundedSearchOverride(getenvBad); err == nil {
		t.Fatalf("set-but-unreadable fixture path must fail loud")
	}
}
