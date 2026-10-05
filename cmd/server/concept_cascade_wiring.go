// concept_cascade_wiring.go — composition root for the concept-delete → edge
// cleanup cascade (CHO-2324).
//
// Mounts POST /api/internal/pubsub/concept-deleted, fronting
// ConceptDeletedSubscriber so chora-consumption's own
// `chora.consumption.concept.deleted.v1` soft-deletes every concept_edges row
// incident to the removed concept — a removed concept leaves no dangling relations.
//
// Fail-soft at boot: with no Edge repo the route is simply unregistered and the
// operator sees a loud log line — never a silent stub that ACKs delete events into
// a void while orphaned edges pile up.
//
// Per secrets-and-env: no inline config. The OIDC verifier reads the same env vars
// every sibling push handler uses (CHORA_PUBSUB_PUSH_AUDIENCE +
// CHORA_PUBSUB_PUSH_TRUSTED_SA).
package main

import (
	"errors"
	"context"

	"log"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireConceptDeletedPushHandler attaches the CHO-2324 inbound cascade adapter.
func wireConceptDeleted(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, bus eventbus.Bus) {
	if srv == nil || ext == nil {
		return
	}
	if ext.ConceptEdges == nil {
		log.Printf("consumption: concept-deleted push NOT wired (no Edge repo); " +
			"POST /api/internal/pubsub/concept-deleted unregistered — orphaned edges will NOT be cleaned")
		return
	}

	sub := subscribers.NewConceptDeletedSubscriber(ext.ConceptEdges)
	if bus == nil {
		log.Printf("consumption: concept-deleted NOT wired (no event bus, NATS_URL unset)")
		return
	}
	go func() {
		log.Printf("consumption: concept-deleted subscriber binding chora.consumption.concept.deleted.v1 (cascade soft-delete incident edges; CHO-2324)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.concept-deleted", subscribers.TopicConceptDeleted), subscribers.ConceptDeletedHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: concept-deleted subscriber exited: %v", err)
		}
	}()
}
