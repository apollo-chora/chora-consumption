// rerooting.go — the pure re-root + re-consolidation engine for the
// learner-sovereign Discovery graph (ADR-212 WS-2, D3 + D8).
//
// HIERARCHY CONVENTION (WS-2, adopted + documented here): a hierarchy Edge
// (Class == EdgeClassHierarchy) is directed source -> target where
//
//	source_concept = PARENT, target_concept = CHILD.
//
// The apex ("root") of the live hierarchy is the concept with no live hierarchy
// edge pointing AT it (no parent). Lateral edges (EdgeClassLateral) are
// cross-links and are NEVER touched by re-rooting.
//
// RE-ROOT IS APPEND-ONLY (D8, "history preserved, never overwritten"). WS-1
// froze Edge endpoints as immutable — Edge.Update persists only
// edge_class/provenance/deleted_at, not source/target. So flipping a parent↔child
// relationship is not an in-place edit: ReRoot SOFT-DELETES the old parent->child
// hierarchy edge and CREATES a new child->parent edge with swapped endpoints. To
// make newRootID the apex, every hierarchy edge on the path from the old root
// DOWN to newRootID is reversed this way; all concepts and all lateral edges are
// preserved untouched. Re-rooting to a concept that is already the apex is a
// no-op (empty plan, no error).
//
// PURITY: stdlib-only (context is imported only for the persistence PORT below,
// not the engine), injected `now`, sentinel errors wrapped with %w — mirroring
// the WS-1 aggregate style (concept_node.go / edge.go). No DB, no I/O.
package conceptgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Re-root sentinels (wrapped with %w so the HTTP/gRPC adapter maps each to its
// own status). Empty target reuses ErrInvalid; a soft-deleted target reuses
// ErrConceptDeleted — both defined in concept_node.go (same package).
var (
	// ErrReRootUnknownRoot — newRootID is not among the supplied concepts.
	ErrReRootUnknownRoot = errors.New("conceptgraph: re-root target concept not found")
	// ErrReRootCycle — the live hierarchy contains a cycle on the walk-up path
	// (corrupt data); refused rather than looping forever.
	ErrReRootCycle = errors.New("conceptgraph: hierarchy cycle detected during re-root")
	// ErrReRootMultipleParents — a concept on the path to the apex has more than
	// one live hierarchy parent, so the re-root is ambiguous.
	ErrReRootMultipleParents = errors.New("conceptgraph: concept has multiple hierarchy parents; re-root is ambiguous")
)

// ReRootPlan is the append-only edit set that makes NewRootID the apex:
// ToSoftDelete holds the existing hierarchy edges to tombstone (already stamped
// with deleted_at = now), ToCreate holds the flipped child->parent hierarchy
// edges to insert. Concepts and lateral edges never appear here. A no-op plan
// (already-apex) has both slices empty.
type ReRootPlan struct {
	NewRootID    string
	ToSoftDelete []Edge
	ToCreate     []Edge
}

// IsNoOp reports whether the plan changes nothing (target already the apex).
func (p ReRootPlan) IsNoOp() bool {
	return len(p.ToSoftDelete) == 0 && len(p.ToCreate) == 0
}

// ReRootApplier persists a ReRootPlan atomically (soft-delete the superseded
// hierarchy edges + create the flipped ones in one transaction). Declared here
// — NOT in ports.go — so WS-2 adds no lines to the WS-1 port file; the pg
// adapter (concept_rerooting.go) implements it.
type ReRootApplier interface {
	Apply(ctx context.Context, plan ReRootPlan) error
}

// ReRoot computes the append-only plan that re-parents the learner's hierarchy so
// newRootID becomes the apex. It is pure: `concepts` and `edges` are read-only
// (only local copies are mutated), `now` is injected. Only LIVE (deleted_at ==
// nil) hierarchy edges are traversed; soft-deleted edges and lateral edges are
// ignored. See the package/file doc for the source=parent / target=child
// convention.
func ReRoot(concepts []ConceptNode, edges []Edge, newRootID string, now time.Time) (ReRootPlan, error) {
	root := strings.TrimSpace(newRootID)
	if root == "" {
		return ReRootPlan{}, fmt.Errorf("%w: new_root_id required", ErrInvalid)
	}

	// Validate the target concept exists AND is live (guard soft-deleted concepts).
	var found bool
	for i := range concepts {
		if concepts[i].ConceptID == root {
			found = true
			if concepts[i].DeletedAt != nil {
				return ReRootPlan{}, fmt.Errorf("%w: %s", ErrConceptDeleted, root)
			}
			break
		}
	}
	if !found {
		return ReRootPlan{}, fmt.Errorf("%w: %s", ErrReRootUnknownRoot, root)
	}

	// Index each child -> its live hierarchy parent edge(s). Only live hierarchy
	// edges participate (guard soft-deleted + lateral edges).
	parentEdges := make(map[string][]Edge, len(edges))
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil || e.Class != EdgeClassHierarchy {
			continue
		}
		parentEdges[e.TargetConceptID] = append(parentEdges[e.TargetConceptID], e)
	}

	n := resolveNow(now)
	plan := ReRootPlan{NewRootID: root}
	visited := map[string]struct{}{root: {}}

	// Walk UP the parent chain from newRootID to the apex, reversing each edge.
	for current := root; ; {
		parents := parentEdges[current]
		if len(parents) == 0 {
			break // no parent -> current is the apex; done
		}
		if len(parents) > 1 {
			return ReRootPlan{}, fmt.Errorf("%w: concept %s has %d hierarchy parents", ErrReRootMultipleParents, current, len(parents))
		}
		parentEdge := parents[0]
		parent := parentEdge.SourceConceptID
		if _, seen := visited[parent]; seen {
			return ReRootPlan{}, fmt.Errorf("%w: concept %s revisited", ErrReRootCycle, parent)
		}

		// (a) supersede the old parent->child edge (append-only: tombstone, keep).
		superseded := parentEdge
		superseded.SoftDelete(n)
		plan.ToSoftDelete = append(plan.ToSoftDelete, superseded)

		// (b) create the flipped child->parent edge (endpoints immutable => new
		// row). Provenance is PRESERVED: re-rooting re-expresses an existing
		// learner-curated relation, it does not re-author it (Learner Ownership).
		flipped, err := NewEdge(NewEdgeInput{
			TenantID:        parentEdge.TenantID,
			LearnerGCID:     parentEdge.LearnerGCID,
			SourceConceptID: current, // was child; becomes parent
			TargetConceptID: parent,  // was parent; becomes child
			Class:           EdgeClassHierarchy,
			Provenance:      parentEdge.Provenance,
			Now:             n,
		})
		if err != nil {
			return ReRootPlan{}, fmt.Errorf("conceptgraph: re-root flip %s->%s: %w", current, parent, err)
		}
		plan.ToCreate = append(plan.ToCreate, *flipped)

		visited[parent] = struct{}{}
		current = parent
	}

	return plan, nil
}
