// companion_suspension_wiring.go: bind the ADR-252 / ADR-254 D11 ADVISORY
// Companion-containment projection.
//
// chora-observability owns the suspension tables and the operator write path;
// chora-model-gateway READS them uncached on every companion turn and is the
// CONTROL (deny-before-debit, fail-closed, ADR-252 D1 / D6). Consumption cannot
// read those tables (they live in chora_observability and cross-DB is
// forbidden), so it projects the audit event
// chora.governance.audit.companion_suspension_changed.v1 into a small local
// table and uses it ONLY to be honest: a 403 COMPANION_SUSPENDED before any
// mana pre-check or SSE header, the ADR-252 Q5 reflection skip (no claim, no
// publish), and companion_status on the profile.
//
// Env (read only here): CHORA_COMPANION_SUSPENSION_SUBSCRIPTION, default
// chora-consumption.governance-audit-companion-suspension-changed (pull, ack
// 60, provisioned in companion_topic_estate.tf).
package main

import (
	"context"
	"errors"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	consumptionpg "github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// newCompanionSuspensionProjection picks the durable projection when a pool is
// available and the in-memory one otherwise. It never returns nil: a nil
// advisor renders no companion_status and drops the honest refusal silently,
// which is worse than an advisory read that starts empty and fills from the
// subscription.
func newCompanionSuspensionProjection(pool *pgxpool.Pool) companion.SuspensionProjection {
	if pool != nil {
		return consumptionpg.NewCompanionSuspensionProjectionRepo(consumptionpg.NewPgxTxRunner(pool))
	}
	return inmem.NewCompanionSuspensionProjection()
}

// wireCompanionSuspension puts ONE projection behind both read surfaces (the
// chat turn on Server, the ADR-235 reflection read on ExtServer) and binds the
// pull projector that keeps it current.
//
// The two degraded modes are announced, never silent:
//   - no pg pool: the projection is in-memory, so a pod restart forgets every
//     containment until the operator changes one again;
//   - no event bus: the projection can never change, so it will report
//     "not paused" forever.
//
// Both are UX degradations only. The gateway still refuses every contained turn
// (ADR-252 D1), which is exactly why this side may be advisory.
func wireCompanionSuspension(
	ctx context.Context,
	srv *httpadapter.Server,
	ext *httpadapter.ExtServer,
	pool *pgxpool.Pool,
	bus eventbus.Bus,
) {
	proj := newCompanionSuspensionProjection(pool)
	if srv != nil {
		srv.CompanionSuspension = proj
	}
	if ext != nil {
		ext.CompanionSuspension = proj
	}
	if pool == nil {
		log.Printf("consumption: companion suspension projection is IN-MEMORY (no pg pool): every containment is forgotten on restart until re-emitted; chora-model-gateway remains the control")
	}
	if bus == nil {
		log.Printf("ERROR consumption: companion suspension projector NOT wired (no event bus): the advisory pause state can never change and will read as not-paused; chora-model-gateway remains the control")
		return
	}

	projector := subscribers.NewCompanionSuspensionProjector(proj)
	sub := subscribers.CompanionSuspensionSubscriptionName()
	go func() {
		log.Printf("consumption: companion suspension projector binding %s -> %s (pull)", sub, subscribers.TopicCompanionSuspensionChanged)
		if err := bus.Subscribe(ctx, consumerConfig(sub, subscribers.TopicCompanionSuspensionChanged), projector.PullHandler()); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: companion suspension projector exited: %v", err)
		}
	}()
}
