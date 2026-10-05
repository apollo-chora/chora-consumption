//go:build eval_fixture

// grounded_eval_fixture_on.go — EVAL-ONLY (build tag `eval_fixture`) test-double
// GroundedSearchPort. Compiled ONLY into an image built with -tags eval_fixture;
// the production build has the no-op override in grounded_eval_fixture_off.go, so
// this deterministic fixture can NEVER be wired into a prod consumption binary
// (the hard "no stub in prod" guarantee — the fixture code is physically absent).
//
// Why it exists: the Seeker skills (fact_check / web_research) reach the web ONLY
// through chora-model-gateway's grounded-search surface, so their ADR-174 §8
// groundedness gate cannot pin a DB pool the way kg_explore does. The gate instead
// runs against this fixture, which returns each golden/adversarial row's PINNED
// cited sources keyed on the (trimmed) directive — so facts_groundedness scores
// each verdict/note against a KNOWN source set (a mis-pinned source reads as
// fabrication, exactly the property the gate measures).
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
)

// envGroundedEvalFixturePath names the fixture file the eval build loads. Unset
// (even in the eval build) ⇒ the override is inert and the real gateway client
// wins — so the eval image degrades to normal behaviour if the fixture is absent.
const envGroundedEvalFixturePath = "CHORA_GROUNDED_SEARCH_EVAL_FIXTURE_PATH"

// groundedEvalFixture maps a screened directive → the pinned grounded result the
// gateway surface WOULD have returned for it.
type groundedEvalFixture struct {
	byDirective map[string]grounded.Result
}

// groundedFixtureFile is the on-disk fixture schema (mounted via ConfigMap in the
// eval pod). A row with an empty hits list is the CITATION-MANDATE (hedge) case.
type groundedFixtureFile struct {
	Rows []struct {
		Directive string `json:"directive"`
		Hits      []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Domain  string `json:"domain"`
		} `json:"hits"`
	} `json:"rows"`
}

// loadGroundedFixture reads + parses the fixture file fail-loud (a bad fixture on
// the eval path must never silently degrade into an unpinned run).
func loadGroundedFixture(path string) (*groundedEvalFixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("grounded eval fixture: read %q: %w", path, err)
	}
	var f groundedFixtureFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("grounded eval fixture: parse %q: %w", path, err)
	}
	if len(f.Rows) == 0 {
		return nil, fmt.Errorf("grounded eval fixture %q: no rows", path)
	}
	byDir := make(map[string]grounded.Result, len(f.Rows))
	for _, r := range f.Rows {
		key := strings.TrimSpace(r.Directive)
		if key == "" {
			return nil, fmt.Errorf("grounded eval fixture %q: a row has an empty directive", path)
		}
		if _, dup := byDir[key]; dup {
			return nil, fmt.Errorf("grounded eval fixture %q: duplicate directive %q", path, key)
		}
		hits := make([]grounded.Hit, 0, len(r.Hits))
		for _, h := range r.Hits {
			hits = append(hits, grounded.Hit{URL: h.URL, Title: h.Title, Snippet: h.Snippet, Domain: h.Domain})
		}
		byDir[key] = grounded.Result{Hits: hits}
	}
	return &groundedEvalFixture{byDirective: byDir}, nil
}

// SearchGround implements GroundedSearchPort against the pinned fixture. An
// unmapped directive is fail-loud (golden/fixture drift surfaces as a 502 at the
// runner) — NEVER a silent empty result that would masquerade as an honest hedge
// and mask the mismatch the gate depends on.
func (g *groundedEvalFixture) SearchGround(_ context.Context, q grounded.Query) (grounded.Result, error) {
	key := strings.TrimSpace(q.Directive)
	res, ok := g.byDirective[key]
	if !ok {
		return grounded.Result{}, fmt.Errorf("grounded eval fixture: no pinned sources for directive %q (golden/fixture drift)", key)
	}
	return res, nil
}

// groundedFixtureQuery builds a minimal valid Query for the fixture (tests only).
func groundedFixtureQuery(directive string) grounded.Query {
	return grounded.Query{
		TenantID:   "11111111-1111-7111-8111-111111111111",
		GCID:       "00000000-0000-7000-8000-000000001999",
		Directive:  directive,
		MaxHits:    5,
		ActionCode: "companion_skill_fact_check",
	}
}

// evalGroundedSearchOverride returns a fixture port when the fixture path env is
// set, else (nil, nil) so the caller falls through to the real gateway client. A
// set-but-unreadable path is fail-loud.
func evalGroundedSearchOverride(getenv func(string) string) (GroundedSearchPort, error) {
	path := strings.TrimSpace(getenv(envGroundedEvalFixturePath))
	if path == "" {
		return nil, nil
	}
	fx, err := loadGroundedFixture(path)
	if err != nil {
		return nil, err
	}
	return fx, nil
}
