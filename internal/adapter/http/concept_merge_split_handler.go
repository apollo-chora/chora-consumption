// concept_merge_split_handler.go — WS-C6 (CHO-2085, ADR-227 D14 + addendum
// #7): the learner-sovereign merge/split doors, nested under the already-
// proxied /v1/me/concept-graph/concepts/{id} subtree (zero gateway plumbing):
//
//	POST /v1/me/concept-graph/concepts/{id}/merge  {survivorConceptId}
//	POST /v1/me/concept-graph/concepts/{id}/split  {children:[{title}...],
//	     childAssignments:{childId:index}, focusChildIndex}
//
// The door orchestrates four aggregate planners and commits their combined
// delta ATOMICALLY via mergesplit.Applier (one chora_consumption tx):
// conceptgraph.PlanMerge/PlanSplit (append-only edges + dual-axis lineage),
// campaign.MergeProgress/SplitProgress (AND-of-rungs / inherit — the operand
// rows tombstone FIRST, becoming the D14 XP-refusal history), topic_retention
// WeakerOf/CloneForTopic (weakest retention travels), goal.RepairConceptSet +
// SetFocusConcept (slug-axis set repair; focus follows the learner's in-flow
// choice). Deliberately NO campaign events fire (the C5 seam: a merge/split
// materialises state without XP or reveal re-fire); the Companion's resonance
// follows a focus retarget POST-commit (addendum #5, C1 idiom — idempotent,
// persisted focus survives a retryable resonance failure).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/mergesplit"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

type conceptMergeReq struct {
	SurvivorConceptID string `json:"survivorConceptId"`
}

type conceptSplitReq struct {
	Children         []conceptSplitChildReq `json:"children"`
	ChildAssignments map[string]int         `json:"childAssignments"`
	FocusChildIndex  *int                   `json:"focusChildIndex"`
}

type conceptSplitChildReq struct {
	Title string `json:"title"`
}

type conceptSplitChildDTO struct {
	ConceptID  string `json:"conceptId"`
	Title      string `json:"title"`
	ConceptKey string `json:"conceptKey"`
}

// mergeSplitDeps fail-loud gate: the doors need the graph, the goal spine,
// the ladder, the retention curve and the atomic applier (no stubs — a
// missing dep is a wiring bug, not a silent no-op).
func (s *ExtServer) mergeSplitDeps(w http.ResponseWriter) bool {
	if s.Concepts == nil || s.ConceptEdges == nil || s.Goals == nil ||
		s.MergeSplit == nil || s.CampaignProgress == nil || s.Retention == nil || s.CampaignResonance == nil {
		extWriteError(w, http.StatusServiceUnavailable, "MERGE_SPLIT_NOT_WIRED", "merge/split surface not wired")
		return false
	}
	return true
}

// mergeSplitGraph loads the learner's live graph + goals and derives the
// immutable-root set (every goal's root concept, D14/ADR-214).
func (s *ExtServer) mergeSplitGraph(ctx context.Context, w http.ResponseWriter, tenantID, gcid string) ([]conceptgraph.ConceptNode, []conceptgraph.Edge, []*goal.Goal, map[string]bool, bool) {
	nodes, err := s.Concepts.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GRAPH_LOAD_FAILED", err.Error())
		return nil, nil, nil, nil, false
	}
	edges, err := s.ConceptEdges.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GRAPH_LOAD_FAILED", err.Error())
		return nil, nil, nil, nil, false
	}
	goals, err := s.Goals.ListByLearner(ctx, tenantID, gcid)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "GOALS_LOAD_FAILED", err.Error())
		return nil, nil, nil, nil, false
	}
	rootIDs := make(map[string]bool, len(goals))
	for _, g := range goals {
		if g != nil && g.RootConceptID != nil && strings.TrimSpace(*g.RootConceptID) != "" {
			rootIDs[*g.RootConceptID] = true
		}
	}
	return derefConcepts(nodes), derefEdges(edges), goals, rootIDs, true
}

// handleMeConceptMerge — POST /v1/me/concept-graph/concepts/{id}/merge.
// {id} is the ABSORBED node; the body names the survivor.
func (s *ExtServer) handleMeConceptMerge(w http.ResponseWriter, r *http.Request, absorbedID string) {
	if !s.mergeSplitDeps(w) {
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	var req conceptMergeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	now := time.Now().UTC()

	nodeVals, edgeVals, goals, rootIDs, ok := s.mergeSplitGraph(ctx, w, tenantID, gcid)
	if !ok {
		return
	}
	plan, err := conceptgraph.PlanMerge(nodeVals, edgeVals, strings.TrimSpace(req.SurvivorConceptID), absorbedID, rootIDs, now)
	if err != nil {
		writeMergeSplitError(w, err)
		return
	}
	// Pre-merge refs, for the ADR-244 D4 delta after Apply succeeds.
	var survivorAtomsBefore []string
	for i := range nodeVals {
		if nodeVals[i].ConceptID == plan.SurvivorID {
			survivorAtomsBefore = append([]string(nil), nodeVals[i].AtomRefs...)
			break
		}
	}

	// Campaign ladder: AND-of-rungs; the operands' live rows tombstone (they
	// become the D14 refusal history the XP consumer reads).
	survivorProgress, err := s.CampaignProgress.GetByConcept(ctx, tenantID, gcid, plan.SurvivorID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PROGRESS_LOAD_FAILED", err.Error())
		return
	}
	absorbedProgress, err := s.CampaignProgress.GetByConcept(ctx, tenantID, gcid, absorbedID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PROGRESS_LOAD_FAILED", err.Error())
		return
	}
	var progressTombstones []string
	if survivorProgress != nil {
		progressTombstones = append(progressTombstones, plan.SurvivorID)
	}
	if absorbedProgress != nil {
		progressTombstones = append(progressTombstones, absorbedID)
	}
	var progressInserts []*campaign.NodeProgress
	if survivorProgress != nil || absorbedProgress != nil {
		merged, err := campaign.MergeProgress(survivorProgress, absorbedProgress, tenantID, gcid, plan.SurvivorID, now)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "PROGRESS_MERGE_FAILED", err.Error())
			return
		}
		if merged != nil {
			progressInserts = append(progressInserts, merged)
		}
	}

	// Retention: the survivor's key carries the WEAKEST curve of the pair.
	survivorKey, absorbedKey := plan.Lineage.ToConceptKey, plan.Lineage.FromConceptKey
	var retentions []*topic_retention.TopicScore
	survivorScore, err := s.Retention.Get(ctx, tenantID, gcid, survivorKey)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "RETENTION_LOAD_FAILED", err.Error())
		return
	}
	absorbedScore, err := s.Retention.Get(ctx, tenantID, gcid, absorbedKey)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "RETENTION_LOAD_FAILED", err.Error())
		return
	}
	if weaker := topic_retention.WeakerOf(survivorScore, absorbedScore, now); weaker != nil && weaker != survivorScore {
		rekeyed, err := topic_retention.CloneForTopic(weaker, survivorKey, now)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "RETENTION_REKEY_FAILED", err.Error())
			return
		}
		retentions = append(retentions, rekeyed)
	}

	// Goal spine: slug-axis concept_set repair + focus follows the merge.
	updatedGoals, resonanceTargets, ok := repairGoalsForRekey(w, goals, absorbedKey, []string{survivorKey}, absorbedID, plan.SurvivorID, now)
	if !ok {
		return
	}

	apply := mergesplit.Apply{
		TenantID:         tenantID,
		LearnerGCID:      gcid,
		Now:              now,
		NodesToTombstone: []conceptgraph.ConceptNode{plan.Absorbed},
		// ADR-244 D6: the survivor carries the union of both operands' atoms.
		NodesToUpdate:               []conceptgraph.ConceptNode{plan.Survivor},
		EdgesToSoftDelete:           plan.EdgesToSoftDelete,
		EdgesToCreate:               plan.EdgesToCreate,
		Lineage:                     []conceptgraph.LineageRecord{plan.Lineage},
		ProgressTombstoneConceptIDs: progressTombstones,
		ProgressToInsert:            progressInserts,
		RetentionToUpsert:           retentions,
		SuggestionRepoints:          []mergesplit.SuggestionRepoint{{FromConceptID: absorbedID, ToConceptID: plan.SurvivorID}},
		GoalsToUpdate:               updatedGoals,
	}
	if err := apply.Validate(); err != nil {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_MERGE", err.Error())
		return
	}
	if err := s.MergeSplit.Apply(ctx, apply); err != nil {
		extWriteError(w, http.StatusInternalServerError, "MERGE_APPLY_FAILED", err.Error())
		return
	}
	// ADR-244 D4/D6: the survivor gained the absorbed node's atoms. Only the
	// SURVIVOR emits. The absorbed node is tombstoned, and reporting a detach
	// for it would misrepresent a concept-lifecycle fact as the learner
	// unbinding atoms they never touched.
	s.emitConceptAtomsBound(ctx, &plan.Survivor, survivorAtomsBefore, events.ChangeSourceMerge)
	if !s.retargetResonance(ctx, w, tenantID, resonanceTargets) {
		return
	}
	extWriteJSON(w, http.StatusOK, map[string]any{
		"survivorConceptId": plan.SurvivorID,
		"absorbedConceptId": absorbedID,
	})
}

// handleMeConceptSplit — POST /v1/me/concept-graph/concepts/{id}/split.
func (s *ExtServer) handleMeConceptSplit(w http.ResponseWriter, r *http.Request, parentID string) {
	if !s.mergeSplitDeps(w) {
		return
	}
	if r.Method != http.MethodPost {
		extWriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
		return
	}
	var req conceptSplitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		extWriteError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	tenantID, gcid, err := extRequireContext(r)
	if err != nil {
		extWriteError(w, http.StatusBadRequest, "MISSING_CONTEXT", err.Error())
		return
	}
	ctx := tracing.WithGCID(tracing.WithTenantID(r.Context(), tenantID), gcid)
	now := time.Now().UTC()

	specs := make([]conceptgraph.SplitChildSpec, 0, len(req.Children))
	for _, c := range req.Children {
		specs = append(specs, conceptgraph.SplitChildSpec{Title: c.Title})
	}
	focusIdx := 0
	if req.FocusChildIndex != nil {
		focusIdx = *req.FocusChildIndex
	}
	if focusIdx < 0 || (len(specs) > 0 && focusIdx >= len(specs)) {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_FOCUS_CHILD", "focusChildIndex outside the children range")
		return
	}

	nodeVals, edgeVals, goals, rootIDs, ok := s.mergeSplitGraph(ctx, w, tenantID, gcid)
	if !ok {
		return
	}
	plan, err := conceptgraph.PlanSplit(nodeVals, edgeVals, parentID, specs, req.ChildAssignments, rootIDs, now)
	if err != nil {
		writeMergeSplitError(w, err)
		return
	}
	chosen := plan.Children[focusIdx]

	// Campaign ladder: children inherit the parent's rungs; the parent's live
	// row tombstones (D14 refusal history) — and NO events fire (C5 seam).
	parentProgress, err := s.CampaignProgress.GetByConcept(ctx, tenantID, gcid, parentID)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "PROGRESS_LOAD_FAILED", err.Error())
		return
	}
	var progressTombstones []string
	var progressInserts []*campaign.NodeProgress
	if parentProgress != nil {
		progressTombstones = append(progressTombstones, parentID)
		childIDs := make([]string, 0, len(plan.Children))
		for _, c := range plan.Children {
			childIDs = append(childIDs, c.ConceptID)
		}
		copies, err := campaign.SplitProgress(parentProgress, tenantID, gcid, childIDs, now)
		if err != nil {
			extWriteError(w, http.StatusInternalServerError, "PROGRESS_SPLIT_FAILED", err.Error())
			return
		}
		progressInserts = copies
	}

	// Retention: each child key gets a clone of the parent's curve.
	parentKey := plan.Lineage[0].FromConceptKey
	var retentions []*topic_retention.TopicScore
	parentScore, err := s.Retention.Get(ctx, tenantID, gcid, parentKey)
	if err != nil {
		extWriteError(w, http.StatusInternalServerError, "RETENTION_LOAD_FAILED", err.Error())
		return
	}
	if parentScore != nil {
		for _, c := range plan.Children {
			clone, err := topic_retention.CloneForTopic(parentScore, c.ConceptKey, now)
			if err != nil {
				extWriteError(w, http.StatusInternalServerError, "RETENTION_REKEY_FAILED", err.Error())
				return
			}
			retentions = append(retentions, clone)
		}
	}

	// Goal spine: parent key leaves every scoping set, child keys join; focus
	// follows the learner's chosen child.
	childKeys := make([]string, 0, len(plan.Children))
	for _, c := range plan.Children {
		childKeys = append(childKeys, c.ConceptKey)
	}
	updatedGoals, resonanceTargets, ok := repairGoalsForRekey(w, goals, parentKey, childKeys, parentID, chosen.ConceptID, now)
	if !ok {
		return
	}

	apply := mergesplit.Apply{
		TenantID:                    tenantID,
		LearnerGCID:                 gcid,
		Now:                         now,
		NodesToTombstone:            []conceptgraph.ConceptNode{plan.Parent},
		NodesToCreate:               plan.Children,
		EdgesToSoftDelete:           plan.EdgesToSoftDelete,
		EdgesToCreate:               plan.EdgesToCreate,
		Lineage:                     plan.Lineage,
		ProgressTombstoneConceptIDs: progressTombstones,
		ProgressToInsert:            progressInserts,
		RetentionToUpsert:           retentions,
		SuggestionRepoints:          []mergesplit.SuggestionRepoint{{FromConceptID: parentID, ToConceptID: chosen.ConceptID}},
		GoalsToUpdate:               updatedGoals,
	}
	if err := apply.Validate(); err != nil {
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_SPLIT", err.Error())
		return
	}
	if err := s.MergeSplit.Apply(ctx, apply); err != nil {
		extWriteError(w, http.StatusInternalServerError, "SPLIT_APPLY_FAILED", err.Error())
		return
	}
	// ADR-244 D4: each split child inherits the parent's atoms, so each is a
	// fresh binding. The tombstoned parent emits nothing, for the same reason
	// the absorbed node does not on merge.
	for i := range plan.Children {
		s.emitConceptMinted(ctx, &plan.Children[i], events.ChangeSourceSplit)
	}
	if !s.retargetResonance(ctx, w, tenantID, resonanceTargets) {
		return
	}
	children := make([]conceptSplitChildDTO, 0, len(plan.Children))
	for _, c := range plan.Children {
		children = append(children, conceptSplitChildDTO{ConceptID: c.ConceptID, Title: c.Title, ConceptKey: c.ConceptKey})
	}
	extWriteJSON(w, http.StatusOK, map[string]any{
		"parentConceptId": parentID,
		"children":        children,
	})
}

// resonanceTarget carries one post-commit resonance retarget (addendum #5).
type resonanceTarget struct {
	companionID string
	conceptID   string
}

// repairGoalsForRekey walks the learner's goals applying the slug-axis
// concept_set repair (removeKey → addKeys, only where the set scopes the
// retired key) and the focus follow (deadConceptID → newFocusID). Returns the
// goals to persist in-tx + the post-commit resonance retargets.
func repairGoalsForRekey(w http.ResponseWriter, goals []*goal.Goal, removeKey string, addKeys []string, deadConceptID, newFocusID string, now time.Time) ([]*goal.Goal, []resonanceTarget, bool) {
	var updated []*goal.Goal
	var targets []resonanceTarget
	for _, g := range goals {
		if g == nil {
			continue
		}
		changed := false
		if conceptSetContains(g.ConceptSet, removeKey) {
			setChanged, err := g.RepairConceptSet([]string{removeKey}, addKeys, now)
			if err != nil {
				extWriteError(w, http.StatusInternalServerError, "CONCEPT_SET_REPAIR_FAILED", err.Error())
				return nil, nil, false
			}
			changed = changed || setChanged
		}
		if g.FocusConceptID != nil && *g.FocusConceptID == deadConceptID {
			focus := newFocusID
			if err := g.SetFocusConcept(&focus, now); err != nil {
				extWriteError(w, http.StatusInternalServerError, "FOCUS_REPAIR_FAILED", err.Error())
				return nil, nil, false
			}
			changed = true
			if g.AttachedCompanionID != nil {
				targets = append(targets, resonanceTarget{companionID: *g.AttachedCompanionID, conceptID: focus})
			}
		}
		if changed {
			updated = append(updated, g)
		}
	}
	return updated, targets, true
}

// retargetResonance applies the post-commit resonance follows (addendum #5,
// C1 idiom): the tx already committed, so a failure here is retryable — the
// persisted focus makes the retry idempotent. Fail-loud 500, never silent.
func (s *ExtServer) retargetResonance(ctx context.Context, w http.ResponseWriter, tenantID string, targets []resonanceTarget) bool {
	for _, t := range targets {
		cid := t.conceptID
		if _, err := s.CampaignResonance.SetResonantConcept(ctx, tenantID, t.companionID, &cid); err != nil {
			extWriteError(w, http.StatusInternalServerError, "RESONANCE_FAILED", err.Error())
			return false
		}
	}
	return true
}

// conceptSetContains reports whether the normalised set holds key exactly.
func conceptSetContains(set []string, key string) bool {
	for _, k := range set {
		if k == key {
			return true
		}
	}
	return false
}

// writeMergeSplitError maps the WS-C6 domain sentinels to HTTP statuses
// (CHO-2085 AC: root-immutable + guard failures are clear 409/422s).
func writeMergeSplitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conceptgraph.ErrRootImmutable):
		extWriteError(w, http.StatusConflict, "ROOT_IMMUTABLE", err.Error())
	case errors.Is(err, conceptgraph.ErrMergeSplitMultipleParents):
		extWriteError(w, http.StatusConflict, "MERGE_SPLIT_AMBIGUOUS", err.Error())
	case errors.Is(err, conceptgraph.ErrMergeSplitUnknownConcept):
		extWriteError(w, http.StatusNotFound, "CONCEPT_NOT_FOUND", err.Error())
	case errors.Is(err, conceptgraph.ErrConceptDeleted):
		extWriteError(w, http.StatusUnprocessableEntity, "CONCEPT_DELETED", err.Error())
	case errors.Is(err, conceptgraph.ErrSplitTooFew):
		extWriteError(w, http.StatusUnprocessableEntity, "SPLIT_TOO_FEW", err.Error())
	case errors.Is(err, conceptgraph.ErrSplitDuplicateChildKey):
		extWriteError(w, http.StatusUnprocessableEntity, "SPLIT_DUPLICATE_KEY", err.Error())
	case errors.Is(err, conceptgraph.ErrSelfLoop), errors.Is(err, conceptgraph.ErrInvalid):
		extWriteError(w, http.StatusUnprocessableEntity, "INVALID_MERGE_SPLIT", err.Error())
	default:
		extWriteError(w, http.StatusInternalServerError, "MERGE_SPLIT_FAILED", err.Error())
	}
}
