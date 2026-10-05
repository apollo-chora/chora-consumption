// topic_retention_wiring.go — composition root for the durable per-(tenant,
// gcid, topic) Ebbinghaus TopicRetention aggregate (CHO-1949).
//
// topic_retention was the ONLY learner-state left purely in-memory: the score
// was wiped on every pod restart and the /v1/me/topic-retention read was
// cross-tenant-unsafe (no RLS). This attaches the RLS-bound pg-backed
// topic_retention.Repository to the ExtServer (and rebuilds SessionCompletion to
// write through it), mirroring wireGoalRepo / wireLearningPathRepo.
//
// Nil pool ⇒ the in-memory adapter from NewExtServer is retained (lost on
// restart — dev/test only). Per secrets-and-env this file reads no env directly;
// the pgx pool is built once in main from Secret Manager.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireTopicRetentionRepo attaches the RLS-bound pg TopicRetention repo to the
// ExtServer. MUST be called AFTER wireAtomIndexRepo + wireLearningPathRepo so
// the rebuilt SessionCompletion binds the already-pg AtomIndex + LearningPath.
func wireTopicRetentionRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: topic_retention in-memory retained (LOST ON RESTART — dev/test only); no pgx pool")
		return
	}
	ext.WireTopicRetentionPg(pg.NewTopicRetentionRepo(pg.NewPgxTxRunner(pool)))
	log.Printf("consumption: topic_retention wired (pg-backed, durable, RLS-bound): GET /v1/me/topic-retention + completion write-through")
}
