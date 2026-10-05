// select_learning_edges.go — CHO-2038 (ADR-212/214): the goal-scoped ceremony
// "learning-edges" selection. When the learner ticks edges the Companion crawl
// proposed at the summon ceremony, each ticked selection mints a ConceptNode AND
// a hierarchy Edge root->concept, so the concept lands in the goal's root subtree
// (SubtreeConceptIDs) and paints on the map. Each edge carries a remediate|explore
// intent (Fork 2, "Both, labelled"). Provenance is companion_suggested_accepted —
// the Companion proposed (crawl), the learner accepted (tick).
//
// PURE domain: stdlib-only, no infra, no clock leak. Dedup is WITHIN the batch
// only (by normalised title, first-seen wins); dedup against the learner's
// EXISTING concepts is an adapter concern (the repo). The caller persists each
// result's node+edge in ONE transaction (mirrors SuggestionAccept.ApplyAccept).
package conceptgraph

import (
	"fmt"
	"strings"
	"time"
)

// LearningEdgeSelection is one ceremony-proposed edge the learner ticked.
type LearningEdgeSelection struct {
	Title    string   // learner-facing concept name (required)
	Intent   Intent   // remediate|explore (required — must be Labelled)
	AtomRefs []string // optional grounding atoms (LearningAtom UUIDs)
}

// SelectLearningEdgesInput is the batch the ceremony confirms for one goal.
type SelectLearningEdgesInput struct {
	TenantID      string
	LearnerGCID   string
	RootConceptID string // the goal's root; each new concept hierarchy-links under it
	Selections    []LearningEdgeSelection
	Now           time.Time
}

// MintedLearningEdge is one minted concept + the hierarchy edge placing it under
// the goal root, plus its intent (mirrors Node.Intent for caller convenience).
type MintedLearningEdge struct {
	Node   *ConceptNode
	Edge   *Edge
	Intent Intent
}

// SelectLearningEdges mints, per ticked selection, a ConceptNode
// (provenance=companion_suggested_accepted, intent carried) and a hierarchy Edge
// root->concept so it joins the goal's subtree. Selections are de-duplicated
// within the batch by normalised title (first-seen wins). Fails loud (ErrInvalid)
// on: blank tenant/learner, blank root (a learning-edge needs a goal map to land
// on), an empty batch, or any selection with a blank title or an
// unlabelled/invalid intent.
func SelectLearningEdges(in SelectLearningEdgesInput) ([]MintedLearningEdge, error) {
	tenant, learner, err := requireTenantLearner(in.TenantID, in.LearnerGCID)
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(in.RootConceptID)
	if root == "" {
		return nil, fmt.Errorf("%w: root_concept_id required (a learning-edge needs a goal map to land on)", ErrInvalid)
	}
	if len(in.Selections) == 0 {
		return nil, fmt.Errorf("%w: at least one learning-edge selection required", ErrInvalid)
	}
	now := resolveNow(in.Now)

	out := make([]MintedLearningEdge, 0, len(in.Selections))
	seen := make(map[string]struct{}, len(in.Selections))
	for _, sel := range in.Selections {
		title := strings.TrimSpace(sel.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: a learning-edge must be named (title required)", ErrInvalid)
		}
		if !sel.Intent.Labelled() {
			return nil, fmt.Errorf("%w: learning-edge %q must be labelled remediate|explore (got %q)", ErrInvalid, title, sel.Intent)
		}
		dedupKey := strings.ToLower(title)
		if _, dup := seen[dedupKey]; dup {
			continue // first-seen wins within the batch
		}
		seen[dedupKey] = struct{}{}

		node, err := NewConceptNode(NewConceptNodeInput{
			TenantID: tenant, LearnerGCID: learner, Title: title,
			AtomRefs: sel.AtomRefs, Provenance: ProvenanceCompanionSuggestedAccepted,
			Intent: sel.Intent, Now: now,
		})
		if err != nil {
			return nil, err
		}
		edge, err := NewEdge(NewEdgeInput{
			TenantID: tenant, LearnerGCID: learner,
			SourceConceptID: root, TargetConceptID: node.ConceptID,
			Class: EdgeClassHierarchy, Provenance: ProvenanceCompanionSuggestedAccepted, Now: now,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, MintedLearningEdge{Node: node, Edge: edge, Intent: sel.Intent})
	}
	return out, nil
}
