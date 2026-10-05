// companion_memory_read_wiring.go — composition root for the ADR-215 WS-1
// tier-(a) learner-facing Companion memory + context READ API (CHO-1997).
//
//	GET /v1/me/companions/{id}/memory
//
// Builds a standalone *CompanionMemoryReadHandler from the pgx pool and returns
// it so main.go can hand it to the router.go mux (the shared serialization
// point). ALL collaborators are constructed here from the one pool so the
// wiring is order-independent (it depends on nothing but the pool):
//   - CompanionMemoryViewRepo — recency read over companion_memory_recall (new).
//   - CompanionInstanceRepo    — persona/rules/focus + the owner-leak guard.
//   - ConceptNodeRepo         — visible KG neighbours (fail-soft enrichment).
//   - AtomIndexRepo           — atom UUID → title/topic resolution (fail-soft).
//
// Nil pool ⇒ a handler with nil essential deps that returns 503 per request
// (fail-soft at boot, fail-loud per request) — the same pattern as wireGoalRepo.
// Per secrets-and-env this file reads no env directly; the pool is built once in
// main from Secret Manager.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCompanionMemoryRead builds the learner-facing Companion memory read handler.
// Always returns a non-nil handler (503-on-request when the pool is absent) so
// the router can register the route unconditionally.
func wireCompanionMemoryRead(pool *pgxpool.Pool) *httpadapter.CompanionMemoryReadHandler {
	if pool == nil {
		log.Printf("consumption: Companion memory read API NOT wired (no pgx pool); GET /v1/me/companions/{id}/memory → 503")
		return httpadapter.NewCompanionMemoryReadHandler(nil, nil, nil, nil)
	}
	tx := pg.NewPgxTxRunner(pool)
	h := httpadapter.NewCompanionMemoryReadHandler(
		pg.NewCompanionMemoryViewRepo(tx), // recency recalls
		pg.NewCompanionInstanceRepo(tx),   // persona/rules/focus + owner-leak guard
		pg.NewConceptNodeRepo(tx),         // visible KG neighbours (fail-soft)
		pg.NewAtomIndexRepo(tx),           // atom UUID → title resolve (fail-soft)
	)
	// ADR-214 goal scoping for "What it can see". Without these the handler can
	// only serve the learner's WHOLE flat concept space, which leaks every other
	// map into this one; with a ?goal_id= present and these absent it fail-CLOSES
	// (503) rather than quietly serving unscoped.
	h.Goals = pg.NewGoalRepo(tx)
	h.ConceptEdges = pg.NewEdgeRepo(tx)
	log.Printf("consumption: Companion memory read API wired (ADR-215 WS-1): GET /v1/me/companions/{id}/memory (goal-scoped: ADR-214)")
	return h
}
