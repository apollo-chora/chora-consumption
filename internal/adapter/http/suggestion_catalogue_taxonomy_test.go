// suggestion_catalogue_taxonomy_test.go - ADR-245 D1.2 at the adapter seam.
//
// The theme's taxonomy anchor is derived from the atoms ALREADY ATTACHED to the
// focal concept. Those refs already reach both producers, so this needs no new
// fetch, but it does widen ResolveSuggestionCatalogue's signature.
//
// ⚠ THE TAXONOMY PATH IS DORMANT IN PRODUCTION TODAY. Wire field 7 IS populated,
// but with freeform content tags: chora-creation falls back to the composer's
// "tags" when no explicit topic_node_ids is set (protomarshal.go:291-293), and
// nothing repo-wide sets that key. So topic_tags carries WORDS, no UUID-shaped
// anchor resolves, and the taxonomy tier never fires against live data. Tests here
// SIMULATE CHO-2149 by seeding atom_index.TopicTags with canonical UUIDs, which
// is the only way to exercise the primary tier before the producer ships.
// ADR-244 §5 shipped a decision that was BUILT-but-INERT and could not tell the
// difference; a test that never exercises the branch would repeat that.
package http

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// Canonical topic_node_ids, shaped exactly as CHO-2149 would deliver them.
const (
	txFractions = "01970000-0000-7000-a000-00000000f001"
	txBiology   = "01970000-0000-7000-a000-00000000f002"
)

const (
	txAtomFocal   = "01980000-0000-7000-9000-00000000cc01" // attached to the concept
	txAtomClassed = "01980000-0000-7000-9000-00000000cc02" // classified, matches
	txAtomOther   = "01980000-0000-7000-9000-00000000cc03" // classified elsewhere
	txAtomPlain   = "01980000-0000-7000-9000-00000000cc04" // unclassified
)

// txFixture simulates a post-CHO-2149 corpus: some atoms carry topic_node_ids
// in topic_tags (which is where protodecode.go:730 puts them), some do not.
func txFixture() (*aaStubPaths, *aaStubIndex) {
	idx := &aaStubIndex{byID: map[string]*atom_index.AtomIndex{
		// The atom the learner already attached to the focal concept. Its
		// classification IS the theme's taxonomy anchor.
		txAtomFocal: {AtomID: txAtomFocal, TenantID: fmTenantID, Title: "Intro unit",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished,
			TopicTags: []string{txFractions}},
		// Classified under the same topic, but with a title that says nothing.
		// Only the taxonomy tier can find this one.
		txAtomClassed: {AtomID: txAtomClassed, TenantID: fmTenantID, Title: "Unit 4 review",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished,
			TopicTags: []string{txFractions}},
		txAtomOther: {AtomID: txAtomOther, TenantID: fmTenantID, Title: "Photosynthesis",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished,
			TopicTags: []string{txBiology}},
		txAtomPlain: {AtomID: txAtomPlain, TenantID: fmTenantID, Title: "Road safety",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished},
	}}
	paths := &aaStubPaths{paths: []*learning_path.LearningPath{
		{AtomIDs: []string{txAtomOther, txAtomPlain, txAtomClassed, txAtomFocal}},
	}}
	return paths, idx
}

// THE PRIMARY TIER, exercised. The focal concept's attached atom is classified
// under txFractions; the catalogue must promote the OTHER atom sharing that
// classification, whose title alone would never have found it.
func TestResolveSuggestionCatalogue_TaxonomyAnchorPromotesClassifiedAtom(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID,
		"", "", []string{txAtomFocal})

	if len(got) != 4 {
		t.Fatalf("got %d atoms, want all 4", len(got))
	}
	// "Unit 4 review" has no lexical signal at all. Only the taxonomy tier can
	// put it first, so its position IS the proof the tier fired.
	if got[0].AtomID != txAtomClassed {
		t.Fatalf("front = %s, want the taxonomy-matched %s: the taxonomy tier "+
			"did not fire (its title carries no lexical signal)", got[0].AtomID, txAtomClassed)
	}
	if got[0].Relevance != "on_theme" {
		t.Errorf("front band = %q, want on_theme", got[0].Relevance)
	}
}

// CONDITION 2a - focal concept with NO attached atoms. There are ~45 legacy
// empty nodes, so this is the NORMAL case, not an edge case. An empty taxonomy
// anchor must fall back to lexical, never to an empty catalogue.
func TestResolveSuggestionCatalogue_EmptyFocalAtomsFallsBackToLexical(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	for name, refs := range map[string][]string{"nil": nil, "empty": {}} {
		got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID,
			"Road safety", "", refs)
		if len(got) != 4 {
			t.Fatalf("%s: got %d atoms, want all 4: an absent anchor must not shorten", name, len(got))
		}
		// Lexical still works: "Road safety" ranks its atom first.
		if got[0].AtomID != txAtomPlain {
			t.Errorf("%s: front = %s, want the lexical match %s", name, got[0].AtomID, txAtomPlain)
		}
	}
}

// CONDITION 2b - a whole-map request has NO focal concept at all: no refs, no
// focal title. It must fall back cleanly rather than erroring or producing an
// empty set.
func TestResolveSuggestionCatalogue_WholeMapRequestFallsBackCleanly(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "", "", nil)
	if len(got) != 4 {
		t.Fatalf("got %d atoms, want all 4", len(got))
	}
	for i, a := range got {
		if a.Relevance != "" {
			t.Errorf("position %d banded %q with nothing to score against", i, a.Relevance)
		}
	}
}

// A focal ref the projection does not know is an honest omission, not an error
// and not a phantom anchor.
func TestResolveSuggestionCatalogue_UnprojectedFocalRefIsSkipped(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID,
		"", "", []string{"01980000-0000-7000-9000-0000000000ff"})
	if len(got) != 4 {
		t.Fatalf("got %d atoms, want all 4", len(got))
	}
}

// CONDITION 1 at the adapter seam: no-starve across all three taxonomy paths.
// A taxonomy hit must never be able to shorten the catalogue.
func TestResolveSuggestionCatalogue_NeverStarvesAcrossTaxonomyPaths(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	cases := map[string]struct {
		theme string
		refs  []string
	}{
		"taxonomy matched":               {"", []string{txAtomFocal}},
		"taxonomy present but unmatched": {"", []string{txAtomOther}},
		"taxonomy absent, lexical only":  {"Fractions", nil},
		"nothing at all":                 {"", nil},
	}
	for name, c := range cases {
		got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, c.theme, "", c.refs)
		if len(got) != 4 {
			t.Errorf("%s: got %d atoms, want all 4", name, len(got))
		}
	}
}

// THE SENTINEL. resolveMapTheme returns the literal "discovery" when a goal has
// neither a root concept title nor a NorthStarNote. Verified before this fix:
// theme "discovery" scored an unrelated atom titled "Discovery of penicillin" at
// 0.600 / on_theme. That is not a starvation bug, it is worse in one respect -
// it produces a CONFIDENTLY WRONG ordering and the prompt then tells the model
// to prefer that atom.
//
// The sentinel must therefore be inert. A goal genuinely titled "Discovery"
// degrades to unscored, which is the safe direction (today's behaviour) rather
// than a confident error.
func TestResolveSuggestionCatalogue_MapThemeSentinelIsInert(t *testing.T) {
	idx := &aaStubIndex{byID: map[string]*atom_index.AtomIndex{
		txAtomPlain: {AtomID: txAtomPlain, TenantID: fmTenantID, Title: "Discovery of penicillin",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished},
		txAtomOther: {AtomID: txAtomOther, TenantID: fmTenantID, Title: "Photosynthesis",
			AtomType: "mcq", CorrectOptionID: "o1", Status: atom_index.StatusPublished},
	}}
	paths := &aaStubPaths{paths: []*learning_path.LearningPath{
		{AtomIDs: []string{txAtomPlain, txAtomOther}},
	}}
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "discovery", "", nil)
	if len(got) != 2 {
		t.Fatalf("got %d atoms, want 2", len(got))
	}
	for i, a := range got {
		if a.Relevance != "" {
			t.Errorf("position %d (%s) banded %q: the resolveMapTheme sentinel "+
				"was treated as a real topic", i, a.AtomID, a.Relevance)
		}
	}
}

// THE GUARD IS DERIVED FROM BEHAVIOUR, NOT FROM A RESTATED LITERAL.
//
// `mapThemeSentinel` hand-restates a value that lives as a bare `return
// "discovery"` in two other files (companion_acquire_handler.go and the campaign
// subscriber's resolveTheme mirror), neither of which this lane owns. A guard
// keyed on a copied literal is the enum-map-drift shape: rename the sentinel at
// source and this guard silently stops matching while every test stays green.
//
// So this test asks the REAL resolver what a themeless goal produces and
// asserts the guard neutralises whatever comes back. Change the sentinel at
// source and this fails, which is the point.
func TestMapThemeSentinel_MatchesWhatTheRealResolverReturns(t *testing.T) {
	s := &ExtServer{} // no Concepts repo: forces the fallback chain to its end
	themeless := &goal.Goal{}

	actual := s.resolveMapTheme(t.Context(), fmTenantID, fmGCID, themeless)
	if actual == "" {
		t.Fatal("resolveMapTheme returned empty; this test can no longer detect drift")
	}
	if scopedMapTheme(actual) != "" {
		t.Errorf("resolveMapTheme returns %q for a themeless goal, but the guard "+
			"does not neutralise it (mapThemeSentinel = %q). The sentinel changed "+
			"at source and this guard is now decorative.", actual, mapThemeSentinel)
	}
}

// The sentinel must not silence a real focal concept. When the map theme is the
// sentinel but a focal title exists, the focal title still scopes the catalogue.
func TestResolveSuggestionCatalogue_SentinelDoesNotSilenceFocalTitle(t *testing.T) {
	paths, idx := txFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}

	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID,
		"discovery", "Photosynthesis", nil)
	if got[0].AtomID != txAtomOther {
		t.Errorf("front = %s, want %s: the sentinel discarded a usable focal title",
			got[0].AtomID, txAtomOther)
	}
}
