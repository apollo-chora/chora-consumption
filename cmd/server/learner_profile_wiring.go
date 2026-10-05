// learner_profile_wiring.go — composition-root wiring for the global
// LearnerProfile projection (ADR-200 WS1.c3). pg-only: the read-model is
// meaningless without a database, so when no pgx pool is present the projection
// stays inactive (the subscriber + push endpoints are simply not mounted).
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireLearnerProfileRepo constructs the LearnerProfileSubscriber on the supplied
// ExtServer over the pgx-backed pg.LearnerProfileRepo when a pgx pool is
// available. No-op (logs) when pool is nil. MUST run before
// wireSubscriberPushHandlers so the per-topic push endpoints mount.
func wireLearnerProfileRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: LearnerProfile projection NOT wired (no pgx pool); ADR-200 read-model inactive")
		return
	}
	ext.WireLearnerProfilePg(pg.NewLearnerProfileRepo(pg.NewPgxTxRunner(pool)))
	log.Printf("consumption: LearnerProfile projection wired (pool=chora_consumption, pg-backed, ADR-200 WS1.c3)")
}
