// companion_ceremony_edge_scout_exclusion_test.go: the ceremony edge-scout's
// two-tier already-conceptualised exclusion.
//
// MEASURED DEFECT (2026-08-07, live): after accepting "Cycling safety
// protocols" onto the map, the very next "Look again" re-proposed that same
// concept: the learner pays 25 mana to be offered something they already own.
// The sibling kg_explore skill has carried this exclusion since CHO-2117
// (buildKgExplorePool: "never re-suggest an owned concept"); the ceremony scout
// never got it.
//
// TIER 1, exact key, DROPS, learner-wide (shipped 5dc1906a6). Comment-sourced
// candidates BYPASS the pool (edgescout.Reconcile's documented carve-out:
// SourceComment is the only non-pool source), and every candidate in the live
// repro was comment-sourced, so the filter runs on the RECONCILED output rather
// than on the pre-turn pool. An all-owned reply is an HONEST EMPTY STATE (owner
// ruling 2026-08-07), never ErrBadReply.
//
// TIER 2, contiguous token containment, DEMOTES, scoped to THIS GOAL's subtree.
// It catches the paraphrase tier 1 cannot ("Cycling safety" does not normalise
// to "cycling-safety-protocols"). It demotes rather than drops because the
// predicate cannot separate a restatement from a genus: see
// edgescout/nearduplicate.go for the measured numbers. It is goal-scoped
// because, unscoped, a concept accepted on one goal would censor an unrelated
// goal; tier 1 stays learner-wide (measured false-positive rate 0 in 245).
package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

func ownedNodes() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-1", Title: "Cycling safety protocols", ConceptKey: "cycling-safety-protocols"},
		// Renamed on the map: the stored key still pins the original vocabulary.
		{ConceptID: "c-2", Title: "My own words for it", ConceptKey: "road-permit-requirements"},
		nil, // defensive: a nil row must not panic
	}
}

// ---------------------------------------------------------------------------
// Tier 1: exact key, drop, learner-wide
// ---------------------------------------------------------------------------

func TestOwnedConceptIndexMapsTitleAndStoredKeyToTheOwnedTitle(t *testing.T) {
	idx := ownedConceptIndex(ownedNodes())
	// Both sides of a renamed node resolve to the title the learner SEES, so a
	// suppression log can always name the concept in the learner's own words.
	for key, wantTitle := range map[string]string{
		"cycling-safety-protocols": "Cycling safety protocols",
		"road-permit-requirements": "My own words for it",
		"my-own-words-for-it":      "My own words for it",
	} {
		if got := idx[key]; got != wantTitle {
			t.Errorf("ownedConceptIndex[%q] = %q, want %q", key, got, wantTitle)
		}
	}
}

func TestDropAlreadyConceptualisedRemovesOwnedCandidates(t *testing.T) {
	idx := ownedConceptIndex(ownedNodes())
	in := []edgescout.Candidate{
		// The live repro: comment-sourced, already owned ⇒ must go.
		{Title: "Cycling safety protocols", Intent: edgescout.IntentRemediate, Source: edgescout.SourceComment},
		// Case/spacing variance must still match.
		{Title: "  ROAD PERMIT requirements ", Intent: edgescout.IntentExplore, Source: edgescout.SourceComment},
		// Genuinely new ⇒ must survive.
		{Title: "Crafting descriptive open-ended answers", Intent: edgescout.IntentRemediate, Source: edgescout.SourceComment},
	}
	got := dropAlreadyConceptualised(in, idx, esGoalID, "turn-1")
	if len(got) != 1 {
		t.Fatalf("kept %d candidates, want 1; got %+v", len(got), got)
	}
	if got[0].Title != "Crafting descriptive open-ended answers" {
		t.Errorf("wrong survivor: %q", got[0].Title)
	}
}

func TestDropAlreadyConceptualisedAllOwnedIsHonestEmptyNotError(t *testing.T) {
	idx := ownedConceptIndex(ownedNodes())
	got := dropAlreadyConceptualised([]edgescout.Candidate{
		{Title: "Cycling safety protocols", Source: edgescout.SourceComment},
	}, idx, esGoalID, "turn-1")
	if len(got) != 0 {
		t.Fatalf("want empty result, got %+v", got)
	}
	// Non-nil so the JSON encoder emits [] rather than null.
	if got == nil {
		t.Error("result must be non-nil so candidates serialises as [] not null")
	}
}

func TestDropAlreadyConceptualisedEmptySetIsPassthrough(t *testing.T) {
	in := []edgescout.Candidate{{Title: "Anything", Source: edgescout.SourceComment}}
	if got := dropAlreadyConceptualised(in, map[string]string{}, esGoalID, "turn-1"); len(got) != 1 {
		t.Fatalf("empty owned-set must keep everything, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Tier 2: contiguous token containment, demote, goal-scoped
// ---------------------------------------------------------------------------

// The token floor the whole tier rests on is a property of the REAL normaliser,
// not of the domain package's assumptions. If lw.NormalizeConceptKey ever stops
// emitting one token per word, the 2-token floor would silently swallow every
// 2-word title and the tier would no-op. This test fails first when that day
// comes.
func TestRealNormaliserTokenContract(t *testing.T) {
	for title, want := range map[string]int{
		"Cycling safety":           2,
		"Cycling safety protocols": 3,
		"Fractions":                1,
		// MEASURED, not assumed: the real normaliser collapses EVERY
		// non-alphanumeric run, so an apostrophe splits a word. A possessive
		// therefore inflates the token count, and the 2-token floor is a floor on
		// TOKENS, not on words: "Earth's" alone clears it. That is a known,
		// bounded over-reach of tier 2 and it costs a DEMOTION, never a drop:
		// one more reason this tier must not delete.
		"Earth's true shape": 4,
	} {
		key := lw.NormalizeConceptKey(title)
		if got := len(strings.Split(key, "-")); got != want {
			t.Errorf("NormalizeConceptKey(%q) = %q: %d tokens, want %d", title, key, got, want)
		}
	}
	// The exact case the floor was specified to protect still holds end to end.
	owned := []edgescout.OwnedConcept{{Title: "Earth's true shape"}}
	got := demoteNearDuplicates([]edgescout.Candidate{{Title: "Earth"}}, owned, esGoalID, "turn-1")
	if got[0].NearDuplicateOf != "" {
		t.Errorf("single-token %q was demoted under %q: the 2-token floor is not holding", got[0].Title, owned[0].Title)
	}
	// The blank-key guard exists because of this: a fully non-ASCII title
	// normalises to "", and two unrelated ones would otherwise collide as a 100
	// percent match.
	if got := lw.NormalizeConceptKey("自転車の安全"); got != "" {
		t.Errorf("NormalizeConceptKey(CJK) = %q, want \"\": the blank-key guard is written against this", got)
	}
}

func TestOwnedInGoalConceptsScopesToTheSubtree(t *testing.T) {
	nodes := []*conceptgraph.ConceptNode{
		{ConceptID: "in-1", Title: "Cycling safety protocols", ConceptKey: "cycling-safety-protocols"},
		{ConceptID: "out-1", Title: "Binary search tree", ConceptKey: "binary-search-tree"},
		nil,
	}
	got := ownedInGoalConcepts(nodes, map[string]bool{"in-1": true})
	if len(got) != 1 || got[0].Title != "Cycling safety protocols" || got[0].Key != "cycling-safety-protocols" {
		t.Fatalf("owned-in-goal = %+v, want only the in-subtree node", got)
	}
	// A rootless goal has no subtree: the containment tier must then apply to
	// nothing rather than to the whole map.
	if got := ownedInGoalConcepts(nodes, nil); len(got) != 0 {
		t.Errorf("nil subtree must yield no owned concepts, got %+v", got)
	}
}

func TestDemoteNearDuplicatesUsesTheRealNormaliser(t *testing.T) {
	owned := []edgescout.OwnedConcept{{Title: "Cycling safety protocols", Key: "cycling-safety-protocols"}}
	in := []edgescout.Candidate{
		{Title: "Cycling safety", Intent: edgescout.IntentRemediate, Source: edgescout.SourceComment},
		{Title: "Crafting descriptive open-ended answers", Intent: edgescout.IntentRemediate, Source: edgescout.SourceComment},
	}
	got := demoteNearDuplicates(in, owned, esGoalID, "turn-1")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: the tier demotes, never deletes; got %+v", len(got), got)
	}
	if got[1].Title != "Cycling safety" || got[1].NearDuplicateOf != "Cycling safety protocols" {
		t.Errorf("last candidate = %+v, want the paraphrase stamped with the owned title", got[1])
	}
	if got[0].NearDuplicateOf != "" {
		t.Errorf("genuinely new candidate = %+v, want an empty near_duplicate_of", got[0])
	}
}

// ---------------------------------------------------------------------------
// End to end over HTTP: the measured case
// ---------------------------------------------------------------------------

// The live repro, driven through the real handler: the learner owns "Cycling
// safety protocols" on THIS goal's subtree, and the turn proposes the
// comment-sourced paraphrase "Cycling safety". It must come back LAST, stamped,
// and still present: a drop here would leave the learner unable to tell a
// suppressed restatement from an honest "nothing new" after a 25-mana charge.
func TestCeremonyEdgeScout_ParaphraseOfOwnedConcept_DemotedNotDropped(t *testing.T) {
	const ownedChildID = "01970000-bbbb-7000-8000-00000000e5d1"
	reply := `{"candidates":[
		{"title":"Cycling safety","intent":"remediate","source":"comment","rationale":"the grader flagged your road positioning"},
		{"title":"Unit conversions","intent":"explore","source":"comment","rationale":"grader flagged repeatedly"}
	]}`
	engine := &fakeChatEngine{frames: esTokenFrames(reply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	esRecordRun(t, srv) // the PAID re-run path: the 25 mana that made this a defect

	// The owned concept sits on a WON CHILD of this goal's root, so it is in the
	// subtree the containment tier is scoped to.
	srv.KgExploreMap = &fakeKgExploreMap{nodes: []*conceptgraph.ConceptNode{
		{ConceptID: esRootID, TenantID: testTenant, LearnerGCID: testGCID, Title: "Flood risk engineering"},
		{ConceptID: ownedChildID, TenantID: testTenant, LearnerGCID: testGCID,
			Title: "Cycling safety protocols", ConceptKey: "cycling-safety-protocols"},
	}}
	srv.KgExploreEdges = &fakeKgExploreEdges{edges: []*conceptgraph.Edge{
		scoutHierEdge(esRootID, ownedChildID),
	}}

	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title           string `json:"title"`
			NearDuplicateOf string `json:"near_duplicate_of"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want BOTH (the paraphrase is demoted, never dropped)", resp.Candidates)
	}
	if resp.Candidates[0].Title != "Unit conversions" || resp.Candidates[0].NearDuplicateOf != "" {
		t.Errorf("head = %+v, want the genuinely new candidate with an empty near_duplicate_of", resp.Candidates[0])
	}
	last := resp.Candidates[len(resp.Candidates)-1]
	if last.Title != "Cycling safety" {
		t.Errorf("the paraphrase must sort LAST in the same array, got %+v", resp.Candidates)
	}
	if last.NearDuplicateOf != "Cycling safety protocols" {
		t.Errorf("near_duplicate_of = %q, want the owned concept title", last.NearDuplicateOf)
	}
}

// The wire field is present on EVERY candidate, "" when genuinely new: an
// un-updated frontend keeps rendering the same array, and an updated one can
// mark the tail without a second array it would never have read.
func TestCeremonyEdgeScout_NearDuplicateFieldAlwaysOnTheWire(t *testing.T) {
	engine := &fakeChatEngine{frames: esTokenFrames(esReply)}
	srv, id, _, _, _ := seedEdgeScoutServer(t, engine)
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var raw struct {
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw.Candidates) == 0 {
		t.Fatal("no candidates to inspect")
	}
	for i, c := range raw.Candidates {
		v, present := c["near_duplicate_of"]
		if !present {
			t.Errorf("candidate %d lacks near_duplicate_of entirely: %v", i, c)
			continue
		}
		if v != "" {
			t.Errorf("candidate %d near_duplicate_of = %v, want \"\" (nothing here is owned)", i, v)
		}
	}
}

// ---------------------------------------------------------------------------
// The virgin-fallback hole (separate, live): the early return seeded owned
// concepts because it returned BEFORE any filter ran.
// ---------------------------------------------------------------------------

// EXACT tier only on the fallback path. The containment tier would routinely
// blank a virgin learner's panel, because the goal ROOT is itself a ConceptNode
// and the ConceptSet seeds are its own vocabulary.
func TestCeremonyEdgeScout_VirginFallback_ExcludesOwnedSeed(t *testing.T) {
	engine := &fakeChatEngine{}
	srv, id, fog, _, _ := seedEdgeScoutServer(t, engine)
	srv.LearnerWeakness = &esLWStub{}        // zero weaknesses
	srv.GradedComments = &esCommentsReader{} // zero comments ⇒ virgin fallback
	fog.pending = []*conceptgraph.Suggestion{
		{
			SuggestionID: "s-owned", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "Flood risk engineering", // ALREADY on the map (it is the goal root node)
		},
		{
			SuggestionID: "s-new", TenantID: testTenant, LearnerGCID: testGCID,
			Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
			Title: "Tail calls",
		},
	}
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title string `json:"title"`
		} `json:"candidates"`
		Fallback bool `json:"fallback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback {
		t.Fatalf("expected the virgin fallback path, got %s", w.Body.String())
	}
	if engine.calls != 0 {
		t.Errorf("virgin fallback must SKIP the engine turn (calls=%d)", engine.calls)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Title != "Tail calls" {
		t.Errorf("candidates = %+v, want only the unowned seed: the fallback returned before any filter ran", resp.Candidates)
	}
}

// The standing contract is that a learner NEVER gets a blank panel. When every
// seed is already owned, the non-empty contract wins over the exclusion: no mana
// was charged on this path, so a stale suggestion costs the learner nothing,
// while a blank panel is the one outcome the ceremony must not produce.
func TestCeremonyEdgeScout_VirginFallback_NeverBlanksThePanel(t *testing.T) {
	engine := &fakeChatEngine{}
	srv, id, fog, _, _ := seedEdgeScoutServer(t, engine)
	srv.LearnerWeakness = &esLWStub{}
	srv.GradedComments = &esCommentsReader{}
	fog.pending = []*conceptgraph.Suggestion{{
		SuggestionID: "s-owned", TenantID: testTenant, LearnerGCID: testGCID,
		Kind: conceptgraph.SuggestionKindConcept, Status: conceptgraph.SuggestionStatusPending,
		Title: "Flood risk engineering", // the ONLY seed, and already owned
	}}
	w := authedReq(t, srv, http.MethodPost, esPath(id), esBody())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []struct {
			Title string `json:"title"`
		} `json:"candidates"`
		Fallback bool `json:"fallback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Fallback {
		t.Fatalf("expected the virgin fallback path, got %s", w.Body.String())
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Title != "Flood risk engineering" {
		t.Errorf("candidates = %+v, want the unfiltered seed: a virgin learner must never get a blank panel", resp.Candidates)
	}
}
