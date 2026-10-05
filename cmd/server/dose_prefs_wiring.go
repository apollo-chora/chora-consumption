// dose_prefs_wiring.go — composition root for the ADR-224 #3 (CHO-2045) per-KG
// daily-dose preference store. Attaches the RLS-bound pg repo to the Server; it
// backs GET/PUT /v1/me/dose-preferences AND the weakness-pool exclude filter in
// doseGrowthEdges (excluded maps' concepts dropped, resolved via srv.Goals).
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
)

// wireDoseKGPrefs attaches the pg per-KG dose-preference repo to the Server.
// Nil pool ⇒ the endpoint 503s + the filter is a no-op (default all-included) —
// fail-soft at boot, fail-loud per request (never a silent dropped exclusion).
func wireDoseKGPrefs(srv *httpadapter.Server, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: dose-preferences repo NOT wired (nil pool) — /v1/me/dose-preferences 503s, dose filter no-op [ADR-224 / CHO-2045]")
		return
	}
	srv.DoseKGPrefs = pg.NewDoseKGPrefRepo(pg.NewPgxTxRunner(pool))
}
