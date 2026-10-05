// hexagon.go — Postgres adapter for the per-user KG hexagon fog cache
// (HexagonNode aggregate). Owns the chora_consumption.kg_hexagon_nodes
// table (introduced in 0002).
//
// Per ADR-143 §6: hexagon fog is event-invalidated. Upsert keys on
// (exploration_id, focal_atom_id); FindFresh + Find honour the
// invalidated_at column for stale-cache filtering.
//
// neighbors_json carries the 6-record array with the snake_case shape
// defined by the HexagonNeighbor json tags (the 0002 CHECK enforces
// jsonb_array_length = 6). The adapter marshals/unmarshals explicitly so
// the persisted shape never drifts with pgx encoder defaults.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ErrInvalidHexagonNode is the sentinel for nil-input writes.
var ErrInvalidHexagonNode = errors.New("pg: hexagon node is nil")

// HexagonRepo is the Postgres-backed userknowledgegraph.HexagonRepo.
type HexagonRepo struct {
	tx TxRunner
}

// NewHexagonRepo constructs the repo.
func NewHexagonRepo(tx TxRunner) *HexagonRepo {
	return &HexagonRepo{tx: tx}
}

// Upsert inserts/replaces the HexagonNode for (exploration_id,
// focal_atom_id) — idx_hex_focal is the conflict target. Clobbers
// neighbors_json, run/model attribution, generated_at and the
// invalidation pair.
func (r *HexagonRepo) Upsert(ctx context.Context, h *userknowledgegraph.HexagonNode) error {
	if h == nil {
		return ErrInvalidHexagonNode
	}
	neighbors, err := json.Marshal(h.Neighbors)
	if err != nil {
		return fmt.Errorf("pg: marshal neighbors_json: %w", err)
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var invalidatedAt any
		if h.InvalidatedAt != nil {
			invalidatedAt = *h.InvalidatedAt
		}
		_, err := q.Exec(ctx, upsertHexagonNodeSQL,
			h.HexNodeID, h.ExplorationID, h.ClusterID,
			h.TenantID, h.UserGCID, h.FocalAtomID,
			string(neighbors), h.GeneratedByRunID, h.GeneratedByModelID,
			h.GeneratedAt, invalidatedAt, reasonOrNil(h.InvalidationReason),
			h.Version,
		)
		return err
	})
}

// Save is a compatibility alias for Upsert (the original adapter method
// name; the ports.HexagonRepo contract names it Upsert).
func (r *HexagonRepo) Save(ctx context.Context, h *userknowledgegraph.HexagonNode) error {
	return r.Upsert(ctx, h)
}

// FindFresh returns the cached hexagon for (exploration, focal) when
// invalidated_at IS NULL. Returns userknowledgegraph.ErrNotFound on a
// cache miss or a stale row — the ports contract the handlers branch on.
func (r *HexagonRepo) FindFresh(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	return r.findOne(ctx, findFreshHexagonSQL, explorationID, focalAtomID)
}

// Find returns the hexagon regardless of fresh/stale state, or
// userknowledgegraph.ErrNotFound.
func (r *HexagonRepo) Find(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	return r.findOne(ctx, findHexagonSQL, explorationID, focalAtomID)
}

func (r *HexagonRepo) findOne(ctx context.Context, sql, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	var out *userknowledgegraph.HexagonNode
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, sql, explorationID, focalAtomID)
		if row == nil {
			return userknowledgegraph.ErrNotFound
		}
		h, err := scanHexagon(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return userknowledgegraph.ErrNotFound
			}
			return err
		}
		out = h
		return nil
	})
	return out, err
}

// FindAllFocalsByUser returns the distinct focal atom ids across all the
// user's hexagons EXCEPT the excluded cluster — the junction-detection
// key (idx_hex_user_focals probe).
func (r *HexagonRepo) FindAllFocalsByUser(ctx context.Context, tenantID, userGCID, excludeClusterID string) ([]string, error) {
	var out []string
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, findAllFocalsByUserSQL, tenantID, userGCID, excludeClusterID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var focal string
			if err := rows.Scan(&focal); err != nil {
				return err
			}
			out = append(out, focal)
		}
		return rows.Err()
	})
	return out, err
}

// HasFocalInCluster reports whether the atom is a focal of any
// non-deleted hexagon in the cluster (junction-detector overlap probe).
func (r *HexagonRepo) HasFocalInCluster(ctx context.Context, clusterID, focalAtomID string) (bool, error) {
	var exists bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, hasFocalInClusterSQL, clusterID, focalAtomID)
		if row == nil {
			return nil
		}
		if err := row.Scan(&exists); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil
			}
			return err
		}
		return nil
	})
	return exists, err
}

// MarkInvalidatedByFocal invalidates every fresh hexagon whose focal is
// the atom OR whose neighbors_json contains it (atom_published /
// atom_revision_updated subscribers).
func (r *HexagonRepo) MarkInvalidatedByFocal(ctx context.Context, atomID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	return r.markInvalidated(ctx, markInvalidatedByFocalSQL, atomID, reason)
}

// MarkInvalidatedByUser invalidates every fresh hexagon of one user
// (user_retention_shifted subscriber).
func (r *HexagonRepo) MarkInvalidatedByUser(ctx context.Context, tenantID, userGCID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	var count int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, markInvalidatedByUserSQL, tenantID, userGCID, string(reason))
		if err != nil {
			return err
		}
		count = int(tag.RowsAffected)
		return nil
	})
	return count, err
}

func (r *HexagonRepo) markInvalidated(ctx context.Context, sql, atomID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	var count int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, sql, atomID, string(reason))
		if err != nil {
			return err
		}
		count = int(tag.RowsAffected)
		return nil
	})
	return count, err
}

// FindInvalidatedOlderThan returns hexagons invalidated before the
// RFC3339 cutoff — the regen sweeper's work query.
func (r *HexagonRepo) FindInvalidatedOlderThan(ctx context.Context, cutoff string) ([]*userknowledgegraph.HexagonNode, error) {
	var out []*userknowledgegraph.HexagonNode
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, findInvalidatedOlderThanSQL, cutoff)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHexagon(rows)
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

func scanHexagon(row scanner) (*userknowledgegraph.HexagonNode, error) {
	var (
		h                  userknowledgegraph.HexagonNode
		neighborsJSON      []byte
		invalidatedAt      *time.Time
		invalidationReason *string
	)
	if err := row.Scan(
		&h.HexNodeID, &h.ExplorationID, &h.ClusterID,
		&h.TenantID, &h.UserGCID, &h.FocalAtomID,
		&neighborsJSON, &h.GeneratedByRunID, &h.GeneratedByModelID,
		&h.GeneratedAt, &invalidatedAt, &invalidationReason,
		&h.Version,
	); err != nil {
		return nil, err
	}
	if len(neighborsJSON) > 0 {
		if err := json.Unmarshal(neighborsJSON, &h.Neighbors); err != nil {
			return nil, fmt.Errorf("pg: unmarshal neighbors_json: %w", err)
		}
	}
	h.InvalidatedAt = invalidatedAt
	if invalidationReason != nil {
		h.InvalidationReason = userknowledgegraph.FogInvalidationReason(*invalidationReason)
	}
	return &h, nil
}

// reasonOrNil maps the "fresh" pseudo-state to a NULL enum value —
// kg_fog_invalidation_reason rejects the empty string and the column is
// semantically "why was this invalidated".
func reasonOrNil(reason userknowledgegraph.FogInvalidationReason) any {
	if reason == "" || reason == userknowledgegraph.FogInvalidationReasonNeverGenerated {
		return nil
	}
	return string(reason)
}

const (
	upsertHexagonNodeSQL = `
		INSERT INTO kg_hexagon_nodes (
			hex_node_id, exploration_id, cluster_id, tenant_id, user_gcid,
			focal_atom_id, neighbors_json, generated_by_run_id, generated_by_model_id,
			generated_at, invalidated_at, invalidation_reason, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12::kg_fog_invalidation_reason, $13)
		ON CONFLICT (exploration_id, focal_atom_id) WHERE deleted_at IS NULL DO UPDATE SET
			neighbors_json       = EXCLUDED.neighbors_json,
			generated_by_run_id  = EXCLUDED.generated_by_run_id,
			generated_by_model_id= EXCLUDED.generated_by_model_id,
			generated_at         = EXCLUDED.generated_at,
			invalidated_at       = EXCLUDED.invalidated_at,
			invalidation_reason  = EXCLUDED.invalidation_reason,
			version              = kg_hexagon_nodes.version + 1`

	hexagonColumnsSQL = `hex_node_id, exploration_id, cluster_id, tenant_id, user_gcid,
			focal_atom_id, neighbors_json, generated_by_run_id, generated_by_model_id,
			generated_at, invalidated_at, invalidation_reason::text, version`

	findFreshHexagonSQL = `
		SELECT ` + hexagonColumnsSQL + `
		FROM kg_hexagon_nodes
		WHERE exploration_id = $1 AND focal_atom_id = $2
			AND invalidated_at IS NULL AND deleted_at IS NULL`

	findHexagonSQL = `
		SELECT ` + hexagonColumnsSQL + `
		FROM kg_hexagon_nodes
		WHERE exploration_id = $1 AND focal_atom_id = $2 AND deleted_at IS NULL`

	findAllFocalsByUserSQL = `
		SELECT DISTINCT focal_atom_id::text
		FROM kg_hexagon_nodes
		WHERE tenant_id = $1 AND user_gcid = $2
			AND cluster_id <> $3::uuid AND deleted_at IS NULL`

	hasFocalInClusterSQL = `
		SELECT EXISTS (
			SELECT 1 FROM kg_hexagon_nodes
			WHERE cluster_id = $1 AND focal_atom_id = $2 AND deleted_at IS NULL
		)`

	markInvalidatedByFocalSQL = `
		UPDATE kg_hexagon_nodes
		SET invalidated_at = now(),
			invalidation_reason = $2::kg_fog_invalidation_reason
		WHERE deleted_at IS NULL AND invalidated_at IS NULL
			AND (focal_atom_id = $1
				OR neighbors_json @> jsonb_build_array(jsonb_build_object('atom_id', $1::text)))`

	markInvalidatedByUserSQL = `
		UPDATE kg_hexagon_nodes
		SET invalidated_at = now(),
			invalidation_reason = $3::kg_fog_invalidation_reason
		WHERE tenant_id = $1 AND user_gcid = $2
			AND deleted_at IS NULL AND invalidated_at IS NULL`

	findInvalidatedOlderThanSQL = `
		SELECT ` + hexagonColumnsSQL + `
		FROM kg_hexagon_nodes
		WHERE invalidated_at IS NOT NULL AND invalidated_at < $1::timestamptz
			AND deleted_at IS NULL`
)
