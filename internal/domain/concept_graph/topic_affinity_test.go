// topic_affinity_test.go - ADR-245 D1/D2: the topic identity the concept graph
// never had, and the affinity ordering built on it.
//
// The measurements that drive every case below were taken live 2026-07-20
// against tenant 11111111-1111-7111-8111-111111111111:
//
//   - 30 of 221 live atom_index rows carry topic_tags at all (13.6%).
//   - On the walk learner's Fractions goal, a hard filter on
//     topic_tags @> {fractions} leaves 0 of 16 entitled atoms.
//   - The single most on-theme atom, "Comparing Fractions: 2/3 vs 3/5", is
//     UNTAGGED, so a tag-only model drops the best candidate first.
//
// So the corpus these tests encode is a sparsely-tagged one. A design that
// only reads topic_tags passes a hand-written test and fails on contact with
// production, which is the exact shape ADR-244 §5 warns about.
package conceptgraph

import (
	"strings"
	"testing"
)

// --- The theme side: a topic identity derived from what the graph HAS ---

// A blank theme yields a signature that scores NOTHING. This is the degenerate
// case the whole design hinges on: with no topic identity resolvable, every
// atom must come back UNSCORED, never off-theme, so the caller can fall back
// to today's behaviour instead of ranking against noise.
func TestThemeSignature_BlankIsEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		sig := NewThemeSignature(in, "")
		if !sig.IsEmpty() {
			t.Errorf("NewThemeSignature(%q) is not empty: a blank theme must not score", in)
		}
	}
}

// Stopwords carry no topic identity, so a theme made only of them is empty.
// Without this, a NorthStarNote like "I want to be good at this" would score
// every atom containing "at".
func TestThemeSignature_StopwordsOnlyIsEmpty(t *testing.T) {
	sig := NewThemeSignature("the and to of", "")
	if !sig.IsEmpty() {
		t.Error("a stopword-only theme must be empty, else it scores every atom")
	}
}

// The focal concept sharpens the theme. On a Fractions goal sitting on the
// "Denominator" concept, both terms are topic identity.
func TestThemeSignature_IncludesFocalConcept(t *testing.T) {
	sig := NewThemeSignature("Fractions", "Denominator")
	if sig.IsEmpty() {
		t.Fatal("theme with a focal concept must not be empty")
	}
	if !sig.Has("fraction") || !sig.Has("denominator") {
		t.Errorf("theme terms = %v, want both fraction and denominator", sig.Terms())
	}
}

// Plural and singular are the same topic. "Fractions" (the goal title) must
// match "fraction" (an atom's tag), or the two sides never meet.
func TestThemeSignature_FoldsPlurals(t *testing.T) {
	if !NewThemeSignature("Fractions", "").Has("fraction") {
		t.Error("Fractions must normalise to fraction")
	}
	if !NewThemeSignature("Fraction", "").Has("fraction") {
		t.Error("Fraction must normalise to fraction")
	}
}

// Terms is sorted, so a theme reads the same in a log line and a test failure
// on every run. Map iteration order would otherwise make both nondeterministic.
func TestThemeSignature_TermsAreSorted(t *testing.T) {
	got := NewThemeSignature("Fractions Algebra", "Denominator").Terms()
	want := []string{"algebra", "denominator", "fraction"}
	if len(got) != len(want) {
		t.Fatalf("Terms() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Terms() = %v, want %v", got, want)
		}
	}
}

// --- The atom side: title carries the load, tags carry the weight ---

// THE CASE THAT DISQUALIFIES A HARD TAG FILTER. This atom is untagged in
// production and is the most on-theme atom the learner has. Its TITLE is the
// only signal, and it must be enough to band it on-theme.
func TestAffinity_UntaggedButOnThemeTitle_IsOnTheme(t *testing.T) {
	theme := NewThemeSignature("Fractions", "")
	score, band := Affinity(theme, NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil))
	if band != AffinityOnTheme {
		t.Fatalf("band = %q (score %.3f), want %q: an untagged on-theme title is the "+
			"single most important candidate in the live corpus", band, score, AffinityOnTheme)
	}
}

// The cross-domain citation ADR-244's GAP describes: a Photosynthesis atom
// under a Fractions concept. It must band off-theme.
func TestAffinity_CrossDomainAtom_IsOffTheme(t *testing.T) {
	theme := NewThemeSignature("Fractions", "")
	score, band := Affinity(theme, NewAtomSignature("Photosynthesis and the Calvin cycle", []string{"biology"}))
	if band != AffinityOffTheme {
		t.Fatalf("band = %q (score %.3f), want %q", band, score, AffinityOffTheme)
	}
}

// A curated tag is a stronger assertion than an incidental title word, so a
// tagged atom must outrank a title-only match on the same theme. This is the
// property that makes the design IMPROVE as tagging coverage grows: tagging an
// atom promotes it, and today's 13.6% coverage is a floor, not a dependency.
func TestAffinity_TagOutranksTitleOnly(t *testing.T) {
	theme := NewThemeSignature("Fractions", "")
	tagged, _ := Affinity(theme, NewAtomSignature("Unit 4 review", []string{"fractions"}))
	titleOnly, _ := Affinity(theme, NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil))
	if tagged <= titleOnly {
		t.Errorf("tagged %.3f must outrank title-only %.3f", tagged, titleOnly)
	}
	if titleOnly <= 0 {
		t.Error("a title-only match must still score above zero")
	}
}

// An empty theme scores nothing and bands everything UNSCORED - never
// off-theme. Banding an atom off-theme on no evidence would let a caller build
// a filter that empties the catalogue for every untagged tenant.
func TestAffinity_EmptyTheme_IsUnscoredNotOffTheme(t *testing.T) {
	empty := NewThemeSignature("", "")
	for _, atom := range []AtomSignature{
		NewAtomSignature("Comparing Fractions", []string{"fractions"}),
		NewAtomSignature("Photosynthesis", nil),
	} {
		score, band := Affinity(empty, atom)
		if band != AffinityUnscored {
			t.Errorf("band = %q (score %.3f), want %q when the theme is empty", band, score, AffinityUnscored)
		}
		if score != 0 {
			t.Errorf("score = %.3f, want 0 for an empty theme", score)
		}
	}
}

// An atom with neither title nor tags cannot be scored against any theme. It
// must band off-theme rather than panic, and must never be dropped by the
// caller purely for being unscoreable.
func TestAffinity_EmptyAtom_ScoresZero(t *testing.T) {
	score, band := Affinity(NewThemeSignature("Fractions", ""), NewAtomSignature("", nil))
	if score != 0 || band != AffinityOffTheme {
		t.Errorf("score/band = %.3f/%q, want 0/%q", score, band, AffinityOffTheme)
	}
}

// A multi-word theme must not punish a partial match into a lower band. The
// live goal titles are phrases ("Introduction to Fractions"), and requiring
// full coverage would band the best atom as merely related.
func TestAffinity_MultiWordTheme_PartialMatchStillOnTheme(t *testing.T) {
	theme := NewThemeSignature("Introduction to Fractions", "")
	score, band := Affinity(theme, NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil))
	if band != AffinityOnTheme {
		t.Fatalf("band = %q (score %.3f), want %q for a partial phrase match", band, score, AffinityOnTheme)
	}
}

// Breadth ranks above a single hit: an atom matching both theme terms must
// outscore one matching a single term, so ordering is meaningful within a band.
func TestAffinity_BroaderMatchRanksHigher(t *testing.T) {
	theme := NewThemeSignature("Fractions", "Denominator")
	both, _ := Affinity(theme, NewAtomSignature("Fractions and denominators", nil))
	one, _ := Affinity(theme, NewAtomSignature("Fractions on a number line", nil))
	if both <= one {
		t.Errorf("two-term match %.3f must outrank one-term %.3f", both, one)
	}
}

// A thin match on a broad theme bands RELATED: real overlap, too weak to call
// on-theme. The middle band matters because it is what the prompt uses to let
// the model reach past the on-theme set when a concept genuinely needs it,
// rather than facing a binary keep/drop.
func TestAffinity_ThinMatchOnBroadTheme_IsRelated(t *testing.T) {
	theme := NewThemeSignature("Fractions decimals percentages ratios", "")
	score, band := Affinity(theme, NewAtomSignature("Comparing Fractions", nil))
	if band != AffinityRelated {
		t.Fatalf("band = %q (score %.3f), want %q", band, score, AffinityRelated)
	}
	if score <= 0 {
		t.Errorf("score = %.3f, want above zero for a real partial match", score)
	}
}

// --- The CHO-2149 hazard: opaque taxonomy ids arriving as topic_tags ---

// A UUID-shaped tag contributes NOTHING to topic identity.
//
// This is dated, not hypothetical. chora-creation's topic_nodes taxonomy
// (CHO-2275/2276, shipped) is propagated on atom events as bare
// `topic_node_ids`, and chora-consumption's decoder maps them straight into
// `topic_tags` (protodecode.go:730 - `out["topic_tags"] = ids`). The producer
// does not populate the field yet (CHO-2149, backlog), so `topic_tags` today
// holds only freeform words. The DAY CHO-2149 ships, this field starts
// receiving UUIDs.
//
// The taxonomy NAMES live in chora_creation and cross-DB reads are forbidden,
// and there is no topic_node projection lane into chora_consumption, so a
// topic UUID is genuinely unresolvable here. Left unguarded it would enter the
// signature as hex-fragment junk ("a000", "f1"), consuming the top tag weight
// while matching nothing - the tag signal would look populated and silently
// contribute zero. Dropping it keeps the scorer honest and makes the eventual
// taxonomy integration an explicit step rather than a silent no-op.
func TestAtomSignature_IgnoresUUIDShapedTags(t *testing.T) {
	taxonomyID := "01970000-0000-7000-a000-0000000000f1"
	theme := NewThemeSignature("Fractions", "")

	withUUIDTag := NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", []string{taxonomyID})
	untagged := NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil)

	gotScore, gotBand := Affinity(theme, withUUIDTag)
	wantScore, wantBand := Affinity(theme, untagged)
	if gotScore != wantScore || gotBand != wantBand {
		t.Errorf("a UUID tag changed the verdict: got %.3f/%q, want %.3f/%q "+
			"(an unresolvable taxonomy id must not act as topic evidence)",
			gotScore, gotBand, wantScore, wantBand)
	}
	// And it must not smuggle hex fragments in as terms.
	for _, junk := range []string{"a000", "7000", "f1", "0000"} {
		if _, ok := withUUIDTag.terms[junk]; ok {
			t.Errorf("UUID fragment %q entered the topic signature", junk)
		}
	}
}

// The guard must be narrow: a legitimate hyphenated topic word is NOT a UUID
// and must keep its full tag weight. A guard that ate real tags would quietly
// discard the very signal the design says improves with tagging coverage.
func TestAtomSignature_KeepsHyphenatedRealTags(t *testing.T) {
	theme := NewThemeSignature("Version Control", "")
	tagged, band := Affinity(theme, NewAtomSignature("Unit 4 review", []string{"version-control"}))
	titleOnly, _ := Affinity(theme, NewAtomSignature("Version control basics", nil))
	if band != AffinityOnTheme {
		t.Fatalf("band = %q, want %q for a real hyphenated tag", band, AffinityOnTheme)
	}
	if tagged <= titleOnly {
		t.Errorf("hyphenated tag %.3f must still outrank title-only %.3f", tagged, titleOnly)
	}
}

// --- Ordering: the property that guarantees the catalogue cannot starve ---

// RankByAffinity REORDERS, it never removes. This is the structural guarantee
// that this design cannot reintroduce the empty-catalogue failure: the output
// length is always the input length, for every theme, including one that
// matches nothing at all.
func TestRankByAffinity_NeverDropsAnAtom(t *testing.T) {
	atoms := []AtomSignature{
		NewAtomSignature("Photosynthesis and the Calvin cycle", []string{"biology"}),
		NewAtomSignature("Road safety for cyclists", nil),
		NewAtomSignature("Scrum ceremonies", []string{"agile"}),
	}
	for _, theme := range []ThemeSignature{
		NewThemeSignature("Fractions", ""),      // matches NOTHING in the list
		NewThemeSignature("", ""),               // no topic identity at all
		NewThemeSignature("Photosynthesis", ""), // matches exactly one
	} {
		ranked := RankByAffinity(theme, atoms)
		if len(ranked) != len(atoms) {
			t.Fatalf("theme %v ranked %d of %d atoms: ranking must never drop",
				theme.Terms(), len(ranked), len(atoms))
		}
	}
}

// Ranking is STABLE: equally-scoring atoms keep their input order. Without
// this the catalogue reshuffles between two identical requests, and a
// truncated tail would differ run to run for no reason.
func TestRankByAffinity_IsStableForEqualScores(t *testing.T) {
	atoms := []AtomSignature{
		NewAtomSignature("Alpha", nil), NewAtomSignature("Beta", nil), NewAtomSignature("Gamma", nil),
	}
	ranked := RankByAffinity(NewThemeSignature("Fractions", ""), atoms)
	for i, want := range []string{"Alpha", "Beta", "Gamma"} {
		if !strings.EqualFold(ranked[i].Signature.Title(), want) {
			t.Errorf("position %d = %q, want %q: equal scores must preserve input order",
				i, ranked[i].Signature.Title(), want)
		}
	}
}

// The on-theme atom sorts to the front even when it is last in input order and
// carries no tags at all.
func TestRankByAffinity_OnThemeSortsFirst(t *testing.T) {
	atoms := []AtomSignature{
		NewAtomSignature("Photosynthesis", []string{"biology"}),
		NewAtomSignature("Scrum ceremonies", []string{"agile"}),
		NewAtomSignature("Comparing Fractions: 2/3 vs 3/5", nil),
	}
	ranked := RankByAffinity(NewThemeSignature("Fractions", ""), atoms)
	if !strings.Contains(ranked[0].Signature.Title(), "Fractions") {
		t.Fatalf("front of ranking = %q, want the Fractions atom", ranked[0].Signature.Title())
	}
	if ranked[0].Band != AffinityOnTheme {
		t.Errorf("front band = %q, want %q", ranked[0].Band, AffinityOnTheme)
	}
}

// With no topic identity the ranking is a pure pass-through in input order and
// every band is unscored. A caller can therefore detect "no theme" from the
// result alone and render exactly what it rendered before this ADR.
func TestRankByAffinity_EmptyThemePreservesOrderAndBandsUnscored(t *testing.T) {
	atoms := []AtomSignature{
		NewAtomSignature("Comparing Fractions", []string{"fractions"}),
		NewAtomSignature("Photosynthesis", nil),
	}
	ranked := RankByAffinity(NewThemeSignature("", ""), atoms)
	for i, r := range ranked {
		if r.Band != AffinityUnscored {
			t.Errorf("position %d band = %q, want %q", i, r.Band, AffinityUnscored)
		}
		if r.Signature.Title() != atoms[i].Title() {
			t.Errorf("position %d = %q, want %q: no theme means no reordering",
				i, r.Signature.Title(), atoms[i].Title())
		}
	}
}
