// goal_knowledge_invalidation_subscriber.go — the event-invalidation lane for
// the CHO-2118 goal-knowledge cache (sub-phase B; owner-signed decision #2).
//
// A cached reflection is a DERIVED projection over three moving things: the
// learner's weaknesses, their progress on the goal, and what the Companion
// remembers. When any of them moves the reflection stops being true, so these
// six events stale it. Six events, three fan-out shapes:
//
//	weakness.analyzed.v1          ─┐ CONCEPT-scoped → resolve to goals
//	weakness.grown.v1             ─┘
//	goal.progress_updated.v1      ─┐ GOAL-scoped    → the goal is named
//	goal.graduated.v1             ─┘
//	companion.chat_turn_completed.v1 ─┐ COMPANION-scoped → every goal it reflects on
//	companion.memory_eviction.v1     ─┘
//
// A SEVENTH signal has no event at all: a goal PATCH that re-roots the goal
// (ADR-214 — the reflection is then about a different subtree entirely, the
// strongest invalidation there is). Goal CRUD emits nothing, so that one is
// invalidated IN-HANDLER at the goal update path via InvalidateGoal.
//
// The concept→goal resolution is the substance here. A weakness is CONCEPT-
// scoped but a reflection is GOAL-scoped, so one concept moving can stale
// several goals — every goal whose concept set contains it. That question
// already has exactly one answer in this codebase (goal_graduation_subscriber
// and the A+ progress ring both ask it): list the learner's goals and intersect
// `Goal.ConceptSet` with the concept key, normalising BOTH sides via
// lw.NormalizeConceptKey. We reuse that and add no second way to answer it —
// a divergent membership rule here would silently mean the ring says 100% while
// the reflection still talks about a concept the learner has mastered.
//
// Scoping is provably tight rather than merely conservative: the tier-1 view
// (companionmind.AssembleGoalScopedView) derives BOTH its shaky concepts and its
// progress from goal-subtree ∩ (weaknesses | mastered). A concept outside a
// goal's set therefore cannot change one byte of that goal's view — so goals
// that do not contain the concept are correctly left alone.
//
// Idempotency (Pub/Sub is at-least-once): invalidation is idempotent in the
// DOMAIN, not in a tracker. GoalKnowledge.Invalidate refuses to overwrite an
// equal-or-stronger reason, so a redelivery — even to a different pod with a
// cold tracker — re-applies the same reason and moves nothing. The in-process
// tracker is only a round-trip saver, and it follows process-then-mark
// (CHO-2107/CHO-2130): the key is recorded ONLY after the invalidation
// persisted, so a transient repo failure NACKs with the key unburned and the
// redelivery genuinely re-runs.
//
// Fail-loud: every repo error returns (→ NACK → redelivery → DLQ). A target row
// that does not exist yet is NOT an error — there is simply no cached reflection
// to stale, so it acks.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// ---------------------------------------------------------------------------
// Payloads (the four not already mirrored in this package)
// ---------------------------------------------------------------------------
//
// WeaknessGrownPayload + WeaknessAnalyzedPayload already exist here (the
// graduation and analysis lanes); they are REUSED rather than re-declared, so
// the wire contract has one home per topic.

// GoalProgressUpdatedPayload mirrors chora.consumption.goal.progress_updated.v1.
type GoalProgressUpdatedPayload struct {
	GoalID      string
	TenantID    string
	LearnerGCID string
}

// GoalGraduatedPayload mirrors chora.consumption.goal.graduated.v1.
type GoalGraduatedPayload struct {
	GoalID      string
	TenantID    string
	LearnerGCID string
}

// CompanionChatTurnCompletedPayload mirrors
// chora.consumption.companion.chat_turn_completed.v1. This is the highest-churn
// event in the set (it fires on EVERY chat turn), which is exactly why the
// domain's TTL floor — not this subscriber — decides when an LLM call is
// actually authorised. Invalidating is cheap; regenerating is not.
type CompanionChatTurnCompletedPayload struct {
	CompanionID string
	TenantID    string
	LearnerGCID string
}

// CompanionMemoryEvictionPayload mirrors
// chora.consumption.companion.memory_eviction.v1. Note the learner field is
// `owner_gcid` on this proto, not `learner_gcid`.
//
// ⚠ DORMANT AS OF 2026-07-14: the topic + DLQ are provisioned (m10-data-plane)
// and the proto contract + generated binding exist, but NOTHING in the platform
// publishes it yet — the Memory-Bank LRU eviction hook is listed as
// "defined-but-never-emitted" (docs/COMPANION-GROWTH-AGENT-BUILDER-CR-2026-07-02
// §Events reality). This handler is complete and tested so the lane lights up
// the moment an emitter lands; it is NOT a stub, but it will see zero traffic
// until then. Practical exposure is small: chat_turn_completed has the identical
// companion-scoped fan-out, so any chatting learner stales the same rows anyway.
type CompanionMemoryEvictionPayload struct {
	CompanionID string
	TenantID    string
	OwnerGCID   string
}

// ---------------------------------------------------------------------------
// GoalKnowledgeInvalidator — the fan-out engine
// ---------------------------------------------------------------------------

// GoalKnowledgeInvalidationStore is the narrow subset of
// companiongoalknowledge.Repository the fan-out needs: list the affected rows, and
// re-persist each one.
//
// Narrow by design (the kg_invalidation KGHexagonInvalidator precedent): the full
// Repository grows methods on other lanes (soft-delete), and a consumer that only
// lists and re-persists should neither track that nor force its fakes to. The pg
// adapter satisfies it structurally.
type GoalKnowledgeInvalidationStore interface {
	ListByGoal(ctx context.Context, tenantID, learnerGCID, goalID string) ([]*fgk.GoalKnowledge, error)
	ListByCompanion(ctx context.Context, tenantID, learnerGCID, companionID string) ([]*fgk.GoalKnowledge, error)
	Upsert(ctx context.Context, k *fgk.GoalKnowledge) error
}

// GoalKnowledgeInvalidator stales cached goal reflections. It is shared by the
// six-event subscriber below AND by the in-handler goal re-root path (a goal
// PATCH emits no event), so the three fan-out shapes have exactly one
// implementation.
//
// It deliberately walks List → Invalidate → Upsert rather than issuing a bulk
// UPDATE: the reason-priority table lives in the domain aggregate, and a bulk
// SQL update would have to re-encode those ranks in SQL, giving the invariant a
// second home that can drift. The fan-out is a learner's goals for one Companion
// — single digits — so the per-row round trip buys the invariant's single home
// for nothing.
type GoalKnowledgeInvalidator struct {
	cache GoalKnowledgeInvalidationStore
	goals goal.Repository
}

// NewGoalKnowledgeInvalidator wires the invalidator. Both deps are mandatory
// (feedback_no_stubs_real_wiring) — a nil dep is a wiring bug, so panic at
// construction rather than fail-open at consume time.
func NewGoalKnowledgeInvalidator(cache GoalKnowledgeInvalidationStore, goals goal.Repository) *GoalKnowledgeInvalidator {
	if cache == nil || goals == nil {
		panic("subscribers: GoalKnowledgeInvalidator requires a goal-knowledge cache and a goal repository")
	}
	return &GoalKnowledgeInvalidator{cache: cache, goals: goals}
}

// InvalidateGoal stales every cached reflection for ONE goal, across every
// Companion bound to it. Returns the number of rows invalidated; a goal with no
// cached reflection yet is a no-op (0, nil) — there is nothing to stale.
//
// This is also the entry point for the in-handler re-root invalidation.
func (i *GoalKnowledgeInvalidator) InvalidateGoal(ctx context.Context, tenantID, learnerGCID, goalID string, reason fgk.InvalidationReason) (int, error) {
	if strings.TrimSpace(goalID) == "" {
		return 0, errors.New("goal_knowledge_invalidation: goal_id required")
	}
	ctx = rlsScope(ctx, tenantID, learnerGCID)
	rows, err := i.cache.ListByGoal(ctx, tenantID, learnerGCID, goalID)
	if err != nil {
		return 0, fmt.Errorf("goal_knowledge_invalidation: list cache by goal %s: %w", goalID, err)
	}
	return i.stale(ctx, rows, reason)
}

// InvalidateCompanion stales every cached reflection a Companion holds, across all
// of the learner's goals — what the Companion remembers has changed, and that
// bears on every goal it reflects on.
func (i *GoalKnowledgeInvalidator) InvalidateCompanion(ctx context.Context, tenantID, learnerGCID, companionID string, reason fgk.InvalidationReason) (int, error) {
	if strings.TrimSpace(companionID) == "" {
		return 0, errors.New("goal_knowledge_invalidation: companion_id required")
	}
	ctx = rlsScope(ctx, tenantID, learnerGCID)
	rows, err := i.cache.ListByCompanion(ctx, tenantID, learnerGCID, companionID)
	if err != nil {
		return 0, fmt.Errorf("goal_knowledge_invalidation: list cache by companion %s: %w", companionID, err)
	}
	return i.stale(ctx, rows, reason)
}

// InvalidateConcepts resolves CONCEPT keys to the goals that contain them and
// stales each of those goals' reflections. This is the weakness lane.
//
// Resolution reuses the codebase's single answer to "which goals contain this
// concept" — Goal.ConceptSet, compared under lw.NormalizeConceptKey (see the
// package header). An empty key set is a no-op ack: an analysis that extracted
// no keyable concept changes nothing.
func (i *GoalKnowledgeInvalidator) InvalidateConcepts(ctx context.Context, tenantID, learnerGCID string, conceptKeys []string, reason fgk.InvalidationReason) (int, error) {
	wanted := normalisedKeySet(conceptKeys)
	if len(wanted) == 0 {
		return 0, nil
	}
	ctx = rlsScope(ctx, tenantID, learnerGCID)

	// The learner's LIVE goals — every status, not just active. An achieved goal
	// still shows its reflection on the Companion tab, so it still goes stale.
	// (The graduation subscriber filters to ACTIVE because only an active goal can
	// graduate; that filter is about a different question and must not be copied.)
	goals, err := i.goals.ListByLearner(ctx, tenantID, learnerGCID)
	if err != nil {
		return 0, fmt.Errorf("goal_knowledge_invalidation: list goals: %w", err)
	}

	total := 0
	for _, g := range goals {
		if g == nil || !goalContainsAnyConcept(g, wanted) {
			continue
		}
		n, err := i.InvalidateGoal(ctx, tenantID, learnerGCID, g.GoalID, reason)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// stale applies the reason to each row and persists it. The aggregate keeps the
// last good text (serve-stale-while-regen) and refuses to let a weaker reason
// overwrite a stronger one — both invariants live there, not here.
func (i *GoalKnowledgeInvalidator) stale(ctx context.Context, rows []*fgk.GoalKnowledge, reason fgk.InvalidationReason) (int, error) {
	n := 0
	for _, row := range rows {
		if row == nil {
			continue
		}
		row.Invalidate(reason)
		if err := i.cache.Upsert(ctx, row); err != nil {
			return n, fmt.Errorf("goal_knowledge_invalidation: upsert %s: %w", row.ID, err)
		}
		n++
	}
	return n, nil
}

// goalContainsAnyConcept reports whether the goal's concept set intersects the
// (already normalised) key set. Both sides are normalised — the goal's set is
// normalised at write time, but a producer may still send a raw label, and a
// missed match is invisible: the learner would simply keep reading a reflection
// that is no longer true.
func goalContainsAnyConcept(g *goal.Goal, wanted map[string]bool) bool {
	for _, k := range g.ConceptSet {
		if wanted[lw.NormalizeConceptKey(k)] {
			return true
		}
	}
	return false
}

// normalisedKeySet folds concept keys into the normalised slug space, dropping
// un-keyable entries (the analyser already drops these; be defensive so one bad
// edge cannot NACK-loop a whole analysis).
func normalisedKeySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		if n := lw.NormalizeConceptKey(k); n != "" {
			set[n] = true
		}
	}
	return set
}

// rlsScope stamps tenant + learner on the ctx so the pg repos' rls.ApplySession
// scopes to the right tenant (mirrors weakness_analyzed_subscriber.upsertEdges).
func rlsScope(ctx context.Context, tenantID, gcid string) context.Context {
	return tracing.WithGCID(tracing.WithTenantID(ctx, tenantID), gcid)
}

// ---------------------------------------------------------------------------
// GoalKnowledgeInvalidationSubscriber — the six inbound handlers
// ---------------------------------------------------------------------------

// GoalKnowledgeInvalidationSubscriber consumes the six-event invalidation set.
// One endpoint fronts it (goal_knowledge_invalidation_push_handler.go), each
// topic on its own subscription so retry/DLQ isolation holds per topic.
type GoalKnowledgeInvalidationSubscriber struct {
	inv     *GoalKnowledgeInvalidator
	tracker *idempotencyTracker
}

// NewGoalKnowledgeInvalidationSubscriber wires the subscriber. The invalidator
// is mandatory — panic at construction rather than fail-open at consume time.
func NewGoalKnowledgeInvalidationSubscriber(inv *GoalKnowledgeInvalidator) *GoalKnowledgeInvalidationSubscriber {
	if inv == nil {
		panic("subscribers: GoalKnowledgeInvalidationSubscriber requires an invalidator")
	}
	return &GoalKnowledgeInvalidationSubscriber{inv: inv, tracker: newIdempotencyTracker()}
}

// HandleWeaknessGrown stales every goal containing the grown concept. A grow
// moves BOTH halves of the tier-1 view for such a goal — the concept leaves the
// shaky list and joins the mastered count — so the reflection is doubly wrong.
func (s *GoalKnowledgeInvalidationSubscriber) HandleWeaknessGrown(ctx context.Context, env events.Envelope, p WeaknessGrownPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(weakness.grown): %w", err)
	}
	if strings.TrimSpace(p.ConceptKey) == "" {
		return errors.New("goal_knowledge_invalidation(weakness.grown): concept_key required")
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateConcepts(ctx, tenantID, gcid, []string{p.ConceptKey}, fgk.ReasonWeaknessGrown)
		return err
	})
}

// HandleWeaknessAnalyzed stales every goal touched by ANY concept in the
// analysis. An analysis with no keyable edge changes nothing and acks.
func (s *GoalKnowledgeInvalidationSubscriber) HandleWeaknessAnalyzed(ctx context.Context, env events.Envelope, p WeaknessAnalyzedPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(weakness.analyzed): %w", err)
	}
	keys := make([]string, 0, len(p.Edges))
	for _, e := range p.Edges {
		if k := strings.TrimSpace(e.ConceptKey); k != "" {
			keys = append(keys, k)
			continue
		}
		keys = append(keys, e.ConceptLabel) // same fallback the analysis lane uses
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateConcepts(ctx, tenantID, gcid, keys, fgk.ReasonWeaknessAnalyzed)
		return err
	})
}

// HandleGoalProgressUpdated stales the named goal — the mastered share moved.
func (s *GoalKnowledgeInvalidationSubscriber) HandleGoalProgressUpdated(ctx context.Context, env events.Envelope, p GoalProgressUpdatedPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(goal.progress_updated): %w", err)
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateGoal(ctx, tenantID, gcid, p.GoalID, fgk.ReasonGoalProgressUpdated)
		return err
	})
}

// HandleGoalGraduated stales the named goal — "you are getting there" is the
// wrong thing to say to a learner who just finished.
func (s *GoalKnowledgeInvalidationSubscriber) HandleGoalGraduated(ctx context.Context, env events.Envelope, p GoalGraduatedPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(goal.graduated): %w", err)
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateGoal(ctx, tenantID, gcid, p.GoalID, fgk.ReasonGoalGraduated)
		return err
	})
}

// HandleChatTurnCompleted stales every goal this Companion reflects on — it has
// new memory of the learner.
func (s *GoalKnowledgeInvalidationSubscriber) HandleChatTurnCompleted(ctx context.Context, env events.Envelope, p CompanionChatTurnCompletedPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.LearnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(companion.chat_turn_completed): %w", err)
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateCompanion(ctx, tenantID, gcid, p.CompanionID, fgk.ReasonChatTurnCompleted)
		return err
	})
}

// HandleMemoryEviction stales every goal this Companion reflects on — a memory it
// was citing is gone. See CompanionMemoryEvictionPayload: DORMANT (no producer).
func (s *GoalKnowledgeInvalidationSubscriber) HandleMemoryEviction(ctx context.Context, env events.Envelope, p CompanionMemoryEvictionPayload) error {
	tenantID, gcid, err := s.identity(env, p.TenantID, p.OwnerGCID)
	if err != nil {
		return fmt.Errorf("goal_knowledge_invalidation(companion.memory_eviction): %w", err)
	}
	return s.run(env, func() error {
		_, err := s.inv.InvalidateCompanion(ctx, tenantID, gcid, p.CompanionID, fgk.ReasonMemoryEvicted)
		return err
	})
}

// identity validates the envelope and resolves (tenant, learner), falling back
// to the envelope when the payload omits them.
func (s *GoalKnowledgeInvalidationSubscriber) identity(env events.Envelope, tenantID, gcid string) (string, string, error) {
	if err := validateInboundEnvelope(env); err != nil {
		return "", "", err
	}
	tenantID, gcid = fallbackIdentity(tenantID, gcid, env)
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" {
		return "", "", errors.New("missing tenant_id/learner_gcid")
	}
	return tenantID, gcid, nil
}

// run applies process-then-mark around one invalidation (CHO-2107/CHO-2130): peek
// the dedupe key, do the work, and record the key ONLY on success. A claim-first
// mark would burn the key before the write, so a transient repo failure would
// NACK and its redelivery would be ack-dropped as a duplicate — leaving a stale
// reflection served as current, permanently.
func (s *GoalKnowledgeInvalidationSubscriber) run(env events.Envelope, fn func() error) error {
	if s.tracker.seen(env.EventID) {
		return nil // already invalidated for this event — ack
	}
	if err := fn(); err != nil {
		return err // UNMARKED → NACK → redelivery re-runs (invalidation is idempotent)
	}
	s.tracker.mark(env.EventID)
	return nil
}
