// companion_memory_delete_wiring.go: composition root for the learner's own
// delete over their conversation memory (register 6.4 R6).
//
//	DELETE /v1/me/companions/{id}/memory/{memoryId}
//
// Mirrors wireCompanionMemoryRead: builds a standalone handler from the one pgx
// pool and returns it for main.go to hand to the router. A nil pool yields a
// handler that 503s per request rather than a nil route, so a learner asking for
// their conversation to be forgotten is told plainly that it did not happen.
// Per secrets-and-env this file reads no env directly.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCompanionMemoryDelete builds the learner-facing memory delete handler.
// Always returns a non-nil handler (503-on-request when the pool is absent).
func wireCompanionMemoryDelete(pool *pgxpool.Pool) *httpadapter.CompanionMemoryDeleteHandler {
	if pool == nil {
		log.Printf("consumption: Companion memory delete NOT wired (no pgx pool); DELETE /v1/me/companions/{id}/memory/{memoryId} → 503")
		return httpadapter.NewCompanionMemoryDeleteHandler(nil)
	}
	h := httpadapter.NewCompanionMemoryDeleteHandler(
		pg.NewCompanionMemoryRepo(pg.NewPgxTxRunner(pool)),
	)
	log.Printf("consumption: Companion memory delete wired (R6): DELETE /v1/me/companions/{id}/memory/{memoryId}")
	return h
}
