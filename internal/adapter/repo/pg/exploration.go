// exploration.go — Postgres adapter for the per-user KG Exploration
// aggregate + its append-only trail (ADR-143). Owns kg_user_explorations
// and kg_exploration_trail (0002).
//
// AppendTrailHop runs the exploration UPDATE and the trail INSERT in the
// SAME transaction — the trail trigger pair (trg_trail_no_update /
// trg_trail_no_delete) enforces append-only at the DB.
package pg

import (
	"context"
	"errors"

	"github.com/apollo-chora/chora-common/rls"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ErrInvalidExploration is the sentinel for nil-input writes.
var ErrInvalidExploration = errors.New("pg: exploration is nil")

// ExplorationRepo is the Postgres-backed userknowledgegraph.ExplorationRepo.
type ExplorationRepo struct {
	tx TxRunner
}

// NewExplorationRepo constructs the repo.
func NewExplorationRepo(tx TxRunner) *ExplorationRepo {
	return &ExplorationRepo{tx: tx}
}

// Save upserts an Exploration by exploration_id (create + focal/status
// transitions share the same write path, mirroring the in-memory adapter).
func (r *ExplorationRepo) Save(ctx context.Context, e *userknowledgegraph.Exploration) error {
	if e == nil {
		return ErrInvalidExploration
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertExplorationSQL,
			e.ExplorationID, e.ClusterID, e.TenantID, e.UserGCID,
			e.StartedAtAtomID, e.CurrentFocalAtomID, string(e.Status), e.Version,
		)
		return err
	})
}

// Load fetches one exploration. Returns userknowledgegraph.ErrNotFound
// when absent (or RLS-hidden).
func (r *ExplorationRepo) Load(ctx context.Context, explorationID string) (*userknowledgegraph.Exploration, error) {
	var out *userknowledgegraph.Exploration
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadExplorationSQL, explorationID)
		if row == nil {
			return userknowledgegraph.ErrNotFound
		}
		e, err := scanExploration(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return userknowledgegraph.ErrNotFound
			}
			return err
		}
		out = e
		return nil
	})
	return out, err
}

// ListByCluster returns the cluster's explorations (active + archived),
// oldest first.
func (r *ExplorationRepo) ListByCluster(ctx context.Context, clusterID string) ([]*userknowledgegraph.Exploration, error) {
	var out []*userknowledgegraph.Exploration
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listExplorationsByClusterSQL, clusterID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanExploration(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// AppendTrailHop persists the focal transition atomically: exploration
// row (new focal + version) AND the append-only trail row in one tx.
func (r *ExplorationRepo) AppendTrailHop(ctx context.Context, e *userknowledgegraph.Exploration, hop *userknowledgegraph.TrailHop) error {
	if e == nil || hop == nil {
		return ErrInvalidExploration
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, upsertExplorationSQL,
			e.ExplorationID, e.ClusterID, e.TenantID, e.UserGCID,
			e.StartedAtAtomID, e.CurrentFocalAtomID, string(e.Status), e.Version,
		); err != nil {
			return err
		}
		_, err := q.Exec(ctx, insertTrailHopSQL,
			hop.TrailID, hop.ExplorationID, hop.ClusterID, hop.TenantID,
			hop.UserGCID, hop.FromFocalAtomID, hop.ToFocalAtomID,
			string(hop.Relation), hop.StepIndex, hop.TraversedAt,
		)
		return err
	})
}

// LoadTrail returns the exploration's hops in chronological order.
func (r *ExplorationRepo) LoadTrail(ctx context.Context, explorationID string) ([]*userknowledgegraph.TrailHop, error) {
	var out []*userknowledgegraph.TrailHop
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, loadTrailSQL, explorationID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var (
				hop      userknowledgegraph.TrailHop
				relation string
			)
			if err := rows.Scan(
				&hop.TrailID, &hop.ExplorationID, &hop.ClusterID,
				&hop.TenantID, &hop.UserGCID, &hop.FromFocalAtomID,
				&hop.ToFocalAtomID, &relation, &hop.StepIndex, &hop.TraversedAt,
			); err != nil {
				return err
			}
			hop.Relation = userknowledgegraph.NeighborRelation(relation)
			out = append(out, &hop)
		}
		return rows.Err()
	})
	return out, err
}

// LatestStepIndex returns the highest step_index, or -1 when the trail
// is empty.
func (r *ExplorationRepo) LatestStepIndex(ctx context.Context, explorationID string) (int, error) {
	latest := -1
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, latestStepIndexSQL, explorationID)
		if row == nil {
			return nil
		}
		if err := row.Scan(&latest); err != nil {
			if errors.Is(err, ErrNoRows) {
				latest = -1
				return nil
			}
			return err
		}
		return nil
	})
	return latest, err
}

func scanExploration(row scanner) (*userknowledgegraph.Exploration, error) {
	var (
		e      userknowledgegraph.Exploration
		status string
	)
	if err := row.Scan(
		&e.ExplorationID, &e.ClusterID, &e.TenantID, &e.UserGCID,
		&e.StartedAtAtomID, &e.CurrentFocalAtomID, &status, &e.Version,
		&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt,
	); err != nil {
		return nil, err
	}
	e.Status = userknowledgegraph.ExplorationStatus(status)
	return &e, nil
}

const (
	upsertExplorationSQL = `
		INSERT INTO kg_user_explorations (
			exploration_id, cluster_id, tenant_id, user_gcid,
			started_at_atom_id, current_focal_atom_id, status, version
		) VALUES ($1, $2, $3, $4, $5, $6, $7::kg_exploration_status, $8)
		ON CONFLICT (exploration_id) DO UPDATE SET
			current_focal_atom_id = EXCLUDED.current_focal_atom_id,
			status                = EXCLUDED.status,
			version               = EXCLUDED.version`

	explorationColumnsSQL = `exploration_id, cluster_id, tenant_id, user_gcid,
			started_at_atom_id, current_focal_atom_id, status::text, version,
			created_at, updated_at, deleted_at`

	loadExplorationSQL = `
		SELECT ` + explorationColumnsSQL + `
		FROM kg_user_explorations
		WHERE exploration_id = $1 AND deleted_at IS NULL`

	listExplorationsByClusterSQL = `
		SELECT ` + explorationColumnsSQL + `
		FROM kg_user_explorations
		WHERE cluster_id = $1 AND deleted_at IS NULL
		ORDER BY created_at ASC`

	insertTrailHopSQL = `
		INSERT INTO kg_exploration_trail (
			trail_id, exploration_id, cluster_id, tenant_id, user_gcid,
			from_focal_atom_id, to_focal_atom_id, relation, step_index, traversed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::kg_neighbor_relation, $9, $10)`

	loadTrailSQL = `
		SELECT trail_id, exploration_id, cluster_id, tenant_id, user_gcid,
			from_focal_atom_id, to_focal_atom_id, relation::text, step_index, traversed_at
		FROM kg_exploration_trail
		WHERE exploration_id = $1
		ORDER BY step_index ASC`

	latestStepIndexSQL = `
		SELECT COALESCE(MAX(step_index), -1)
		FROM kg_exploration_trail
		WHERE exploration_id = $1`
)
