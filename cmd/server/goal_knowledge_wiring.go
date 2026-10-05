// goal_knowledge_wiring.go — composition root for the CHO-2118 goal-knowledge
// cache invalidation lane (sub-phase B). It attaches BOTH halves of the
// invalidation set, which share one invalidator so the fan-out rules have a
// single implementation:
//
//  1. the six-topic push inbox (/api/internal/pubsub/goal-knowledge-invalidation)
//     — weakness.analyzed/grown · goal.progress_updated/graduated ·
//     companion.chat_turn_completed/memory_eviction;
//  2. ext.GoalKnowledge — the in-handler goal RE-ROOT invalidation, which has no
//     event behind it (goal CRUD emits nothing, ADR-214).
//
// MUST run AFTER wireGoalRepo: the concept→goal resolution reads ext.Goals.
//
// Per secrets-and-env the OIDC verifier is wired from env here.
package main

import (
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"errors"
	"context"

	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// wireGoalKnowledgeInvalidation attaches the goal-knowledge invalidation lane.
// Fail-soft at boot (log + skip) when a prerequisite is missing — exactly like
// the sibling wirings. MUST run AFTER wireGoalRepo (it needs ext.Goals to
// resolve concept → affected goals).
func wireGoalKnowledgeInvalidation(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: goal-knowledge invalidation NOT wired (no pgx pool) — cached reflections will only refresh on max-age")
		return
	}
	if ext.Goals == nil {
		log.Printf("consumption: goal-knowledge invalidation NOT wired (goals repo absent — run wireGoalRepo first)")
		return
	}
	cache := pg.NewGoalKnowledgeRepo(pg.NewPgxTxRunner(pool))

	// ONE invalidator serves both entry points: the six events and the in-handler
	// re-root. Two instances would be two copies of the fan-out rules.
	inv := subscribers.NewGoalKnowledgeInvalidator(cache, ext.Goals)

	// (2) in-handler re-root (ADR-214) — the goal PATCH door.
	ext.GoalKnowledge = inv

	// (1) the six-topic push inbox.
	if bus == nil {
		log.Printf("consumption: goal-knowledge invalidation NOT wired (no event bus, NATS_URL unset)")
		return
	}
	sub := subscribers.NewGoalKnowledgeInvalidationSubscriber(inv)
	go func() {
		log.Printf("consumption: goal-knowledge invalidation subscriber binding the six invalidation topics (CHO-2118) + in-handler goal re-root invalidation")
		for _, subject := range []string{
			subscribers.TopicWeaknessGrown,
			subscribers.TopicGKWeaknessAnalyzed,
			subscribers.TopicGKGoalProgressUpdated,
			subscribers.TopicGKGoalGraduated,
			subscribers.TopicGKChatTurnCompleted,
			subscribers.TopicGKCompanionMemoryEvicts,
		} {
			if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.goal-knowledge-invalidation", subject), subscribers.GoalKnowledgeInvalidationHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("ERROR consumption: goal-knowledge-invalidation subscriber exited: %v", err)
			}
		}
	}()
}

// wireGoalKnowledgeCompletion attaches the COMPLETION inbox — the fog
// orchestrator's goal_knowledge.synthesized.v1, drift-guarded before it is
// cached. MUST run AFTER wireGoalRepo (the root guard reads ext.Goals).
//
// `views` re-assembles the tier-1 view as it stands now; its ContentHash is the
// comparand for the content-hash drift guard. It is the READ PATH's goal-scoped
// view service (CHO-2116) — deliberately NOT re-implemented here, because the
// echoed hash is only meaningful against the SAME assembly the request was built
// from (companionmind.AssembleGoalScopedView is composed once so its two callers
// cannot drift; a second gatherer that ordered memories differently would
// mismatch every hash and silently refuse every synthesis forever).
//
// ⚠ Until the read path lands and passes its assembler here, this lane is DARK
// and says so at boot: completions would not be cached. Nothing is faked — a nil
// assembler means the drift guard cannot run, and writing an UNGUARDED synthesis
// is precisely the failure the guard exists to prevent. (Nothing is lost
// meanwhile: the read path is also what EMITS the requests, so until it lands
// there are no completions to receive.)
func wireGoalKnowledgeCompletion(ctx context.Context, srv *httpadapter.Server, ext *httpadapter.ExtServer, pool *pgxpool.Pool, views subscribers.GoalScopedViewAssembler, bus eventbus.Bus) {
	if pool == nil {
		log.Printf("consumption: goal-knowledge completion NOT wired (no pgx pool)")
		return
	}
	if ext.Goals == nil {
		log.Printf("consumption: goal-knowledge completion NOT wired (goals repo absent — run wireGoalRepo first)")
		return
	}
	if views == nil {
		log.Printf("consumption: goal-knowledge completion NOT wired (no goal-scoped view assembler — the drift guard cannot run, so syntheses are NOT cached; pass the read-path assembler from main.go once CHO-2116 lands)")
		return
	}

	if bus == nil {
		log.Printf("consumption: goal-knowledge completion NOT wired (no event bus, NATS_URL unset)")
		return
	}
	sub := subscribers.NewGoalKnowledgeCompletionSubscriber(
		pg.NewGoalKnowledgeRepo(pg.NewPgxTxRunner(pool)),
		ext.Goals,
		views,
	)
	go func() {
		log.Printf("consumption: goal-knowledge completion subscriber binding chora.consumption.goal_knowledge.synthesized.v1 (CHO-2118 completion; drift-guarded)")
		if err := bus.Subscribe(ctx, consumerConfig("chora-consumption.goal-knowledge-completion", events.TopicGoalKnowledgeSynthesized), subscribers.GoalKnowledgeCompletionHandler(sub)); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("ERROR consumption: goal-knowledge-completion subscriber exited: %v", err)
		}
	}()
}
