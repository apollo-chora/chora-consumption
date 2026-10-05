package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	fgk "github.com/apollo-chora/chora-consumption/internal/domain/companion_goal_knowledge"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

// goal_knowledge_invalidation_subscriber_test.go — CHO-2118 sub-phase B.
//
// The load-bearing case is the CONCEPT→GOAL fan-out: a weakness is concept-
// scoped, a reflection is goal-scoped, so one weakness moving must stale every
// goal whose concept set contains that concept — and nothing else.

const (
	gkiTenant     = "11111111-1111-7000-8000-000000000001"
	gkiLearner    = "22222222-2222-7000-8000-000000000002"
	gkiCompanion  = "33333333-3333-7000-8000-000000000003"
	gkiCompanion2 = "33333333-3333-7000-8000-000000000099"
)

var gkiNow = time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)

// --- fakes ---

// gkiCache is an in-memory companiongoalknowledge.Repository.
type gkiCache struct {
	rows      []*fgk.GoalKnowledge
	upserts   int
	listErr   error
	upsertErr error
}

func (c *gkiCache) FindByGoal(_ context.Context, tenantID, gcid, companionID, goalID string) (*fgk.GoalKnowledge, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	for _, r := range c.rows {
		if r.TenantID == tenantID && r.LearnerGCID == gcid && r.CompanionID == companionID && r.GoalID == goalID {
			return r, nil
		}
	}
	return nil, nil
}

func (c *gkiCache) ListByGoal(_ context.Context, tenantID, gcid, goalID string) ([]*fgk.GoalKnowledge, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := []*fgk.GoalKnowledge{}
	for _, r := range c.rows {
		if r.TenantID == tenantID && r.LearnerGCID == gcid && r.GoalID == goalID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *gkiCache) ListByCompanion(_ context.Context, tenantID, gcid, companionID string) ([]*fgk.GoalKnowledge, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := []*fgk.GoalKnowledge{}
	for _, r := range c.rows {
		if r.TenantID == tenantID && r.LearnerGCID == gcid && r.CompanionID == companionID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *gkiCache) Upsert(_ context.Context, k *fgk.GoalKnowledge) error {
	if c.upsertErr != nil {
		return c.upsertErr
	}
	c.upserts++
	for i, r := range c.rows {
		if r.ID == k.ID {
			c.rows[i] = k
			return nil
		}
	}
	c.rows = append(c.rows, k)
	return nil
}

// find returns the cached row for (companion, goal), or nil.
func (c *gkiCache) find(companionID, goalID string) *fgk.GoalKnowledge {
	for _, r := range c.rows {
		if r.CompanionID == companionID && r.GoalID == goalID {
			return r
		}
	}
	return nil
}

// gkiGoals is an in-memory goal.Repository.
type gkiGoals struct {
	goals   []*goal.Goal
	listErr error
	getErr  error
}

func (g *gkiGoals) Create(context.Context, *goal.Goal) error { return nil }
func (g *gkiGoals) GetByID(_ context.Context, _, _, id string) (*goal.Goal, error) {
	if g.getErr != nil {
		return nil, g.getErr
	}
	for _, x := range g.goals {
		if x.GoalID == id {
			return x, nil
		}
	}
	return nil, nil
}
func (g *gkiGoals) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	if g.listErr != nil {
		return nil, g.listErr
	}
	return g.goals, nil
}
func (g *gkiGoals) Update(context.Context, *goal.Goal) error { return nil }

// --- helpers ---

func gkiEnv(eventID string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: "gki-idem-" + eventID,
		TenantID:       gkiTenant,
		GCID:           gkiLearner,
		OccurredAt:     gkiNow,
		PublishedAt:    gkiNow,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
}

// gkiGoal builds a live goal with the given normalised concept set.
func gkiGoal(id string, status goal.Status, concepts ...string) *goal.Goal {
	return &goal.Goal{
		GoalID:      id,
		TenantID:    gkiTenant,
		LearnerGCID: gkiLearner,
		ConceptSet:  concepts,
		Status:      status,
	}
}

// gkiFreshRow builds a SYNTHESISED (fresh) cache row — the interesting starting
// state, because an invalidation must stale it while KEEPING its text.
func gkiFreshRow(t *testing.T, companionID, goalID string) *fgk.GoalKnowledge {
	t.Helper()
	k, err := fgk.NewGoalKnowledge(gkiTenant, gkiLearner, companionID, goalID, nil)
	if err != nil {
		t.Fatalf("NewGoalKnowledge: %v", err)
	}
	if err := k.RecordSynthesis("You are steady on fractions.", "run-1", "gemini-x", "v1", "hash-1", gkiNow); err != nil {
		t.Fatalf("RecordSynthesis: %v", err)
	}
	return k
}

func gkiSub(cache *gkiCache, goals *gkiGoals) *GoalKnowledgeInvalidationSubscriber {
	return NewGoalKnowledgeInvalidationSubscriber(NewGoalKnowledgeInvalidator(cache, goals))
}

// --- concept → goal fan-out (the hard one) ---

// A weakness is CONCEPT-scoped; a reflection is GOAL-scoped. One concept moving
// must stale every goal whose concept set contains it — and leave the rest alone.
func TestGoalKnowledgeInvalidation_WeaknessGrown_FansOutToEveryGoalContainingTheConcept(t *testing.T) {
	inScopeA := gkiGoal("goal-a", goal.StatusActive, "fractions", "decimals")
	outOfScope := gkiGoal("goal-b", goal.StatusActive, "photosynthesis")
	inScopeC := gkiGoal("goal-c", goal.StatusActive, "fractions")

	cache := &gkiCache{rows: []*fgk.GoalKnowledge{
		gkiFreshRow(t, gkiCompanion, "goal-a"),
		gkiFreshRow(t, gkiCompanion2, "goal-a"), // second Companion on the same goal
		gkiFreshRow(t, gkiCompanion, "goal-b"),
		gkiFreshRow(t, gkiCompanion, "goal-c"),
	}}
	goals := &gkiGoals{goals: []*goal.Goal{inScopeA, outOfScope, inScopeC}}

	sub := gkiSub(cache, goals)
	err := sub.HandleWeaknessGrown(context.Background(), gkiEnv("evt-1"), WeaknessGrownPayload{
		GrowthEdgeID: "edge-1",
		TenantID:     gkiTenant,
		LearnerGCID:  gkiLearner,
		ConceptKey:   "fractions",
	})
	if err != nil {
		t.Fatalf("HandleWeaknessGrown: %v", err)
	}

	// Both companions on goal-a, plus goal-c — staled with the right reason.
	for _, want := range []struct{ companion, goalID string }{
		{gkiCompanion, "goal-a"}, {gkiCompanion2, "goal-a"}, {gkiCompanion, "goal-c"},
	} {
		row := cache.find(want.companion, want.goalID)
		if row.Status != fgk.StatusStale {
			t.Errorf("(%s,%s): status = %q, want stale", want.companion, want.goalID, row.Status)
		}
		if row.InvalidationReason != fgk.ReasonWeaknessGrown {
			t.Errorf("(%s,%s): reason = %q, want weakness_grown", want.companion, want.goalID, row.InvalidationReason)
		}
		// serve-stale-while-regen: the last good text must survive invalidation.
		if !row.Servable() {
			t.Errorf("(%s,%s): row lost its text on invalidation — learner would see a blank", want.companion, want.goalID)
		}
	}

	// The goal that does NOT contain the concept must be untouched.
	if b := cache.find(gkiCompanion, "goal-b"); b.InvalidatedAt != nil {
		t.Errorf("goal-b invalidated by a concept outside its set (reason=%q)", b.InvalidationReason)
	}
}

// weakness.analyzed carries MANY edges — every concept in the batch fans out.
func TestGoalKnowledgeInvalidation_WeaknessAnalyzed_FansOutOverAllEdges(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{
		gkiFreshRow(t, gkiCompanion, "goal-a"),
		gkiFreshRow(t, gkiCompanion, "goal-b"),
		gkiFreshRow(t, gkiCompanion, "goal-c"),
	}}
	goals := &gkiGoals{goals: []*goal.Goal{
		gkiGoal("goal-a", goal.StatusActive, "fractions"),
		gkiGoal("goal-b", goal.StatusActive, "photosynthesis"),
		gkiGoal("goal-c", goal.StatusActive, "long-division"),
	}}

	sub := gkiSub(cache, goals)
	err := sub.HandleWeaknessAnalyzed(context.Background(), gkiEnv("evt-2"), WeaknessAnalyzedPayload{
		UploadID:    "upload-1",
		TenantID:    gkiTenant,
		LearnerGCID: gkiLearner,
		Edges: []ExtractedGrowthEdgePayload{
			{ConceptKey: "fractions", ConceptLabel: "Fractions"},
			{ConceptKey: "long-division", ConceptLabel: "Long division"},
		},
	})
	if err != nil {
		t.Fatalf("HandleWeaknessAnalyzed: %v", err)
	}

	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonWeaknessAnalyzed {
		t.Errorf("goal-a reason = %q, want weakness_analyzed", got.InvalidationReason)
	}
	if got := cache.find(gkiCompanion, "goal-c"); got.InvalidationReason != fgk.ReasonWeaknessAnalyzed {
		t.Errorf("goal-c reason = %q, want weakness_analyzed", got.InvalidationReason)
	}
	if got := cache.find(gkiCompanion, "goal-b"); got.InvalidatedAt != nil {
		t.Error("goal-b invalidated though none of its concepts were analysed")
	}
}

// The event may carry a raw label; the goal stores a normalised slug. Membership
// must normalise BOTH sides (as lw.CountMastered does) or the fan-out silently
// misses and the learner reads a stale reflection forever.
func TestGoalKnowledgeInvalidation_ConceptKeyMatchIsNormalised(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "long-division")}}

	sub := gkiSub(cache, goals)
	if err := sub.HandleWeaknessGrown(context.Background(), gkiEnv("evt-3"), WeaknessGrownPayload{
		TenantID: gkiTenant, LearnerGCID: gkiLearner,
		ConceptKey: "Long Division!", // raw, unnormalised
	}); err != nil {
		t.Fatalf("HandleWeaknessGrown: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidatedAt == nil {
		t.Error(`"Long Division!" did not match goal concept "long-division" — normalisation missing`)
	}
}

// A graduated goal still shows a reflection, so it must still be invalidated.
// (The graduation subscriber filters to ACTIVE; invalidation deliberately does not.)
func TestGoalKnowledgeInvalidation_InvalidatesNonActiveGoalsToo(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-done")}}
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-done", goal.StatusAchieved, "fractions")}}

	sub := gkiSub(cache, goals)
	if err := sub.HandleWeaknessGrown(context.Background(), gkiEnv("evt-4"), WeaknessGrownPayload{
		TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions",
	}); err != nil {
		t.Fatalf("HandleWeaknessGrown: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-done"); got.InvalidatedAt == nil {
		t.Error("achieved goal not invalidated — its cached reflection would go stale forever")
	}
}

func TestGoalKnowledgeInvalidation_WeaknessAnalyzed_NoEdgesIsNoOpAck(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "fractions")}}

	sub := gkiSub(cache, goals)
	if err := sub.HandleWeaknessAnalyzed(context.Background(), gkiEnv("evt-5"), WeaknessAnalyzedPayload{
		UploadID: "upload-2", TenantID: gkiTenant, LearnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("empty-edge analysis must ack: %v", err)
	}
	if cache.upserts != 0 {
		t.Errorf("upserts = %d, want 0 (nothing to invalidate)", cache.upserts)
	}
}

// --- direct goal fan-out ---

func TestGoalKnowledgeInvalidation_GoalProgressUpdated_InvalidatesThatGoal(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{
		gkiFreshRow(t, gkiCompanion, "goal-a"),
		gkiFreshRow(t, gkiCompanion, "goal-b"),
	}}
	sub := gkiSub(cache, &gkiGoals{})

	if err := sub.HandleGoalProgressUpdated(context.Background(), gkiEnv("evt-6"), GoalProgressUpdatedPayload{
		GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("HandleGoalProgressUpdated: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonGoalProgressUpdated {
		t.Errorf("goal-a reason = %q, want goal_progress_updated", got.InvalidationReason)
	}
	if got := cache.find(gkiCompanion, "goal-b"); got.InvalidatedAt != nil {
		t.Error("goal-b invalidated by another goal's progress event")
	}
}

func TestGoalKnowledgeInvalidation_GoalGraduated_InvalidatesThatGoal(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
	sub := gkiSub(cache, &gkiGoals{})

	if err := sub.HandleGoalGraduated(context.Background(), gkiEnv("evt-7"), GoalGraduatedPayload{
		GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("HandleGoalGraduated: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonGoalGraduated {
		t.Errorf("reason = %q, want goal_graduated", got.InvalidationReason)
	}
}

// --- companion fan-out ---

func TestGoalKnowledgeInvalidation_ChatTurnCompleted_FansOutByCompanion(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{
		gkiFreshRow(t, gkiCompanion, "goal-a"),
		gkiFreshRow(t, gkiCompanion, "goal-b"),
		gkiFreshRow(t, gkiCompanion2, "goal-a"), // a DIFFERENT companion — untouched
	}}
	sub := gkiSub(cache, &gkiGoals{})

	if err := sub.HandleChatTurnCompleted(context.Background(), gkiEnv("evt-8"), CompanionChatTurnCompletedPayload{
		CompanionID: gkiCompanion, TenantID: gkiTenant, LearnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("HandleChatTurnCompleted: %v", err)
	}
	for _, goalID := range []string{"goal-a", "goal-b"} {
		if got := cache.find(gkiCompanion, goalID); got.InvalidationReason != fgk.ReasonChatTurnCompleted {
			t.Errorf("(%s): reason = %q, want chat_turn_completed", goalID, got.InvalidationReason)
		}
	}
	if got := cache.find(gkiCompanion2, "goal-a"); got.InvalidatedAt != nil {
		t.Error("a chat turn with one Companion staled ANOTHER Companion's reflection")
	}
}

func TestGoalKnowledgeInvalidation_MemoryEviction_FansOutByCompanion(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
	sub := gkiSub(cache, &gkiGoals{})

	if err := sub.HandleMemoryEviction(context.Background(), gkiEnv("evt-9"), CompanionMemoryEvictionPayload{
		CompanionID: gkiCompanion, TenantID: gkiTenant, OwnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("HandleMemoryEviction: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonMemoryEvicted {
		t.Errorf("reason = %q, want memory_evicted", got.InvalidationReason)
	}
}

// --- no target row ---

func TestGoalKnowledgeInvalidation_NoCachedRow_IsNoOpAck(t *testing.T) {
	cache := &gkiCache{} // nothing cached yet
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "fractions")}}
	sub := gkiSub(cache, goals)

	if err := sub.HandleWeaknessGrown(context.Background(), gkiEnv("evt-10"), WeaknessGrownPayload{
		TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions",
	}); err != nil {
		t.Fatalf("no cached reflection must ack, not error: %v", err)
	}
	if cache.upserts != 0 {
		t.Errorf("upserts = %d, want 0", cache.upserts)
	}
}

// --- idempotency (Pub/Sub is at-least-once) ---

// The honest redelivery test: a SECOND pod (fresh in-process tracker) receives
// the same event. State must not move.
func TestGoalKnowledgeInvalidation_RedeliveryToAnotherPodIsIdempotent(t *testing.T) {
	row := gkiFreshRow(t, gkiCompanion, "goal-a")
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{row}}
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "fractions")}}
	env := gkiEnv("evt-11")
	p := WeaknessGrownPayload{TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions"}

	if err := gkiSub(cache, goals).HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	first := cache.find(gkiCompanion, "goal-a")
	firstAt, firstReason := *first.InvalidatedAt, first.InvalidationReason

	// Fresh subscriber == a different pod, whose in-memory tracker has never
	// seen this event id. The DOMAIN must carry idempotency, not the tracker.
	if err := gkiSub(cache, goals).HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got := cache.find(gkiCompanion, "goal-a")
	if !got.InvalidatedAt.Equal(firstAt) {
		t.Errorf("redelivery moved invalidated_at: %v → %v", firstAt, *got.InvalidatedAt)
	}
	if got.InvalidationReason != firstReason {
		t.Errorf("redelivery changed reason: %q → %q", firstReason, got.InvalidationReason)
	}
	if !got.Servable() {
		t.Error("redelivery blanked the cached text")
	}
}

// A redelivered duplicate on the SAME pod short-circuits before touching the repo.
func TestGoalKnowledgeInvalidation_DuplicateOnSamePodSkipsRepo(t *testing.T) {
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "fractions")}}
	sub := gkiSub(cache, goals)
	env := gkiEnv("evt-12")
	p := WeaknessGrownPayload{TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions"}

	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("first: %v", err)
	}
	afterFirst := cache.upserts
	if err := sub.HandleWeaknessGrown(context.Background(), env, p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if cache.upserts != afterFirst {
		t.Errorf("duplicate re-wrote the repo: upserts %d → %d", afterFirst, cache.upserts)
	}
}

// Priority semantics survive the event lane: a noisy chat turn must never mask
// a re-root in the audit trail.
func TestGoalKnowledgeInvalidation_WeakerReasonNeverDowngradesStronger(t *testing.T) {
	row := gkiFreshRow(t, gkiCompanion, "goal-a")
	row.Invalidate(fgk.ReasonGoalRerooted) // strongest signal already recorded
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{row}}
	sub := gkiSub(cache, &gkiGoals{})

	if err := sub.HandleChatTurnCompleted(context.Background(), gkiEnv("evt-13"), CompanionChatTurnCompletedPayload{
		CompanionID: gkiCompanion, TenantID: gkiTenant, LearnerGCID: gkiLearner,
	}); err != nil {
		t.Fatalf("HandleChatTurnCompleted: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonGoalRerooted {
		t.Errorf("reason downgraded to %q — a chat turn masked the re-root", got.InvalidationReason)
	}
}

// --- fail-loud ---

func TestGoalKnowledgeInvalidation_MissingEnvelopeFieldsNACK(t *testing.T) {
	sub := gkiSub(&gkiCache{}, &gkiGoals{})
	if err := sub.HandleWeaknessGrown(context.Background(), events.Envelope{}, WeaknessGrownPayload{
		TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions",
	}); err == nil {
		t.Fatal("an incomplete envelope must NACK, not ack")
	}
}

func TestGoalKnowledgeInvalidation_MissingIdentityNACKs(t *testing.T) {
	sub := gkiSub(&gkiCache{}, &gkiGoals{})
	cases := map[string]func() error{
		"goal_progress: no goal_id": func() error {
			return sub.HandleGoalProgressUpdated(context.Background(), gkiEnv("e-a"), GoalProgressUpdatedPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"goal_graduated: no goal_id": func() error {
			return sub.HandleGoalGraduated(context.Background(), gkiEnv("e-b"), GoalGraduatedPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"chat_turn: no companion_id": func() error {
			return sub.HandleChatTurnCompleted(context.Background(), gkiEnv("e-c"), CompanionChatTurnCompletedPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"memory_eviction: no companion_id": func() error {
			return sub.HandleMemoryEviction(context.Background(), gkiEnv("e-d"), CompanionMemoryEvictionPayload{
				TenantID: gkiTenant, OwnerGCID: gkiLearner,
			})
		},
		"weakness_grown: no concept_key": func() error {
			return sub.HandleWeaknessGrown(context.Background(), gkiEnv("e-e"), WeaknessGrownPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
	}
	for name, run := range cases {
		if err := run(); err == nil {
			t.Errorf("%s: expected a NACK (error), got nil", name)
		}
	}
}

// A repo failure must NACK for redelivery — never a silent ack that leaves a
// stale reflection served as current.
func TestGoalKnowledgeInvalidation_RepoErrorsNACK(t *testing.T) {
	goals := &gkiGoals{goals: []*goal.Goal{gkiGoal("goal-a", goal.StatusActive, "fractions")}}

	t.Run("cache list error", func(t *testing.T) {
		cache := &gkiCache{listErr: errors.New("pg down")}
		sub := gkiSub(cache, goals)
		if err := sub.HandleGoalGraduated(context.Background(), gkiEnv("evt-14"), GoalGraduatedPayload{
			GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
		}); err == nil {
			t.Fatal("a cache read failure must NACK")
		}
	})

	t.Run("upsert error", func(t *testing.T) {
		cache := &gkiCache{
			rows:      []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")},
			upsertErr: errors.New("pg write failed"),
		}
		sub := gkiSub(cache, goals)
		if err := sub.HandleGoalGraduated(context.Background(), gkiEnv("evt-15"), GoalGraduatedPayload{
			GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
		}); err == nil {
			t.Fatal("a cache write failure must NACK")
		}
	})

	t.Run("goal list error on the concept path", func(t *testing.T) {
		cache := &gkiCache{rows: []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")}}
		sub := gkiSub(cache, &gkiGoals{listErr: errors.New("pg down")})
		if err := sub.HandleWeaknessGrown(context.Background(), gkiEnv("evt-16"), WeaknessGrownPayload{
			TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions",
		}); err == nil {
			t.Fatal("a goal read failure must NACK — silently skipping would leave the reflection stale")
		}
	})
}

// A failed handler must NOT burn the dedupe key, or the redelivery is swallowed
// as a duplicate and the reflection stays stale forever (the CHO-2130 bug).
func TestGoalKnowledgeInvalidation_FailureDoesNotBurnDedupeKey(t *testing.T) {
	cache := &gkiCache{
		rows:      []*fgk.GoalKnowledge{gkiFreshRow(t, gkiCompanion, "goal-a")},
		upsertErr: errors.New("transient pg failure"),
	}
	sub := gkiSub(cache, &gkiGoals{})
	env := gkiEnv("evt-17")
	p := GoalGraduatedPayload{GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner}

	if err := sub.HandleGoalGraduated(context.Background(), env, p); err == nil {
		t.Fatal("expected the upsert failure to NACK")
	}
	// Pub/Sub redelivers the SAME event id; the recovered repo must now apply it.
	cache.upsertErr = nil
	if err := sub.HandleGoalGraduated(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery after recovery: %v", err)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidatedAt == nil {
		t.Error("redelivery was ack-dropped as a duplicate — the invalidation was lost")
	}
}

// EVERY handler must reject an incomplete envelope — not just the one that
// happened to get a test. A subscriber that acks an unvalidated event is a
// silent hole in the lane.
func TestGoalKnowledgeInvalidation_AllSixHandlersRejectABadEnvelope(t *testing.T) {
	sub := gkiSub(&gkiCache{}, &gkiGoals{})
	bad := events.Envelope{} // no event_id, no tenant, no traceparent…

	handlers := map[string]func() error{
		"weakness.grown": func() error {
			return sub.HandleWeaknessGrown(context.Background(), bad, WeaknessGrownPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner, ConceptKey: "fractions",
			})
		},
		"weakness.analyzed": func() error {
			return sub.HandleWeaknessAnalyzed(context.Background(), bad, WeaknessAnalyzedPayload{
				TenantID: gkiTenant, LearnerGCID: gkiLearner,
				Edges: []ExtractedGrowthEdgePayload{{ConceptKey: "fractions"}},
			})
		},
		"goal.progress_updated": func() error {
			return sub.HandleGoalProgressUpdated(context.Background(), bad, GoalProgressUpdatedPayload{
				GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"goal.graduated": func() error {
			return sub.HandleGoalGraduated(context.Background(), bad, GoalGraduatedPayload{
				GoalID: "goal-a", TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"companion.chat_turn_completed": func() error {
			return sub.HandleChatTurnCompleted(context.Background(), bad, CompanionChatTurnCompletedPayload{
				CompanionID: gkiCompanion, TenantID: gkiTenant, LearnerGCID: gkiLearner,
			})
		},
		"companion.memory_eviction": func() error {
			return sub.HandleMemoryEviction(context.Background(), bad, CompanionMemoryEvictionPayload{
				CompanionID: gkiCompanion, TenantID: gkiTenant, OwnerGCID: gkiLearner,
			})
		},
	}
	for name, run := range handlers {
		if err := run(); err == nil {
			t.Errorf("%s: acked an event with an incomplete envelope — must NACK", name)
		}
	}
}

func TestNewGoalKnowledgeInvalidationSubscriber_PanicsOnNilInvalidator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a nil invalidator is a wiring bug — must panic at construction, not fail open")
		}
	}()
	NewGoalKnowledgeInvalidationSubscriber(nil)
}

func TestNewGoalKnowledgeInvalidator_PanicsOnNilDeps(t *testing.T) {
	for name, build := range map[string]func(){
		"nil cache": func() { NewGoalKnowledgeInvalidator(nil, &gkiGoals{}) },
		"nil goals": func() { NewGoalKnowledgeInvalidator(&gkiCache{}, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("a nil dependency is a wiring bug — it must panic at construction, not fail open at consume time")
				}
			}()
			build()
		})
	}
}

// The in-handler re-root path (goal PATCH emits no event) shares this entry point.
func TestGoalKnowledgeInvalidator_InvalidateGoal_RerootIsTheStrongestSignal(t *testing.T) {
	row := gkiFreshRow(t, gkiCompanion, "goal-a")
	row.Invalidate(fgk.ReasonChatTurnCompleted) // a weak signal got there first
	cache := &gkiCache{rows: []*fgk.GoalKnowledge{row}}
	inv := NewGoalKnowledgeInvalidator(cache, &gkiGoals{})

	n, err := inv.InvalidateGoal(context.Background(), gkiTenant, gkiLearner, "goal-a", fgk.ReasonGoalRerooted)
	if err != nil {
		t.Fatalf("InvalidateGoal: %v", err)
	}
	if n != 1 {
		t.Errorf("invalidated %d rows, want 1", n)
	}
	if got := cache.find(gkiCompanion, "goal-a"); got.InvalidationReason != fgk.ReasonGoalRerooted {
		t.Errorf("reason = %q, want goal_rerooted to override the weaker chat_turn_completed", got.InvalidationReason)
	}
}
