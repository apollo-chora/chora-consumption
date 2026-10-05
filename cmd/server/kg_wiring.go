// kg_wiring.go — boot wiring for the per-user Knowledge Graph (ADR-143):
// pg-backed canvas repos + the fog orchestrator repoint (ADR-169).
//
// Without the pg swap the canvas state (clusters / explorations / hexagon
// fog / trail) lived in process memory only — every pod roll wiped the
// learner's knowledge graph (and main-push pipelines roll this service
// many times a day). The pg adapters are RLS-bound; the KG routes wrap
// the request ctx via withKGRLSContext at registration.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/clusterprojection"
)

// wireUserKGPg swaps the in-memory per-user KG repos for the pg adapters
// (kg_user_map_clusters / kg_user_explorations / kg_hexagon_nodes +
// kg_exploration_trail / kg_junctions, all RLS-bound) so canvas state
// survives pod restarts and replicas. No-op without a pool (tests / dev).
func wireUserKGPg(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: per-user KG repos NOT swapped (no pgx pool); in-memory adapters retained (NOT durable across restart)")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	clusterRepo := pg.NewMapClusterRepo(tx)
	ext.WireUserKGPg(
		clusterRepo,
		pg.NewExplorationRepo(tx),
		pg.NewHexagonRepo(tx),
		pg.NewJunctionRepo(tx),
	)
	// ADR-223: the learner-triggered fog MapCluster -> sovereign Goal projector
	// (cluster loader + the atomic concept+goal+cluster applier, same pool).
	ext.ClusterProjector = clusterprojection.NewProjector(clusterRepo, pg.NewClusterProjectionApplier(tx))
	log.Printf("consumption: per-user KG wired (pool=chora_consumption, pg-backed: clusters/explorations/hexagon-fog/trail/junctions + ADR-223 cluster projector, durable across restart + replicas)")
}
