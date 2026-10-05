// cluster_projection.go — Postgres applier for the fog MapCluster → sovereign
// Goal projection (clusterprojection.Applier, ADR-223 D2). It materialises the
// root ConceptNode + the anchored Goal + the projected-cluster flip in ONE
// transaction, mirroring the SuggestionAccepter.ApplyAccept precedent so the
// three cross-aggregate writes stay consistent (no Goal referencing a missing
// root; no cluster marked projected without its Goal). All three aggregates
// live in chora_consumption, so a single transaction is legitimate — this is an
// application-service applier, not a domain-model cross-aggregate transaction.
//
// Reuses the canonical insert/upsert SQL consts (insertConceptNodeSQL /
// insertGoalSQL / upsertMapClusterSQL) from this package, so schema drift is
// caught once by the prepare-smoke integration test.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/clusterprojection"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ClusterProjectionApplier is the Postgres-backed clusterprojection.Applier.
type ClusterProjectionApplier struct {
	tx TxRunner
}

// NewClusterProjectionApplier constructs the applier around a TxRunner.
func NewClusterProjectionApplier(tx TxRunner) *ClusterProjectionApplier {
	return &ClusterProjectionApplier{tx: tx}
}

// Compile-time check: the pg applier satisfies the domain port.
var _ clusterprojection.Applier = (*ClusterProjectionApplier)(nil)

// Apply inserts the root ConceptNode, the anchored Goal, and flips the cluster
// to PROJECTED — all inside one RLS-scoped transaction.
func (a *ClusterProjectionApplier) Apply(
	ctx context.Context,
	root *conceptgraph.ConceptNode,
	g *goal.Goal,
	cluster *userknowledgegraph.MapCluster,
) error {
	return a.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertConceptNodeSQL, conceptNodeInsertArgs(root)...); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, insertGoalSQL,
			g.GoalID, g.TenantID, g.LearnerGCID, string(g.Kind), g.ChoraTargetRef,
			g.ConceptSet, g.MasteredConceptCount, string(g.Status), g.NorthStarNote,
			g.AttachedCompanionID, g.RootConceptID, g.PersonalCompletedAt, g.CreatedAt, g.UpdatedAt,
		); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertMapClusterSQL,
			cluster.ClusterID, cluster.TenantID, cluster.UserGCID, cluster.DisplayName, cluster.SeedTopic,
			cluster.SeedAtomID, string(cluster.Status), nullableUUID(cluster.MergedIntoClusterID),
			nullableUUID(cluster.MergedViaAtomID), nullableUUID(cluster.ProjectedIntoGoalID),
			cluster.NodeCount, cluster.Version,
		)
		return err
	})
}
