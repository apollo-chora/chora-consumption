// overlay.go — W5: read-time Growth-Edge overlay for the per-user Knowledge
// Graph paint (ADR-143). The KG read handlers build an OverlayIndex from the
// learner's ACTIVE edges and annotate cluster cards / fog nodes whose labels
// slug-match an edge's concept_key or tags. Strictly a read-side projection:
// no KG node is ever mutated (the locked Epic-1b "read-overlay" decision).
package learner_weakness

import "strings"

// OverlayIndex maps slugged concept labels/tags to the learner's strongest
// matching active Growth Edge (byKey), and — with HIGHEST precedence — the
// resolved on-map ConceptNode id (byTargetID, ADR-238) so the paint lights the
// correct node by identity even when the concept_key slug has drifted (rename /
// merge / pre-0098 edges).
type OverlayIndex struct {
	byKey      map[string]*LearnerWeakness
	byTargetID map[string]*LearnerWeakness
}

// BuildOverlayIndex indexes each active (non-grown, non-deleted) edge under
// its concept_key, its concept_label slug, and every tag slug. Indexing the
// concept_label matters because the analyser mints short / domain-prefixed
// concept_keys (e.g. "binary-search") that do NOT slug-match the concept NODE
// title the paint joins on (matchFE(node.Title)), whereas the human
// concept_label ("Binary Search Algorithm") usually equals that title — so
// without it a real weakness leaves its node dark (KG #8, CHO-2065). concept_key
// stays the canonical (gcid,concept_key) upsert/idempotency key — untouched.
// When two edges collide on a key the stronger (shakier) one wins — the paint
// surfaces the most urgent frontier.
func BuildOverlayIndex(edges []LearnerWeakness) *OverlayIndex {
	ix := &OverlayIndex{
		byKey:      make(map[string]*LearnerWeakness, len(edges)),
		byTargetID: make(map[string]*LearnerWeakness, len(edges)),
	}
	for i := range edges {
		e := &edges[i]
		if e.IsGrown() || e.DeletedAt != nil {
			continue
		}
		keys := append([]string{e.ConceptKey, e.ConceptLabel}, e.Tags...)
		for _, k := range keys {
			slug := NormalizeConceptKey(k)
			if slug == "" {
				continue
			}
			if cur, ok := ix.byKey[slug]; !ok || e.Strength > cur.Strength {
				ix.byKey[slug] = e
			}
		}
		// ADR-238 id-join: index the resolved on-map ConceptNode id under the
		// SAME strongest-wins rule as byKey. "" = UNMATCHED / pre-0098 — skipped.
		if id := strings.TrimSpace(e.TargetConceptID); id != "" {
			if cur, ok := ix.byTargetID[id]; !ok || e.Strength > cur.Strength {
				ix.byTargetID[id] = e
			}
		}
	}
	return ix
}

// Match returns the strongest edge matching ANY of the supplied labels
// (slugified), preferring earlier labels; nil when none match. Nil-safe.
func (ix *OverlayIndex) Match(labels ...string) *LearnerWeakness {
	if ix == nil || len(ix.byKey) == 0 {
		return nil
	}
	for _, label := range labels {
		slug := NormalizeConceptKey(label)
		if slug == "" {
			continue
		}
		if e, ok := ix.byKey[slug]; ok {
			return e
		}
	}
	return nil
}

// MatchConceptID returns the strongest active edge whose resolved on-map
// TargetConceptID equals conceptID (ADR-238 id-join), or nil. Nil-safe; the
// query is trimmed, and a blank id resolves nothing.
func (ix *OverlayIndex) MatchConceptID(conceptID string) *LearnerWeakness {
	if ix == nil || len(ix.byTargetID) == 0 {
		return nil
	}
	id := strings.TrimSpace(conceptID)
	if id == "" {
		return nil
	}
	return ix.byTargetID[id]
}

// Empty reports whether the index holds no paintable edges — by slug OR by
// resolved concept id. Nil-safe. (An id-only overlay must NOT report Empty, or
// the paint short-circuits before the id-join can fire.)
func (ix *OverlayIndex) Empty() bool {
	return ix == nil || (len(ix.byKey) == 0 && len(ix.byTargetID) == 0)
}
