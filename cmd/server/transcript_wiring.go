// transcript_wiring.go — composition-root wiring for the StudentTranscript
// read-model (W6 Slice 1, Four-Mode plan "Outcome spine"). pg-only: the
// read-model is meaningless without a database, so when no pgx pool is
// present the projection stays inactive (the subscriber fan-out + the two
// read routes 503 rather than silently serving an always-empty list).
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireTranscriptRepo constructs the TranscriptSubscriber + attaches the
// Transcript read repo on the supplied ExtServer over the pgx-backed
// pg.TranscriptRepo when a pgx pool is available. No-op (logs) when pool is
// nil. MUST run before wireSubscriberPushHandlers so the LearnerProfile push
// endpoints' Transcript fan-out picks up ext.TranscriptSub.
func wireTranscriptRepo(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: StudentTranscript projection NOT wired (no pgx pool); W6 Slice 1 read-model inactive")
		return
	}
	ext.WireTranscriptPg(pg.NewTranscriptRepo(pg.NewPgxTxRunner(pool)))
	log.Printf("consumption: StudentTranscript projection wired (pool=chora_consumption, pg-backed, W6 Slice 1)")
}
