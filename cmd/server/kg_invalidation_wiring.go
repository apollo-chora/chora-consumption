// kg_invalidation_wiring.go — cmd/server boot helper for the three KG hexagon
// fog-cache invalidation subscribers (ADR-143 / ADR-204 Slice F.1).
//
// The subscribers (AtomPublishedKG / AtomRevisionUpdatedKG /
// UserRetentionShiftedKG) and their HTTP push handler were built + tested
// (internal/adapter/subscribers/kg_invalidation_subscriber.go +
// internal/adapter/http/kg_invalidation_pubsub_handler.go) but never wired —
// srv.KGInvalidationPushHandler stayed nil so router.go skipped the
// /internal/pubsub/kg-retention mount and content/retention events never
// invalidated stale fog. This helper closes that gap.
//
// NB(CHO-1452): the chora.consumption.user_retention.shifted.v1 topic is NOT
// YET PROVISIONED. The handler is mounted regardless — it acks+drops any
// unrecognised topic safely (see dispatchKGInvalidation), so wiring the
// UserRetentionShifted subscriber now is harmless and ready for activation.
//
// Per the always-loaded secrets-and-env rule: the OIDC verifier comes from env
// at boot, reusing the same accessors wireSubscriberPushHandlers uses.
package main

import (
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"errors"
	"context"

	"log"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireKGInvalidationPushHandler constructs the three KG fog-cache invalidation
// subscribers over the ExtServer's (pg-backed after WireUserKGPg) hexagon repo
// + (durable after RewireKGEvents) KG event publisher, fronts them with the
// push handler, and attaches it to the Phyllis MVP server so Routes() mounts
// /internal/pubsub/kg-retention.
//
// MUST be called AFTER wireUserKGPg (so the invalidator is the durable pg
// hexagon repo) AND AFTER RewireKGEvents (so the subscribers' own
// PublishHexagonFogInvalidated emits land in the durable outbox). No-op when
// the KG repos / publisher are unavailable — boot never fails.
func wireKGInvalidation(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, bus eventbus.Bus) {
	if srv == nil || ext == nil {
		return
	}
	if ext.KGHexagons == nil || ext.KGEvents == nil {
		log.Printf("consumption: KG fog-invalidation push handler NOT wired (KG hexagon repo / event publisher unavailable)")
		return
	}
	if bus == nil {
		log.Printf("consumption: KG fog-invalidation NOT wired (no event bus, NATS_URL unset)")
		return
	}
	deps := struct {
		AtomPublished        *subscribers.AtomPublishedKGSubscriber
		AtomRevisionUpdated  *subscribers.AtomRevisionUpdatedKGSubscriber
		UserRetentionShifted *subscribers.UserRetentionShiftedKGSubscriber
	}{
		AtomPublished:        subscribers.NewAtomPublishedKGSubscriber(ext.KGHexagons, ext.KGEvents),
		AtomRevisionUpdated:  subscribers.NewAtomRevisionUpdatedKGSubscriber(ext.KGHexagons, ext.KGEvents),
		UserRetentionShifted: subscribers.NewUserRetentionShiftedKGSubscriber(ext.KGHexagons, ext.KGEvents),
	}
	go func() {
		log.Printf("consumption: KG fog-invalidation subscribers binding atom.published + atom.updated + user_retention.shifted (KG fog-cache invalidation)")
		handler := subscribers.KGInvalidationHandler(deps.AtomPublished, deps.AtomRevisionUpdated, deps.UserRetentionShifted)
		for _, subject := range []string{subscribers.TopicCreationAtomPublished, events.TopicAtomRevisionUpdated, events.TopicUserRetentionShifted} {
			if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.kg-invalidation", subject), handler); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ERROR consumption: kg-invalidation subscriber exited: %v", err)
			}
		}
	}()
}
