// companion_goal_knowledge.go — Postgres adapter for the CHO-2118 goal-knowledge
// cache (companiongoalknowledge.Repository, sub-phase B). Schema mirrors
// migrations/0092_familiar_goal_knowledge.up.sql.
//
// Direct ancestor: hexagon.go (kg_hexagon_nodes) — the same cache-row shape
// (materialise → cache-hit-on-read → event-invalidate → lazy-regen → decision-
// stamp). House style for scoping follows campaign.go: every read/write runs
// rls.ApplySession (SET LOCAL chora.tenant_id [+ chora.user_gcid]) inside the
// transaction BEFORE the query, and the tenant + learner predicate is spelled
// out on top. FORCE RLS is defence-in-depth, not a substitute for the predicate.
//
// Three things this file is deliberately careful about:
//
//   - NULL is not "". generated_by_run_id and root_concept_id are UUID columns,
//     and an empty string is not a uuid (22P02) — so a never-generated row MUST
//     bind SQL NULL. On the way back, a NULL generated_at MUST scan as nil: nil
//     is what "never generated" means to the domain, and a zero time would make
//     an invalidation flip the row to 'stale' (schema: "invalidated; last good
//     text still servable") when it has no text to serve.
//   - The write never blanks synthesis_text. An invalidated row keeps its last
//     good reflection (serve-stale-while-regen), so EXCLUDED.synthesis_text
//     carries the text the domain preserved.
//   - No invalidation priority in SQL. The port is List → Invalidate → Upsert so
//     the reason-priority table lives only in the domain; a bulk UPDATE that
//     ranked reasons here would give that invariant a second home to drift in.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	companiongoalknowledge "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
)

var (
	// ErrInvalidGoalKnowledge is the nil-input write sentinel — a nil aggregate
	// reaching the repo is a caller bug, not an empty write (fail loud).
	ErrInvalidGoalKnowledge = errors.New("pg: goal knowledge is nil")

	// ErrGoalKnowledgeDeleted refuses an Upsert of a soft-deleted aggregate.
	//
	// This is a structural limit, not a policy choice: the ON CONFLICT arbiter is
	// the LIVE partial unique index, which by definition contains no soft-deleted
	// rows — so a proposed row carrying deleted_at can never match it. Postgres
	// falls through to a plain INSERT and collides on the aggregate's existing id
	// (23505 on the pkey). Refusing here turns that opaque, load-bearing failure
	// into a named one.
	//
	// Soft-deleting these rows (Companion retirement, account closure) needs a
	// dedicated scoped UPDATE — the softDeleteMemoryByCompanionSQL shape — behind a
	// new port method. The Repository port has no such method today.
	ErrGoalKnowledgeDeleted = errors.New("pg: cannot upsert a soft-deleted goal knowledge row; soft-delete needs a dedicated scoped UPDATE")
)

// GoalKnowledgeRepo is the Postgres-backed companiongoalknowledge.Repository.
type GoalKnowledgeRepo struct {
	tx TxRunner
}

// NewGoalKnowledgeRepo constructs the repo around a TxRunner.
func NewGoalKnowledgeRepo(tx TxRunner) *GoalKnowledgeRepo {
	return &GoalKnowledgeRepo{tx: tx}
}

// Compile-time checks: the pg adapter satisfies both domain ports.
var (
	_ companiongoalknowledge.Repository        = (*GoalKnowledgeRepo)(nil)
	_ companiongoalknowledge.ClosureRepository = (*GoalKnowledgeRepo)(nil)
)

// Upsert persists the LIVE row against the (tenant, learner, companion, goal)
// unique key — uq_companion_goal_knowledge_live is the ON CONFLICT arbiter.
//
// A soft-deleted row is invisible to that arbiter, which is what makes
// resurrection work: once a tombstone exists, the next Upsert of a freshly
// minted aggregate inserts a new live row beside it rather than conflicting. The
// same property is why an already-soft-deleted aggregate cannot be written back
// through this path — see ErrGoalKnowledgeDeleted.
func (r *GoalKnowledgeRepo) Upsert(ctx context.Context, k *companiongoalknowledge.GoalKnowledge) error {
	if k == nil {
		return ErrInvalidGoalKnowledge
	}
	if k.DeletedAt != nil {
		return ErrGoalKnowledgeDeleted
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertGoalKnowledgeSQL,
			k.ID, k.TenantID, k.LearnerGCID, k.CompanionID, k.GoalID,
			nullIfEmpty(k.SynthesisText), string(k.Status),
			nullIfEmpty(k.GeneratedByRunID), nullIfEmpty(k.GeneratedByModelID),
			nullIfEmpty(k.PromptVersion), nullIfEmpty(k.ContentHash), k.GeneratedAt,
			nullIfEmptyPtr(k.RootConceptID),
			k.RequestedAt, k.InvalidatedAt, gkReason(k.InvalidationReason),
			k.CreatedAt, k.UpdatedAt,
		)
		return err
	})
}

// FindByGoal returns the live cache row for (tenant, learner, companion, goal),
// or (nil, nil) when none exists yet — a cache miss is normal, not an error.
func (r *GoalKnowledgeRepo) FindByGoal(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (*companiongoalknowledge.GoalKnowledge, error) {
	var out *companiongoalknowledge.GoalKnowledge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, findGoalKnowledgeByGoalSQL, tenantID, learnerGCID, companionID, goalID)
		if row == nil {
			return nil // stub Querier — cache miss
		}
		k, err := scanGoalKnowledge(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil // never synthesised for this (companion, goal) yet
			}
			return err
		}
		out = k
		return nil
	})
	return out, err
}

// ListByGoal returns the live rows for (tenant, learner, goal) across every
// Companion — the goal-scoped invalidation fan-out.
func (r *GoalKnowledgeRepo) ListByGoal(ctx context.Context, tenantID, learnerGCID, goalID string) ([]*companiongoalknowledge.GoalKnowledge, error) {
	return r.list(ctx, listGoalKnowledgeByGoalSQL, tenantID, learnerGCID, goalID)
}

// ListByCompanion returns the live rows for (tenant, learner, companion) across
// every goal — the memory-scoped invalidation fan-out.
func (r *GoalKnowledgeRepo) ListByCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) ([]*companiongoalknowledge.GoalKnowledge, error) {
	return r.list(ctx, listGoalKnowledgeByCompanionSQL, tenantID, learnerGCID, companionID)
}

func (r *GoalKnowledgeRepo) list(ctx context.Context, sql, tenantID, learnerGCID, scopeID string) ([]*companiongoalknowledge.GoalKnowledge, error) {
	out := make([]*companiongoalknowledge.GoalKnowledge, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, tenantID, learnerGCID, scopeID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanGoalKnowledge(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

// SoftDeleteByCompanion tombstones every live reflection one Companion holds for
// the learner (Companion retirement). Returns the number of rows tombstoned.
func (r *GoalKnowledgeRepo) SoftDeleteByCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) (int64, error) {
	return r.softDelete(ctx, softDeleteGoalKnowledgeByCompanionSQL, tenantID, learnerGCID, companionID)
}

// SoftDeleteByLearner tombstones every live reflection the learner holds, across
// every goal and Companion (account closure). Returns the number tombstoned.
func (r *GoalKnowledgeRepo) SoftDeleteByLearner(ctx context.Context, tenantID, learnerGCID string) (int64, error) {
	return r.softDelete(ctx, softDeleteGoalKnowledgeByLearnerSQL, tenantID, learnerGCID)
}

// softDelete runs a scoped tombstoning UPDATE and reports the rows affected.
//
// rls.ApplySession is MANDATORY here, and not merely as defence-in-depth. The
// table is FORCE RLS with tenant_isolation on current_setting('chora.tenant_id',
// true), which yields NULL when the GUC is unset — so `tenant_id = NULL` is NULL,
// no row is visible, and the UPDATE tombstones NOTHING while returning (0, nil).
// A closure purge that silently purges nothing is indistinguishable from a
// learner who had no cached reflections. The count is returned rather than
// swallowed so the caller can tell those apart.
func (r *GoalKnowledgeRepo) softDelete(ctx context.Context, sql string, args ...any) (int64, error) {
	var n int64
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("pg: soft-delete goal knowledge: %w", err)
		}
		n = tag.RowsAffected
		return nil
	})
	return n, err
}

// scanGoalKnowledge maps one row (goalKnowledgeColumns order) into the domain
// aggregate. The nullable columns land in *string / *time.Time locals so a SQL
// NULL round-trips as nil (pointers) or "" (value strings) — never as a zero
// time, which the domain would read as "generated at year one".
func scanGoalKnowledge(s scanner) (*companiongoalknowledge.GoalKnowledge, error) {
	var (
		k             companiongoalknowledge.GoalKnowledge
		synthesisText *string
		status        string
		runID         *string
		modelID       *string
		promptVersion *string
		contentHash   *string
		generatedAt   *time.Time
		rootConceptID *string
		requestedAt   *time.Time
		invalidatedAt *time.Time
		reason        string
		deletedAt     *time.Time
	)
	if err := s.Scan(
		&k.ID, &k.TenantID, &k.LearnerGCID, &k.CompanionID, &k.GoalID,
		&synthesisText, &status,
		&runID, &modelID, &promptVersion, &contentHash, &generatedAt,
		&rootConceptID,
		&requestedAt, &invalidatedAt, &reason,
		&k.CreatedAt, &k.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	k.SynthesisText = derefStr(synthesisText)
	k.Status = companiongoalknowledge.Status(status)
	k.GeneratedByRunID = derefStr(runID)
	k.GeneratedByModelID = derefStr(modelID)
	k.PromptVersion = derefStr(promptVersion)
	k.ContentHash = derefStr(contentHash)
	k.GeneratedAt = generatedAt
	k.RootConceptID = rootConceptID
	k.RequestedAt = requestedAt
	k.InvalidatedAt = invalidatedAt
	k.InvalidationReason = companiongoalknowledge.InvalidationReason(reason)
	k.DeletedAt = deletedAt
	return &k, nil
}

// nullIfEmptyPtr is nullIfEmpty for an optional string the domain models as a
// pointer (root_concept_id — a goal may have no root). Both nil and a pointer to
// "" become SQL NULL: the column is UUID, and an empty string is not a uuid.
func nullIfEmptyPtr(p *string) any {
	if p == nil {
		return nil
	}
	return nullIfEmpty(*p)
}

// gkReason guards the NOT NULL invalidation_reason enum. The domain's zero value
// is the empty string, which is not a valid enum label (22P02) — it means the
// same thing as the column default, so it binds as the 'never_generated'
// pseudo-state (the hexagon.go reasonOrNil idiom, inverted for a NOT NULL column).
func gkReason(reason companiongoalknowledge.InvalidationReason) string {
	if reason == "" {
		return string(companiongoalknowledge.ReasonNeverGenerated)
	}
	return string(reason)
}

// --- SQL templates (review-via-test; PREPARE-smoked against a real PG) ---

const (
	// status / invalidation_reason come back ::text so they scan into Go strings;
	// the nullable UUIDs likewise (the concept_suggestion.go idiom).
	goalKnowledgeColumns = `id, tenant_id, learner_gcid, companion_id, goal_id,
       synthesis_text, status::text,
       generated_by_run_id::text, generated_by_model_id, prompt_version, content_hash, generated_at,
       root_concept_id::text,
       requested_at, invalidated_at, invalidation_reason::text,
       created_at, updated_at, deleted_at`

	// deleted_at is absent by design: this statement writes LIVE rows only (the
	// column defaults to NULL). Carrying it would be a lie — the arbiter is the
	// live partial index, so a soft-deleted row can never reach DO UPDATE, and a
	// `deleted_at = EXCLUDED.deleted_at` line could only ever write NULL while
	// reading as though soft-delete were supported here. It is not; see
	// ErrGoalKnowledgeDeleted.
	upsertGoalKnowledgeSQL = `
INSERT INTO companion_goal_knowledge
       (id, tenant_id, learner_gcid, companion_id, goal_id,
        synthesis_text, status,
        generated_by_run_id, generated_by_model_id, prompt_version, content_hash, generated_at,
        root_concept_id,
        requested_at, invalidated_at, invalidation_reason,
        created_at, updated_at)
VALUES ($1, $2, $3, $4, $5,
        $6, $7::companion_goal_knowledge_status,
        $8, $9, $10, $11, $12,
        $13,
        $14, $15, $16::companion_goal_knowledge_invalidation_reason,
        $17, $18)
ON CONFLICT (tenant_id, learner_gcid, companion_id, goal_id) WHERE deleted_at IS NULL
DO UPDATE SET
       synthesis_text        = EXCLUDED.synthesis_text,
       status                = EXCLUDED.status,
       generated_by_run_id   = EXCLUDED.generated_by_run_id,
       generated_by_model_id = EXCLUDED.generated_by_model_id,
       prompt_version        = EXCLUDED.prompt_version,
       content_hash          = EXCLUDED.content_hash,
       generated_at          = EXCLUDED.generated_at,
       root_concept_id       = EXCLUDED.root_concept_id,
       requested_at          = EXCLUDED.requested_at,
       invalidated_at        = EXCLUDED.invalidated_at,
       invalidation_reason   = EXCLUDED.invalidation_reason,
       updated_at            = EXCLUDED.updated_at`

	// Soft-delete (ClosureRepository): a scoped tombstoning UPDATE, never a DELETE
	// and never an upsert. `deleted_at IS NULL` in the predicate makes it
	// idempotent — a second retirement/closure pass tombstones nothing and
	// reports 0 rather than re-stamping the tombstones.
	softDeleteGoalKnowledgeByCompanionSQL = `
UPDATE companion_goal_knowledge
   SET deleted_at = now(), updated_at = now()
 WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3
   AND deleted_at IS NULL`

	softDeleteGoalKnowledgeByLearnerSQL = `
UPDATE companion_goal_knowledge
   SET deleted_at = now(), updated_at = now()
 WHERE tenant_id = $1 AND learner_gcid = $2
   AND deleted_at IS NULL`

	findGoalKnowledgeByGoalSQL = `
SELECT ` + goalKnowledgeColumns + `
  FROM companion_goal_knowledge
 WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3 AND goal_id = $4
   AND deleted_at IS NULL`

	listGoalKnowledgeByGoalSQL = `
SELECT ` + goalKnowledgeColumns + `
  FROM companion_goal_knowledge
 WHERE tenant_id = $1 AND learner_gcid = $2 AND goal_id = $3
   AND deleted_at IS NULL`

	listGoalKnowledgeByCompanionSQL = `
SELECT ` + goalKnowledgeColumns + `
  FROM companion_goal_knowledge
 WHERE tenant_id = $1 AND learner_gcid = $2 AND companion_id = $3
   AND deleted_at IS NULL`
)
