// campaign_xp_wiring.go — composition root for the WS-C5 campaign conquest
// XP consumer (CHO-2084, ADR-227 D10 + addendum #6): registers
// /api/internal/pubsub/campaign-growth-events, folding the three XP-bearing
// campaign.* topics into goal-routed growth.Service.AwardExp calls
// (ResolveForGoal — its first real caller). Nil pool / growth service ⇒
// skipped (the push subs are only provisioned once the handler is live —
// five-wirings discipline). Per secrets-and-env the OIDC verifier + the
// seal-spacing tunable read env HERE (cmd/server is the only env-reading
// layer); the D10 "tier-S ~1/week" spacing default lives with the
// subscriber (DefaultSealXPMinSpacing).
package main

import (
	"errors"
	"context"

	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// wireCampaignXP composes the pg-backed deps off the pool and delegates to
// wireCampaignXPPush. MUST follow wireGrowthService (the award port).
func wireCampaignXP(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, growthSvc *growth.Service, bus eventbus.Bus) {
	if pool == nil || growthSvc == nil {
		log.Printf("consumption: campaign XP NOT wired (pool/growth absent)")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	resolver := subscribers.NewRepoCompanionResolver(pg.NewCompanionInstanceRepo(tx), pg.NewGoalRepo(tx))
	wireCampaignXPPush(ctx, srv, growthSvc, resolver, pg.NewGrowthRepo(tx), pg.NewCampaignLineageHistoryRepo(tx), bus)
}

// wireCampaignXPPush attaches the campaign XP inbound adapter over the
// supplied ports (the seam the wiring test pins). Any nil dep skips with a
// loud log — the route then stays unmounted and the subs must not be
// provisioned.
func wireCampaignXPPush(
	ctx context.Context,
	srv *httpadapter.Server,
	award subscribers.AwardExpPort,
	resolver subscribers.GoalCompanionResolver,
	history subscribers.GrowthAwardHistory,
	lineage subscribers.CampaignLineageHistory,
	bus eventbus.Bus,
) {
	if srv == nil || award == nil || resolver == nil || history == nil || lineage == nil {
		log.Printf("consumption: campaign XP NOT wired (missing award/resolver/history/lineage)")
		return
	}

	spacing := time.Duration(0) // 0 → subscriber default (7d)
	if v := strings.TrimSpace(os.Getenv("CHORA_CAMPAIGN_SEAL_XP_MIN_SPACING")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Printf("consumption: CHORA_CAMPAIGN_SEAL_XP_MIN_SPACING=%q invalid (want Go duration > 0); keeping default %v", v, subscribers.DefaultSealXPMinSpacing)
		} else {
			spacing = d
		}
	}

	sub := subscribers.NewCampaignXPSubscriber(award, resolver, history, lineage, spacing, nil)
	if bus == nil {
		log.Printf("consumption: campaign XP NOT wired (no event bus, NATS_URL unset)")
		return
	}
	effective := spacing
	if effective <= 0 {
		effective = subscribers.DefaultSealXPMinSpacing
	}
	go func() {
		log.Printf("consumption: campaign XP subscriber binding the three campaign.* topics (WS-C5; rung/won/sealed via ResolveForGoal; seal spacing=%v)", effective)
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.campaign-xp", subscribers.TopicCampaignRungCleared), subscribers.CampaignXPHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: campaign-xp subscriber exited: %v", err)
		}
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.campaign-xp-node-won", subscribers.TopicCampaignNodeWon), subscribers.CampaignXPHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: campaign-xp subscriber exited: %v", err)
		}
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.campaign-xp-goal-sealed", subscribers.TopicCampaignGoalSealed), subscribers.CampaignXPHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: campaign-xp subscriber exited: %v", err)
		}
	}()
}
