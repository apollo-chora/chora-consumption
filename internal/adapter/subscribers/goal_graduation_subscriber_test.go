// goal_graduation_subscriber_test.go — behaviour coverage for the §3 Goal
// graduation subscriber (CHO-1962): graduate at 100%, progress below 100%,
// idempotent redelivery, curiosity skip, unchanged-count no-op, nil-publisher
// persistence, and fail-loud on a mastered-set read error.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

const (
	ggTenant  = "11111111-1111-7111-8111-111111111111"
	ggLearner = "22222222-2222-7222-8222-222222222222"
)

var ggNow = time.Date(2026, 6, 29, 15, 0, 0, 0, time.UTC)

// --- fakes ---

type ggFakeGoalRepo struct {
	goals     []*goal.Goal
	listErr   error
	updateErr error
	updateCnt int
}

func (r *ggFakeGoalRepo) Create(context.Context, *goal.Goal) error { return nil }
func (r *ggFakeGoalRepo) GetByID(context.Context, string, string, string) (*goal.Goal, error) {
	return nil, nil
}
func (r *ggFakeGoalRepo) ListByLearner(context.Context, string, string) ([]*goal.Goal, error) {
	return r.goals, r.listErr
}
func (r *ggFakeGoalRepo) Update(_ context.Context, g *goal.Goal) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	r.updateCnt++
	return nil
}

type ggFakeGrownLister struct {
	keys []string // grown concept keys
	err  error
}

func (l *ggFakeGrownLister) ListAll(context.Context, lw.ListQuery) ([]lw.LearnerWeakness, error) {
	if l.err != nil {
		return nil, l.err
	}
	out := make([]lw.LearnerWeakness, 0, len(l.keys))
	for _, k := range l.keys {
		out = append(out, lw.LearnerWeakness{ConceptKey: k, Status: lw.StatusGrown})
	}
	return out, nil
}

// --- helpers ---

func ggEnv() events.Envelope {
	return events.Envelope{
		EventID:        "01971a90-bbbb-7000-8000-000000000001",
		IdempotencyKey: "wg-idem-1",
		TenantID:       ggTenant,
		GCID:           ggLearner,
		OccurredAt:     ggNow,
		PublishedAt:    ggNow,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  events.SourceProject,
		SourceService:  events.SourceService,
		SchemaVersion:  1,
	}
}

func ggPayload() WeaknessGrownPayload {
	return WeaknessGrownPayload{
		GrowthEdgeID: "edge-1",
		TenantID:     ggTenant,
		LearnerGCID:  ggLearner,
		ConceptKey:   "single-responsibility",
	}
}

func ggActiveGoal(id string, concepts []string, masteredCount int) *goal.Goal {
	return &goal.Goal{
		GoalID: id, TenantID: ggTenant, LearnerGCID: ggLearner,
		Kind: goal.KindThemeMastery, ConceptSet: concepts, Status: goal.StatusActive,
		MasteredConceptCount: masteredCount, CreatedAt: ggNow, UpdatedAt: ggNow,
	}
}

func ggSub(repo *ggFakeGoalRepo, lister *ggFakeGrownLister, pub events.Publisher) *GoalGraduationSubscriber {
	s := NewGoalGraduationSubscriber(repo, lister).WithClock(func() time.Time { return ggNow })
	if pub != nil {
		s = s.WithPublisher(pub)
	}
	return s
}

// --- tests ---

func TestGoalGraduation_GraduatesWhenAllConceptsMastered(t *testing.T) {
	g := ggActiveGoal("g1", []string{"Single Responsibility", "Open Closed"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	// grown keys are already-normalised slugs; CountMastered normalises the goal's
	// concept labels the same way, so the intersection is full → graduate.
	lister := &ggFakeGrownLister{keys: []string{"single-responsibility", "open-closed"}}
	pub := events.NewInMemoryPublisher()

	if err := ggSub(repo, lister, pub).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if g.Status != goal.StatusAchieved {
		t.Errorf("status = %q; want achieved", g.Status)
	}
	if g.MasteredConceptCount != 2 {
		t.Errorf("mastered_concept_count = %d; want 2", g.MasteredConceptCount)
	}
	if repo.updateCnt != 1 {
		t.Errorf("Update called %d times; want 1", repo.updateCnt)
	}
	evs := pub.Events()
	if len(evs) != 1 {
		t.Fatalf("published %d events; want 1", len(evs))
	}
	ev := evs[0]
	if ev.Topic != events.TopicGoalGraduated {
		t.Errorf("topic = %q; want %q", ev.Topic, events.TopicGoalGraduated)
	}
	if ev.Payload["goal_id"] != "g1" || ev.Payload["kind"] != "theme_mastery" {
		t.Errorf("payload goal_id/kind = %v/%v", ev.Payload["goal_id"], ev.Payload["kind"])
	}
	if ev.Payload["mastered_concepts"] != 2 || ev.Payload["total_concepts"] != 2 {
		t.Errorf("mastered/total = %v/%v; want 2/2", ev.Payload["mastered_concepts"], ev.Payload["total_concepts"])
	}
	wantKey := "chora.consumption.goal.graduated:g1:" + ggNow.Format(time.RFC3339Nano)
	if ev.Envelope.IdempotencyKey != wantKey {
		t.Errorf("idempotency_key = %q; want %q", ev.Envelope.IdempotencyKey, wantKey)
	}
}

func TestGoalGraduation_EmitsProgressBelow100(t *testing.T) {
	g := ggActiveGoal("g2", []string{"a", "b", "c"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a", "b"}} // 2 of 3
	pub := events.NewInMemoryPublisher()

	if err := ggSub(repo, lister, pub).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if g.Status != goal.StatusActive {
		t.Errorf("status = %q; want still active", g.Status)
	}
	if g.MasteredConceptCount != 2 {
		t.Errorf("count = %d; want 2", g.MasteredConceptCount)
	}
	evs := pub.Events()
	if len(evs) != 1 || evs[0].Topic != events.TopicGoalProgressUpdated {
		t.Fatalf("want exactly 1 progress event; got %d %+v", len(evs), evs)
	}
	if evs[0].Payload["progress_percent"] != 67 { // round(100*2/3)
		t.Errorf("progress_percent = %v; want 67", evs[0].Payload["progress_percent"])
	}
}

func TestGoalGraduation_IdempotentOnRedelivery(t *testing.T) {
	g := ggActiveGoal("g1", []string{"a", "b"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a", "b"}}
	pub := events.NewInMemoryPublisher()
	sub := ggSub(repo, lister, pub)

	env := ggEnv()
	for i := 0; i < 2; i++ {
		if err := sub.Handle(context.Background(), env, ggPayload()); err != nil {
			t.Fatalf("Handle #%d: %v", i, err)
		}
	}
	if n := len(pub.Events()); n != 1 {
		t.Errorf("published %d events across redelivery; want exactly 1", n)
	}
}

func TestGoalGraduation_CuriosityGoalSkipped(t *testing.T) {
	g := ggActiveGoal("gc", []string{}, 0) // empty concept set
	g.Kind = goal.KindCuriosity
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a", "b"}}
	pub := events.NewInMemoryPublisher()

	if err := ggSub(repo, lister, pub).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if g.Status != goal.StatusActive {
		t.Errorf("curiosity goal mutated: %q", g.Status)
	}
	if repo.updateCnt != 0 || len(pub.Events()) != 0 {
		t.Errorf("curiosity goal must not persist/emit; updates=%d events=%d", repo.updateCnt, len(pub.Events()))
	}
}

func TestGoalGraduation_UnchangedCountNoEvent(t *testing.T) {
	g := ggActiveGoal("g5", []string{"a", "b"}, 1) // stored count already 1
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a"}} // still 1 of 2
	pub := events.NewInMemoryPublisher()

	if err := ggSub(repo, lister, pub).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if repo.updateCnt != 0 || len(pub.Events()) != 0 {
		t.Errorf("unchanged count must not persist/emit; updates=%d events=%d", repo.updateCnt, len(pub.Events()))
	}
}

func TestGoalGraduation_NilPublisherStillPersists(t *testing.T) {
	g := ggActiveGoal("g6", []string{"a"}, 0)
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{g}}
	lister := &ggFakeGrownLister{keys: []string{"a"}}

	if err := ggSub(repo, lister, nil).Handle(context.Background(), ggEnv(), ggPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if g.Status != goal.StatusAchieved {
		t.Errorf("status = %q; want achieved (persist without publisher)", g.Status)
	}
	if repo.updateCnt != 1 {
		t.Errorf("Update called %d times; want 1", repo.updateCnt)
	}
}

func TestGoalGraduation_MasteredReadErrorFailsLoud(t *testing.T) {
	repo := &ggFakeGoalRepo{goals: []*goal.Goal{ggActiveGoal("g7", []string{"a"}, 0)}}
	lister := &ggFakeGrownLister{err: context.DeadlineExceeded}

	if err := ggSub(repo, lister, events.NewInMemoryPublisher()).Handle(context.Background(), ggEnv(), ggPayload()); err == nil {
		t.Fatal("want fail-loud error when the mastered-set read fails")
	}
}

func TestGoalGraduation_RejectsInvalidEnvelope(t *testing.T) {
	sub := ggSub(&ggFakeGoalRepo{}, &ggFakeGrownLister{}, nil)
	if err := sub.Handle(context.Background(), events.Envelope{}, ggPayload()); err == nil {
		t.Fatal("want error on an incomplete envelope")
	}
}
