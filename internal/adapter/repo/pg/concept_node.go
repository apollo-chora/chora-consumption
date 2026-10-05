// concept_node.go — Postgres adapter for the learner-owned ConceptNode
// aggregate (conceptgraph.ConceptNodeRepository, ADR-212 WS-1). Schema mirrors
// migrations/0056_discovery_concept_graph.up.sql.
//
// Per multi-tenant-rls: every read/write runs rls.ApplySession (SET LOCAL
// chora.tenant_id [+ chora.user_gcid]) inside the transaction BEFORE the domain
// query; per-learner scoping is an explicit learner_gcid predicate (writes are
// scoped by id AND tenant AND learner as defence-in-depth). atom_refs is a
// UUID[] — bound from []string via a ::uuid[] cast and read back as ::text[].
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// ConceptNodeRepo is the Postgres-backed conceptgraph.ConceptNodeRepository.
type ConceptNodeRepo struct {
	tx TxRunner
}

// NewConceptNodeRepo constructs the repo around a TxRunner.
func NewConceptNodeRepo(tx TxRunner) *ConceptNodeRepo {
	return &ConceptNodeRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ conceptgraph.ConceptNodeRepository = (*ConceptNodeRepo)(nil)

// conceptNodeInsertArgs builds the argument list for insertConceptNodeSQL.
//
// insertConceptNodeSQL / updateConceptNodeSQL are shared by eight call sites
// across five files (this repo, the suggestion accept, the learning-edge
// applier, the cluster projection and merge/split). When `sub_goal` +
// `sub_goal_provenance` were appended to the statements, only this file's call
// sites were updated and the rest kept passing the old arity: every one of
// them then failed at runtime with "expected 12 parameters but got 10".
// Routing every call site through these builders means the arity lives in ONE
// place; concept_node_args_test.go pins it to the statement's placeholder count.
func conceptNodeInsertArgs(c *conceptgraph.ConceptNode) []any {
	return []any{
		c.ConceptID, c.TenantID, c.LearnerGCID, c.Title, c.AtomRefs,
		string(c.Provenance), string(c.Intent), c.CreatedAt, c.UpdatedAt, c.ConceptKey,
		c.SubGoal, string(c.SubGoalProvenance),
	}
}

// conceptNodeUpdateArgs builds the argument list for updateConceptNodeSQL.
// See conceptNodeInsertArgs for why this exists.
func conceptNodeUpdateArgs(c *conceptgraph.ConceptNode) []any {
	return []any{
		c.ConceptID, c.TenantID, c.LearnerGCID, c.Title, c.AtomRefs,
		string(c.Provenance), string(c.Intent), c.UpdatedAt, c.DeletedAt,
		c.SubGoal, string(c.SubGoalProvenance),
	}
}

// Create persists a new ConceptNode.
func (r *ConceptNodeRepo) Create(ctx context.Context, c *conceptgraph.ConceptNode) error {
	if c == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertConceptNodeSQL, conceptNodeInsertArgs(c)...)
		return err
	})
}

// Update persists the mutable fields (title, atom_refs, provenance, deleted_at)
// scoped to (id, tenant, learner).
func (r *ConceptNodeRepo) Update(ctx context.Context, c *conceptgraph.ConceptNode) error {
	if c == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, updateConceptNodeSQL, conceptNodeUpdateArgs(c)...)
		return err
	})
}

// GetByID returns one live ConceptNode for the learner, or (nil, nil) when none
// matches (deleted_at IS NULL filter + ErrNoRows → not found).
func (r *ConceptNodeRepo) GetByID(ctx context.Context, tenantID, learnerGCID, conceptID string) (*conceptgraph.ConceptNode, error) {
	var out *conceptgraph.ConceptNode
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getConceptNodeSQL, conceptID, tenantID, learnerGCID)
		if row == nil {
			return nil // stub Querier — not found
		}
		c, err := scanConceptNode(row)
		if err != nil {
			if err == ErrNoRows {
				return nil
			}
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// ListByLearner returns the learner's live concepts (deleted_at IS NULL), newest first.
func (r *ConceptNodeRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.ConceptNode, error) {
	out := make([]*conceptgraph.ConceptNode, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listConceptNodesSQL, tenantID, learnerGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConceptNode(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// scanConceptNode maps one row (the shared SELECT column order) into a domain
// ConceptNode. Satisfied by both Row and Rows.
func scanConceptNode(s interface{ Scan(dest ...any) error }) (*conceptgraph.ConceptNode, error) {
	var (
		c           conceptgraph.ConceptNode
		provenance  string
		intent      string
		deletedAt   *time.Time
		subGoal     string
		subGoalProv string
	)
	if err := s.Scan(
		&c.ConceptID, &c.TenantID, &c.LearnerGCID, &c.Title, &c.AtomRefs,
		&provenance, &intent, &c.CreatedAt, &c.UpdatedAt, &deletedAt, &c.ConceptKey,
		&subGoal, &subGoalProv,
	); err != nil {
		return nil, err
	}
	c.Provenance = conceptgraph.Provenance(provenance)
	c.Intent = conceptgraph.Intent(intent)
	c.DeletedAt = deletedAt
	c.SubGoal = subGoal
	c.SubGoalProvenance = conceptgraph.Provenance(subGoalProv)
	return &c, nil
}

// --- SQL templates (review-via-test). atom_refs: bind []string → ::uuid[];
// read back as ::text[] so pgx scans into []string. ---

const (
	insertConceptNodeSQL = `
INSERT INTO concept_nodes
       (id, tenant_id, learner_gcid, title, atom_refs, provenance, intent, created_at, updated_at, concept_key, sub_goal, sub_goal_provenance)
VALUES ($1, $2, $3, $4, $5::uuid[], $6, $7, $8, $9, $10, $11, $12)`

	updateConceptNodeSQL = `
UPDATE concept_nodes SET
       title               = $4,
       atom_refs           = $5::uuid[],
       provenance          = $6,
       intent              = $7,
       updated_at          = $8,
       deleted_at          = $9,
       sub_goal            = $10,
       sub_goal_provenance = $11
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3`

	getConceptNodeSQL = `
SELECT id, tenant_id, learner_gcid, title, atom_refs::text[], provenance, intent,
       created_at, updated_at, deleted_at, concept_key,
       COALESCE(sub_goal, ''), COALESCE(sub_goal_provenance, '')
  FROM concept_nodes
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3 AND deleted_at IS NULL`

	listConceptNodesSQL = `
SELECT id, tenant_id, learner_gcid, title, atom_refs::text[], provenance, intent,
       created_at, updated_at, deleted_at, concept_key,
       COALESCE(sub_goal, ''), COALESCE(sub_goal_provenance, '')
  FROM concept_nodes
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
 ORDER BY created_at DESC`
)
