// concept_edge.go — Postgres adapter for the learner-owned Edge aggregate
// (conceptgraph.EdgeRepository, ADR-212 WS-1). Schema mirrors
// migrations/0056_discovery_concept_graph.up.sql.
//
// Same RLS + scoping contract as concept_node.go. Endpoints are opaque concept
// UUIDs (no FK, #3). ListByConcept returns a concept's hex-face relations —
// edges where it is the source OR the target.
package pg

import (
	"context"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// EdgeRepo is the Postgres-backed conceptgraph.EdgeRepository.
type EdgeRepo struct {
	tx TxRunner
}

// NewEdgeRepo constructs the repo around a TxRunner.
func NewEdgeRepo(tx TxRunner) *EdgeRepo {
	return &EdgeRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ conceptgraph.EdgeRepository = (*EdgeRepo)(nil)

// Create persists a new Edge.
func (r *EdgeRepo) Create(ctx context.Context, e *conceptgraph.Edge) error {
	if e == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertConceptEdgeSQL,
			e.EdgeID, e.TenantID, e.LearnerGCID, e.SourceConceptID, e.TargetConceptID,
			string(e.Class), string(e.Provenance), e.CreatedAt, e.UpdatedAt,
		)
		return err
	})
}

// Update persists the mutable fields (edge_class, provenance, deleted_at) scoped
// to (id, tenant, learner). Endpoints are immutable — a re-pointed relation is a
// new edge.
func (r *EdgeRepo) Update(ctx context.Context, e *conceptgraph.Edge) error {
	if e == nil {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, updateConceptEdgeSQL,
			e.EdgeID, e.TenantID, e.LearnerGCID, string(e.Class), string(e.Provenance),
			e.UpdatedAt, e.DeletedAt,
		)
		return err
	})
}

// SoftDeleteByConcept soft-deletes every LIVE edge where conceptID is the source
// OR the target, scoped to (tenant, learner). Idempotent — the `deleted_at IS
// NULL` predicate means a re-run over an already-clean concept touches 0 rows.
// Returns the number of edges soft-deleted (the concept.deleted.v1 cascade,
// CHO-2324).
func (r *EdgeRepo) SoftDeleteByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string, now time.Time) (int64, error) {
	var n int64
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, softDeleteConceptEdgesByConceptSQL, tenantID, learnerGCID, conceptID, now)
		if err != nil {
			return err
		}
		n = tag.RowsAffected
		return nil
	})
	return n, err
}

// GetByID returns one live Edge for the learner, or (nil, nil) when none matches.
func (r *EdgeRepo) GetByID(ctx context.Context, tenantID, learnerGCID, edgeID string) (*conceptgraph.Edge, error) {
	var out *conceptgraph.Edge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, getConceptEdgeSQL, edgeID, tenantID, learnerGCID)
		if row == nil {
			return nil
		}
		e, err := scanEdge(row)
		if err != nil {
			if err == ErrNoRows {
				return nil
			}
			return err
		}
		out = e
		return nil
	})
	return out, err
}

// ListByLearner returns the learner's live edges (deleted_at IS NULL), newest first.
func (r *EdgeRepo) ListByLearner(ctx context.Context, tenantID, learnerGCID string) ([]*conceptgraph.Edge, error) {
	return r.listBy(ctx, listConceptEdgesByLearnerSQL, tenantID, learnerGCID)
}

// ListByConcept returns the concept's live relations — edges where it is the
// source OR the target (pre-partition; see conceptgraph.PartitionByHexFace).
func (r *EdgeRepo) ListByConcept(ctx context.Context, tenantID, learnerGCID, conceptID string) ([]*conceptgraph.Edge, error) {
	return r.listBy(ctx, listConceptEdgesByConceptSQL, tenantID, learnerGCID, conceptID)
}

func (r *EdgeRepo) listBy(ctx context.Context, sql string, args ...any) ([]*conceptgraph.Edge, error) {
	out := make([]*conceptgraph.Edge, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEdge(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// scanEdge maps one row (the shared SELECT column order) into a domain Edge.
func scanEdge(s interface{ Scan(dest ...any) error }) (*conceptgraph.Edge, error) {
	var (
		e          conceptgraph.Edge
		class      string
		provenance string
		deletedAt  *time.Time
	)
	if err := s.Scan(
		&e.EdgeID, &e.TenantID, &e.LearnerGCID, &e.SourceConceptID, &e.TargetConceptID,
		&class, &provenance, &e.CreatedAt, &e.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	e.Class = conceptgraph.EdgeClass(class)
	e.Provenance = conceptgraph.Provenance(provenance)
	e.DeletedAt = deletedAt
	return &e, nil
}

// --- SQL templates (review-via-test) ---

const (
	insertConceptEdgeSQL = `
INSERT INTO concept_edges
       (id, tenant_id, learner_gcid, source_concept_id, target_concept_id,
        edge_class, provenance, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	updateConceptEdgeSQL = `
UPDATE concept_edges SET
       edge_class = $4,
       provenance = $5,
       updated_at = $6,
       deleted_at = $7
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3`

	softDeleteConceptEdgesByConceptSQL = `
UPDATE concept_edges SET
       deleted_at = $4,
       updated_at = $4
 WHERE tenant_id = $1 AND learner_gcid = $2
   AND (source_concept_id = $3 OR target_concept_id = $3)
   AND deleted_at IS NULL`

	getConceptEdgeSQL = `
SELECT id, tenant_id, learner_gcid, source_concept_id, target_concept_id,
       edge_class, provenance, created_at, updated_at, deleted_at
  FROM concept_edges
 WHERE id = $1 AND tenant_id = $2 AND learner_gcid = $3 AND deleted_at IS NULL`

	listConceptEdgesByLearnerSQL = `
SELECT id, tenant_id, learner_gcid, source_concept_id, target_concept_id,
       edge_class, provenance, created_at, updated_at, deleted_at
  FROM concept_edges
 WHERE tenant_id = $1 AND learner_gcid = $2 AND deleted_at IS NULL
 ORDER BY created_at DESC`

	listConceptEdgesByConceptSQL = `
SELECT id, tenant_id, learner_gcid, source_concept_id, target_concept_id,
       edge_class, provenance, created_at, updated_at, deleted_at
  FROM concept_edges
 WHERE tenant_id = $1 AND learner_gcid = $2
   AND (source_concept_id = $3 OR target_concept_id = $3)
   AND deleted_at IS NULL
 ORDER BY created_at DESC`
)
