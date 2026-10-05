// topic_taxonomy_test.go - ADR-245 D1.2: the taxonomy tier.
//
// chora-creation ships a real tenant taxonomy (`topic_nodes`, CHO-2275/2276)
// and propagates it on atom events as `topic_node_ids`, which chora-consumption
// decodes into `atom_index.TopicTags` (protodecode.go:730). Matching those ids
// is scoping by CONSTRUCTION rather than by string similarity.
//
// THE INSIGHT THAT MAKES IT REACHABLE: you never need the taxonomy NAME. The
// names live in chora_creation and cross-DB reads are forbidden, which looked
// like a blocker until the framing was questioned. Comparing id to id needs no
// name at all, and the THEME side gets its ids from the focal concept's
// ALREADY-ATTACHED atoms, which the learner curated. That respects ADR-212 D1
// sovereignty by construction: D1 makes a concept's TITLE learner-authored free
// text and says nothing about the atoms the learner chose to attach.
//
// THE LIMIT, which is why lexical remains the primary path in practice: the
// bridge needs atoms already attached, and ADR-244 §5's census reports only
// 5 of 50 concept nodes carry any atom_refs. So for ~90% of concepts there is
// nothing to derive an anchor from, and those are exactly the concepts the
// proposal engine exists to fill. The taxonomy tier is a promotion when the
// evidence is there, never a precondition.
package conceptgraph

import "testing"

// Canonical taxonomy ids, shaped exactly as topic_node_ids arrive on the wire.
const (
	taxFractions = "01970000-0000-7000-a000-00000000f001"
	taxBiology   = "01970000-0000-7000-a000-00000000f002"
	taxAlgebra   = "01970000-0000-7000-a000-00000000f003"
)

// --- The theme side: taxonomy ids come from the learner's own attachments ---

func TestThemeSignature_WithTaxonomyCarriesIDs(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})
	if !theme.HasTaxonomy() {
		t.Fatal("theme did not retain its taxonomy anchor")
	}
	// The lexical terms must survive alongside it: the tiers compose, and the
	// lexical fallback has to remain available when a candidate is unclassified.
	if !theme.Has("fraction") {
		t.Error("attaching taxonomy destroyed the lexical signature")
	}
}

// A blank or malformed id is not an anchor. Without this, an empty string in
// atom_refs would create a taxonomy set that matches any other empty string.
func TestThemeSignature_WithTaxonomyRejectsNonIDs(t *testing.T) {
	for _, bad := range [][]string{nil, {}, {""}, {"   "}, {"fractions"}, {"not-a-uuid"}} {
		theme := NewThemeSignature("Fractions", "").WithTaxonomy(bad)
		if theme.HasTaxonomy() {
			t.Errorf("WithTaxonomy(%q) produced an anchor from a non-id", bad)
		}
	}
}

// WithTaxonomy must not mutate the receiver: the resolver builds one theme and
// the planner contract in this package is that value objects are copied, never
// aliased.
func TestThemeSignature_WithTaxonomyDoesNotMutateReceiver(t *testing.T) {
	base := NewThemeSignature("Fractions", "")
	_ = base.WithTaxonomy([]string{taxFractions})
	if base.HasTaxonomy() {
		t.Error("WithTaxonomy mutated its receiver")
	}
}

// --- The atom side: UUID tags are ROUTED to taxonomy, not discarded ---

// D1.1 originally DROPPED opaque ids so they could not pose as lexical terms.
// D1.2 refines that: they are still barred from the lexical signature, but they
// are now retained as taxonomy identity rather than thrown away.
func TestAtomSignature_RoutesUUIDTagsToTaxonomyNotToTerms(t *testing.T) {
	atom := NewAtomSignature("Unit 4 review", []string{taxFractions, "revision"})
	if !atom.HasTaxonomy() {
		t.Fatal("a topic_node_id tag was not retained as taxonomy identity")
	}
	// Still barred from the lexical side (the D1.1 guarantee).
	for _, junk := range []string{"a000", "7000", "f001", "0000"} {
		if _, ok := atom.terms[junk]; ok {
			t.Errorf("UUID fragment %q leaked into the lexical signature", junk)
		}
	}
	// The freeform tag beside it is unaffected.
	if _, ok := atom.terms["revision"]; !ok {
		t.Error("a freeform tag alongside a taxonomy id was lost")
	}
}

// --- Tier 1: a taxonomy match is the strongest evidence available ---

func TestAffinity_TaxonomyMatchOutranksEveryLexicalScore(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})

	// Classified under the goal's topic, but with a title that says nothing.
	taxonomyHit, band := Affinity(theme, NewAtomSignature("Unit 4 review", []string{taxFractions}))
	if band != AffinityOnTheme {
		t.Fatalf("taxonomy match banded %q, want %q", band, AffinityOnTheme)
	}
	// The best a purely lexical atom can do: an exact freeform tag match.
	bestLexical, _ := Affinity(theme, NewAtomSignature("Fractions", []string{"fractions"}))
	if taxonomyHit <= bestLexical {
		t.Errorf("taxonomy %.3f must outrank the best lexical %.3f: a curated "+
			"classification is stronger evidence than any string overlap",
			taxonomyHit, bestLexical)
	}
}

// Broader taxonomy overlap ranks higher, so ordering is meaningful inside the
// taxonomy tier too.
func TestAffinity_BroaderTaxonomyOverlapRanksHigher(t *testing.T) {
	theme := NewThemeSignature("", "").WithTaxonomy([]string{taxFractions, taxAlgebra})
	both, _ := Affinity(theme, NewAtomSignature("x", []string{taxFractions, taxAlgebra}))
	one, _ := Affinity(theme, NewAtomSignature("x", []string{taxFractions}))
	if both <= one {
		t.Errorf("two-id overlap %.3f must outrank one-id %.3f", both, one)
	}
}

// A taxonomy-only theme (no usable title at all) still works. This is the
// whole-map / sentinel-theme shape, where the lexical side has nothing.
func TestAffinity_TaxonomyWorksWithNoLexicalTheme(t *testing.T) {
	theme := NewThemeSignature("", "").WithTaxonomy([]string{taxFractions})
	score, band := Affinity(theme, NewAtomSignature("Unit 4 review", []string{taxFractions}))
	if band != AffinityOnTheme || score <= maxLexicalScore {
		t.Errorf("score/band = %.3f/%q, want a taxonomy-tier hit", score, band)
	}
}

// --- Tier 2: a taxonomy MISS must not demote. This is the load-bearing rule ---

// THE PROMOTE-NEVER-DEMOTE CONTRACT. Both sides are classified and they do not
// overlap, yet the title is squarely on theme. The verdict must fall through to
// the lexical tier rather than being called off-theme.
//
// Why: taxonomy coverage will be partial for a long time (CHO-2149 populates
// going FORWARD, and an atom may legitimately carry one id of several topics it
// touches). Treating a non-overlap as a negative verdict would let incomplete
// curation demote atoms that the evidence says are relevant, and would hand the
// taxonomy tier the power to reshape the catalogue downward - which is the
// starvation shape this design forbids, arriving through a different door.
func TestAffinity_TaxonomyMissFallsBackToLexicalNeverDemotes(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})
	classifiedElsewhere := NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", []string{taxBiology})

	score, band := Affinity(theme, classifiedElsewhere)
	if band != AffinityOnTheme {
		t.Fatalf("band = %q (score %.3f), want %q: a taxonomy miss must fall "+
			"through to lexical evidence, never override it downward", band, score, AffinityOnTheme)
	}
	// And it must score exactly as if the taxonomy were absent entirely.
	lexicalOnly, _ := Affinity(
		NewThemeSignature("Fractions", ""),
		NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil),
	)
	if score != lexicalOnly {
		t.Errorf("miss scored %.3f, want the pure lexical %.3f: a miss must be "+
			"inert, not a penalty", score, lexicalOnly)
	}
}

// An unclassified atom on a taxonomy-anchored theme is the COMMON case while
// field 7 still carries words rather than taxonomy ids. It must score on
// lexical evidence, not be excluded for
// lacking a classification it was never given.
func TestAffinity_UnclassifiedAtomStillScoresLexically(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})
	score, band := Affinity(theme, NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil))
	if band != AffinityOnTheme || score <= 0 {
		t.Errorf("score/band = %.3f/%q, want an on-theme lexical verdict", score, band)
	}
}

// --- Condition 1: no-starve across all THREE paths ---

// len(out) == len(in) for taxonomy-matched, taxonomy-present-but-unmatched, and
// taxonomy-absent. A taxonomy hit must never be able to shorten the catalogue.
func TestRankByAffinity_NeverDropsAnAtom_AcrossAllThreeTaxonomyPaths(t *testing.T) {
	atoms := []AtomSignature{
		NewAtomSignature("Unit 4 review", []string{taxFractions}), // classified, matches
		NewAtomSignature("Photosynthesis", []string{taxBiology}),  // classified, does not match
		NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil),  // unclassified, lexical hit
		NewAtomSignature("Road safety for cyclists", nil),         // unclassified, no hit
	}
	cases := map[string]ThemeSignature{
		"taxonomy matched":               NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions}),
		"taxonomy present but unmatched": NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxAlgebra}),
		"taxonomy absent":                NewThemeSignature("Fractions", ""),
		"taxonomy only, no lexical":      NewThemeSignature("", "").WithTaxonomy([]string{taxFractions}),
		"nothing at all":                 NewThemeSignature("", ""),
	}
	for name, theme := range cases {
		ranked := RankByAffinity(theme, atoms)
		if len(ranked) != len(atoms) {
			t.Errorf("%s: ranked %d of %d atoms: ranking must never drop",
				name, len(ranked), len(atoms))
		}
	}
}

// The classified-and-matching atom sorts to the front even though its title is
// meaningless and it sits first only by luck of scoring.
func TestRankByAffinity_TaxonomyHitSortsAboveLexicalHit(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})
	atoms := []AtomSignature{
		NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil),  // strong lexical
		NewAtomSignature("Unit 4 review", []string{taxFractions}), // taxonomy match, weak title
	}
	ranked := RankByAffinity(theme, atoms)
	if ranked[0].Signature.Title() != "Unit 4 review" {
		t.Fatalf("front = %q, want the taxonomy match", ranked[0].Signature.Title())
	}
	if ranked[0].Signal != SignalTaxonomy {
		t.Errorf("front signal = %q, want %q", ranked[0].Signal, SignalTaxonomy)
	}
	if ranked[1].Signal != SignalLexical {
		t.Errorf("second signal = %q, want %q", ranked[1].Signal, SignalLexical)
	}
}

// Signal is the observability seam that makes an INERT taxonomy tier visible.
// "The Companion proposed nothing useful" and "the taxonomy branch never fired"
// are indistinguishable without it.
func TestRankByAffinity_SignalReportsWhichTierDecided(t *testing.T) {
	theme := NewThemeSignature("Fractions", "").WithTaxonomy([]string{taxFractions})
	ranked := RankByAffinity(theme, []AtomSignature{
		NewAtomSignature("Unit 4 review", []string{taxFractions}),
		NewAtomSignature("Comparing Fractions", nil),
		NewAtomSignature("Road safety", nil),
	})
	want := []string{SignalTaxonomy, SignalLexical, SignalNone}
	for i, w := range want {
		if ranked[i].Signal != w {
			t.Errorf("position %d signal = %q, want %q", i, ranked[i].Signal, w)
		}
	}
}
