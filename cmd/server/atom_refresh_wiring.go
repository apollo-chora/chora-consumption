// atom_refresh_wiring.go - composition root for the ADR-244 D5 standing
// refresh trigger: builds the AtomRefreshSubscriber over the pg
// atom_refresh_ledger (migration 0108) + the pg graph/campaign/atom_index
// repos + the durable outbox publisher, and hands it to ExtServer so
// subscriber_wiring.go can (a) fan it out from the LIVE atom-published inbox
// and (b) front it with the NEW /api/internal/pubsub/atom-updated inbox.
//
// Proposals only, zero atom_refs writes (ADR-244 D1 / ADR-212 D9). The
// request rides request_source=atom_refresh, mana-exempt at the fog
// orchestrator's metering seam; deploy the fog orchestrator FIRST or the
// gateway FallbackMap meters the request as knowledge_graph_traverse.
//
// Nil pool ⇒ skipped loudly (five-wirings discipline: the push subs are only
// provisioned once the handler is live). Per secrets-and-env cmd/server is
// the only env-reading layer; this file reads none.
package main

import (
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	atomrefresh "github.com/apollo-chora/chora-consumption/internal/domain/atom_refresh"
)

// wireAtomRefresh constructs ExtServer.AtomRefreshSub. MUST follow the outbox
// bootstrap (outboxStore) and precede wireSubscriberPushHandlers (which
// attaches the fan-out + the atom-updated push handler).
func wireAtomRefresh(ext *httpadapter.ExtServer, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store, fallback events.Publisher) {
	if ext == nil {
		return
	}
	if pool == nil {
		log.Printf("consumption: ADR-244 D5 atom refresh NOT wired (no pgx pool); atom-published fan-out + /api/internal/pubsub/atom-updated stay dark")
		return
	}
	pub := fallback
	if outboxStore != nil {
		pub = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "concept_suggestion",
		})
	} else {
		log.Printf("consumption: atom refresh rides the in-memory publisher (CHORA_OUTBOX_DSN unset; NOT durable)")
	}
	if pub == nil {
		log.Printf("consumption: ADR-244 D5 atom refresh NOT wired (no publisher)")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	ext.AtomRefreshSub = subscribers.NewAtomRefreshSubscriber(
		pg.NewAtomRefreshLedgerRepo(tx),
		pg.NewGoalRepo(tx),
		pg.NewConceptNodeRepo(tx),
		pg.NewEdgeRepo(tx),
		pg.NewCampaignProgressRepo(tx),
		pg.NewAtomIndexRepo(tx),
		pub,
	).WithCatalogue(ext) // ADR-244 D2: same entitled-catalogue resolution as the learner door + free reveal
	log.Printf("consumption: ADR-244 D5 atom refresh wired (ledger=pg mig 0108; caps %d/event, %d/learner/day)",
		atomrefresh.MaxProposalsPerAtomEvent, atomrefresh.MaxProposalsPerLearnerPerDay)
}
