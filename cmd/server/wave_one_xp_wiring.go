// wave_one_xp_wiring.go — composition root for the F-I3 Wave-1 companion-EXP
// consumer (CHO-2090, ADR-228 D4 Wave 1): builds the WaveOneXPSubscriber and
// registers its two dedicated inboxes
//
//	/api/internal/pubsub/weakness-grown-exp   (weakness.grown.v1, BINARY)
//	/api/internal/pubsub/module-completed-exp (module_progress.completed.v1,
//	                                           JSON — ⚠ DARK: no delivery
//	                                           emitter yet, do not provision
//	                                           the sub until it lands)
//
// The third Wave-1 leg (submission_graded) has NO inbox here — it rides the
// EXISTING submission-graded-derived subscription: main passes the returned
// subscriber into wireDerivedWeakness, whose push dispatch awards after the
// derived projection (see submission_graded_derived_pubsub_handler.go).
//
// Nil pool / growth service ⇒ skipped with a loud log (the push subs are
// only provisioned once the handler is live — five-wirings discipline).
// Attribution is the ACTIVE COMPANION (GQ-26): none of the Wave-1 events
// carries a goal_id, so the resolver's goal read only feeds the
// most-recent-summon fallback chain. Per secrets-and-env the OIDC verifier
// reads env HERE (cmd/server is the only env-reading layer).
package main

import (
	"errors"
	"context"

	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// wireWaveOneXP composes the pg-backed deps off the pool and delegates to
// wireWaveOneXPPush. MUST follow wireGrowthService (the award port). Returns
// the subscriber (nil when prerequisites are missing) so main can hand it to
// wireDerivedWeakness for the submission_graded leg.
func wireWaveOneXP(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, growthSvc *growth.Service, bus eventbus.Bus) *subscribers.WaveOneXPSubscriber {
	if pool == nil || growthSvc == nil {
		log.Printf("consumption: wave-1 XP NOT wired (pool/growth absent); weakness-grown-exp + module-completed-exp not registered; submission_graded rides derived-only")
		return nil
	}
	tx := pg.NewPgxTxRunner(pool)
	resolver := subscribers.NewRepoCompanionResolver(pg.NewCompanionInstanceRepo(tx), pg.NewGoalRepo(tx))
	return wireWaveOneXPPush(ctx, bus, srv, growthSvc, resolver)
}

// wireWaveOneXPPush attaches the Wave-1 XP inbound adapters over the supplied
// ports. Any nil dep skips with a loud log — the routes then stay unmounted
// and the subs must not be provisioned.
func wireWaveOneXPPush(
	ctx context.Context,
	bus eventbus.Bus,
	
	srv *httpadapter.Server,
	award subscribers.AwardExpPort,
	resolver subscribers.CompanionResolver,
) *subscribers.WaveOneXPSubscriber {
	if srv == nil || award == nil || resolver == nil {
		log.Printf("consumption: wave-1 XP NOT wired (missing award/resolver); weakness-grown-exp + module-completed-exp not registered")
		return nil
	}

	sub := subscribers.NewWaveOneXPSubscriber(award, resolver)
	if bus == nil {
		log.Printf("consumption: wave-1 XP NOT wired (no event bus, NATS_URL unset)")
		return sub
	}
	go func() {
		log.Printf("consumption: wave-1 XP subscribers binding weakness.grown + module_progress.completed (F-I3; module leg DARK until the delivery emitter lands)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.weakness-grown-exp", subscribers.TopicWeaknessGrown), subscribers.WeaknessGrownExpHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: weakness-grown-exp subscriber exited: %v", err)
		}
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.module-completed-exp", subscribers.TopicModuleProgressCompleted), subscribers.ModuleCompletedExpHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: module-completed-exp subscriber exited: %v", err)
		}
	}()
	return sub
}
