// retention_wiring.go — composition root for the durable Maya retention loop
// (L4 / CHO-1702, no-debt directive 2026-06-10).
//
// Swaps the in-memory SM-2 / streak / XP stores on the Phyllis Server for the
// pgx-backed adapters (migration 0048: sm2_states / learner_seen_topics /
// learner_streaks / learner_xp) when a pgx pool is available, so the
// learner's Ebbinghaus state, daily streak and lifetime XP survive pod
// restarts and are replica-consistent. In-memory remains the fallback for
// tests / dev without a database (matches course_content_wiring.go).
//
// Per feedback_no_inline_config the pg pool is supplied by bootstrapDBPool;
// per multi-tenant-rls the pg adapters run rls.ApplySession internally —
// tenant context flows from the HTTP middleware via repoCtx.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireRetentionStores swaps SM2/Streaks/XP + BOTH atom-session stores
// (legacy Server lane + the live ExtServer lane) for pg-backed adapters
// when a pool is available. No-op (logs LOUDLY) when pool is nil.
func wireRetentionStores(srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: retention stores NOT swapped (no pgx pool); in-memory SM-2/streak/XP retained (LOST ON RESTART — dev/test only)")
		return
	}
	txRunner := pg.NewPgxTxRunner(pool)
	// CHO-2029: durable atom sessions — swap the per-pod in-memory stores
	// (both the legacy /api/sessions lane on Server AND the live
	// /v1/me/atom-sessions + /api/atoms/{id}/session lane on ExtServer)
	// for the pg adapter so start/submit survive replica hops.
	sessions := pg.NewAtomSessionRepo(txRunner)
	srv.Sessions = sessions
	if ext != nil {
		ext.Sessions = sessions
	}
	srv.SM2 = pg.NewSM2StateRepo(txRunner)
	srv.Streaks = pg.NewLearnerStreakRepo(txRunner)
	srv.XP = pg.NewLearnerXPRepo(txRunner)
	log.Printf("consumption: retention stores wired (atomic_sessions/sm2_states/learner_streaks/learner_xp, pg-backed, durable across restart + replicas)")
}
