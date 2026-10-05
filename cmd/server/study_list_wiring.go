// study_list_wiring.go — composition root for the collection→study-list inbound
// adapter (ADR-233 / spec-001 US5, WS-4).
//
// Mounts POST /api/internal/pubsub/collection-converted-to-study-list, fronting
// CollectionConvertedToStudyListSubscriber so chora-creation's
// `chora.creation.collection.converted_to_study_list.v1` derives a SPACED,
// collection-provenanced LearningPath — and emits learning_path.bootstrapped.v1
// so the list actually reaches the daily dose's curiosity slot.
//
// ORDERING (load-bearing, do not move this call earlier):
//
//	main.go:272 → ext.Publisher swapped to the DURABLE outbox publisher
//	main.go:424 → ext.Paths swapped to the pg-backed LearningPath repo
//	main.go:549 → wireSubscriberPushHandlers
//	           →  wireCollectionConvertedToStudyListPushHandler   ← HERE
//
// Wiring this BEFORE the publisher swap would bind the default IN-MEMORY
// publisher, so `learning_path.bootstrapped.v1` would be written to a buffer no
// dispatcher drains: the study list would be built but never reach
// active_path_topics → never reach the dose → INERT. Wiring it before the repo
// swap would bind the in-memory path repo, losing every study list on pod
// restart. Both failures are silent — hence this note.
//
// Per secrets-and-env: no inline config. The OIDC verifier reads the same env
// vars every sibling push handler uses (CHORA_PUBSUB_PUSH_AUDIENCE +
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

// wireCollectionConvertedToStudyListPushHandler attaches the WS-4 inbound
// adapter. Fail-soft at boot (the route simply is not registered, and the
// operator sees a loud log line) — never a silent stub that ACKs events into a
// void.
func wireCollectionConvertedToStudyList(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, bus eventbus.Bus) {
	if srv == nil || ext == nil {
		return
	}
	if ext.Paths == nil {
		log.Printf("consumption: collection-converted-to-study-list push NOT wired (no LearningPath repo); " +
			"POST /api/internal/pubsub/collection-converted-to-study-list unregistered")
		return
	}
	if ext.Publisher == nil {
		// Refuse rather than mount a handler that would build the path but never
		// emit learning_path.bootstrapped.v1 — an INERT study list is worse than
		// an unmounted route, because it looks like success.
		log.Printf("consumption: collection-converted-to-study-list push NOT wired (no Publisher); " +
			"a study list without learning_path.bootstrapped.v1 never reaches the dose's curiosity slot")
		return
	}

	sub := subscribers.NewCollectionConvertedToStudyListSubscriber(ext.Paths, ext.Publisher)
	if bus == nil {
		log.Printf("consumption: collection-converted-to-study-list NOT wired (no event bus, NATS_URL unset)")
		return
	}
	go func() {
		log.Printf("consumption: collection-converted-to-study-list subscriber binding chora.creation.collection.converted_to_study_list.v1 (spaced study-list LearningPath + learning_path.bootstrapped.v1 → curiosity slot; ADR-233 WS-4)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.collection-converted-to-study-list", subscribers.TopicCollectionConvertedToStudyList), subscribers.CollectionConvertedToStudyListHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: collection-converted-to-study-list subscriber exited: %v", err)
		}
	}()
}
