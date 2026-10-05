// dosing_projections_wiring.go — composition root for the two daily-dose
// projections (CHO-2167, closing CHO-1471).
//
// # THE BUG THIS CLOSES
//
// `topic_accuracy` (dose WEAKNESS slot) and `active_path_topics` (dose
// CURIOSITY slot) both shipped with durable, RLS-bound pg adapters over tables
// that migration 0004 created. Neither was ever bound: Server/ExtServer held
// the CONCRETE *inmem.* types, so the composition root had nowhere to put a pg
// repo. The projections looked durable from every angle — table, migration,
// adapter, subscriber, tests — and ran entirely out of the pod's heap.
//
// Measured live 2026-07-14 (chora_consumption):
//
//   - active_path_topics ............ 0 rows. Never written, ever.
//   - topic_accuracy ............... 3 rows, written by DerivedWeaknessProjector
//     — which DOES hold the pg adapter. The dose composer was reading a
//     DIFFERENT, empty, in-memory store. A split-brain, not just volatility.
//
// THE RULE (and why the order below is load-bearing)
//
// Bind pg when the pool is available; keep in-memory ONLY as the dev fallback;
// and say out loud which one is live. A volatile projection quietly
// masquerading as durable is exactly the failure this ticket exists to kill, so
// the fallback log names the consequence rather than whispering "in-memory".
//
// This MUST run BEFORE ExtServer.ShareDosingProjections (main.go), which copies
// srv.TopicAccuracy / srv.ActivePathTopics onto the ExtServer and rebuilds the
// subscribers from them. Run it after, and the subscribers get rebound to the
// in-memory repos the Server was constructed with — the pg swap would be
// silently undone and the boot log would be a lie.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireDosingProjections swaps the in-memory dose projections for the pg-backed
// (durable, RLS-bound) adapters when a pgx pool is available. No-op on a nil
// Server. In-memory is retained when there is no pool.
func wireDosingProjections(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if srv == nil {
		return
	}
	if pool == nil {
		log.Printf("consumption: dosing projections NOT swapped (no pgx pool); " +
			"in-memory topic_accuracy + active_path_topics retained " +
			"(LOST ON RESTART, NOT SHARED ACROSS REPLICAS — dev/test only: the daily dose's " +
			"WEAKNESS + CURIOSITY slots will read an empty projection)")
		return
	}

	txRunner := pg.NewPgxTxRunner(pool)
	srv.TopicAccuracy = pg.NewTopicAccuracyRepo(txRunner)
	srv.ActivePathTopics = pg.NewActivePathTopicsRepo(txRunner)

	log.Printf("consumption: dosing projections wired " +
		"(topic_accuracy + active_path_topics, pg-backed, pool=chora_consumption, " +
		"durable across restart + replicas) — dose WEAKNESS slot now reads the SAME " +
		"topic_accuracy rows DerivedWeaknessProjector writes; dose CURIOSITY slot now " +
		"reads durable active_path_topics")
}
