// goal_wiring.go — composition root for the learner-owned Goal aggregate
// (ADR-204 §2). Attaches the pg Goal repo to the ExtServer so the CRUD API
// (POST/GET /v1/me/goals + PATCH /v1/me/goals/{id}) is live.
//
// Nil pool ⇒ the routes return 503 (fail-soft at boot, fail-loud per request) —
// the same pattern as wireGrowthEdgeReadRepo. Per secrets-and-env this file reads
// no env directly; the pgx pool is built once in main from Secret Manager.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireGoalRepo attaches the RLS-bound pg Goal repo to the ExtServer.
func wireGoalRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: goals API NOT wired (no pgx pool); /v1/me/goals → 503")
		return
	}
	ext.Goals = pg.NewGoalRepo(pg.NewPgxTxRunner(pool))
	log.Printf("consumption: goals API wired (pg Goal repo): POST/GET /v1/me/goals + PATCH /v1/me/goals/{id}")
}
