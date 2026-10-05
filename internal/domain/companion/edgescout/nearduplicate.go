// nearduplicate.go: the near-duplicate DEMOTION tier of the ceremony
// edge-scout's already-conceptualised exclusion.
//
// Tier 1 (exact, in the adapter) DROPS a candidate whose normalised key equals
// an owned concept key. It is learner-wide and its measured false-positive rate
// is 0 in 245.
//
// Tier 2 (this file) catches the PARAPHRASE the exact tier cannot: "Cycling
// safety" normalises to "cycling-safety", which does not equal
// "cycling-safety-protocols", so the concept the learner just accepted came
// straight back on the next scout (measured live 2026-08-07, after a 25-mana
// charge).
//
// WHY THIS DEMOTES AND NEVER DELETES: the predicate provably cannot separate "a
// restatement of an owned concept" from "the parent/genus of one".
// cycling-safety inside cycling-safety-protocols (correct) and binary-search
// inside binary-search-tree or product-owner inside
// product-owner-accountabilities (both wrong) are the SAME string relation.
// Measured on realistic corpora, 13 of 16 drops were wrong, and one corpus
// dropped every candidate: the response became {"candidates":[]}, which the
// learner cannot tell apart from an honest "nothing new" after paying 25 mana.
// Demotion keeps every candidate reachable and leaves the judgement with the
// learner, who is the only party that can actually make it.
//
// WHY INTEGER ARITHMETIC AND NOT A COVERAGE RATIO: the true positive and five
// known false positives all sit at exactly cover 0.6667, so a maintainer nudging
// a 0.6 threshold to 0.7 would silently revert the fix while every test still
// passed. The integer form also states its own meaning: the owned title adds at
// most ONE qualifying word.
//
// SCOPE: the caller fences the owned side to THIS GOAL'S subtree. Unscoped, a
// concept accepted on one goal would censor an unrelated goal.
//
// HEXAGONAL purity: stdlib-only, no infra imports. The concept-key normaliser
// lives in the learner_weakness domain, so it is INJECTED rather than imported.
package edgescout

import "strings"

// Near-duplicate predicate constants. Both are load bearing and both are
// integers on purpose (see the package-level note above).
const (
	// NearDuplicateMinTokens is the floor on the CANDIDATE's token count.
	// Mandatory: 32 to 39 percent of real concept titles are single-token, and an
	// unfloored rule demotes "Fractions" under "Adding fractions", "Algebra"
	// under "Algebra I" and "Earth" under "Earth's true shape".
	NearDuplicateMinTokens = 2
	// NearDuplicateMaxExtraTokens is how many tokens the OWNED title may add over
	// the candidate: exactly one qualifying word.
	NearDuplicateMaxExtraTokens = 1
)

// OwnedConcept is one concept the learner already holds on their knowledge map,
// as this tier sees it. Title is learner-facing and is what gets echoed back as
// Candidate.NearDuplicateOf; Key is the stored concept_key, which survives a
// rename and therefore still pins the original vocabulary. Both are matched.
type OwnedConcept struct {
	Title string
	Key   string
}

// DemoteNearDuplicates stamps every candidate that restates an owned concept
// with that concept's title, then stable-partitions the stamped candidates to
// the END of the SAME slice.
//
// It NEVER removes a candidate, and it never emits a separate array: a separate
// array would be invisible to an un-updated frontend, which recreates exactly
// the harm this fixes (the learner is shown a concept they already own, with no
// signal that they do).
//
// An inbound NearDuplicateOf is discarded and re-derived, so a stale or forged
// stamp cannot survive a pass. Relative order within each group is preserved:
// the upstream cosine ranking is not re-litigated here.
//
// normalise MUST be non-nil (the adapter passes lw.NormalizeConceptKey): a nil
// func is a wiring bug and panics rather than silently disabling the tier.
func DemoteNearDuplicates(cands []Candidate, owned []OwnedConcept, normalise func(string) string) []Candidate {
	if normalise == nil {
		panic("edgescout: DemoteNearDuplicates requires a non-nil normalise func (wiring bug: a nil func would silently disable the near-duplicate tier)")
	}

	// Normalise the owned side once. A node contributes up to two keys (its
	// current title and its stored concept_key); both point at the title the
	// learner actually sees.
	type ownedKey struct{ display, key string }
	keys := make([]ownedKey, 0, len(owned)*2)
	for _, o := range owned {
		display := strings.TrimSpace(o.Title)
		if display == "" {
			display = strings.TrimSpace(o.Key)
		}
		if display == "" {
			continue
		}
		for _, raw := range [2]string{o.Title, o.Key} {
			if k := normalise(raw); k != "" {
				keys = append(keys, ownedKey{display: display, key: k})
			}
		}
	}

	kept := make([]Candidate, 0, len(cands))
	demoted := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		c.NearDuplicateOf = "" // never trust an inbound stamp
		candKey := normalise(c.Title)
		for _, o := range keys {
			if isNearDuplicateKey(candKey, o.key) {
				c.NearDuplicateOf = o.display // first match in owned order wins
				break
			}
		}
		if c.NearDuplicateOf == "" {
			kept = append(kept, c)
			continue
		}
		demoted = append(demoted, c)
	}
	return append(kept, demoted...)
}

// isNearDuplicateKey is THE predicate, stated once:
//
//	candKey is a near-duplicate of ownedKey when candKey's tokens occur as a
//	CONTIGUOUS, IN-ORDER run STRICTLY inside ownedKey's tokens, candKey has at
//	least NearDuplicateMinTokens tokens, and ownedKey has at most
//	NearDuplicateMaxExtraTokens tokens more than candKey.
//
// FORWARD DIRECTION ONLY. Reverse containment (an owned title sitting inside a
// candidate) is deliberately NOT implemented: measured 24 to 27 percent drop
// rate, because every specialisation of an owned concept would match.
//
// The blank-key guard on BOTH sides is load bearing, not defensive padding: the
// normaliser returns "" for a fully non-ASCII title (CJK, for one), so without
// it two unrelated non-Latin titles would collide as a 100 percent match.
//
// The two length checks together pin len(owned) == len(cand)+1 exactly. They are
// written separately because each states a different rule: strict containment
// (an equal key belongs to the exact tier) and the one-qualifying-word budget.
func isNearDuplicateKey(candKey, ownedKey string) bool {
	if candKey == "" || ownedKey == "" {
		return false
	}
	cand := tokeniseKey(candKey)
	owned := tokeniseKey(ownedKey)
	if len(cand) < NearDuplicateMinTokens {
		return false
	}
	if len(owned) <= len(cand) {
		return false // strictly inside: equal keys are the exact tier's business
	}
	if len(owned) > len(cand)+NearDuplicateMaxExtraTokens {
		return false // the owned title adds more than one qualifying word
	}
	return containsTokenRun(owned, cand)
}

// tokeniseKey splits a normalised concept key into its tokens. The normaliser
// collapses every non-alphanumeric run to a single hyphen and trims the ends, so
// empty tokens cannot occur; they are dropped anyway so a hand-built key can
// never smuggle one in.
func tokeniseKey(key string) []string {
	parts := strings.Split(key, "-")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// containsTokenRun reports whether needle occurs in hay as a contiguous,
// in-order run of WHOLE tokens. Token alignment is the point: a raw substring
// test would match "art-work" inside "art-workshop-notes".
func containsTokenRun(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		matched := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
