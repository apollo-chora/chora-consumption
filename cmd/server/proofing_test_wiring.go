// proofing_test_wiring.go — composition root for the CHO-2040 (R8-6/R8-7)
// Virgin Proofing Test COMPOSED runner
// (POST /v1/me/companions/{id}/proofing-test + GET /v1/me/proofing-tests +
// the /api/internal/pubsub/proofing-test-terminal inbox).
//
// Attaches the runner's ports to the Phyllis Server:
//
//   - ProofingTests — the pg ProofingTest aggregate repo (RLS-bound).
//   - ProofingTickedEdges — READ-ONLY composed view over the SAME pg
//     ConceptNode + Edge repos the concept-graph surfaces use (subtree ∩
//     intent-tagged; never a write path — the KG session owns writes).
//   - ProofingMana — reserve→settle/refund over the boot-constructed
//     chora-identity ManaClient (the ONLY ManaQuoter implementation that
//     carries the CreditMana refund lane; a fake/nil quoter leaves the
//     runner honestly unwired).
//   - ProofingPublisher — the SAME outbox publisher instance srv.Publisher
//     carries in production (durable emission; the started.v2 topic is the
//     outbox's ONE allowlisted cross-domain request lane), re-labelled with
//     the proofing_test aggregate type for audit rows.
//   - ProofingTerminalPushHandler — the ai_assist.{completed,refused}.v1
//     inbox that advances requested → ready|failed (+ refund on refusal).
//
// Any port left nil ⇒ the runner refuses per-request with 503
// PROOFING_TEST_NOT_WIRED (fail-soft at boot, fail-loud per request — the
// EDGE_SCOUT_NOT_WIRED pattern).
//
// ⚠ Deploy-time (NOT applied from code):
//   - the two Pub/Sub push SUBSCRIPTIONS on chora.creation.ai_assist
//     .{completed,refused}.v1 targeting
//     https://api.chora.site/api/internal/pubsub/proofing-test-terminal DO
//     NOT EXIST yet — provision OOB (gcloud; same-project OIDC push SA per
//     [[project_pubsub_push_crossproject_oidc_block_tf_drift_2026_06_29]]).
//     Until then rows stay honestly `requested`.
//   - chora-consumption's publisher SA needs roles/pubsub.publisher on the
//     chora.creation.ai_assist.started.v2 topic + pubsub.viewer on its
//     compose-v2 schema (terraform grants content-team SAs schema-viewer
//     already; verify the topic-level publisher binding).
//   - the new /v1/me/proofing-tests + companions/{id}/proofing-test routes
//     need the gateway 3-list sync ([[project_gateway_route_three_list_sync]]).
package main

import (
	"errors"
	"context"

	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireProofingTest attaches the proofing-test runner's ports. outboxStore is
// the SAME durable store the main outbox publisher writes (nil when the
// outbox is not bootstrapped — the runner then stays unwired rather than
// publishing into a void).
func wireProofingTest(ctx context.Context, srv *httpadapter.Server, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: proofing-test runner NOT wired (no pgx pool); POST /v1/me/companions/{id}/proofing-test → 503 PROOFING_TEST_NOT_WIRED")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	repo := pg.NewProofingTestRepo(tx)
	srv.ProofingTests = repo
	srv.ProofingTickedEdges = pg.NewProofingTickedEdgeReader(
		pg.NewConceptNodeRepo(tx), pg.NewEdgeRepo(tx))

	// Mana: the reserve→refund lane needs the CONCRETE ManaClient (CreditMana
	// is deliberately not on the companion.ManaQuoter port). A nil/fake quoter
	// (identity unreachable at boot) leaves the runner honestly unwired.
	mc, ok := srv.ManaQuoter.(*clients.ManaClient)
	if !ok || mc == nil {
		log.Printf("consumption: proofing-test mana NOT wired (ManaQuoter is %T — need the chora-identity gRPC ManaClient; set CHORA_IDENTITY_MANA_GRPC_URL); runner → 503 PROOFING_TEST_NOT_WIRED", srv.ManaQuoter)
		return
	}
	srv.ProofingMana = clients.NewProofingManaReserver(mc)

	// Publisher: a DEDICATED outbox publisher instance over the SAME durable
	// store, labelled with the proofing_test aggregate type (audit rows key
	// on the assist_id). nil store ⇒ unwired (fail-loud per request).
	if outboxStore == nil {
		log.Printf("consumption: proofing-test publisher NOT wired (outbox store not bootstrapped); runner → 503 PROOFING_TEST_NOT_WIRED")
		return
	}
	srv.ProofingPublisher = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
		Store:         outboxStore,
		AggregateType: "proofing_test",
	})

	// Terminal inbox — ai_assist.{completed,refused}.v1 → ready|failed(+refund).
	if bus == nil {
		log.Printf("consumption: proofing-test terminal NOT wired (no event bus, NATS_URL unset)")
		return
	}
	sub := subscribers.NewProofingTestTerminalSubscriber(repo, srv.ProofingMana)
	// WS-C3 (CHO-2082): the campaign question-set bridge rides the SAME
	// shared terminal subscription — both bridges run per event; the
	// non-owner of an assist_id ack-skips (each is redelivery-idempotent).
	campaignBridge := subscribers.NewCampaignQuestionTerminalSubscriber(pg.NewCampaignQuestionSetRepo(tx))
	go func() {
		log.Printf("consumption: proofing-test terminal subscribers binding ai_assist.{completed,refused}.v1 (CHO-2040 R8-6 + CHO-2082 campaign bridge): POST /v1/me/companions/{id}/proofing-test + GET /v1/me/proofing-tests")
		handler := subscribers.ProofingTestTerminalHandler(sub, campaignBridge)
		for _, subject := range []string{subscribers.TopicAiAssistCompletedV1, subscribers.TopicAiAssistRefusedV1} {
			if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.proofing-test-terminal", subject), handler); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ERROR consumption: proofing-test-terminal subscriber exited: %v", err)
			}
		}
	}()
}
