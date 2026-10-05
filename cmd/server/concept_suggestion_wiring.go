// concept_suggestion_wiring.go — composition root for the Companion suggestion
// curation API (ADR-212 WS-4). Attaches the RLS-bound pg Suggestion repo
// (satisfying both SuggestionRepository and SuggestionAccepter) to the
// ExtServer so GET /v1/me/concept-graph/suggestions + POST .../{id}/accept|dismiss
// are live. Nil pool ⇒ the routes return 503 (fail-soft at boot, fail-loud per
// request) — the same pattern as wireConceptGraphReRoot. Per secrets-and-env
// this file reads no env directly; the pgx pool is built once in main.
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
)

// wireConceptSuggestions attaches the pg suggestion repo (repo + accepter) to
// the ExtServer.
func wireConceptSuggestions(ext *httpadapter.ExtServer, pool *pgxpool.Pool) {
	if pool == nil {
		log.Printf("consumption: concept suggestions NOT wired (no pgx pool); /v1/me/concept-graph/suggestions* → 503")
		return
	}
	repo := pg.NewSuggestionRepo(pg.NewPgxTxRunner(pool))
	ext.Suggestions = repo
	ext.SuggestionAccept = repo
	log.Printf("consumption: concept suggestions wired (ADR-212 WS-4): GET /v1/me/concept-graph/suggestions + POST .../{id}/accept|dismiss")
}

// wireConceptSuggestionEmittedPushHandler attaches the emitted.v1 inbound adapter
// so /api/internal/pubsub/concept-suggested lands registered on Routes(),
// persisting the concept-shaped fog orchestrator's proposed concepts/edges as
// pending Suggestions. Nil pool ⇒ skipped. Per secrets-and-env the OIDC verifier
// is wired from the same env vars the other push handlers use.
func wireConceptSuggestionEmitted(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: concept-suggestion-emitted push NOT wired (no pgx pool)")
		return
	}
	repo := pg.NewSuggestionRepo(pg.NewPgxTxRunner(pool))
	sub := subscribers.NewConceptSuggestionEmittedSubscriber(repo)
	if bus == nil {
		log.Printf("consumption: concept-suggestion-emitted NOT wired (no event bus, NATS_URL unset)")
		return
	}
	go func() {
		log.Printf("consumption: concept-suggestion-emitted subscriber binding chora.consumption.concept_suggestion.emitted.v1 (ADR-212 WS-4 suggestion ingest; repo=pg)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.concept-suggestion-emitted", subscribers.TopicConceptSuggestionEmitted), subscribers.ConceptSuggestionEmittedHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: concept-suggestion-emitted subscriber exited: %v", err)
		}
	}()
}
