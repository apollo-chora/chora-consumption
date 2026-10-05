// subtree.go — WS-A2 (My Knowledge unification, CHO-2005): scope a "map" to its
// Goal's root concept. Per ADR-214 D1 a learner Goal is anchored to a ConceptNode
// and "the goal's scope = the sub-tree under that root". This is the pure engine
// that computes that sub-tree's concept-id membership; the HTTP map read-model
// (maps_handler.go) filters the learner's concepts + edges to it and reuses the
// A1 overlay paint.
//
// HIERARCHY CONVENTION (shared with rerooting.go): a hierarchy Edge is directed
// source=PARENT -> target=CHILD. The sub-tree under a root is therefore its
// DOWNWARD hierarchy reach. LATERAL edges are cross-links (relates-to), NOT
// containment — they are NEVER traversed to grow the set, so a lateral junction
// into another map's hierarchy cannot merge the two maps (themed maps stay
// distinct; an intra-tree lateral simply renders, it adds no member).
//
// PURITY: stdlib-only, no I/O, no clock. `concepts` and `edges` are read-only.
package conceptgraph

import "strings"

// SubtreeConceptIDs returns the set of concept ids in the sub-tree rooted at
// rootID (ADR-214 D1), walking LIVE hierarchy edges downward from the root.
//
// Membership rules:
//   - rootID must name a LIVE concept (deleted_at == nil) — otherwise the set is
//     empty (an unknown, blank, or soft-deleted root has no map).
//   - only LIVE hierarchy edges are followed; soft-deleted and lateral edges are
//     ignored, and a child that is itself soft-deleted is skipped (pruning its
//     descendants too).
//   - cycle-guarded via the visited set (corrupt R<->A data terminates).
//
// The result always contains rootID when rootID is live; it is a fresh map the
// caller owns.
func SubtreeConceptIDs(concepts []ConceptNode, edges []Edge, rootID string) map[string]bool {
	set := map[string]bool{}
	root := strings.TrimSpace(rootID)
	if root == "" {
		return set
	}

	// Index live concepts so traversal never enters a soft-deleted node.
	live := make(map[string]bool, len(concepts))
	for i := range concepts {
		if concepts[i].DeletedAt == nil {
			live[concepts[i].ConceptID] = true
		}
	}
	if !live[root] {
		return set // unknown or soft-deleted root -> no map
	}

	// Child adjacency from LIVE hierarchy edges only (source=parent -> target=child).
	children := make(map[string][]string, len(edges))
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil || e.Class != EdgeClassHierarchy {
			continue
		}
		children[e.SourceConceptID] = append(children[e.SourceConceptID], e.TargetConceptID)
	}

	// Breadth-first downward walk, cycle-guarded, skipping non-live children.
	set[root] = true
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if set[child] || !live[child] {
				continue
			}
			set[child] = true
			queue = append(queue, child)
		}
	}
	return set
}
