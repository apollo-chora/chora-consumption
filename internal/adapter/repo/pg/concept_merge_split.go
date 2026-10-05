// concept_merge_split.go — WS-C6 (CHO-2085, ADR-227 D14 + addendum #7):
// the atomic merge/split applier + the lineage-ancestry paint read.
//
// MergeSplitRepo commits one mergesplit.Apply in ONE tx.RunInTx: node
// tombstones/creates + edge flips (reusing the WS-1 SQL), dual-axis lineage
// inserts, ladder tombstone-then-insert (the tombstones ARE the D14
// XP-refusal history — and the partial unique index on live rows demands the
// tombstone lands first), retention re-keys, pending-suggestion focal
// repoints and goal concept_set/focus repairs. A failure anywhere rolls the
// whole restructure back — a half-applied merge would corrupt the map AND
// open the XP re-award hole.
//
// ConceptLineageRepo serves the read side: per-learner transitive ancestor
// concept_keys (to→from walk over live lineage rows, cycle-guarded) so the
// overlay/mastery paint survives re-keying (addendum #7 "join survival").
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/mergesplit"
)

// MergeSplitRepo applies a mergesplit.Apply atomically.
type MergeSplitRepo struct {
	tx TxRunner
}

// NewMergeSplitRepo constructs the applier around a TxRunner.
func NewMergeSplitRepo(tx TxRunner) *MergeSplitRepo {
	return &MergeSplitRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ mergesplit.Applier = (*MergeSplitRepo)(nil)

// Apply persists the full dual-axis delta in one transaction. Statement
// order matters twice: RLS session first, and ladder tombstones before the
// fresh ladder inserts (partial unique index on live rows + the D14 refusal
// history contract).
func (r *MergeSplitRepo) Apply(ctx context.Context, in mergesplit.Apply) error {
	if err := in.Validate(); err != nil {
		return err
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		// (a) graph nodes: tombstone the absorbed/parent copies (stamped by the
		// planner), then mint the split children.
		for i := range in.NodesToTombstone {
			n := in.NodesToTombstone[i]
			if _, err := q.Exec(ctx, updateConceptNodeSQL, conceptNodeUpdateArgs(&n)...); err != nil {
				return err
			}
		}
		// ADR-244 D6: live survivors whose atom_refs changed. Same full-row SQL
		// as the tombstone loop, but DeletedAt stays nil so the node lives on.
		for i := range in.NodesToUpdate {
			n := in.NodesToUpdate[i]
			if _, err := q.Exec(ctx, updateConceptNodeSQL, conceptNodeUpdateArgs(&n)...); err != nil {
				return err
			}
		}
		for i := range in.NodesToCreate {
			n := in.NodesToCreate[i]
			if _, err := q.Exec(ctx, insertConceptNodeSQL, conceptNodeInsertArgs(&n)...); err != nil {
				return err
			}
		}
		// (b) edges: supersede + re-express (append-only, WS-1 SQL).
		for i := range in.EdgesToSoftDelete {
			e := in.EdgesToSoftDelete[i]
			if _, err := q.Exec(ctx, updateConceptEdgeSQL,
				e.EdgeID, e.TenantID, e.LearnerGCID, string(e.Class), string(e.Provenance),
				e.UpdatedAt, e.DeletedAt,
			); err != nil {
				return err
			}
		}
		for i := range in.EdgesToCreate {
			e := in.EdgesToCreate[i]
			if _, err := q.Exec(ctx, insertConceptEdgeSQL,
				e.EdgeID, e.TenantID, e.LearnerGCID, e.SourceConceptID, e.TargetConceptID,
				string(e.Class), string(e.Provenance), e.CreatedAt, e.UpdatedAt,
			); err != nil {
				return err
			}
		}
		// (c) lineage: the dual-axis ancestry record (both directions walked
		// later: XP refusal + paint survival).
		for i := range in.Lineage {
			l := in.Lineage[i]
			if _, err := q.Exec(ctx, insertConceptLineageSQL,
				domain.NewUUIDv7(), in.TenantID, in.LearnerGCID, string(l.Operation),
				l.FromConceptID, l.ToConceptID, l.FromConceptKey, l.ToConceptKey, in.Now,
			); err != nil {
				return err
			}
		}
		// (d) ladder: tombstone the operands' live rows FIRST (refusal history
		// + the live-row partial unique index), then insert the AND/inherited
		// rows.
		for _, conceptID := range in.ProgressTombstoneConceptIDs {
			if _, err := q.Exec(ctx, tombstoneCampaignProgressSQL,
				in.TenantID, in.LearnerGCID, conceptID, in.Now,
			); err != nil {
				return err
			}
		}
		for i := range in.ProgressToInsert {
			p := in.ProgressToInsert[i]
			if p == nil {
				continue
			}
			if _, err := q.Exec(ctx, upsertCampaignProgressSQL,
				p.ID, p.TenantID, p.LearnerGCID, p.ConceptID,
				p.RungsCleared, p.RungClearedAt, p.CurrentRungCorrect,
				p.WonAt, p.LastAdvanceDate, p.CreatedAt, p.UpdatedAt, p.DeletedAt,
			); err != nil {
				return err
			}
		}
		// (e) retention re-keys (weakest survives a merge; clones follow a split).
		for i := range in.RetentionToUpsert {
			s := in.RetentionToUpsert[i]
			if s == nil {
				continue
			}
			if _, err := q.Exec(ctx, upsertTopicRetentionSQL,
				s.TenantID, s.GCID, s.TopicID, s.Strength,
				s.RetentionScore, s.ReviewCount, s.LastReviewedAt,
			); err != nil {
				return err
			}
		}
		// (f) pending suggestion focal repoints (UUID axis — zombie inboxes).
		for _, rp := range in.SuggestionRepoints {
			if _, err := q.Exec(ctx, repointPendingSuggestionFocalSQL,
				in.TenantID, in.LearnerGCID, rp.FromConceptID, rp.ToConceptID, in.Now,
			); err != nil {
				return err
			}
		}
		// (g) goal spine repairs (concept_set + focus, full-row update).
		for i := range in.GoalsToUpdate {
			g := in.GoalsToUpdate[i]
			if g == nil {
				continue
			}
			if _, err := q.Exec(ctx, updateGoalSQL,
				g.GoalID, g.TenantID, g.LearnerGCID, string(g.Status), g.ConceptSet,
				g.NorthStarNote, g.ChoraTargetRef, g.AttachedCompanionID, g.UpdatedAt,
				g.DeletedAt, g.MasteredConceptCount, g.RootConceptID, g.PersonalCompletedAt,
				g.FocusConceptID, g.CampaignSealedAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// ConceptLineageRepo serves the paint's ancestor-key read
// (http.ConceptLineageReader).
type ConceptLineageRepo struct {
	tx TxRunner
}

// NewConceptLineageRepo constructs the lineage read around a TxRunner.
func NewConceptLineageRepo(tx TxRunner) *ConceptLineageRepo {
	return &ConceptLineageRepo{tx: tx}
}

// conceptLineageRow is one live lineage edge (to ← from on both axes).
type conceptLineageRow struct {
	ToConceptID    string
	FromConceptID  string
	FromConceptKey string
}

// AncestorKeysByConcept returns, per live-descendant concept id, the
// transitive ancestor concept_keys (merge: absorbed keys; split: the parent
// chain's). A learner with no merge/split history reads an empty map.
func (r *ConceptLineageRepo) AncestorKeysByConcept(ctx context.Context, tenantID, learnerGCID string) (map[string][]string, error) {
	var rows []conceptLineageRow
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rs, err := q.Query(ctx, listConceptLineageSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rs == nil {
			return nil // stub Querier — no history
		}
		defer rs.Close()
		for rs.Next() {
			var row conceptLineageRow
			if err := rs.Scan(&row.ToConceptID, &row.FromConceptID, &row.FromConceptKey); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return walkAncestorKeys(rows), nil
}

// walkAncestorKeys folds lineage rows into a per-descendant transitive
// ancestor-key index (BFS up the to→from links, de-duplicated,
// cycle-guarded — corrupt cyclic data terminates instead of looping).
func walkAncestorKeys(rows []conceptLineageRow) map[string][]string {
	if len(rows) == 0 {
		return map[string][]string{}
	}
	parents := make(map[string][]conceptLineageRow, len(rows))
	for _, r := range rows {
		parents[r.ToConceptID] = append(parents[r.ToConceptID], r)
	}
	out := make(map[string][]string, len(parents))
	for id := range parents {
		visited := map[string]bool{id: true}
		seenKey := map[string]bool{}
		var keys []string
		queue := []string{id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, p := range parents[cur] {
				if !seenKey[p.FromConceptKey] && p.FromConceptKey != "" {
					seenKey[p.FromConceptKey] = true
					keys = append(keys, p.FromConceptKey)
				}
				if !visited[p.FromConceptID] {
					visited[p.FromConceptID] = true
					queue = append(queue, p.FromConceptID)
				}
			}
		}
		out[id] = keys
	}
	return out
}

const (
	// tombstoneCampaignProgressSQL soft-deletes a node's LIVE ladder row —
	// the row becomes D14 refusal history (read by priorLadderStateSQL).
	tombstoneCampaignProgressSQL = `
UPDATE campaign_node_progress SET
       deleted_at = $4,
       updated_at = $4
 WHERE tenant_id = $1 AND learner_gcid = $2 AND concept_id = $3 AND deleted_at IS NULL`

	// insertConceptLineageSQL writes one dual-axis ancestry record (mig 0077;
	// id minted app-side as UUIDv7 per ddd-enforcement #7).
	insertConceptLineageSQL = `
INSERT INTO concept_node_lineage
       (id, tenant_id, learner_gcid, operation, from_concept_id, to_concept_id,
        from_concept_key, to_concept_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`

	// repointPendingSuggestionFocalSQL re-targets the learner's PENDING
	// suggestions scoped to a retired focal (they would otherwise never
	// render — the node is gone from the map).
	repointPendingSuggestionFocalSQL = `
UPDATE concept_suggestions SET
       focal_concept_id = $4,
       updated_at       = $5
 WHERE tenant_id = $1 AND learner_gcid = $2 AND focal_concept_id = $3
   AND status = 'pending' AND deleted_at IS NULL`

	// listConceptLineageSQL loads the learner's live lineage edges for the
	// in-memory transitive walk (learner graphs are small by construction).
	listConceptLineageSQL = `
SELECT to_concept_id, from_concept_id, from_concept_key
  FROM concept_node_lineage
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL`
)
