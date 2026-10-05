// concept_graph_wiring.go — composition root for the learner-sovereign
// concept-graph read + re-root API (ADR-212 WS-2, CHO-1995).
//
// Attaches the RLS-bound pg ConceptNode + Edge repos + the ReRootApplier to the
// ExtServer so GET /v1/me/concept-graph + POST /v1/me/concept-graph/reroot are
// live. Nil pool ⇒ the routes return 503 (fail-soft at boot, fail-loud per
// request) — the same pattern as wireGoalRepo. Per secrets-and-env this file
// reads no env directly; the pgx pool is built once in main from Secret Manager.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireConceptGraphReRoot attaches the pg concept-node + edge repos + re-root
// applier to the ExtServer.
func wireConceptGraphReRoot(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: concept-graph re-root NOT wired (no pgx pool); /v1/me/concept-graph* → 503")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	ext.Concepts = pg.NewConceptNodeRepo(tx)
	ext.ConceptEdges = pg.NewEdgeRepo(tx)
	ext.ConceptReRoot = pg.NewConceptReRootRepo(tx)
	ext.LearningEdges = pg.NewLearningEdgeRepo(tx) // CHO-2038 ceremony learning-edges
	// WS-C6 (CHO-2085, ADR-227 D14 + addendum #7): the atomic merge/split
	// applier + the lineage-ancestry paint read. The doors additionally need
	// Goals/CampaignProgress/Retention/CampaignResonance (wired by their own
	// composition roots) — mergeSplitDeps gates 503 until all are live.
	ext.MergeSplit = pg.NewMergeSplitRepo(tx)
	ext.ConceptLineage = pg.NewConceptLineageRepo(tx)
	log.Printf("consumption: concept-graph re-root wired (ADR-212 WS-2): GET /v1/me/concept-graph + POST /v1/me/concept-graph/reroot + POST /v1/me/goals/{id}/learning-edges + POST /v1/me/concept-graph/concepts/{id}/{merge|split} (WS-C6 D14)")
}
