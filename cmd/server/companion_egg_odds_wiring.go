// companion_egg_odds_wiring.go: composition root for the effective per-learner
// egg odds surface (owner ruling 2026-08-07).
//
//	GET /v1/me/companions/{id}/egg-odds
//
// Builds a standalone *CompanionEggOddsHandler from the pgx pool and the SHARED
// breed-distribution provider, and returns it so main.go can hand it to the
// router.go mux.
//
// WHY THE PROVIDER IS SHARED. The handler publishes the pool the reveal rolls,
// so both must read the SAME declared SKU table. The provider caches
// PreviewEggOdds responses for CHORA_TENANCY_EGG_ODDS_CACHE_TTL (5 min by
// default); a second client instance would carry a second cache with its own
// expiry, and for one TTL window after a tenant admin edits a SKU the odds
// endpoint could publish table A while a reveal rolled table B. That is the
// declared-vs-observed gap this endpoint exists to close, so the two surfaces
// share one provider (and one gRPC connection to chora-tenancy).
//
// Nil pool ⇒ a handler with nil essential deps that answers 503 per request
// (fail-soft at boot, fail-loud per request), matching wireCompanionMemoryRead.
// Per secrets-and-env this file reads no env directly.
package main

import (
	"log"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

var (
	breedDistOnce     sync.Once
	breedDistProvider growth.BreedDistributionProvider
)

// sharedBreedDistributionProvider memoises bootstrapBreedDistributionProvider
// so every caller in this process gets the SAME client, hence the same cache
// and the same declared table. Env is read once, on first call; it does not
// change mid-process.
func sharedBreedDistributionProvider() growth.BreedDistributionProvider {
	breedDistOnce.Do(func() {
		breedDistProvider = bootstrapBreedDistributionProvider()
	})
	return breedDistProvider
}

// wireCompanionEggOdds builds the effective per-learner odds handler. Always
// returns a non-nil handler (503-on-request when the pool is absent) so the
// router can register the route unconditionally.
func wireCompanionEggOdds(pool *pgxpool.Pool) *httpadapter.CompanionEggOddsHandler {
	if pool == nil {
		log.Printf("consumption: effective egg odds NOT wired (no pgx pool); GET /v1/me/companions/{id}/egg-odds → 503")
		return httpadapter.NewCompanionEggOddsHandler(nil, nil)
	}
	// Same repo the growth service reads through, so the owned-species set
	// behind the published odds is the one RevealBreed subtracts.
	repo := pg.NewGrowthRepo(pg.NewPgxTxRunner(pool))
	h := httpadapter.NewCompanionEggOddsHandler(repo, sharedBreedDistributionProvider())
	log.Printf("consumption: effective egg odds WIRED: GET /v1/me/companions/{id}/egg-odds (declared + effective, owner ruling 2026-08-07)")
	return h
}
