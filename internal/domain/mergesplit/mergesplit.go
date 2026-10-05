// Package mergesplit is the WS-C6 (CHO-2085, ADR-227 D14 + addendum #7)
// cross-aggregate USE-CASE port: one concept merge/split touches the graph
// (nodes + edges + lineage), the campaign ladder (AND-of-rungs / inherit),
// the retention curve (weakest / clone) and the goal spine (concept_set +
// focus) — all inside chora_consumption. A half-applied merge corrupts the
// map AND opens the D14 XP re-award hole (the refusal reads tombstones the
// same apply writes), so the whole delta commits in ONE transaction via the
// Applier port. This package composes the four aggregates' already-validated
// outputs; it authors no business rules of its own (those live in
// conceptgraph.PlanMerge/PlanSplit, campaign.MergeProgress/SplitProgress,
// topic_retention.WeakerOf/CloneForTopic, goal.RepairConceptSet).
//
// Mirrors the SuggestionAccepter / ReRootApplier precedent (a port whose pg
// adapter spans tables in one tx), widened to the D14 blast radius.
package mergesplit

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// SuggestionRepoint re-targets the learner's PENDING concept_suggestions
// whose focal_concept_id points at a retired node (UUID axis, addendum #7):
// merge → the survivor; split → the primary (focus-designated) child. Zombie
// pending rows scoped to a tombstoned focal would otherwise never render.
type SuggestionRepoint struct {
	FromConceptID string
	ToConceptID   string
}

// Apply is the full single-transaction delta of one merge or one split.
// Every slice may be empty; TenantID/LearnerGCID/Now are mandatory.
type Apply struct {
	TenantID    string
	LearnerGCID string
	Now         time.Time

	// Graph (conceptgraph aggregate) — from PlanMerge/PlanSplit.
	NodesToTombstone []conceptgraph.ConceptNode // stamped copies (DeletedAt set)
	NodesToCreate    []conceptgraph.ConceptNode // split children
	// NodesToUpdate are LIVE nodes whose row changed without being tombstoned
	// (ADR-244 D6: the merge survivor carrying the union of both operands'
	// atom_refs). Distinct from NodesToTombstone because the SQL is the same
	// full-row update but the intent is opposite, and conflating them is how a
	// survivor would get a deleted_at.
	NodesToUpdate     []conceptgraph.ConceptNode
	EdgesToSoftDelete []conceptgraph.Edge
	EdgesToCreate     []conceptgraph.Edge
	Lineage           []conceptgraph.LineageRecord

	// Campaign ladder — tombstone the operands' live rows FIRST (their state
	// becomes the D14 refusal history), then insert the AND/inherited rows.
	ProgressTombstoneConceptIDs []string
	ProgressToInsert            []*campaign.NodeProgress

	// Retention slug re-key — pre-computed weakest/clone rows to upsert.
	RetentionToUpsert []*topic_retention.TopicScore

	// Pending suggestion focal repoints (UUID axis).
	SuggestionRepoints []SuggestionRepoint

	// Goals whose concept_set / focus changed (already mutated in memory via
	// RepairConceptSet / SetFocusConcept); persisted with the full-row update.
	GoalsToUpdate []*goal.Goal
}

// Validate fail-louds on a structurally unusable delta before any SQL runs.
func (a Apply) Validate() error {
	if a.TenantID == "" || a.LearnerGCID == "" {
		return fmt.Errorf("mergesplit: tenant_id and learner_gcid are required")
	}
	if a.Now.IsZero() {
		return fmt.Errorf("mergesplit: now is required (injected clock)")
	}
	if len(a.NodesToTombstone) == 0 && len(a.NodesToCreate) == 0 &&
		len(a.EdgesToSoftDelete) == 0 && len(a.EdgesToCreate) == 0 && len(a.Lineage) == 0 {
		return fmt.Errorf("mergesplit: empty graph delta (nothing to apply)")
	}
	return nil
}

// Applier persists one Apply atomically (ONE chora_consumption transaction).
// Implemented by *pg.MergeSplitRepo.
type Applier interface {
	Apply(ctx context.Context, in Apply) error
}
