// companion_instance_wiring.go — composition root for the multi-Companion
// (1:N) CompanionInstanceRepository port.
//
// Background — M14.1 paydown #M14.1-famrepo-scan (2026-05-16):
//
//	The Postgres-backed adapter (pg.CompanionInstanceRepo) had stubbed
//	`row.Scan()` / `rows.Scan()` calls in M14.1, so even when the seed
//	migration 0036 placed Eira directly into chora_consumption, the read-
//	side `GET /v1/me/companions` route returned an empty `items[]` because
//	the in-memory adapter (still wired at the composition root) had nothing
//	to read. The defect blocked the Phyllis demo Round-15 closure.
//
//	After filling the Scan paths in pg/companion_instance.go, the in-memory
//	adapter is overridden with the pg-backed one when a pgx pool is
//	available. The in-memory path remains the fallback for tests / dev when
//	no pool is configured (matches the chat_wiring.go pattern).
//
// Per feedback_no_inline_config the pg pool is supplied by bootstrapDBPool;
// this helper has no env knowledge of its own.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireCompanionInstanceRepo swaps the in-memory CompanionInstanceRepo on the
// supplied Server for the pgx-backed pg.CompanionInstanceRepo when a pgx
// pool is available. When pool is nil it logs + returns without swapping
// (the in-memory adapter from NewServer continues to back the surface;
// useful for tests + local dev without a database).
//
// Per multi-tenant-rls SKILL every repo method internally runs
// rls.ApplySession (SET LOCAL chora.tenant_id + chora.user_gcid) — this
// helper does NOT configure tenant context; that flows from the HTTP
// middleware via tracing.WithTenantID / tracing.WithGCID on the request
// context.
func wireCompanionInstanceRepo(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: CompanionInstanceRepo NOT swapped (no pgx pool); in-memory adapter retained")
		return
	}
	txRunner := pg.NewPgxTxRunner(pool)
	srv.CompanionInstances = pg.NewCompanionInstanceRepo(txRunner)
	log.Printf("consumption: CompanionInstanceRepo wired (pool=chora_consumption, pg-backed)")
}
