// concept_paint_lineage.go — WS-C6 (CHO-2085, ADR-227 addendum #7): the
// overlay/mastery paint keys on the STORED concept_key (minted-once, stable
// across renames — the shared learner_weakness vocabulary) and resolves
// through concept_node_lineage ancestor keys so slug-axis joins survive a
// merge/split re-keying. Read-time enrichment stays fail-soft: a nil/erroring
// lineage reader yields no ancestor keys, never a broken read.
package http

import (
	"context"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ConceptLineageReader is the narrow lineage read the paint needs: for one
// learner, every concept's transitive ancestor concept_keys (merge: the
// absorbed side; split: the parent chain). Implemented by
// *pg.ConceptLineageRepo. A learner with no merge/split history reads an
// empty map.
type ConceptLineageReader interface {
	AncestorKeysByConcept(ctx context.Context, tenantID, learnerGCID string) (map[string][]string, error)
}

// learnerLineageKeys loads the learner's lineage ancestor-key index.
// Fail-soft: nil reader or a read error yields nil (no lineage paint) — the
// overlay is enrichment, the graph read never breaks on it.
func (s *ExtServer) learnerLineageKeys(ctx context.Context, tenantID, gcid string) map[string][]string {
	if s.ConceptLineage == nil {
		return nil
	}
	keys, err := s.ConceptLineage.AncestorKeysByConcept(ctx, tenantID, gcid)
	if err != nil {
		return nil
	}
	return keys
}

// conceptPaintLabels returns the labels a concept paints by: its STORED
// concept_key first (title fallback only for pre-0074 rows that never got a
// key), then its lineage ancestors' keys. Never the live title when a key
// exists — a rename must not re-derive the join (addendum #7).
func conceptPaintLabels(n *conceptgraph.ConceptNode, ancestorKeys map[string][]string) []string {
	if n == nil {
		return nil
	}
	own := n.ConceptKey
	if own == "" {
		own = n.Title
	}
	anc := ancestorKeys[n.ConceptID]
	labels := make([]string, 0, 1+len(anc))
	labels = append(labels, own)
	labels = append(labels, anc...)
	return labels
}

// masteredByLabels reports whether ANY paint label resolves into the mastered
// (grown) concept-key set. Nil-safe on both sides.
func masteredByLabels(labels []string, mastered map[string]bool) bool {
	if len(mastered) == 0 {
		return false
	}
	for _, l := range labels {
		if mastered[lw.NormalizeConceptKey(l)] {
			return true
		}
	}
	return false
}
