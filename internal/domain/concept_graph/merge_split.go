// merge_split.go — the pure merge + split planners for the learner-sovereign
// Discovery graph (ADR-227 D14, WS-C6 / CHO-2085). A learner consolidates two
// concepts into one (MERGE) or fractures one concept into several (SPLIT) as
// their map matures. Both are dual-axis operations: they restructure the concept
// UUID hierarchy AND emit a LineageRecord on the concept_key slug axis so WS-C5
// can refuse XP re-awards across lineage and WS-C6 can repair the slug-keyed
// joins (migration 0077's concept_node_lineage table).
//
// HIERARCHY CONVENTION (shared with rerooting.go / subtree.go): a hierarchy Edge
// is directed source=PARENT -> target=CHILD; live == deleted_at == nil. Lateral
// edges (EdgeClassLateral) are relates-to cross-links, never containment.
//
// APPEND-ONLY (WS-1 froze Edge endpoints as immutable): re-pointing an edge is
// SOFT-DELETE the old + NewEdge with the new endpoints, PRESERVING provenance —
// merge/split re-EXPRESS existing learner-curated relations, they do not
// re-author them (Learner Ownership). Concepts are soft-deleted, never
// hard-deleted (#5); split children are minted fresh via NewConceptNode.
//
// PURITY: stdlib-only, injected `now`, sentinels wrapped with %w — mirroring the
// WS-1/WS-2 aggregate style. `concepts` and `edges` are read-only; only local
// copies are ever mutated. No DB, no I/O.
package conceptgraph

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Merge/split sentinels (wrapped with %w so the HTTP/gRPC adapter maps each to
// its own status). Empty/self operands reuse ErrInvalid; a soft-deleted operand
// reuses ErrConceptDeleted; a self-referential re-pointed lateral reuses the
// aggregate's ErrSelfLoop implicitly (dropped, never emitted).
var (
	// ErrRootImmutable — the operand is a goal's focus/root concept, which cannot
	// be destroyed. NB merging INTO a root keeps the root; only a root as the
	// ABSORBED (merge) or the split PARENT is refused.
	ErrRootImmutable = errors.New("conceptgraph: a goal's root concept cannot be merged away or split")
	// ErrMergeSplitUnknownConcept — an operand id is not among the supplied concepts.
	ErrMergeSplitUnknownConcept = errors.New("conceptgraph: merge/split concept not found")
	// ErrMergeSplitMultipleParents — a manipulated node has more than one live
	// hierarchy parent, so the single-parent-per-path splice is ambiguous.
	ErrMergeSplitMultipleParents = errors.New("conceptgraph: concept has multiple hierarchy parents; merge/split is ambiguous")
	// ErrSplitTooFew — a split must yield at least two children (else it is a rename).
	ErrSplitTooFew = errors.New("conceptgraph: a split needs at least two children")
	// ErrSplitDuplicateChildKey — two child titles normalise to the same concept_key.
	ErrSplitDuplicateChildKey = errors.New("conceptgraph: two split children normalise to the same concept key")
)

// LineageOperation is the D14 ancestry operation recorded on both identity axes
// (concept UUID + concept_key slug), mirroring migration 0077's
// concept_node_lineage.operation CHECK.
type LineageOperation string

const (
	LineageOperationMerge LineageOperation = "merge"
	LineageOperationSplit LineageOperation = "split"
)

// LineageRecord is one row of concept_node_lineage: a directed ancestry link on
// BOTH axes. Merge: From = absorbed node, To = survivor. Split: From = parent,
// To = a child. The *_ConceptKey fields are the slug axis (minted-once from
// title, NOT identity) so slug-keyed joins survive the restructure.
type LineageRecord struct {
	Operation      LineageOperation
	FromConceptID  string // merge: absorbed node; split: parent
	ToConceptID    string // merge: survivor;      split: child
	FromConceptKey string // slug axis (concept_key ancestry)
	ToConceptKey   string
}

// MergePlan is the append-only edit set that consolidates `absorbed` into
// `survivor`: EdgesToSoftDelete tombstones the superseded edges (stamped at now),
// EdgesToCreate holds the re-expressed edges, Absorbed is the tombstoned concept
// copy, Lineage is the absorbed->survivor ancestry link. The survivor concept is
// never modified.
type MergePlan struct {
	SurvivorID string
	// Survivor is the survivor copy carrying the UNION of both operands'
	// atom_refs (ADR-244 D6). It is a live node (DeletedAt nil) and must be
	// UPDATED, not tombstoned. Before D6 the absorbed node was tombstoned with
	// its atoms still attached, so every binding curated there vanished from
	// the live graph — split already inherited the parent's atoms, and merge is
	// its dual.
	Survivor          ConceptNode
	Absorbed          ConceptNode
	EdgesToSoftDelete []Edge
	EdgesToCreate     []Edge
	Lineage           LineageRecord
}

// SplitPlan is the append-only edit set that fractures `parent` into Children:
// EdgesToSoftDelete tombstones the parent's edges, EdgesToCreate re-expresses
// them onto the children, Parent is the tombstoned concept copy, Lineage holds
// one parent->child record per child in child order.
type SplitPlan struct {
	Parent            ConceptNode
	Children          []ConceptNode
	EdgesToSoftDelete []Edge
	EdgesToCreate     []Edge
	Lineage           []LineageRecord
}

// SplitChildSpec names one child a split produces (its concept_key is derived
// from the title at mint).
type SplitChildSpec struct {
	Title string
}

// PlanMerge computes the append-only plan that consolidates `absorbed` into
// `survivor`. It is pure: `concepts`/`edges` are read-only, `now` is injected,
// only LIVE edges participate. goalRootIDs holds the learner's goal focus/root
// concepts — the absorbed operand may not be one (merging INTO a root is fine).
// See the file doc for the hierarchy + append-only conventions.
func PlanMerge(concepts []ConceptNode, edges []Edge, survivorID, absorbedID string, goalRootIDs map[string]bool, now time.Time) (MergePlan, error) {
	survivor := strings.TrimSpace(survivorID)
	absorbed := strings.TrimSpace(absorbedID)
	if survivor == "" || absorbed == "" {
		return MergePlan{}, fmt.Errorf("%w: survivor_id and absorbed_id required", ErrInvalid)
	}
	if survivor == absorbed {
		return MergePlan{}, fmt.Errorf("%w: cannot merge a concept into itself", ErrInvalid)
	}

	// Both operands must exist AND be live.
	survivorNode, err := requireLiveConcept(concepts, survivor)
	if err != nil {
		return MergePlan{}, err
	}
	absorbedNode, err := requireLiveConcept(concepts, absorbed)
	if err != nil {
		return MergePlan{}, err
	}

	// A goal's root cannot be destroyed; only the SURVIVOR may be a root.
	if goalRootIDs[absorbed] {
		return MergePlan{}, fmt.Errorf("%w: %s is a goal root", ErrRootImmutable, absorbed)
	}

	// Absorbed's live hierarchy parent — ambiguous if there is more than one.
	if aps := liveParentEdges(edges, absorbed); len(aps) > 1 {
		return MergePlan{}, fmt.Errorf("%w: absorbed %s has %d hierarchy parents", ErrMergeSplitMultipleParents, absorbed, len(aps))
	}

	// pathChild = absorbed's live hierarchy child whose downward subtree contains
	// survivor (may BE survivor). Re-parenting it under survivor would create a
	// cycle, so instead it takes absorbed's structural place. There is at most one
	// under the single-parent discipline; the first match wins deterministically.
	var pathChild string
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil || e.Class != EdgeClassHierarchy || e.SourceConceptID != absorbed {
			continue
		}
		if SubtreeConceptIDs(concepts, edges, e.TargetConceptID)[survivor] {
			pathChild = e.TargetConceptID
			break
		}
	}
	// If survivor is where we splice (it takes absorbed's parent), its own parent
	// count must be unambiguous too — the "walk through it" case.
	if pathChild == survivor {
		if sps := liveParentEdges(edges, survivor); len(sps) > 1 {
			return MergePlan{}, fmt.Errorf("%w: survivor %s has %d hierarchy parents", ErrMergeSplitMultipleParents, survivor, len(sps))
		}
	}

	n := resolveNow(now)

	// Existing live hierarchy pairs (dedupe survivor->Y re-parents) + live lateral
	// pairs NOT touching absorbed (dedupe re-pointed laterals).
	liveHierPair := map[string]bool{}
	lateralPair := map[string]bool{}
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil {
			continue
		}
		switch e.Class {
		case EdgeClassHierarchy:
			liveHierPair[hierKey(e.SourceConceptID, e.TargetConceptID)] = true
		case EdgeClassLateral:
			if e.SourceConceptID != absorbed && e.TargetConceptID != absorbed {
				lateralPair[unorderedPairKey(e.SourceConceptID, e.TargetConceptID)] = true
			}
		}
	}
	createdHier := map[string]bool{}

	plan := MergePlan{SurvivorID: survivor}

	// Single pass over input edges preserves deterministic output order.
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil {
			continue
		}
		switch {
		case e.Class == EdgeClassHierarchy && e.TargetConceptID == absorbed:
			// Absorbed's parent edge P->absorbed: tombstone it, and hand P's slot to
			// the pathChild if one exists (else absorbed was an apex — nothing to make).
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			if pathChild != "" {
				created, err := newEdgeFrom(e, e.SourceConceptID, pathChild, EdgeClassHierarchy, n)
				if err != nil {
					return MergePlan{}, err
				}
				plan.EdgesToCreate = append(plan.EdgesToCreate, created)
			}

		case e.Class == EdgeClassHierarchy && e.SourceConceptID == absorbed:
			// Absorbed's child edge absorbed->X: always tombstoned. The pathChild
			// takes absorbed's place (handled via the parent edge / apex) so it gets
			// no survivor->X edge; every other child re-parents under survivor.
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			if e.TargetConceptID == pathChild {
				continue
			}
			key := hierKey(survivor, e.TargetConceptID)
			if liveHierPair[key] || createdHier[key] {
				continue // a live survivor->X already exists — no duplicate
			}
			created, err := newEdgeFrom(e, survivor, e.TargetConceptID, EdgeClassHierarchy, n)
			if err != nil {
				return MergePlan{}, err
			}
			plan.EdgesToCreate = append(plan.EdgesToCreate, created)
			createdHier[key] = true

		case e.Class == EdgeClassLateral && (e.SourceConceptID == absorbed || e.TargetConceptID == absorbed):
			// Absorbed's lateral: tombstone + re-point the absorbed endpoint to
			// survivor, unless it self-loops onto survivor or duplicates a pair.
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			srcIsAbsorbed := e.SourceConceptID == absorbed
			other := e.SourceConceptID
			if srcIsAbsorbed {
				other = e.TargetConceptID
			}
			if other == survivor {
				continue // would self-loop survivor~survivor
			}
			pk := unorderedPairKey(survivor, other)
			if lateralPair[pk] {
				continue // dedupe against a live/created survivor lateral
			}
			newSrc, newTgt := survivor, other
			if !srcIsAbsorbed {
				newSrc, newTgt = other, survivor
			}
			created, err := newEdgeFrom(e, newSrc, newTgt, EdgeClassLateral, n)
			if err != nil {
				return MergePlan{}, err
			}
			plan.EdgesToCreate = append(plan.EdgesToCreate, created)
			lateralPair[pk] = true
		}
	}

	// ADR-244 D6: the survivor inherits the absorbed node's atom bindings before
	// the absorbed copy is tombstoned. Order-preserving (survivor's own refs
	// first) and de-duplicated. requireLiveConcept returns a VALUE copy whose
	// AtomRefs slice still shares the caller's backing array, so this builds a
	// fresh slice rather than appending — the file's purity contract is that
	// `concepts` is read-only.
	survivorNode.AtomRefs = unionAtomRefs(survivorNode.AtomRefs, absorbedNode.AtomRefs)
	survivorNode.touch(n)
	plan.Survivor = survivorNode

	absorbedNode.SoftDelete(n) // absorbedNode is a copy; the input is never mutated
	plan.Absorbed = absorbedNode
	plan.Lineage = LineageRecord{
		Operation:      LineageOperationMerge,
		FromConceptID:  absorbedNode.ConceptID,
		ToConceptID:    survivorNode.ConceptID,
		FromConceptKey: absorbedNode.ConceptKey,
		ToConceptKey:   survivorNode.ConceptKey,
	}
	return plan, nil
}

// PlanSplit computes the append-only plan that fractures `parent` into the
// children named by `specs`. It is pure (see file doc). assignChildren routes an
// existing live hierarchy child of the parent to the split-child index that
// should adopt it (a missing key defaults to child 0); its keys must be live
// hierarchy children and its values in [0,len(specs)). goalRootIDs holds the
// learner's goal roots — the parent may not be one.
func PlanSplit(concepts []ConceptNode, edges []Edge, parentID string, specs []SplitChildSpec, assignChildren map[string]int, goalRootIDs map[string]bool, now time.Time) (SplitPlan, error) {
	parent := strings.TrimSpace(parentID)
	if parent == "" {
		return SplitPlan{}, fmt.Errorf("%w: parent_id required", ErrInvalid)
	}
	parentNode, err := requireLiveConcept(concepts, parent)
	if err != nil {
		return SplitPlan{}, err
	}
	if goalRootIDs[parent] {
		return SplitPlan{}, fmt.Errorf("%w: %s is a goal root", ErrRootImmutable, parent)
	}
	if len(specs) < 2 {
		return SplitPlan{}, fmt.Errorf("%w: got %d", ErrSplitTooFew, len(specs))
	}
	// Titles required + child concept_keys pairwise distinct.
	seenKey := make(map[string]bool, len(specs))
	for _, s := range specs {
		if strings.TrimSpace(s.Title) == "" {
			return SplitPlan{}, fmt.Errorf("%w: a split child must be named (title required)", ErrInvalid)
		}
		key := normalizeConceptKey(s.Title)
		if seenKey[key] {
			return SplitPlan{}, fmt.Errorf("%w: %q", ErrSplitDuplicateChildKey, key)
		}
		seenKey[key] = true
	}

	// Parent's live hierarchy children (edge-live adjacency) for assignment checks.
	liveChildren := map[string]bool{}
	for i := range edges {
		e := edges[i]
		if e.DeletedAt == nil && e.Class == EdgeClassHierarchy && e.SourceConceptID == parent {
			liveChildren[e.TargetConceptID] = true
		}
	}
	for childID, idx := range assignChildren {
		if !liveChildren[childID] {
			return SplitPlan{}, fmt.Errorf("%w: assignment references %s which is not a live hierarchy child of %s", ErrInvalid, childID, parent)
		}
		if idx < 0 || idx >= len(specs) {
			return SplitPlan{}, fmt.Errorf("%w: assignment index %d out of range [0,%d)", ErrInvalid, idx, len(specs))
		}
	}

	// Parent's live hierarchy parent — ambiguous if there is more than one.
	if pps := liveParentEdges(edges, parent); len(pps) > 1 {
		return SplitPlan{}, fmt.Errorf("%w: parent %s has %d hierarchy parents", ErrMergeSplitMultipleParents, parent, len(pps))
	}

	n := resolveNow(now)

	// Mint the children (order matches specs). Each organises the parent's atoms
	// (learner detaches later) and inherits the parent's Intent; provenance is
	// learner-authored (the learner performed the split).
	children := make([]ConceptNode, 0, len(specs))
	for _, s := range specs {
		child, err := NewConceptNode(NewConceptNodeInput{
			TenantID:    parentNode.TenantID,
			LearnerGCID: parentNode.LearnerGCID,
			Title:       s.Title,
			AtomRefs:    parentNode.AtomRefs,
			Provenance:  ProvenanceLearnerAuthored,
			Intent:      parentNode.Intent,
			Now:         n,
		})
		if err != nil {
			return SplitPlan{}, fmt.Errorf("conceptgraph: split mint child %q: %w", s.Title, err)
		}
		children = append(children, *child)
	}
	head := children[0].ConceptID // the child that adopts the parent's laterals

	// Live lateral pairs NOT touching parent (dedupe re-pointed laterals).
	lateralPair := map[string]bool{}
	for i := range edges {
		e := edges[i]
		if e.DeletedAt == nil && e.Class == EdgeClassLateral &&
			e.SourceConceptID != parent && e.TargetConceptID != parent {
			lateralPair[unorderedPairKey(e.SourceConceptID, e.TargetConceptID)] = true
		}
	}

	plan := SplitPlan{Children: children}

	// Single pass over input edges preserves deterministic output order.
	for i := range edges {
		e := edges[i]
		if e.DeletedAt != nil {
			continue
		}
		switch {
		case e.Class == EdgeClassHierarchy && e.TargetConceptID == parent:
			// Parent's parent edge P->parent: fan out P->child for every child so the
			// children become siblings under P (apex parent => no fan-out, children
			// become apexes).
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			for j := range children {
				created, err := newEdgeFrom(e, e.SourceConceptID, children[j].ConceptID, EdgeClassHierarchy, n)
				if err != nil {
					return SplitPlan{}, err
				}
				plan.EdgesToCreate = append(plan.EdgesToCreate, created)
			}

		case e.Class == EdgeClassHierarchy && e.SourceConceptID == parent:
			// Parent's child edge parent->X: re-parent X under its assigned child
			// (missing assignment defaults to child 0).
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			idx := assignChildren[e.TargetConceptID]
			created, err := newEdgeFrom(e, children[idx].ConceptID, e.TargetConceptID, EdgeClassHierarchy, n)
			if err != nil {
				return SplitPlan{}, err
			}
			plan.EdgesToCreate = append(plan.EdgesToCreate, created)

		case e.Class == EdgeClassLateral && (e.SourceConceptID == parent || e.TargetConceptID == parent):
			// Parent's lateral: tombstone + re-point the parent endpoint to child 0,
			// same self-loop + unordered-pair dedupe as merge.
			plan.EdgesToSoftDelete = append(plan.EdgesToSoftDelete, softDeleted(e, n))
			srcIsParent := e.SourceConceptID == parent
			other := e.SourceConceptID
			if srcIsParent {
				other = e.TargetConceptID
			}
			if other == head {
				continue // self-loop (defensive; head is freshly minted)
			}
			pk := unorderedPairKey(head, other)
			if lateralPair[pk] {
				continue
			}
			newSrc, newTgt := head, other
			if !srcIsParent {
				newSrc, newTgt = other, head
			}
			created, err := newEdgeFrom(e, newSrc, newTgt, EdgeClassLateral, n)
			if err != nil {
				return SplitPlan{}, err
			}
			plan.EdgesToCreate = append(plan.EdgesToCreate, created)
			lateralPair[pk] = true
		}
	}

	parentNode.SoftDelete(n) // parentNode is a copy; the input is never mutated
	plan.Parent = parentNode
	for j := range children {
		plan.Lineage = append(plan.Lineage, LineageRecord{
			Operation:      LineageOperationSplit,
			FromConceptID:  parentNode.ConceptID,
			ToConceptID:    children[j].ConceptID,
			FromConceptKey: parentNode.ConceptKey,
			ToConceptKey:   children[j].ConceptKey,
		})
	}
	return plan, nil
}

// --- shared helpers ---

// requireLiveConcept returns a COPY of the concept named id (so callers may
// tombstone it without mutating the input), or a sentinel if it is missing or
// soft-deleted.
// unionAtomRefs returns survivor's refs followed by any of absorbed's it does
// not already hold, de-duplicated and order-preserving (ADR-244 D6). It always
// allocates, so neither input's backing array is ever written through.
func unionAtomRefs(survivor, absorbed []string) []string {
	out := make([]string, 0, len(survivor)+len(absorbed))
	seen := make(map[string]bool, len(survivor)+len(absorbed))
	for _, group := range [][]string{survivor, absorbed} {
		for _, ref := range group {
			r := strings.TrimSpace(ref)
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func requireLiveConcept(concepts []ConceptNode, id string) (ConceptNode, error) {
	for i := range concepts {
		if concepts[i].ConceptID == id {
			if concepts[i].DeletedAt != nil {
				return ConceptNode{}, fmt.Errorf("%w: %s", ErrConceptDeleted, id)
			}
			return concepts[i], nil
		}
	}
	return ConceptNode{}, fmt.Errorf("%w: %s", ErrMergeSplitUnknownConcept, id)
}

// liveParentEdges returns the live hierarchy edges whose target is child (its
// parent edges).
func liveParentEdges(edges []Edge, child string) []Edge {
	var out []Edge
	for i := range edges {
		e := edges[i]
		if e.DeletedAt == nil && e.Class == EdgeClassHierarchy && e.TargetConceptID == child {
			out = append(out, e)
		}
	}
	return out
}

// softDeleted stamps a COPY of e with deleted_at = now (e is passed by value).
func softDeleted(e Edge, now time.Time) Edge {
	e.SoftDelete(now)
	return e
}

// newEdgeFrom re-expresses an edge on new endpoints, inheriting tenant/learner/
// provenance from the template (append-only re-point). A corrupt template
// (e.g. an invalid stored provenance) surfaces loudly rather than being swallowed.
func newEdgeFrom(template Edge, source, target string, class EdgeClass, now time.Time) (Edge, error) {
	created, err := NewEdge(NewEdgeInput{
		TenantID:        template.TenantID,
		LearnerGCID:     template.LearnerGCID,
		SourceConceptID: source,
		TargetConceptID: target,
		Class:           class,
		Provenance:      template.Provenance,
		Now:             now,
	})
	if err != nil {
		return Edge{}, fmt.Errorf("conceptgraph: merge/split re-express %s->%s: %w", source, target, err)
	}
	return *created, nil
}

// hierKey is the directed-pair key for hierarchy dedupe (source->target).
func hierKey(source, target string) string { return source + "\x00" + target }

// unorderedPairKey is the direction-agnostic key for lateral dedupe.
func unorderedPairKey(a, b string) string {
	if a <= b {
		return a + "\x00" + b
	}
	return b + "\x00" + a
}
