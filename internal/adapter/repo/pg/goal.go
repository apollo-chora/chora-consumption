// goal.go — Postgres adapter for the learner-owned Goal aggregate
// (goal.Repository, ADR-204 §2). Schema mirrors migrations/0051_goals.up.sql.
//
// Per multi-tenant-rls SKILL every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// query; per-learner scoping is an explicit learner_gcid predicate (and writes
// are scoped by id AND tenant AND learner as defence-in-depth). Cross-DB queries
// are forbidden — the Goal lives wholly in chora_consumption.
//
// chora_target_ref + attached_companion_id are nullable (curiosity has no target;
// the Companion bond is optional 1:1) — bound/scanned as *string. concept_set is a
// TEXT[] mapped to []string by pgx natively.
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// GoalRepo is the Postgres-backed goal.Repository.
type GoalRepo struct {
	tx TxRunner
}

// NewGoalRepo constructs the repo around a TxRunner.
func NewGoalRepo(tx TxRunner) *GoalRepo {
	return &GoalRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ goal.Repository = (*GoalRepo)(nil)

// Create persists a new Goal.
func (r *GoalRepo) Create(ctx context.Context, g *goal.Goal) error {
	if g == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertGoalSQL,
			g.GoalID, g.TenantID, g.LearnerGCID, string(g.Kind), g.ChoraTargetRef,
			g.ConceptSet, g.MasteredConceptCount, string(g.Status), g.NorthStarNote,
			g.AttachedCompanionID, g.RootConceptID, g.PersonalCompletedAt,
			g.FocusConceptID, g.CampaignSealedAt, g.CreatedAt, g.UpdatedAt,
		)
		return err
	})
}

// Update persists the mutable fields of an existing Goal, scoped to
// (id, tenant, learner). kind is immutable (a new aim is a new goal).
func (r *GoalRepo) Update(ctx context.Context, g *goal.Goal) error {
	if g == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, updateGoalSQL,
			g.GoalID, g.TenantID, g.LearnerGCID, string(g.Status), g.ConceptSet,
			g.NorthStarNote, g.ChoraTargetRef, g.AttachedCompanionID, g.UpdatedAt,
			g.DeletedAt, g.MasteredConceptCount, g.RootConceptID, g.PersonalCompletedAt,
			g.FocusConceptID, g.CampaignSealedAt,
		)
		return err
	})
}

// GetByID returns one live Goal by id for the learner, or (nil, nil) when none
// matches (deleted_at IS NULL filter + ErrNoRows → not found).
func (r *GoalRepo) GetByID(ctx context.Context, tenantID, learnerGCID, goalID string) (*goal.Goal, error) {
	var out *goal.Goal
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getGoalSQL, goalID, tenantID, learnerGCID)
		if row == nil {
			return nil // stub Querier — not found
		}
		g, err := scanGoal(row)
		if err != nil {
			if err == ErrNoRows {
				return nil // not found
			}
			return err
		}
		out = g
		return nil
	})
	return out, err
}

// ListByLearner returns the learner's live Goals (deleted_at IS NULL), newest first.
func (r *GoalRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*goal.Goal, error) {
	out := make([]*goal.Goal, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listGoalsSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGoal(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// ListCompanionedByTenant returns every live, companion-attached, rooted Goal
// across ALL learners of the tenant (ADR-244 D5: the standing refresh trigger
// enumerates reachable targets tenant-wide). RLS scopes the read to the tenant
// GUC; the deliberate absence of a learner predicate mirrors
// MarkInvalidatedByFocal's tenant-wide precedent (hexagon.go). Filtered in SQL
// to attached_companion_id + root_concept_id NOT NULL because unattached or
// rootless goals can never render a suggestion's Add/Dismiss controls.
func (r *GoalRepo) ListCompanionedByTenant(ctx context.Context, tenantID string) ([]*goal.Goal, error) {
	out := make([]*goal.Goal, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listCompanionedGoalsByTenantSQL, tenantID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier: empty
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGoal(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// scanGoal maps one row (the shared SELECT column order) into a domain Goal.
// scanner is satisfied by both Row and Rows.
func scanGoal(s interface{ Scan(dest ...any) error }) (*goal.Goal, error) {
	var (
		g              goal.Goal
		kind           string
		status         string
		target         *string
		companion      *string
		rootConcept    *string
		personalDoneAt *time.Time
		focusConcept   *string
		sealedAt       *time.Time
		deletedAt      *time.Time
	)
	if err := s.Scan(
		&g.GoalID, &g.TenantID, &g.LearnerGCID, &kind, &target, &g.ConceptSet,
		&g.MasteredConceptCount, &status, &g.NorthStarNote, &companion,
		&rootConcept, &personalDoneAt, &focusConcept, &sealedAt,
		&g.CreatedAt, &g.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	g.Kind = goal.Kind(kind)
	g.Status = goal.Status(status)
	g.ChoraTargetRef = target
	g.AttachedCompanionID = companion
	g.RootConceptID = rootConcept
	g.PersonalCompletedAt = personalDoneAt
	g.FocusConceptID = focusConcept
	g.CampaignSealedAt = sealedAt
	g.DeletedAt = deletedAt
	return &g, nil
}

// --- SQL templates (review-via-test) ---

const (
	insertGoalSQL = `
INSERT INTO goals
       (id, tenant_id, learner_gcid, kind, chora_target_ref, concept_set,
        mastered_concept_count, status, north_star_note, attached_companion_id,
        root_concept_id, personal_completed_at, focus_concept_id,
        campaign_sealed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	updateGoalSQL = `
UPDATE goals SET
       status                 = $4,
       concept_set            = $5,
       north_star_note        = $6,
       chora_target_ref       = $7,
       attached_companion_id   = $8,
       updated_at             = $9,
       deleted_at             = $10,
       mastered_concept_count = $11,
       root_concept_id        = $12,
       personal_completed_at  = $13,
       focus_concept_id       = $14,
       campaign_sealed_at     = $15
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3`

	getGoalSQL = `
SELECT id, tenant_id, learner_gcid, kind, chora_target_ref, concept_set,
       mastered_concept_count, status, north_star_note, attached_companion_id,
       root_concept_id, personal_completed_at, focus_concept_id,
       campaign_sealed_at, created_at, updated_at, deleted_at
  FROM goals
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3 AND deleted_at IS NULL`

	listGoalsSQL = `
SELECT id, tenant_id, learner_gcid, kind, chora_target_ref, concept_set,
       mastered_concept_count, status, north_star_note, attached_companion_id,
       root_concept_id, personal_completed_at, focus_concept_id,
       campaign_sealed_at, created_at, updated_at, deleted_at
  FROM goals
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
 ORDER BY created_at DESC`

	listCompanionedGoalsByTenantSQL = `
SELECT id, tenant_id, learner_gcid, kind, chora_target_ref, concept_set,
       mastered_concept_count, status, north_star_note, attached_companion_id,
       root_concept_id, personal_completed_at, focus_concept_id,
       campaign_sealed_at, created_at, updated_at, deleted_at
  FROM goals
 WHERE tenant_id = $1 AND deleted_at IS NULL
   AND attached_companion_id IS NOT NULL AND root_concept_id IS NOT NULL
 ORDER BY learner_gcid, created_at DESC`
)
