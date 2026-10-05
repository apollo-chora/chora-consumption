// concept_ancestors.go — the focal node's place in the goal hierarchy
// (ADR-247 D3). A pure walk over the learner's live concept nodes + edges,
// mirroring the rerooting parent-index idiom (hierarchy edges are directed
// source=parent -> target=child, so a child's parent is the source of the
// hierarchy edge whose target is the child). Consumed by Capability A
// (question generation) and Capability B (fog suggestions) to frame a prompt
// with "in service of {goal}, under {ancestors}".
//
// HEXAGONAL purity: stdlib-only, no infra imports (mirrors rerooting/subtree).
package conceptgraph

// AncestorContext is the focal node's lineage framing for prompt composition:
// the goal (root concept) title plus the ancestor concept titles from the focal
// node's parent up to (but excluding) the root, nearest-parent-first. Both are
// best-effort enrichment: an absent root node yields an empty GoalTitle and a
// focal node with no live hierarchy parent yields no Ancestors.
type AncestorContext struct {
	GoalTitle string
	Ancestors []string
}

// ancestorWalkMaxDepth caps the parent walk so a malformed multi-parent or
// cyclic graph can never spin. Goal hierarchies are shallow (a few rings); this
// is a safety backstop, not a product limit.
const ancestorWalkMaxDepth = 32

// WalkAncestors walks the hierarchy parent chain from focalID up to rootID over
// the learner's live nodes + edges, returning the goal (root concept) title and
// the ancestor titles nearest-first, excluding the focal node itself and the
// root (the root is the goal, surfaced as GoalTitle). It is pure, cycle-guarded
// and depth-capped; a node with more than one live hierarchy parent follows the
// FIRST seen (best-effort prompt context, not a re-root correctness operation).
func WalkAncestors(focalID, rootID string, nodes []*ConceptNode, edges []*Edge) AncestorContext {
	byID := make(map[string]*ConceptNode, len(nodes))
	for _, n := range nodes {
		if n != nil {
			byID[n.ConceptID] = n
		}
	}
	// parent[child] = the source (parent) of the first LIVE hierarchy edge whose
	// target is the child. Lateral + deleted edges are not lineage.
	parent := make(map[string]string, len(edges))
	for _, e := range edges {
		if e == nil || e.DeletedAt != nil || e.Class != EdgeClassHierarchy {
			continue
		}
		if _, seen := parent[e.TargetConceptID]; !seen {
			parent[e.TargetConceptID] = e.SourceConceptID
		}
	}

	out := AncestorContext{}
	if root, ok := byID[rootID]; ok {
		out.GoalTitle = root.Title
	}

	visited := map[string]bool{focalID: true}
	cur := focalID
	for i := 0; i < ancestorWalkMaxDepth; i++ {
		p, ok := parent[cur]
		if !ok || p == "" || p == rootID {
			break // reached the root (excluded) or a top with no live parent
		}
		if visited[p] {
			break // cycle guard
		}
		visited[p] = true
		if n, ok := byID[p]; ok && n.Title != "" {
			out.Ancestors = append(out.Ancestors, n.Title)
		}
		cur = p
	}
	return out
}
