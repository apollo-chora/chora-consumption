// nearduplicate_test.go: the near-duplicate DEMOTION tier.
//
// MEASURED DEFECT (2026-08-07): the exact-key exclusion shipped in 5dc1906a6
// catches a re-proposal of "Cycling safety protocols" verbatim, after a rename,
// and across case/spacing. It does NOT catch a PARAPHRASE: "Cycling safety"
// normalises to "cycling-safety", which is not equal to
// "cycling-safety-protocols", so the learner is offered a restatement of a
// concept they already own.
//
// The predicate below cannot separate "restatement of an owned concept" from
// "the parent/genus of one": cycling-safety inside cycling-safety-protocols
// (correct) and binary-search inside binary-search-tree (wrong) are the SAME
// string relation. Measured on realistic corpora, 13 of 16 drops were wrong, and
// one corpus dropped EVERY candidate, rendering {"candidates":[]}:
// indistinguishable from an honest "nothing new" after charging 25 mana.
// So this tier DEMOTES and never deletes: the tests below pin that as a
// load-bearing property, not an implementation detail.
package edgescout

import (
	"strings"
	"testing"
)

// testNormalise mirrors lw.NormalizeConceptKey. The pure package must stay
// stdlib-only, so the real normaliser is INJECTED by the adapter and pinned by
// an adapter-level test; this local copy keeps the pure tests honest about the
// vocabulary the predicate assumes (lower-case, non-alphanumeric runs collapsed
// to one hyphen, ends trimmed, "" for an all-punctuation or non-ASCII input).
func testNormalise(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// The predicate, stated once and tested exhaustively: candidate key C is a
// near-duplicate of owned key O when C occurs as a CONTIGUOUS, IN-ORDER token
// run STRICTLY inside O, C has at least 2 tokens, and O has at most one token
// more than C. Forward direction only.
func TestIsNearDuplicateKeyPredicate(t *testing.T) {
	cases := []struct {
		name  string
		cand  string
		owned string
		want  bool
	}{
		{"the measured case: owned title adds one qualifying word",
			"cycling-safety", "cycling-safety-protocols", true},
		{"run at the tail is still a contiguous in-order run",
			"safety-protocols", "cycling-safety-protocols", true},
		{"KNOWN false positive, tolerated ONLY because this demotes and never deletes",
			"binary-search", "binary-search-tree", true},

		{"reverse containment is NEVER implemented: measured 24-27 percent drop rate",
			"cycling-safety-protocols", "cycling-safety", false},
		{"equal keys belong to the exact tier, not this one",
			"cycling-safety", "cycling-safety", false},
		{"owned adds TWO words: outside the budget",
			"binary-search", "binary-search-tree-rotations", false},
		{"not contiguous",
			"cycling-protocols", "cycling-safety-protocols", false},
		{"not in order",
			"safety-cycling", "cycling-safety-protocols", false},
		{"raw substring but NOT token-aligned: art-work sits inside art-workshop-notes as bytes",
			"art-work", "art-workshop-notes", false},

		{"single-token floor: Fractions under Adding fractions",
			"fractions", "adding-fractions", false},
		{"single-token floor: Algebra under Algebra I",
			"algebra", "algebra-i", false},
		{"single-token floor: Earth under Earth's true shape",
			"earth", "earths-true-shape", false},

		{"blank candidate key: a non-ASCII title normalises to empty",
			"", "cycling-safety-protocols", false},
		{"blank owned key",
			"cycling-safety", "", false},
		{"both blank: two unrelated CJK titles must NOT collide as a 100 percent match",
			"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNearDuplicateKey(tc.cand, tc.owned); got != tc.want {
				t.Errorf("isNearDuplicateKey(%q, %q) = %v, want %v", tc.cand, tc.owned, got, tc.want)
			}
		})
	}
}

// The token budget is INTEGER arithmetic, never a float coverage threshold: the
// true positive and the known false positives all sit at exactly cover 0.6667,
// so a maintainer nudging 0.6 to 0.7 would silently revert the fix.
func TestNearDuplicateBudgetIsIntegerNotACoverageThreshold(t *testing.T) {
	// cover(cycling-safety, cycling-safety-protocols) == 2/3, and so does
	// cover(binary-search, binary-search-tree). One is a restatement and one is a
	// genus. No float threshold can separate them, which is exactly why both are
	// demoted rather than dropped.
	if !isNearDuplicateKey("cycling-safety", "cycling-safety-protocols") {
		t.Error("the true positive must match")
	}
	if !isNearDuplicateKey("binary-search", "binary-search-tree") {
		t.Error("the known false positive must ALSO match: the predicate cannot tell them apart")
	}
	if NearDuplicateMinTokens != 2 {
		t.Errorf("NearDuplicateMinTokens = %d, want 2: 32-39 percent of real titles are single-token", NearDuplicateMinTokens)
	}
	if NearDuplicateMaxExtraTokens != 1 {
		t.Errorf("NearDuplicateMaxExtraTokens = %d, want 1: the owned title adds at most ONE qualifying word", NearDuplicateMaxExtraTokens)
	}
}

func owned2() []OwnedConcept {
	return []OwnedConcept{
		{Title: "Cycling safety protocols", Key: "cycling-safety-protocols"},
		// Renamed on the map: the stored key still pins the original vocabulary.
		{Title: "My own words for it", Key: "road-permit-requirements"},
	}
}

// LOAD BEARING: demote, never delete. A dropped candidate is unrecoverable and
// the panel cannot tell the learner why it shrank.
func TestDemoteNearDuplicatesNeverDeletes(t *testing.T) {
	in := []Candidate{
		{Title: "Cycling safety", Intent: IntentRemediate, Source: SourceComment},
		{Title: "Road permit", Intent: IntentExplore, Source: SourceComment},
		{Title: "Crafting descriptive open-ended answers", Intent: IntentRemediate, Source: SourceComment},
	}
	got := DemoteNearDuplicates(in, owned2(), testNormalise)
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d: this tier must DEMOTE, never delete; got %+v", len(got), len(in), got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.Title] = true
	}
	for _, c := range in {
		if !seen[c.Title] {
			t.Errorf("candidate %q disappeared", c.Title)
		}
	}
}

func TestDemoteNearDuplicatesStampsTheMatchedOwnedTitleAndSortsLast(t *testing.T) {
	in := []Candidate{
		{Title: "Cycling safety", Intent: IntentRemediate, Source: SourceComment},
		{Title: "Crafting descriptive open-ended answers", Intent: IntentRemediate, Source: SourceComment},
	}
	got := DemoteNearDuplicates(in, owned2(), testNormalise)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Title != "Crafting descriptive open-ended answers" || got[0].NearDuplicateOf != "" {
		t.Errorf("head = %+v, want the genuinely new candidate with an empty near_duplicate_of", got[0])
	}
	if got[1].Title != "Cycling safety" {
		t.Errorf("near-duplicate must sort LAST in the SAME array, got %+v", got)
	}
	if got[1].NearDuplicateOf != "Cycling safety protocols" {
		t.Errorf("near_duplicate_of = %q, want the matched OWNED title", got[1].NearDuplicateOf)
	}
}

// A renamed node keeps its stored key: the tier must read BOTH sides, and the
// stamp must name what the learner SEES (the current title).
func TestDemoteNearDuplicatesMatchesTheStoredKeyOfARenamedNode(t *testing.T) {
	in := []Candidate{{Title: "Road permit", Intent: IntentExplore, Source: SourceComment}}
	got := DemoteNearDuplicates(in, owned2(), testNormalise)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].NearDuplicateOf != "My own words for it" {
		t.Errorf("near_duplicate_of = %q, want the node's CURRENT title (matched via its stored key)", got[0].NearDuplicateOf)
	}
}

// Relative order inside each group is preserved: demotion is a stable partition,
// never a re-rank. The ranking upstream is the CR-locked cosine order.
func TestDemoteNearDuplicatesIsAStablePartition(t *testing.T) {
	in := []Candidate{
		{Title: "Cycling safety", Source: SourceComment},
		{Title: "Alpha new", Source: SourceComment},
		{Title: "Road permit", Source: SourceComment},
		{Title: "Beta new", Source: SourceComment},
	}
	got := DemoteNearDuplicates(in, owned2(), testNormalise)
	want := []string{"Alpha new", "Beta new", "Cycling safety", "Road permit"}
	for i, w := range want {
		if got[i].Title != w {
			t.Fatalf("order = %v, want %v", titlesOf(got), want)
		}
	}
}

func TestDemoteNearDuplicatesEmptyOwnedIsPassthrough(t *testing.T) {
	in := []Candidate{{Title: "Cycling safety", Source: SourceComment}}
	got := DemoteNearDuplicates(in, nil, testNormalise)
	if len(got) != 1 || got[0].NearDuplicateOf != "" {
		t.Fatalf("empty owned set must stamp nothing, got %+v", got)
	}
}

// An empty input stays non-nil so the wire encodes [] rather than null.
func TestDemoteNearDuplicatesReturnsNonNil(t *testing.T) {
	if got := DemoteNearDuplicates(nil, owned2(), testNormalise); got == nil {
		t.Error("result must be non-nil so candidates serialises as [] not null")
	}
}

// An inbound stamp is never trusted: the tier re-derives it every call, so a
// stale or forged near_duplicate_of cannot survive.
func TestDemoteNearDuplicatesReDerivesTheStamp(t *testing.T) {
	in := []Candidate{{Title: "Crafting descriptive open-ended answers", Source: SourceComment, NearDuplicateOf: "forged"}}
	got := DemoteNearDuplicates(in, owned2(), testNormalise)
	if got[0].NearDuplicateOf != "" {
		t.Errorf("near_duplicate_of = %q, want it re-derived to empty", got[0].NearDuplicateOf)
	}
}

// A nil normaliser is a WIRING bug. It must fail loud: defaulting to a no-op
// would silently disable the whole tier while every test still passed.
func TestDemoteNearDuplicatesNilNormaliserFailsLoud(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a nil normalise func must panic, never silently disable the tier")
		}
	}()
	DemoteNearDuplicates([]Candidate{{Title: "Cycling safety"}}, owned2(), nil)
}

func titlesOf(cands []Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Title)
	}
	return out
}
