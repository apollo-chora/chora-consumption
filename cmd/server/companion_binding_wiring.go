// companion_binding_wiring.go — composition root for Companion acquisition for a
// map (WS-A3, My Knowledge unification, CHO-2005; ADR-214 D1).
//
// Attaches the RLS-bound pg Companion Instance repo to the ExtServer so POST
// /v1/me/companions/acquire can mint the Instance; the map↔Companion link is the
// Goal itself (Goal.AttachedCompanionID — wired separately via the goal repo), so
// the theme-keyed CompanionMapBinding repo is RETIRED. Nil pool ⇒ the route
// returns 503 (fail-soft at boot, fail-loud per request). Per secrets-and-env this
// file reads no env directly; the pgx pool is built once in main.
package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// wireCompanionAcquisition attaches the pg Companion instance repo to the ExtServer
// for map acquisition (POST /v1/me/companions/acquire) and — CHO-2013 P1 (R3-1) —
// the Summoner that applies the companion-side effects of a goals-PATCH attach
// (specialization derive + resonant-pick clear + specialization_changed emit).
// Without an outbox store the Summoner stays unwired and the attach door
// refuses 503 (fail-loud; acquire itself still works).
func wireCompanionAcquisition(ext *httpadapter.ExtServer, pool *pgxpool.Pool, store consumptionoutbox.Store) {
	if pool == nil {
		log.Printf("consumption: companion-per-map acquire NOT wired (no pgx pool); /v1/me/companions/acquire → 503")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	ext.CompanionInstances = pg.NewCompanionInstanceRepo(tx)
	log.Printf("consumption: companion-per-map acquire wired (ADR-214 D1): POST /v1/me/companions/acquire")

	if store == nil {
		log.Printf("consumption: Summoner NOT wired (no outbox store); goals-PATCH attach → 503 SUMMON_NOT_WIRED")
		return
	}
	publisher := consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
		Store:         store,
		AggregateType: "companion",
	})
	summoner, err := companion.NewSummoner(companion.SummonerConfig{
		Instances: pg.NewCompanionInstanceRepo(tx),
		Resonance: resonanceClearAdapter{growth: pg.NewGrowthRepo(tx)},
		Outbox:    consumptionoutbox.NewLoadoutOutbox(publisher),
		NewID:     domain.NewUUIDv7,
	})
	if err != nil {
		log.Printf("consumption: Summoner NOT wired: %v — goals-PATCH attach → 503 SUMMON_NOT_WIRED", err)
		return
	}
	ext.Summoner = summoner
	log.Printf("consumption: Summoner wired (CHO-2013 P1, R3-1): goals-PATCH attach derives specialization + clears resonance")
}

// resonanceClearAdapter bridges the growth repo's SetResonantConcept(nil) to
// the companion.ResonanceClearer port.
type resonanceClearAdapter struct {
	growth *pg.GrowthRepo
}

func (a resonanceClearAdapter) ClearResonance(ctx context.Context, tenantID, companionID string) error {
	_, err := a.growth.SetResonantConcept(ctx, tenantID, companionID, nil)
	return err
}
