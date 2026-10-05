// suggestion_catalogue_relevance_test.go - ADR-245 D3: the ADR-244 D2 catalogue
// is ranked by topic affinity to the goal theme, and the ranking CANNOT starve
// it.
//
// The failure this guards against is specific and was measured live 2026-07-20:
// a hard filter on topic_tags @> {fractions} leaves 0 of 16 entitled atoms on
// the walk learner's Fractions goal, because 86% of the corpus is untagged. An
// empty catalogue then hits the ADR-244 D3 fail-closed gate, which drops EVERY
// atom_ref and reintroduces the exact bug ADR-244 shipped to fix. So the
// contract asserted here is not "the filter is well tuned", it is "there is no
// filter": ranking reorders and truncation is bounded by the pre-existing cap.
package http

import (
	"fmt"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// maxCatalogueForTest reads the production cap rather than restating it, so a
// change to the cap cannot leave this test silently asserting a stale number.
const maxCatalogueForTest = events.MaxAtomCatalogueInSuggestionRequest

// scPadID mints a distinct well-formed atom id for the truncation fixture.
func scPadID(i int) string { return fmt.Sprintf("01980000-0000-7000-9000-0000000c%04d", i) }

const (
	scAtomFractions = "01980000-0000-7000-9000-00000000bb01"
	scAtomPhoto     = "01980000-0000-7000-9000-00000000bb02"
	scAtomScrum     = "01980000-0000-7000-9000-00000000bb03"
	scAtomTagged    = "01980000-0000-7000-9000-00000000bb04"
)

// scFixture mirrors the shape of the live catalogue that exposed the gap: a
// mostly off-theme, mostly UNTAGGED entitled set, whose single best candidate
// carries no tags at all.
func scFixture() (*aaStubPaths, *aaStubIndex) {
	idx := &aaStubIndex{byID: map[string]*atom_index.AtomIndex{
		scAtomPhoto: {AtomID: scAtomPhoto, TenantID: fmTenantID, Title: "Photosynthesis and the Calvin cycle",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished,
			TopicTags: []string{"biology"}},
		scAtomScrum: {AtomID: scAtomScrum, TenantID: fmTenantID, Title: "Scrum ceremonies",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished,
			TopicTags: []string{"agile"}},
		// UNTAGGED and squarely on theme: the atom a tag filter drops first.
		scAtomFractions: {AtomID: scAtomFractions, TenantID: fmTenantID, Title: "Comparing Fractions: 2/3 vs 3/5",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished},
		scAtomTagged: {AtomID: scAtomTagged, TenantID: fmTenantID, Title: "Unit 4 review",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished,
			TopicTags: []string{"fractions"}},
	}}
	paths := &aaStubPaths{paths: []*learning_path.LearningPath{
		{AtomIDs: []string{scAtomPhoto, scAtomScrum, scAtomFractions, scAtomTagged}},
	}}
	return paths, idx
}

// THE NON-STARVATION CONTRACT. A theme that matches NOTHING in the entitled set
// must still yield the entire entitled set. If this ever fails, the fog receives
// a short or empty catalogue, the D3 gate drops every ref it cannot vouch for,
// and the Companion silently stops citing atoms.
func TestResolveSuggestionCatalogue_UnmatchedThemeKeepsEveryAtom(t *testing.T) {
	paths, idx := scFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}
	full := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "", "", nil)
	scoped := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "Quantum Chromodynamics", "", nil)
	if len(scoped) != len(full) {
		t.Fatalf("themed catalogue has %d atoms, unthemed has %d: ranking must never drop an atom",
			len(scoped), len(full))
	}
	if len(scoped) == 0 {
		t.Fatal("catalogue is empty: the fixture itself is broken (positive control)")
	}
}

// The degenerate case the whole design hinges on: no topic identity resolvable
// means the catalogue is byte-for-byte what it was before this ADR, in the same
// order, with no relevance claimed on any entry.
func TestResolveSuggestionCatalogue_NoThemeDegradesToPreADR245(t *testing.T) {
	paths, idx := scFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}
	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "", "", nil)

	want := []string{scAtomPhoto, scAtomScrum, scAtomFractions, scAtomTagged}
	if len(got) != len(want) {
		t.Fatalf("got %d atoms, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].AtomID != id {
			t.Errorf("position %d = %s, want %s: no theme means no reordering", i, got[i].AtomID, id)
		}
		if got[i].Relevance != "" {
			t.Errorf("position %d claims relevance %q with no theme to score against",
				i, got[i].Relevance)
		}
	}
}

// With a theme, the on-theme atoms sort to the front and every entry is banded.
// The UNTAGGED Fractions atom must be present and on-theme; the tagged one must
// outrank it, because a curated tag is stronger evidence than a title word.
func TestResolveSuggestionCatalogue_RanksOnThemeFirstAndBandsEveryEntry(t *testing.T) {
	paths, idx := scFixture()
	s := &ExtServer{Paths: paths, AtomIndex: idx}
	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "Fractions", "", nil)

	if len(got) != 4 {
		t.Fatalf("got %d atoms, want all 4", len(got))
	}
	if got[0].AtomID != scAtomTagged || got[1].AtomID != scAtomFractions {
		t.Errorf("front of catalogue = %s,%s; want tagged %s then untagged-on-theme %s",
			got[0].AtomID, got[1].AtomID, scAtomTagged, scAtomFractions)
	}
	for i, a := range got {
		if a.Relevance == "" {
			t.Errorf("position %d (%s) is unbanded: the prompt cannot distinguish it", i, a.AtomID)
		}
	}
	if got[0].Relevance != "on_theme" || got[1].Relevance != "on_theme" {
		t.Errorf("bands = %q,%q; want both on_theme", got[0].Relevance, got[1].Relevance)
	}
	// The cross-domain atoms stay PRESENT, banded off-theme. Presence is the
	// point: ADR-245 D3 scopes by ordering and instruction, never by removal.
	tail := map[string]string{got[2].AtomID: got[2].Relevance, got[3].AtomID: got[3].Relevance}
	for _, id := range []string{scAtomPhoto, scAtomScrum} {
		band, ok := tail[id]
		if !ok {
			t.Errorf("off-theme atom %s was DROPPED from the catalogue", id)
			continue
		}
		if band != "off_theme" {
			t.Errorf("atom %s banded %q, want off_theme", id, band)
		}
	}
}

// The focal concept sharpens the theme: on a Fractions goal focused on
// "Denominator", an atom about denominators must outrank a generic fractions
// atom. This proves the focal title reaches the scorer, not just the map theme.
func TestResolveSuggestionCatalogue_FocalTitleSharpensRanking(t *testing.T) {
	idx := &aaStubIndex{byID: map[string]*atom_index.AtomIndex{
		scAtomFractions: {AtomID: scAtomFractions, TenantID: fmTenantID, Title: "Fractions on a number line",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished},
		scAtomTagged: {AtomID: scAtomTagged, TenantID: fmTenantID, Title: "Fractions and denominators",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished},
	}}
	paths := &aaStubPaths{paths: []*learning_path.LearningPath{
		{AtomIDs: []string{scAtomFractions, scAtomTagged}},
	}}
	s := &ExtServer{Paths: paths, AtomIndex: idx}
	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "Fractions", "Denominator", nil)
	if len(got) != 2 {
		t.Fatalf("got %d atoms, want 2", len(got))
	}
	if got[0].AtomID != scAtomTagged {
		t.Errorf("front = %s, want %s: the focal concept must reach the scorer",
			got[0].AtomID, scAtomTagged)
	}
}

// Truncation is where ranking earns its keep: when the entitled set exceeds the
// cap, the atoms that survive must be the most relevant ones, not whichever
// happened to come first in path order. Before ADR-245 the tail was arbitrary.
func TestResolveSuggestionCatalogue_TruncationKeepsTheMostRelevant(t *testing.T) {
	byID := map[string]*atom_index.AtomIndex{}
	ids := make([]string, 0, maxCatalogueForTest+1)
	// One on-theme atom, deliberately placed LAST so path order would drop it.
	for i := 0; i < maxCatalogueForTest; i++ {
		id := scPadID(i)
		byID[id] = &atom_index.AtomIndex{AtomID: id, TenantID: fmTenantID, Title: "Photosynthesis and the Calvin cycle",
			AtomType: "mcq", CorrectOptionID: "opt-1", Status: atom_index.StatusPublished}
		ids = append(ids, id)
	}
	byID[scAtomFractions] = &atom_index.AtomIndex{AtomID: scAtomFractions, TenantID: fmTenantID,
		Title: "Comparing Fractions: 2/3 vs 3/5", AtomType: "mcq", CorrectOptionID: "opt-1",
		Status: atom_index.StatusPublished}
	ids = append(ids, scAtomFractions)

	s := &ExtServer{
		Paths:     &aaStubPaths{paths: []*learning_path.LearningPath{{AtomIDs: ids}}},
		AtomIndex: &aaStubIndex{byID: byID},
	}
	got := s.ResolveSuggestionCatalogue(t.Context(), fmTenantID, fmGCID, "Fractions", "", nil)
	if len(got) != maxCatalogueForTest {
		t.Fatalf("got %d atoms, want the cap %d", len(got), maxCatalogueForTest)
	}
	if got[0].AtomID != scAtomFractions {
		t.Errorf("front = %s, want the on-theme atom %s to survive truncation",
			got[0].AtomID, scAtomFractions)
	}
}
