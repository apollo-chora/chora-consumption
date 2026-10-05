// atom_semantic_edge.go — Postgres adapter for the atom_semantic_edges
// authoring-time edges (relocated from chora_creation per ADR-143).
// Owns the chora_consumption.atom_semantic_edges table.
package pg

import (
	"context"
	"errors"

	"github.com/apollo-chora/chora-common/rls"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ErrInvalidAtomSemanticEdge is the sentinel for nil-input writes.
var ErrInvalidAtomSemanticEdge = errors.New("pg: atom_semantic_edge is nil")

// AtomSemanticEdgesRepo is the Postgres-backed repo.
type AtomSemanticEdgesRepo struct {
	tx TxRunner
}

// NewAtomSemanticEdgesRepo constructs the repo.
func NewAtomSemanticEdgesRepo(tx TxRunner) *AtomSemanticEdgesRepo {
	return &AtomSemanticEdgesRepo{tx: tx}
}

// Save upserts an edge by (tenant_id, source_atom_id, target_atom_id).
func (r *AtomSemanticEdgesRepo) Save(ctx context.Context, e *userknowledgegraph.AtomSemanticEdge) error {
	if e == nil {
		return ErrInvalidAtomSemanticEdge
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertAtomSemanticEdgeSQL,
			e.EdgeID, e.TenantID, e.SourceAtomID, e.TargetAtomID,
			string(e.EdgeType), e.Weight, e.Version,
		)
		return err
	})
}

// FindByAtoms returns the edge for the (source, target) pair, or nil
// + ErrNoRows.
func (r *AtomSemanticEdgesRepo) FindByAtoms(ctx context.Context, tenantID, sourceAtomID, targetAtomID string) (*userknowledgegraph.AtomSemanticEdge, error) {
	var out *userknowledgegraph.AtomSemanticEdge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, findEdgeByAtomsSQL, tenantID, sourceAtomID, targetAtomID)
		if row == nil {
			return nil
		}
		var (
			e        userknowledgegraph.AtomSemanticEdge
			edgeType string
		)
		if err := row.Scan(
			&e.EdgeID, &e.TenantID, &e.SourceAtomID, &e.TargetAtomID,
			&edgeType, &e.Weight, &e.Version,
		); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil
			}
			return err
		}
		e.EdgeType = userknowledgegraph.AtomEdgeType(edgeType)
		out = &e
		return nil
	})
	return out, err
}

// ListBySource returns up to `limit` edges for one source atom.
func (r *AtomSemanticEdgesRepo) ListBySource(ctx context.Context, tenantID, sourceAtomID string, limit int) ([]*userknowledgegraph.AtomSemanticEdge, error) {
	out := make([]*userknowledgegraph.AtomSemanticEdge, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listEdgesBySourceSQL, tenantID, sourceAtomID, sqlLimit(limit))
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e        userknowledgegraph.AtomSemanticEdge
				edgeType string
			)
			if err := rows.Scan(
				&e.EdgeID, &e.TenantID, &e.SourceAtomID, &e.TargetAtomID,
				&edgeType, &e.Weight, &e.Version,
			); err != nil {
				return err
			}
			e.EdgeType = userknowledgegraph.AtomEdgeType(edgeType)
			out = append(out, &e)
		}
		return rows.Err()
	})
	return out, err
}

const (
	upsertAtomSemanticEdgeSQL = `
		INSERT INTO atom_semantic_edges (
			edge_id, tenant_id, source_atom_id, target_atom_id,
			edge_type, weight, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, source_atom_id, target_atom_id) DO UPDATE SET
			edge_type = EXCLUDED.edge_type,
			weight    = EXCLUDED.weight,
			version   = EXCLUDED.version`

	findEdgeByAtomsSQL = `
		SELECT edge_id, tenant_id, source_atom_id, target_atom_id,
			edge_type::text, weight, version
		FROM atom_semantic_edges
		WHERE tenant_id = $1 AND source_atom_id = $2 AND target_atom_id = $3
			AND deleted_at IS NULL`

	listEdgesBySourceSQL = `
		SELECT edge_id, tenant_id, source_atom_id, target_atom_id,
			edge_type::text, weight, version
		FROM atom_semantic_edges
		WHERE tenant_id = $1 AND source_atom_id = $2 AND deleted_at IS NULL
		ORDER BY weight DESC
		LIMIT $3`
)
