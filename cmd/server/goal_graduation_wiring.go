// goal_graduation_wiring.go — composition root for §3 Goal graduation
// (CHO-1962, ADR-204 §9). Mounts the /api/internal/pubsub/weakness-grown-goals
// push inbox: chora-consumption consumes its OWN chora.consumption.weakness.
// grown.v1 and reconciles the learner's Goals (graduate at 100% / progress
// below).
//
// MUST run AFTER wireGoalRepo (ext.Goals) and wireGrowthEdgeReadRepo
// (ext.LearnerWeakness). outboxStore is the SAME durable store the dispatcher
// drains; a dedicated "goal" aggregate publisher labels the emitted goal.* rows
// correctly (rather than reusing the "learner_weakness" publisher). Per
// secrets-and-env the OIDC verifier is wired from env here.
package main

import (
	"errors"
	"context"

	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	consumptionoutbox "github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireGoalGraduation attaches the Goal-graduation push inbox. Fail-soft at boot
// (log + skip) when prerequisites are missing — exactly like the sibling wiring.
func wireGoalGraduation(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, outboxStore consumptionoutbox.Store, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: goal-graduation push NOT wired (no pgx pool)")
		return
	}
	if ext.Goals == nil {
		log.Printf("consumption: goal-graduation push NOT wired (goals repo absent — run wireGoalRepo first)")
		return
	}
	if ext.LearnerWeakness == nil {
		log.Printf("consumption: goal-graduation push NOT wired (LearnerWeakness read repo absent)")
		return
	}

	sub := subscribers.NewGoalGraduationSubscriber(ext.Goals, ext.LearnerWeakness)
	if outboxStore != nil {
		sub = sub.WithPublisher(consumptionoutbox.NewPublisher(consumptionoutbox.PublisherConfig{
			Store:         outboxStore,
			AggregateType: "goal",
		}))
	} else {
		log.Printf("consumption: goal-graduation reward events DISABLED (no outbox store) — graduation still persists")
	}

	if bus == nil {
		log.Printf("consumption: goal-graduation NOT wired (no event bus, NATS_URL unset)")
		return
	}
	go func() {
		log.Printf("consumption: goal-graduation subscriber binding chora.consumption.weakness.grown.v1 (§3 Goal graduation; goals=pg, mastered=pg)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.goal-graduation", subscribers.TopicWeaknessGrown), subscribers.GoalGraduationHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: goal-graduation subscriber exited: %v", err)
		}
	}()
}
