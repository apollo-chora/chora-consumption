// Package pg is the Postgres adapter for the per-user Knowledge Graph
// (ADR-143). All repos use libs/chora-go-common/rls to set the
// `chora.tenant_id` and `chora.user_gcid` session variables (per the
// multi-tenant-rls skill) before any query.
//
// SCHEMA: see migrations/0002_per_user_knowledge_graph.sql +
// migrations/0003_per_user_kg.sql.
//
// This file owns MapClusterRepo + JunctionRepo. ExplorationRepo lives in
// exploration.go; HexagonRepo in hexagon.go. The Querier/Row/Rows/TxRunner
// seam keeps pgx out of unit tests (stub queriers assert SQL + the
// rls.ApplySession contract); production wires PgxTxRunner (pgx_runtime.go).
//
// Per feedback_no_inline_config: connection strings come from env vars
// resolved at boot time (cmd/server). This package never reads env.
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// Querier is the minimal contract this adapter needs from a Postgres
// driver. pgx.Tx satisfies it via a thin wrapper at cmd/server boot;
// tests can inject a stub.
//
// NOTE: signature mirrors rls.Execer with extra read paths.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Row is the single-row result.
type Row interface {
	Scan(dest ...any) error
}

// Rows is the multi-row result.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// TxRunner abstracts "Begin → fn(querier) → Commit/Rollback" so callers
// don't have to import pgx into chora-consumption. Production
// implementation wraps pgxpool.Pool.BeginTx.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error
}

// ErrNotImplemented is retained for historical callers/tests of the
// pre-pgx scaffold era; the KG repos in this package are fully executing.
var ErrNotImplemented = errors.New("pg: pgx adapter not yet wired (M12)")

// ErrInvalidMapCluster / ErrInvalidJunction are the nil-input write sentinels.
var (
	ErrInvalidMapCluster = errors.New("pg: map cluster is nil")
	ErrInvalidJunction   = errors.New("pg: junction is nil")
)

// MapClusterRepo is the Postgres-backed userknowledgegraph.MapClusterRepo.
type MapClusterRepo struct {
	tx TxRunner
}

// NewMapClusterRepo constructs a Postgres repo around a TxRunner.
func NewMapClusterRepo(tx TxRunner) *MapClusterRepo {
	return &MapClusterRepo{tx: tx}
}

// Save upserts a MapCluster by cluster_id (create + status transitions
// share the path, mirroring the in-memory adapter; updated_at is bumped
// by trg_kg_clusters_updated_at).
func (r *MapClusterRepo) Save(ctx context.Context, c *userknowledgegraph.MapCluster) error {
	if c == nil {
		return ErrInvalidMapCluster
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertMapClusterSQL,
			c.ClusterID, c.TenantID, c.UserGCID, c.DisplayName, c.SeedTopic,
			c.SeedAtomID, string(c.Status), nullableUUID(c.MergedIntoClusterID),
			nullableUUID(c.MergedViaAtomID), nullableUUID(c.ProjectedIntoGoalID),
			c.NodeCount, c.Version,
		)
		return err
	})
}

// Load fetches a cluster by ID. Returns userknowledgegraph.ErrNotFound
// when absent or RLS-hidden.
func (r *MapClusterRepo) Load(ctx context.Context, clusterID string) (*userknowledgegraph.MapCluster, error) {
	var out *userknowledgegraph.MapCluster
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadMapClusterSQL, clusterID)
		if row == nil {
			return userknowledgegraph.ErrNotFound
		}
		c, err := scanMapCluster(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return userknowledgegraph.ErrNotFound
			}
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// ListActiveByUser returns ACTIVE clusters for the (tenant, user).
func (r *MapClusterRepo) ListActiveByUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.MapCluster, error) {
	return r.list(ctx, listActiveMapClustersSQL, tenantID, userGCID)
}

// ListAllByUser returns ALL non-deleted clusters for the (tenant, user)
// regardless of status — the canvas management panel's listing.
func (r *MapClusterRepo) ListAllByUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.MapCluster, error) {
	return r.list(ctx, listAllMapClustersSQL, tenantID, userGCID)
}

// FindBySurvivor returns clusters merged INTO a surviving cluster.
func (r *MapClusterRepo) FindBySurvivor(ctx context.Context, tenantID, survivingClusterID string) ([]*userknowledgegraph.MapCluster, error) {
	return r.list(ctx, findClustersBySurvivorSQL, tenantID, survivingClusterID)
}

func (r *MapClusterRepo) list(ctx context.Context, sql string, args ...any) ([]*userknowledgegraph.MapCluster, error) {
	var out []*userknowledgegraph.MapCluster
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
			c, err := scanMapCluster(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// CountActiveByUser returns COUNT(*) of active clusters (cap pre-check).
func (r *MapClusterRepo) CountActiveByUser(ctx context.Context, tenantID, userGCID string) (int, error) {
	var n int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, countActiveMapClustersSQL, tenantID, userGCID)
		if row == nil {
			return nil
		}
		if err := row.Scan(&n); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil
			}
			return err
		}
		return nil
	})
	return n, err
}

func scanMapCluster(row scanner) (*userknowledgegraph.MapCluster, error) {
	var (
		c          userknowledgegraph.MapCluster
		status     string
		mergedInto *string
		mergedVia  *string
		projected  *string
	)
	if err := row.Scan(
		&c.ClusterID, &c.TenantID, &c.UserGCID, &c.DisplayName, &c.SeedTopic,
		&c.SeedAtomID, &status, &mergedInto, &mergedVia, &projected,
		&c.NodeCount, &c.Version, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt,
	); err != nil {
		return nil, err
	}
	c.Status = userknowledgegraph.ClusterStatus(status)
	if mergedInto != nil {
		c.MergedIntoClusterID = *mergedInto
	}
	if mergedVia != nil {
		c.MergedViaAtomID = *mergedVia
	}
	if projected != nil {
		c.ProjectedIntoGoalID = *projected
	}
	return &c, nil
}

// SQL templates.
const (
	upsertMapClusterSQL = `
		INSERT INTO kg_user_map_clusters (
			cluster_id, tenant_id, user_gcid, display_name, seed_topic, seed_atom_id,
			status, merged_into_cluster_id, merged_via_atom_id, projected_into_goal_id,
			node_count, version
		) VALUES ($1,$2,$3,$4,$5,$6,$7::kg_cluster_status,$8,$9,$10,$11,$12)
		ON CONFLICT (cluster_id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			merged_into_cluster_id = EXCLUDED.merged_into_cluster_id,
			merged_via_atom_id = EXCLUDED.merged_via_atom_id,
			projected_into_goal_id = EXCLUDED.projected_into_goal_id,
			node_count = EXCLUDED.node_count,
			version = EXCLUDED.version`

	mapClusterColumnsSQL = `cluster_id, tenant_id, user_gcid, display_name, seed_topic,
			seed_atom_id, status::text, merged_into_cluster_id, merged_via_atom_id,
			projected_into_goal_id, node_count, version, created_at, updated_at, deleted_at`

	loadMapClusterSQL = `
		SELECT ` + mapClusterColumnsSQL + `
		FROM kg_user_map_clusters
		WHERE cluster_id = $1 AND deleted_at IS NULL`

	listActiveMapClustersSQL = `
		SELECT ` + mapClusterColumnsSQL + `
		FROM kg_user_map_clusters
		WHERE tenant_id = $1 AND user_gcid = $2
			AND status = 'active' AND deleted_at IS NULL
		ORDER BY created_at ASC`

	listAllMapClustersSQL = `
		SELECT ` + mapClusterColumnsSQL + `
		FROM kg_user_map_clusters
		WHERE tenant_id = $1 AND user_gcid = $2 AND deleted_at IS NULL
		ORDER BY created_at ASC`

	countActiveMapClustersSQL = `
		SELECT COUNT(*) FROM kg_user_map_clusters
		WHERE tenant_id = $1 AND user_gcid = $2
			AND status = 'active' AND deleted_at IS NULL`

	findClustersBySurvivorSQL = `
		SELECT ` + mapClusterColumnsSQL + `
		FROM kg_user_map_clusters
		WHERE tenant_id = $1 AND merged_into_cluster_id = $2 AND deleted_at IS NULL`
)

// JunctionRepo is the Postgres-backed userknowledgegraph.JunctionRepo.
type JunctionRepo struct {
	tx TxRunner
}

// NewJunctionRepo constructs a Postgres junction repo.
func NewJunctionRepo(tx TxRunner) *JunctionRepo {
	return &JunctionRepo{tx: tx}
}

// Save upserts a Junction (insert at detection; conflict-update for the
// accept/reject status transitions).
func (r *JunctionRepo) Save(ctx context.Context, j *userknowledgegraph.Junction) error {
	if j == nil {
		return ErrInvalidJunction
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var resolvedAt any
		if j.ResolvedAt != nil {
			resolvedAt = *j.ResolvedAt
		}
		_, err := q.Exec(ctx, upsertJunctionSQL,
			j.JunctionID, j.TenantID, j.UserGCID, j.ClusterAID, j.ClusterBID,
			j.OverlapAtomIDs, string(j.Status), nullableUUID(j.ResolvedViaAtomID),
			j.DetectedAt, resolvedAt, j.Version,
		)
		return err
	})
}

// Load returns a junction by ID, or userknowledgegraph.ErrNotFound.
func (r *JunctionRepo) Load(ctx context.Context, junctionID string) (*userknowledgegraph.Junction, error) {
	var out *userknowledgegraph.Junction
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadJunctionSQL, junctionID)
		if row == nil {
			return userknowledgegraph.ErrNotFound
		}
		j, err := scanJunction(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return userknowledgegraph.ErrNotFound
			}
			return err
		}
		out = j
		return nil
	})
	return out, err
}

// ListPendingForUser returns the PENDING junctions for one user.
func (r *JunctionRepo) ListPendingForUser(ctx context.Context, tenantID, userGCID string) ([]*userknowledgegraph.Junction, error) {
	var out []*userknowledgegraph.Junction
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listPendingJunctionsSQL, tenantID, userGCID)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJunction(rows)
			if err != nil {
				return err
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// FindPendingByPair returns the pending junction for the unordered
// cluster pair, or (nil, nil) when none exists (the detector's
// idempotency probe — absence is a normal outcome, not an error).
func (r *JunctionRepo) FindPendingByPair(ctx context.Context, tenantID, userGCID, clusterAID, clusterBID string) (*userknowledgegraph.Junction, error) {
	var out *userknowledgegraph.Junction
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, findPendingJunctionByPairSQL, tenantID, userGCID, clusterAID, clusterBID)
		if row == nil {
			return nil
		}
		j, err := scanJunction(row)
		if err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil
			}
			return err
		}
		out = j
		return nil
	})
	return out, err
}

func scanJunction(row scanner) (*userknowledgegraph.Junction, error) {
	var (
		j           userknowledgegraph.Junction
		status      string
		resolvedVia *string
	)
	if err := row.Scan(
		&j.JunctionID, &j.TenantID, &j.UserGCID, &j.ClusterAID, &j.ClusterBID,
		&j.OverlapAtomIDs, &status, &resolvedVia, &j.DetectedAt,
		&j.ResolvedAt, &j.Version, &j.CreatedAt, &j.UpdatedAt, &j.DeletedAt,
	); err != nil {
		return nil, err
	}
	j.Status = userknowledgegraph.JunctionStatus(status)
	if resolvedVia != nil {
		j.ResolvedViaAtomID = *resolvedVia
	}
	return &j, nil
}

const (
	upsertJunctionSQL = `
		INSERT INTO kg_junctions (
			junction_id, tenant_id, user_gcid, cluster_a_id, cluster_b_id,
			overlap_atom_ids, status, resolved_via_atom_id, detected_at,
			resolved_at, version
		) VALUES ($1,$2,$3,$4,$5,$6::uuid[],$7::kg_junction_status,$8,$9,$10,$11)
		ON CONFLICT (junction_id) DO UPDATE SET
			status = EXCLUDED.status,
			resolved_via_atom_id = EXCLUDED.resolved_via_atom_id,
			resolved_at = EXCLUDED.resolved_at,
			version = EXCLUDED.version`

	junctionColumnsSQL = `junction_id, tenant_id, user_gcid, cluster_a_id, cluster_b_id,
			overlap_atom_ids::text[], status::text, resolved_via_atom_id, detected_at,
			resolved_at, version, created_at, updated_at, deleted_at`

	loadJunctionSQL = `
		SELECT ` + junctionColumnsSQL + `
		FROM kg_junctions
		WHERE junction_id = $1 AND deleted_at IS NULL`

	listPendingJunctionsSQL = `
		SELECT ` + junctionColumnsSQL + `
		FROM kg_junctions
		WHERE tenant_id = $1 AND user_gcid = $2
			AND status = 'pending' AND deleted_at IS NULL
		ORDER BY detected_at ASC`

	findPendingJunctionByPairSQL = `
		SELECT ` + junctionColumnsSQL + `
		FROM kg_junctions
		WHERE tenant_id = $1 AND user_gcid = $2
			AND LEAST(cluster_a_id, cluster_b_id) = LEAST($3::uuid, $4::uuid)
			AND GREATEST(cluster_a_id, cluster_b_id) = GREATEST($3::uuid, $4::uuid)
			AND status = 'pending' AND deleted_at IS NULL`
)

// SchemaSummary is a developer hint string surfaced in errors so it's
// trivial to confirm migrations are present.
func SchemaSummary() string {
	return fmt.Sprintf(
		"chora_consumption KG schema: kg_user_map_clusters + kg_user_explorations + kg_hexagon_nodes + kg_exploration_trail + atom_semantic_edges (0002) + kg_junctions + kg_active_clusters_per_user view (0003); RLS on chora.tenant_id + chora.user_gcid",
	)
}
