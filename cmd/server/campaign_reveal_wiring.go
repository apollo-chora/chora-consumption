// campaign_reveal_wiring.go - composition root for the WS-C4 free-on-win fog
// reveal (CHO-2083, ADR-227 D2 + addendum #1): registers
// /api/internal/pubsub/campaign-node-won, folding each
// chora.consumption.campaign.node_won.v1 into exactly ONE mana-exempt
// concept_suggestion.requested.v1 (claim-then-mark on the pg RevealLedger,
// migration 0080). Nil pool ⇒ skipped (the push sub is only provisioned once
// the handler is live - five-wirings discipline). Per secrets-and-env the OIDC
// verifier reads the same env vars as the sibling push handlers; cmd/server is
// the only env-reading layer.
package main

import (
	"errors"
	"context"

	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireCampaignRevealPush attaches the node_won.v1 inbound adapter. MUST follow
// the outbox bootstrap (outboxStore) - the free reveal rides the durable
// outbox; the in-memory fallback (dev without CHORA_OUTBOX_DSN) is loud.
func wireCampaignReveal(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store, fallback events.Publisher, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: campaign free-on-win reveal NOT wired (no pgx pool); /api/internal/pubsub/campaign-node-won not registered")
		return
	}
	pub := fallback
	if outboxStore != nil {
		pub = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "concept_suggestion",
		})
	} else {
		log.Printf("consumption: campaign free reveal rides the in-memory publisher (CHORA_OUTBOX_DSN unset; NOT durable)")
	}
	if pub == nil {
		log.Printf("consumption: campaign free-on-win reveal NOT wired (no publisher)")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	// node_revealed.v1 is a campaign event - it rides a campaign-typed outbox
	// publisher (aggregate_type "campaign", matching focus/seal/node_won), NOT
	// the reveal request's concept_suggestion publisher. Both still persist
	// durably before emit and dedupe on the won event id; a nil outbox falls
	// back to the same in-memory publisher (dev, loud).
	revealEventPub := fallback
	if outboxStore != nil {
		revealEventPub = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "campaign",
		})
	}
	revealEmitter := events.NewCampaignEmitter(revealEventPub, nil)
	sub := subscribers.NewCampaignNodeWonSubscriber(
		pg.NewCampaignRevealLedgerRepo(tx),
		pg.NewGoalRepo(tx),
		pg.NewConceptNodeRepo(tx),
		pub,
		revealEmitter,
	).WithCatalogue(ext). // ADR-244 D2: the free-on-win reveal cites entitled atoms too
		// ADR-247 Capability B (CHO-2328): enrich the reveal with the won node's
		// diagnosed weakness (F2, narrow WeaknessContextLoader) + goal/ancestor
		// lineage (F3). Both optional - the reveal still publishes when absent.
		WithWeakness(pg.NewLearnerWeaknessRepo(tx)).
		WithEdges(pg.NewEdgeRepo(tx))
	if bus == nil {
		log.Printf("consumption: campaign free-on-win reveal NOT wired (no event bus, NATS_URL unset)")
		return
	}
	go func() {
		log.Printf("consumption: campaign free-on-win reveal subscriber binding chora.consumption.campaign.node_won.v1 (WS-C4; ledger=pg mig 0080)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.campaign-node-won", subscribers.TopicCampaignNodeWon), subscribers.CampaignNodeWonHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: campaign-node-won subscriber exited: %v", err)
		}
	}()
}
