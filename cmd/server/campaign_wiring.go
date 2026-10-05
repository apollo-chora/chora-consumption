// campaign_wiring.go - composition root for the ADR-227 Companion Campaign
// goal doors (WS-C1, CHO-2080): POST /v1/me/goals/{id}/campaign/{focus|seal}.
//
// Attaches the pg campaign ladder repo (frontier walk), the growth repo as
// the narrow resonance-reconcile surface (addendum #5), and a
// campaign-typed outbox publisher behind the CampaignEmitter (every
// campaign.* event carries goal_id per addendum #6; aggregate_id derives
// from it for audit/replay). Nil pool/store ⇒ the doors return 503
// (fail-soft at boot, fail-loud per request) - the wireGoalRepo pattern.
//
// The WS-C2/C3 grading callers construct campaign.NewGrader over the same
// repos + emitter when they land; cmd/server resolves
// CAMPAIGN_RUNG_CLEAR_CORRECT there. This file resolves the seal-side
// tunable CAMPAIGN_RESEAL_MIN_NEW_WINS (cmd/server is the ONLY env-reading
// layer per feedback_no_inline_config).
package main

import (
	"context"

	"github.com/apollo-chora/chora-common/eventbus"
	"github.com/apollo-chora/chora-consumption/internal/domain/clusterprojection"
	"log"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain/campaign"
)

// wireCampaign attaches the campaign doors + the WS-C2 campaign-dose spine.
// MUST follow wireGoalRepo (the doors 503 without ext.Goals), the outbox
// bootstrap (outboxStore), wireTopicRetentionRepo (pg ext.Retention),
// wireConceptGraphReRoot (ext.Concepts) and wireDoseKGPrefs (srv.DoseKGPrefs).
func wireCampaign(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: campaign doors NOT wired (no pgx pool); /v1/me/goals/{id}/campaign/* → 503")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	ext.CampaignProgress = pg.NewCampaignProgressRepo(tx)
	ext.CampaignResonance = pg.NewGrowthRepo(tx)

	pub := ext.Publisher // in-memory fallback when the outbox store is absent (dev)
	if outboxStore != nil {
		pub = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "campaign",
		})
	} else {
		log.Printf("consumption: campaign events ride the in-memory publisher (CHORA_OUTBOX_DSN unset; NOT durable)")
	}
	emitter := events.NewCampaignEmitter(pub, nil)
	ext.CampaignEvents = emitter

	// ADR-244 D4 (CHO-2303): the atom-to-concept binding becomes a domain
	// event. Its own outbox aggregate_type ("concept") so a concept replay
	// never drags campaign rows along. Same durability posture as above: the
	// in-memory publisher is a DEV fallback and says so loudly.
	conceptPub := ext.Publisher
	if outboxStore != nil {
		conceptPub = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "concept",
		})
	} else {
		log.Printf("consumption: concept.atoms_bound rides the in-memory publisher (CHORA_OUTBOX_DSN unset; NOT durable)")
	}
	conceptEmitter := events.NewConceptEmitter(conceptPub, nil)
	ext.ConceptEvents = conceptEmitter

	// ADR-244 D4: the cluster projection mints a root carrying the cluster's
	// seed atom, which is a binding. Attached HERE rather than in kg_wiring
	// because wireUserKGPg builds the projector long BEFORE ConceptEvents
	// exists, so wiring it there would silently attach nothing.
	if p, ok := ext.ClusterProjector.(*clusterprojection.Projector); ok && p != nil {
		p.WithBindingSink(events.NewClusterProjectionBindingSink(conceptEmitter))
	} else {
		log.Printf("consumption: cluster-projection binding events NOT wired (projector absent or unexpected type) - projections will not emit atoms_bound")
	}

	if v := os.Getenv("CAMPAIGN_RESEAL_MIN_NEW_WINS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			log.Printf("consumption: CAMPAIGN_RESEAL_MIN_NEW_WINS=%q invalid (want int >= 1); keeping domain default", v)
		} else {
			ext.CampaignResealMinNewWins = n
		}
	}
	log.Printf("consumption: campaign doors wired (focus+seal): POST /v1/me/goals/{id}/campaign/{focus|seal}")

	wireCampaignDose(srv, ext, emitter, tx, outboxStore)

	// WS-C4 (CHO-2083) - the free-on-win reveal consumer rides the same boot
	// ordering (pool + outboxStore in scope; goals/concepts repos are
	// constructed fresh off the pool inside).
	wireCampaignReveal(ctx, srv, ext, pool, outboxStore, ext.Publisher, bus)
}

// wireCampaignDose builds the ADR-227 D12 campaign-dose spine (WS-C2,
// CHO-2081): ONE campaign.Grader over the ladder + retention + verified
// events, and ONE CampaignDose handed (same pointer) to BOTH servers so the
// dose compose side and the answers-door fold elect the same daily march.
// WS-C3 (CHO-2082) adds the question lane: bank repo + level-matched
// retrieval (reusing the drillcache ContentRetrieval client + the LW
// embedder) + a dedicated campaign_question outbox publisher for the ≤2/day
// qgen gap requests (mana-exempt; terminal events chain through the
// proofing push handler). Every lane member is individually optional -
// fail-soft at boot with a loud log; the dose then composes without that
// lane. cmd/server resolves CAMPAIGN_RUNG_CLEAR_CORRECT here (the ONLY env
// layer).
func wireCampaignDose(srv *httpadapter.Server, ext *httpadapter.ExtServer, emitter *events.CampaignEmitter, tx pg.TxRunner, outboxStore consumptionoutbox.Store) {
	if ext.Goals == nil || ext.Concepts == nil || ext.Retention == nil {
		log.Printf("consumption: campaign dose axis NOT wired (goals/concepts/retention absent); dose composes without a campaign slot")
		return
	}

	rungClear := 0
	if v := os.Getenv("CAMPAIGN_RUNG_CLEAR_CORRECT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			log.Printf("consumption: CAMPAIGN_RUNG_CLEAR_CORRECT=%q invalid (want int >= 1); keeping domain default", v)
		} else {
			rungClear = n
		}
	}

	grader, err := campaign.NewGrader(campaign.GraderConfig{
		Progress:         ext.CampaignProgress,
		Retention:        ext.Retention,
		Events:           emitter,
		RungClearCorrect: rungClear,
	})
	if err != nil {
		log.Printf("consumption: campaign dose axis NOT wired (grader: %v)", err)
		return
	}

	dose := &httpadapter.CampaignDose{
		Goals:     ext.Goals,
		Prefs:     srv.DoseKGPrefs, // nil ⇒ no per-map exclusions (dev)
		Progress:  ext.CampaignProgress,
		Retention: ext.Retention,
		Concepts:  ext.Concepts,
		Grader:    grader,
		// ADR-247 Cap A: enrich the qgen prompt with the node's diagnosed
		// weakness (F2, narrow WeaknessContextLoader) + goal/ancestor lineage (F3).
		Weakness: pg.NewLearnerWeaknessRepo(tx),
		Edges:    ext.ConceptEdges,
	}

	// WS-C3 (CHO-2082) - the retrieval-first question lane.
	bank := pg.NewCampaignQuestionSetRepo(tx)
	dose.Bank = bank
	ext.CampaignQuestionBank = bank
	// B6 item 3: the same repo answers the remaining-taps read. Bound from the
	// SAME object deliberately, so the budget the card renders and the count
	// the enforcement path checks can never come from different places.
	ext.PracticeBudget = bank
	if contentRetrievalClient != nil {
		dose.Searcher = contentRetrievalClient
	} else {
		log.Printf("consumption: campaign retrieval lane OFF (ContentRetrieval client absent - SVC_CREATION_GRPC_URL)")
	}
	if srv.Embedder != nil {
		dose.Embedder = clients.NewLearnerWeaknessEmbedder(srv.Embedder)
	} else {
		log.Printf("consumption: campaign retrieval lane OFF (embedder absent - CHORA_MODEL_GATEWAY_GRPC_URL)")
	}
	if outboxStore != nil {
		dose.QGen = consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "campaign_question",
		})
	} else {
		log.Printf("consumption: campaign qgen gap lane OFF (outbox store not bootstrapped)")
	}

	srv.CampaignDose = dose
	ext.CampaignDose = dose
	log.Printf("consumption: campaign dose axis wired (ADR-227 D12+D13): dose campaign slot + answers-door ladder fold + question lane (bank/retrieval/qgen)")
}
