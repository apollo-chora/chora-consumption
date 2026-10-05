// concept_set_repair.go — WS-C6 (CHO-2085, ADR-227 D14 + addendum #7): the
// slug axis of a concept merge/split reaches goals.concept_set. concept_key
// is minted-once-from-title (NOT node identity), so when a merge retires the
// absorbed node's key or a split retires the parent's and mints the
// children's, every goal scoping those keys re-keys its set here. The caller
// (the merge/split door) computes remove/add from the lineage record and
// persists only when the set actually changed.
package goal

import "time"

// RepairConceptSet drops `remove` keys and appends `add` keys (first-seen
// order, normalised + de-duplicated exactly like NewGoal's constructor set).
// Returns whether the set changed; a no-op repair leaves UpdatedAt untouched
// so callers can skip the UPDATE. Guarded on soft-delete (fail-loud).
func (g *Goal) RepairConceptSet(remove, add []string, now time.Time) (bool, error) {
	if g.DeletedAt != nil {
		return false, ErrDeleted
	}
	drop := make(map[string]struct{}, len(remove))
	for _, k := range normaliseConcepts(remove) {
		drop[k] = struct{}{}
	}
	next := make([]string, 0, len(g.ConceptSet)+len(add))
	seen := make(map[string]struct{}, len(g.ConceptSet)+len(add))
	changed := false
	for _, k := range g.ConceptSet {
		if _, gone := drop[k]; gone {
			changed = true
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		next = append(next, k)
	}
	for _, k := range normaliseConcepts(add) {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		next = append(next, k)
		changed = true
	}
	if !changed {
		return false, nil
	}
	g.ConceptSet = next
	g.UpdatedAt = now.UTC()
	return true, nil
}
