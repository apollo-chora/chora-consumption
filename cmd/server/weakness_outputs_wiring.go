// weakness_outputs_wiring.go - ADR-254 D4 (W4 consumption cut): bind the
// weakness.outputs_generated.v1 projector (ADR-205 WS-7 artifacts, incl. the
// new companion_voice kind) to its PULL subscription. Env (read only here):
// CHORA_WEAKNESS_OUTPUTS_SUBSCRIPTION, default
// chora-consumption.consumption-weakness-outputs-generated (provisioned by
// the coordinator's companion_topic_estate.tf).
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	consumptionpg "github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

const envWeaknessOutputsSub = "CHORA_WEAKNESS_OUTPUTS_SUBSCRIPTION"

// wireWeaknessOutputsConsumer starts the pull consumer when both the pool (the
// growth_edge_outputs projection) and the event bus are present; otherwise it
// says loudly what is missing (the artifacts the learner paid for would stay
// undelivered, which must never be silent).
func wireWeaknessOutputsConsumer(ctx context.Context, pool *pgxpool.Pool, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: weakness.outputs_generated consumer NOT wired (no pg pool): generated artifacts would be undelivered")
		return
	}
	if bus == nil {
		log.Printf("ERROR consumption: weakness.outputs_generated consumer NOT wired (no event bus): generated artifacts stay undelivered")
		return
	}
	sub := strings.TrimSpace(os.Getenv(envWeaknessOutputsSub))
	if sub == "" {
		sub = subscribers.WeaknessOutputsSubscription
	}
	repo := consumptionpg.NewGrowthEdgeOutputRepo(consumptionpg.NewPgxTxRunner(pool))
	handler := subscribers.WeaknessOutputsPullHandler(subscribers.NewWeaknessOutputsGeneratedSubscriber(repo))
	go func() {
		log.Printf("consumption: weakness.outputs_generated consumer binding %s (pull)", sub)
		if err := bus.Subscribe(ctx, consumerConfig(sub, "chora.consumption.weakness.outputs_generated.v1"), handler); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: weakness.outputs_generated consumer exited: %v", err)
		}
	}()
}
