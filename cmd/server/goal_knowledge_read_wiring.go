// goal_knowledge_read_wiring.go — composition root for the CHO-2118 READ path:
// GET /v1/me/goals/{id}/knowledge (tier-1 block + cached reflection + the
// lazy-regen request emitter).
//
// It returns the goal-scoped view ASSEMBLER as well as attaching the read
// service, because that same assembler is the completion lane's drift guard:
// main.go hands it to wireGoalKnowledgeCompletion, which stays DARK without it.
// One assembly, two consumers — the request's content_hash and the completion's
// drift check are computed by the same code, which is the only thing that makes
// the echoed hash mean anything.
//
// MUST run AFTER wireGoalRepo: the assembler reads ext.Goals (the same RLS-bound
// pg repo the goal CRUD uses — never a second instance, so the goal the reflection
// is about is the goal the learner sees).
//
// Per secrets-and-env the policy tunables come from env here, with the domain
// defaults as the fallback. No inline config.
package main

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-consumption/internal/adapter/goalscopedview"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
)

// The assembler must satisfy BOTH ports — the read path's (goal preloaded) and
// the completion subscriber's (loads the goal itself). Asserted at compile time
// so a signature drift is a build error here rather than a nil lane at boot.
var (
	_ httpadapter.GoalScopedViewAssembler = (*goalscopedview.Assembler)(nil)
	_ subscribers.GoalScopedViewAssembler = (*goalscopedview.Assembler)(nil)
	_ httpadapter.GoalKnowledgeCache      = (*pg.GoalKnowledgeRepo)(nil)
)

// wireGoalKnowledgeRead attaches the goal-knowledge read service and returns the
// view assembler for the completion lane. Nil pool ⇒ the sub-resource 503s per
// request (fail-soft at boot, fail-loud per request) and the completion lane is
// told plainly that it has no assembler.
func wireGoalKnowledgeRead(ext *httpadapter.ExtServer, pool *pgxpool.Pool) *goalscopedview.Assembler {
	if pool == nil {
		log.Printf("consumption: goal-knowledge read NOT wired (no pgx pool); GET /v1/me/goals/{id}/knowledge → 503")
		return nil
	}
	if ext.Goals == nil {
		log.Printf("consumption: goal-knowledge read NOT wired (goals repo absent — run wireGoalRepo first); GET /v1/me/goals/{id}/knowledge → 503")
		return nil
	}
	tx := pg.NewPgxTxRunner(pool)

	views := &goalscopedview.Assembler{
		Goals:      ext.Goals,                     // the SAME repo the goal CRUD uses
		Weaknesses: pg.NewLearnerWeaknessRepo(tx), // one list ⇒ mastered + shaky
		Memories:   pg.NewCompanionMemoryViewRepo(tx),
		Companions: pg.NewCompanionInstanceRepo(tx), // Companion name (enrichment)
		Concepts:   pg.NewConceptNodeRepo(tx),       // root concept title (enrichment)
	}

	floor := durationFromEnv("CHORA_GOAL_KNOWLEDGE_REGEN_FLOOR", fgk.DefaultRegenFloor)
	maxAge := durationFromEnv("CHORA_GOAL_KNOWLEDGE_MAX_AGE", fgk.DefaultMaxAge)
	pendingTTL := durationFromEnv("CHORA_GOAL_KNOWLEDGE_PENDING_TTL", fgk.DefaultPendingTTL)

	ext.GoalKnowledgeRead = &httpadapter.GoalKnowledgeReadService{
		Cache:      pg.NewGoalKnowledgeRepo(tx),
		Views:      views,
		Floor:      floor,
		MaxAge:     maxAge,
		PendingTTL: pendingTTL,
	}
	log.Printf("consumption: goal-knowledge read wired (CHO-2118): GET /v1/me/goals/{id}/knowledge (regen floor=%v max-age=%v pending-ttl=%v)",
		floor, maxAge, pendingTTL)
	return views
}

// durationFromEnv reads a Go duration from env, falling back to the domain
// default. An INVALID value is logged loudly and does not silently become a
// zero duration — a zero regen floor would regenerate the reflection on every
// chat turn (an LLM call per message), which is exactly what the floor exists to
// prevent.
func durationFromEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("consumption: %s=%q invalid (want a Go duration > 0); keeping the default %v", key, raw, def)
		return def
	}
	return d
}
