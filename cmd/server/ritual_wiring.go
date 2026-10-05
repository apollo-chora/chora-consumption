// ritual_wiring.go — composition root for CHO-2016 Grimoire Rituals v1
// (ADR-219). Attaches the designer + runner ports to the Phyllis Server:
//
//   - Rituals / RitualRuns — pg aggregate + run repos (RLS-bound; migration 0071).
//   - RitualCaps — capability resolver over srv.Loadouts (equipped-active +
//     craft) + srv.Growth (stage) + srv.SkillCatalog + the rituals repo.
//   - RitualPublisher — publish/enable over the repo + caps + a DEDICATED outbox
//     publisher (aggregate_type=ritual) on the SAME durable store.
//   - RitualRunner — the deterministic runner. Its RUN lane needs the CONCRETE
//     ManaClient (CreditMana refund lane, not on the companion.ManaQuoter port)
//   - the companion engine; either missing ⇒ the designer stays wired but RUN
//     returns 503 RITUALS_RUN_NOT_WIRED (fail-loud, never a fake run).
//
// ⚠ Deploy-time (NOT applied from code):
//   - the new /v1/me/companions/{id}/rituals* routes need the gateway
//     CompanionBridge sync (4 aggregator methods + handleCompanionSubpath cases).
//   - Cloud Armor rule 994 is CONDITIONAL — extend only if a rituals POST edge
//     live-verify 403s (GET never needs it).
//   - the per-step gateway turn (Armor + real model output) + per-turn allowlist
//     NARROWING (companion_chat_adk_go ToolFilter) are deploy-verified; v1 forwards
//     the narrowed allowlist advisorily.
//   - only the CHAT sink is wired here; memory_note / suggestion_inbox /
//     question_bank / notification / calendar_artifact register their subsystem
//     writers as they land (an unwired sink fails loud → the run refunds).
package main

import (
	"context"
	"errors"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// wireRituals attaches the Rituals designer + runner ports. outboxStore is the
// SAME durable store the main outbox publisher writes.
func wireRituals(srv *httpadapter.Server, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store) {
	if pool == nil {
		log.Printf("consumption: rituals NOT wired (no pgx pool); /v1/me/companions/{id}/rituals → 503 RITUALS_NOT_WIRED")
		return
	}
	tx := pg.NewPgxTxRunner(pool)
	ritualRepo := pg.NewCompanionRitualRepo(tx)
	runRepo := pg.NewCompanionRitualRunRepo(tx)
	srv.Rituals = ritualRepo
	srv.RitualRuns = runRepo

	if outboxStore == nil {
		log.Printf("consumption: rituals publisher NOT wired (outbox store not bootstrapped); rituals → 503 RITUALS_NOT_WIRED")
		return
	}
	ritualOutbox := consumptionoutbox.NewRitualOutbox(consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
		Store:         outboxStore,
		AggregateType: "ritual",
	}))

	if srv.Loadouts == nil || srv.SkillCatalog == nil || srv.Growth == nil {
		log.Printf("consumption: rituals caps NOT wired (loadouts/catalog/growth missing — run growth_wiring first); rituals → 503 RITUALS_NOT_WIRED")
		return
	}
	caps := clients.NewRitualCapabilityResolver(
		srv.Loadouts,
		ritualStageReader{read: srv.Growth.GetCompanionGrowth},
		srv.SkillCatalog,
		ritualRepo,
	)
	srv.RitualCaps = caps

	// ONE sink router serves both lanes (N5): the publisher refuses to publish a
	// sink this deployment cannot write, and the runner writes through the same
	// map, so the publish gate and the run can never disagree about what is
	// wired. Built here, above the publisher, and reused by the runner below.
	sinkRouter := clients.NewRitualSinkRouter(nil) // chat wired; other sinks register as they land

	pub, err := companion.NewRitualPublisher(companion.RitualPublisherConfig{
		Repo: ritualRepo, Caps: caps, Outbox: ritualOutbox, Sinks: sinkRouter,
		// CHO-2225: the envelope event_id must be a UUIDv7.
		NewID: domain.NewUUIDv7,
	})
	if err != nil {
		log.Printf("consumption: rituals publisher build failed: %v — rituals → 503 RITUALS_NOT_WIRED", err)
		return
	}
	srv.RitualPublisher = pub
	// Serve the same registry the publish gate reads, so the composer greys
	// from the deployment's own answer (B3b) instead of a client-side guess.
	srv.RitualSinks = sinkRouter
	// State what this deployment can actually write, not what the schema
	// permits: five of the six closed sinks have no writer today, and a ritual
	// declaring one is now refused at publish rather than failing every run.
	log.Printf("consumption: rituals DESIGNER wired (CHO-2016): GET/POST /v1/me/companions/{id}/rituals + publish; wired sinks %v (unwired → 422 RITUAL_SINK_NOT_WIRED)", sinkRouter.WiredSinks())

	// RUN lane — the concrete ManaClient (refund) + the companion engine.
	mc, ok := srv.ManaQuoter.(*clients.ManaClient)
	if !ok || mc == nil {
		log.Printf("consumption: rituals RUN NOT wired (ManaQuoter is %T — need the chora-identity gRPC ManaClient; set CHORA_IDENTITY_MANA_GRPC_URL); run → 503 RITUALS_RUN_NOT_WIRED", srv.ManaQuoter)
		return
	}
	if srv.CompanionEngine == nil {
		log.Printf("consumption: rituals RUN NOT wired (CompanionEngine unset); run → 503 RITUALS_RUN_NOT_WIRED")
		return
	}
	stepTurn := clients.NewCompanionEngineStepTurn(
		srv.CompanionEngine,
		ritualStepCtxResolver{resolveConfig: srv.CompanionConfigJSONResolver},
	)
	runner, err := companion.NewRitualRunner(companion.RitualRunnerConfig{
		Steps:     clients.NewRitualStepExecutor(stepTurn),
		Sink:      sinkRouter, // same registry the publish gate reads (N5)
		Mana:      clients.NewRitualManaReserver(mc),
		Runs:      runRepo,
		Outbox:    ritualOutbox,
		Catalogue: srv.SkillCatalog,
	})
	if err != nil {
		log.Printf("consumption: rituals runner build failed: %v — run → 503 RITUALS_RUN_NOT_WIRED", err)
		return
	}
	srv.RitualRunner = runner
	log.Printf("consumption: rituals RUN lane wired (CHO-2016): POST /v1/me/companions/{id}/rituals/{rid}/run [engine turn deploy-verified]")
}

// ritualStageReader adapts srv.Growth.GetCompanionGrowth to the capability
// resolver's CompanionStageReader (stage from the ADR-149 growth axis).
type ritualStageReader struct {
	read func(ctx context.Context, tenantID, companionID, callerGCID string) (*growth.State, error)
}

func (r ritualStageReader) StageForCompanion(ctx context.Context, tenantID, companionID, ownerGCID string) (int, error) {
	st, err := r.read(ctx, tenantID, companionID, ownerGCID)
	if err != nil {
		return 0, err
	}
	if st == nil {
		return 0, errors.New("ritual stage: nil growth state") // fail-loud (never default stage 0)
	}
	return st.GrowthStage, nil
}

// ritualStepCtxResolver resolves the per-step engine context (config JSON via
// the existing resolver; mana tier defaults to the safe basic floor — precise
// per-user tier resolution is a deploy-time refinement, and a ritual step is
// not per-step debited anyway).
type ritualStepCtxResolver struct {
	resolveConfig func(ctx context.Context, tenantID, companionID, callerGCID string) (string, error)
}

func (r ritualStepCtxResolver) ResolveRitualStepContext(ctx context.Context, tenantID, companionID, gcid string) (clients.RitualStepTurnContext, error) {
	cfg := ""
	if r.resolveConfig != nil {
		c, err := r.resolveConfig(ctx, tenantID, companionID, gcid)
		if err != nil {
			return clients.RitualStepTurnContext{}, err
		}
		cfg = c
	}
	return clients.RitualStepTurnContext{ManaTier: growth.DefaultManaTier, CompanionConfigJSON: cfg}, nil
}
