// selection.go: pure candidate ranking for the ADR-244 D5 refresh trigger.
// Given one published/updated atom's textual signal (title + topic tags) and
// the reachable (won focal hex, companion-attached goal) candidates across the
// tenant's learners, rank the pairings worth a proposal request and drop the
// rest. This is deliberately a cheap lexical affinity, not semantic recall:
// the fog orchestrator's LLM (with the ADR-245 banded catalogue) makes the
// real relevance call; this pre-filter only stops obviously off-topic fan-out
// from spending LLM calls.
package atomrefresh

import (
	"sort"
	"strings"
)

// AtomSignal is the textual identity of the triggering atom, read from the
// atom_index projection (title + topic_tags).
type AtomSignal struct {
	Title string
	Tags  []string
}

// Candidate is one reachable (learner, goal, focal) pairing the trigger could
// propose into.
type Candidate struct {
	LearnerGCID    string
	GoalID         string
	FocalConceptID string
	FocalTitle     string
	// Theme is the goal's map theme (root concept title, NorthStarNote
	// fallback), the same value the suggestion request will carry.
	Theme string
}

// stopTokens are common English glue words excluded from affinity scoring on
// top of the 3-character minimum.
var stopTokens = map[string]bool{
	"the": true, "and": true, "for": true, "with": true,
	"from": true, "into": true, "over": true, "your": true,
}

// RankCandidates scores each candidate's lexical affinity to the atom and
// returns the scorers ordered best-first. Focal-title overlap outranks
// theme-only overlap (weight 2 vs 1); zero-affinity candidates are dropped so
// the caller never proposes an atom into a map it shares no vocabulary with.
// Ties break on (LearnerGCID, FocalConceptID) so ranking is deterministic and
// input-order independent.
func RankCandidates(atom AtomSignal, cands []Candidate) []Candidate {
	atomTokens := tokenSet(atom.Title)
	for _, tag := range atom.Tags {
		for tok := range tokenSet(tag) {
			atomTokens[tok] = true
		}
	}
	if len(atomTokens) == 0 {
		return nil
	}

	type scored struct {
		c     Candidate
		score int
	}
	ranked := make([]scored, 0, len(cands))
	for _, c := range cands {
		score := 2*overlap(atomTokens, tokenSet(c.FocalTitle)) + overlap(atomTokens, tokenSet(c.Theme))
		if score == 0 {
			continue
		}
		ranked = append(ranked, scored{c: c, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].c.LearnerGCID != ranked[j].c.LearnerGCID {
			return ranked[i].c.LearnerGCID < ranked[j].c.LearnerGCID
		}
		return ranked[i].c.FocalConceptID < ranked[j].c.FocalConceptID
	})
	out := make([]Candidate, len(ranked))
	for i, s := range ranked {
		out[i] = s.c
	}
	return out
}

// tokenSet lowercases, splits on non-alphanumeric runs, and drops short + stop
// tokens.
func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(tok) < 3 || stopTokens[tok] {
			continue
		}
		set[tok] = true
	}
	return set
}

// overlap counts tokens present in both sets.
func overlap(a, b map[string]bool) int {
	n := 0
	for tok := range b {
		if a[tok] {
			n++
		}
	}
	return n
}
