// goal_knowledge_completion_subscriber.go — the completion half of the CHO-2118
// goal-knowledge lane (sub-phase B). Consumes
// chora.consumption.goal_knowledge.synthesized.v1 from the fog orchestrator and
// writes the reflection into the cache — but only after proving it still
// describes the world.
//
// # The drift guard is the point
//
// A synthesis takes seconds-to-tens-of-seconds. In that window the goal can
// RE-ROOT (ADR-214 — the reflection is then about a different concept subtree
// entirely) or the INPUTS can move (a weakness grows, a memory lands). That is
// why synthesized.v1 echoes back `root_concept_id` and `content_hash` verbatim
// from the request: they are what the model actually saw, and we re-check both
// against the world as it stands NOW, immediately before writing.
//
//	root_concept_id != the goal's CURRENT root   → refuse (wrong subtree)
//	content_hash    != the CURRENT view's hash   → refuse (facts moved)
//
// A refusal is a SUCCESSFUL ACK, not an error: nothing is broken, the work was
// simply superseded. The row keeps its previous text and stays stale/claimable,
// so the next read re-requests. Serving a reflection about a world that no
// longer exists is worse than serving the previous one — so we refuse, and log
// loudly so the refusal RATE is visible (a high rate means the TTL floor is
// mistuned, not that the code is wrong).
//
// The content hash must be computed by the SAME assembly the request used —
// hence the GoalScopedViewAssembler port rather than a second gatherer here. A
// duplicate assembly that ordered memories differently would mismatch EVERY hash
// and silently refuse every synthesis forever; companionmind.AssembleGoalScopedView
// is deliberately composed once so its two callers cannot drift.
//
// # Idempotency
//
// Dedupe keys on the envelope's IDEMPOTENCY_KEY, not event_id. The fog
// orchestrator DERIVES the completion's key from the request (rather than minting
// a fresh UUID) precisely so that a redelivered request — Pub/Sub is at-least-once
// — yields a second completion the inbox can recognise as the same work. A
// genuine regen carries a new request key and is correctly NOT deduped.
//
// The inbox is in-memory, like every sibling subscriber here, and that is
// sufficient: correctness does not rest on it. The write is idempotent underneath
// — re-applying the same completion re-records identical text and stamps — and a
// duplicate that arrives after the facts moved is caught by the drift guard
// rather than overwriting a correctly-invalidated row. A cross-replica duplicate
// therefore costs one redundant write, never a corrupt row. (A durable
// PostgresStore inbox would save that write; no consumption subscriber wires one
// today, so adding the seam for this one alone would be untested generality.)
package subscribers

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// GoalKnowledgeWriter is the narrow write-side subset of
// companiongoalknowledge.Repository this subscriber needs.
//
// Narrow by design: the full Repository is growing methods on parallel lanes
// (soft-delete), and a consumer that reads one row and re-persists it should not
// have to track that. The pg adapter satisfies it structurally.
type GoalKnowledgeWriter interface {
	FindByGoal(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (*fgk.GoalKnowledge, error)
	Upsert(ctx context.Context, k *fgk.GoalKnowledge) error
}

// GoalScopedViewAssembler re-assembles the tier-1 view for one (companion, goal)
// as it stands RIGHT NOW. Its ContentHash is the comparand for the drift guard —
// the same assembly the request was built from, which is the only thing that
// makes the echoed hash meaningful.
//
// Implemented by the read-path's goal-scoped view service (CHO-2116).
type GoalScopedViewAssembler interface {
	AssembleGoalScopedView(ctx context.Context, tenantID, learnerGCID, companionID, goalID string) (companionmind.GoalScopedView, error)
}

// GoalKnowledgeCompletionSubscriber writes a completed synthesis into the cache,
// drift-guarded.
type GoalKnowledgeCompletionSubscriber struct {
	cache GoalKnowledgeWriter
	goals goal.Repository
	views GoalScopedViewAssembler
	inbox idempotent.Store
	ttl   time.Duration
}

// NewGoalKnowledgeCompletionSubscriber wires the subscriber with an in-memory
// inbox. All deps are mandatory (feedback_no_stubs_real_wiring) — a nil dep is a
// wiring bug, and a missing assembler in particular would mean writing an
// UNGUARDED synthesis, so panic at construction rather than fail open.
func NewGoalKnowledgeCompletionSubscriber(cache GoalKnowledgeWriter, goals goal.Repository, views GoalScopedViewAssembler) *GoalKnowledgeCompletionSubscriber {
	if cache == nil || goals == nil || views == nil {
		panic("subscribers: GoalKnowledgeCompletionSubscriber requires a cache writer, a goal repository and a view assembler")
	}
	return &GoalKnowledgeCompletionSubscriber{
		cache: cache,
		goals: goals,
		views: views,
		inbox: idempotent.NewMemoryStore(),
		ttl:   inboxTTL,
	}
}

// Handle processes one synthesized.v1 delivery. Returning an error NACKs (Pub/Sub
// redelivers, DLQ after max attempts); returning nil acks — including a
// deliberate refusal.
func (s *GoalKnowledgeCompletionSubscriber) Handle(ctx context.Context, env events.Envelope, p events.GoalKnowledgeSynthesizedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return fmt.Errorf("goal_knowledge_completion: %w", err)
	}
	tenantID, gcid := fallbackIdentity(p.TenantID, p.LearnerGCID, env)
	companionID := strings.TrimSpace(p.CompanionID)
	goalID := strings.TrimSpace(p.GoalID)
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" || companionID == "" || goalID == "" {
		return fmt.Errorf("goal_knowledge_completion: missing mandatory fields (tenant=%q gcid=%q companion=%q goal=%q)",
			tenantID, gcid, companionID, goalID)
	}

	// Dedupe on the REQUEST-derived key (see the package header) — NOT event_id,
	// which is fresh on every publish. Process commits the key only on success, so
	// a transient failure leaves it unclaimed and the redelivery genuinely re-runs.
	key := "goal_knowledge.synthesized:" + tenantID + "|" + gcid + "|" + env.IdempotencyKey
	return s.inbox.Process(ctx, key, s.ttl, func() error {
		return s.record(ctx, env, p, tenantID, gcid, companionID, goalID)
	})
}

func (s *GoalKnowledgeCompletionSubscriber) record(ctx context.Context, env events.Envelope, p events.GoalKnowledgeSynthesizedPayload, tenantID, gcid, companionID, goalID string) error {
	ctx = rlsScope(ctx, tenantID, gcid)

	// --- guard 1: is the reflection still about THIS subtree? ---
	//
	// Compared against the goal's CURRENT root, never against the cache row's own
	// root: the row still carries the root it was CREATED with (an invalidation
	// does not move it), so comparing the two would happily accept a synthesis of
	// an abandoned subtree.
	g, err := s.goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_completion: load goal %s: %w", goalID, err)
	}
	if g == nil {
		// Soft-deleted mid-synthesis. There is nothing to reflect on.
		log.Printf("consumption: goal_knowledge synthesis REFUSED (goal gone) tenant=%s gcid=%s companion=%s goal=%s run=%s",
			tenantID, gcid, companionID, goalID, p.GeneratedByRunID)
		return nil
	}
	currentRoot := derefRoot(g.RootConceptID)
	synthesisRoot := strings.TrimSpace(p.RootConceptID)
	if synthesisRoot != currentRoot {
		// A rootless goal is legitimate, so "" == "" passes; only a real move fails.
		log.Printf("consumption: goal_knowledge synthesis REFUSED (goal re-rooted mid-synthesis) tenant=%s gcid=%s companion=%s goal=%s synthesis_root=%q current_root=%q run=%s",
			tenantID, gcid, companionID, goalID, synthesisRoot, currentRoot, p.GeneratedByRunID)
		return nil
	}

	// --- guard 2: did the facts move under the synthesis? ---
	view, err := s.views.AssembleGoalScopedView(ctx, tenantID, gcid, companionID, goalID)
	if err != nil {
		// Cannot verify drift ⇒ never write blind. NACK and re-check on redelivery.
		return fmt.Errorf("goal_knowledge_completion: re-assemble view for drift check (companion=%s goal=%s): %w", companionID, goalID, err)
	}
	if current := view.ContentHash(); current != strings.TrimSpace(p.ContentHash) {
		log.Printf("consumption: goal_knowledge synthesis REFUSED (inputs moved mid-synthesis) tenant=%s gcid=%s companion=%s goal=%s synthesis_hash=%s current_hash=%s run=%s",
			tenantID, gcid, companionID, goalID, p.ContentHash, current, p.GeneratedByRunID)
		return nil
	}

	// --- the write ---
	row, err := s.cache.FindByGoal(ctx, tenantID, gcid, companionID, goalID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_completion: load cache row (companion=%s goal=%s): %w", companionID, goalID, err)
	}
	if row == nil {
		// The read path creates + claims the row before requesting, so a missing row
		// means it was removed (soft-deleted goal / closed account) while the model
		// wrote. Minting one here would resurrect deliberately-removed data.
		log.Printf("consumption: goal_knowledge synthesis REFUSED (cache row gone) tenant=%s gcid=%s companion=%s goal=%s run=%s",
			tenantID, gcid, companionID, goalID, p.GeneratedByRunID)
		return nil
	}

	generatedAt := parseGeneratedAt(p.GeneratedAt, env.OccurredAt, companionID, goalID)

	// The model looked and honestly had nothing to say about this goal (CHO-2180).
	// That is a CONCLUSION, and it is recorded as one — stamped, terminal, and no
	// longer claimed — so the read path can answer "no reflection yet" instead of
	// leaving the learner on a "reflecting…" marker that nothing will ever resolve,
	// and so the next read does not re-buy the same guaranteed-to-decline call.
	//
	// The drift guards above have already run: a decline about a goal that has
	// since re-rooted, or whose inputs have moved, is refused exactly like a
	// decline-shaped reflection would be. Silence about a world that no longer
	// exists is not worth caching either.
	if p.NothingToSay {
		if err := row.RecordNoReflection(p.GeneratedByRunID, p.GeneratedByModelID, p.PromptVersion, p.ContentHash, generatedAt); err != nil {
			log.Printf("consumption: goal_knowledge decline REFUSED by the aggregate (%v) tenant=%s gcid=%s companion=%s goal=%s run=%s model=%s",
				err, tenantID, gcid, companionID, goalID, p.GeneratedByRunID, p.GeneratedByModelID)
			return nil
		}
		row.RootConceptID = rootPtr(currentRoot)
		if err := s.cache.Upsert(ctx, row); err != nil {
			return fmt.Errorf("goal_knowledge_completion: persist decline (companion=%s goal=%s): %w", companionID, goalID, err)
		}
		log.Printf("consumption: goal_knowledge CONCLUDED SILENT (nothing true to say) tenant=%s gcid=%s companion=%s goal=%s run=%s model=%s",
			tenantID, gcid, companionID, goalID, p.GeneratedByRunID, p.GeneratedByModelID)
		return nil
	}

	if err := row.RecordSynthesis(p.SynthesisText, p.GeneratedByRunID, p.GeneratedByModelID, p.PromptVersion, p.ContentHash, generatedAt); err != nil {
		// The domain refuses a blank reflection (a fabricated success), an oversize
		// one (the model ignored the output contract), and one that cannot say which
		// prompt/model produced it (unexplainable — ADR-197). All three are terminal
		// for THESE bytes: a redelivery of the identical payload can never satisfy
		// the aggregate, so NACKing would only burn retries into the DLQ. Ack, log
		// loudly, and leave the row claimable — the next read requests a fresh
		// synthesis, which is the only thing that can actually fix it.
		log.Printf("consumption: goal_knowledge synthesis REFUSED by the aggregate (%v) tenant=%s gcid=%s companion=%s goal=%s run=%s model=%s chars=%d",
			err, tenantID, gcid, companionID, goalID, p.GeneratedByRunID, p.GeneratedByModelID, len([]rune(p.SynthesisText)))
		return nil
	}

	// Stamp the root this reflection was generated against — it has just been
	// PROVEN to be the goal's current root by guard 1.
	//
	// RecordSynthesis does not do this, and the read path gates serving on
	// RootMatches(currentRoot). Without this line a re-rooted goal's row keeps its
	// ORIGINAL root forever: RootMatches stays false, the read path re-requests on
	// every single render, and the learner never sees a reflection — an LLM call
	// per read, indefinitely. (The aggregate is the better long-term home for this,
	// as part of the decision stamp; that is a domain change, flagged not smuggled.)
	row.RootConceptID = rootPtr(currentRoot)

	if err := s.cache.Upsert(ctx, row); err != nil {
		return fmt.Errorf("goal_knowledge_completion: persist synthesis (companion=%s goal=%s): %w", companionID, goalID, err)
	}
	return nil
}

// derefRoot reads a nullable root as a trimmed value; a rootless goal is
// legitimate and yields "".
func derefRoot(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// rootPtr is derefRoot's inverse: "" is a rootless goal (nil), not an empty root.
func rootPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// parseGeneratedAt reads the producer's RFC3339 stamp, falling back to the
// envelope's occurred_at — a real time for this event, never a fabricated one.
// The value feeds the TTL floor, so it must be honest.
func parseGeneratedAt(raw string, occurredAt time.Time, companionID, goalID string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return occurredAt.UTC()
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		log.Printf("consumption: goal_knowledge completion has an unparseable generated_at %q (%v) — falling back to the envelope occurred_at (companion=%s goal=%s)",
			raw, err, companionID, goalID)
		return occurredAt.UTC()
	}
	return t.UTC()
}
