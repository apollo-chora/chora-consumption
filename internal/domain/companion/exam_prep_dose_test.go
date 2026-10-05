// BE-EP1 — Tests for the exam-prep-coach enrichment of the Daily Dose.
//
// When a learner has an active exam-prep goal, the Companion daily dose
// should swap its curiosity slot for atoms recommended by the
// chora-ai-kernel-orchestrator exam-prep-coach (PE-02). This file owns
// the pure-domain test contract — the HTTP adapter is wired separately.
//
// TDD strict (RED → GREEN → REFACTOR per .claude/rules/development-execution.md).
package companion

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeExamPrepCoach captures inputs + returns canned weakness-drill recs
// so domain tests run without an HTTP round-trip.
type fakeExamPrepCoach struct {
	calls []ExamPrepCoachRequest
	resp  *ExamPrepCoachRecommendation
	err   error
}

func (f *fakeExamPrepCoach) RecommendDose(_ context.Context, req ExamPrepCoachRequest) (*ExamPrepCoachRecommendation, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// fixtureExamPrepSeeds returns 8 atoms across 4 topics, including 2 atoms
// each on the canonical exam-prep weakness topics ("velocity_estimation",
// "empiricism") so the coach can pick from them.
func fixtureExamPrepSeeds() []AtomSeed {
	topics := []string{"velocity_estimation", "empiricism", "kanban", "agile"}
	letters := []string{"a", "b"}
	seeds := make([]AtomSeed, 0, 8)
	for i, topic := range topics {
		for j, letter := range letters {
			seeds = append(seeds, AtomSeed{
				AtomID: "01970000-0000-7000-b000-0000000000" + leftpad(i, 1) + leftpad(j, 1),
				Topic:  topic,
				Title:  topic + " atom " + letter,
			})
		}
	}
	return seeds
}

// TestComposeDailyDoseWithCoach_NilGoalFallsBackToVanilla — no exam-prep
// goal => same composition as ComposeDailyDose (no coach call).
func TestComposeDailyDoseWithCoach_NilGoalFallsBackToVanilla(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	coach := &fakeExamPrepCoach{}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds:      seeds,
		States:     map[string]SM2State{},
		SeenTopics: map[string]bool{},
		Now:        now,
	}, coach, nil) // nil goal => vanilla path

	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
	if len(coach.calls) != 0 {
		t.Errorf("coach should NOT be called when goal nil; got %d calls", len(coach.calls))
	}
}

// TestComposeDailyDoseWithCoach_NilCoachFallsBackToVanilla — even with a
// goal, a nil coach port must NOT panic; it falls back to vanilla. This
// keeps the demo flow alive when the orchestrator base URL is unset
// (per feedback_no_inline_config — config-absent ≠ runtime crash).
func TestComposeDailyDoseWithCoach_NilCoachFallsBackToVanilla(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	goal := &ExamPrepGoal{
		LearnerGCID: "018f-marcus",
		ExamID:      "scrum-master-cert",
		ExamDate:    now.AddDate(0, 0, 14),
		Retention: []ExamPrepTopicRetention{
			{Topic: "velocity_estimation", Score: 0.10},
		},
	}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, nil, goal)

	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("dose size = %d, want %d (vanilla fallback)", len(dose.Entries), DoseTargetSize)
	}
}

// TestComposeDailyDoseWithCoach_GoalReplacesCuriosityWithDrills — when
// learner has an active exam-prep goal AND coach returns recs, the
// curiosity slot is replaced with weakness-drill atoms tagged
// DoseReasonExamPrepDrill.
func TestComposeDailyDoseWithCoach_GoalReplacesCuriosityWithDrills(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{
			Topics: []string{"velocity_estimation", "empiricism"},
		},
	}
	goal := &ExamPrepGoal{
		LearnerGCID: "018f-marcus",
		ExamID:      "scrum-master-cert",
		ExamDate:    now.AddDate(0, 0, 14),
		Retention: []ExamPrepTopicRetention{
			{Topic: "velocity_estimation", Score: 0.10},
			{Topic: "empiricism", Score: 0.30},
			{Topic: "kanban", Score: 0.85},
		},
	}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds:      seeds,
		States:     map[string]SM2State{},
		SeenTopics: map[string]bool{},
		Now:        now,
	}, coach, goal)

	if len(coach.calls) != 1 {
		t.Fatalf("coach calls = %d, want 1", len(coach.calls))
	}
	if coach.calls[0].LearnerGCID != "018f-marcus" {
		t.Errorf("LearnerGCID = %q, want 018f-marcus", coach.calls[0].LearnerGCID)
	}
	if coach.calls[0].ExamID != "scrum-master-cert" {
		t.Errorf("ExamID = %q", coach.calls[0].ExamID)
	}
	if len(coach.calls[0].Retention) != 3 {
		t.Errorf("Retention forwarded = %d, want 3", len(coach.calls[0].Retention))
	}

	// Total ≤ DoseTargetSize; at least one drill entry.
	if len(dose.Entries) > DoseTargetSize {
		t.Errorf("dose size = %d, MUST NOT exceed %d", len(dose.Entries), DoseTargetSize)
	}
	drills := countByReason(dose.Entries, DoseReasonExamPrepDrill)
	if drills < 1 {
		t.Errorf("drill entries = %d, want ≥1 (coach-recommended atoms)", drills)
	}
	cur := countByReason(dose.Entries, DoseReasonCuriosity)
	// Curiosity slot is REPLACED — must not exceed drill+curiosity total budget.
	if drills+cur > DoseCuriosityCount {
		t.Errorf("drills(%d)+curiosity(%d) = %d, must not exceed curiosity budget %d",
			drills, cur, drills+cur, DoseCuriosityCount)
	}

	// Every drill entry MUST come from a coach-recommended topic.
	allowed := map[string]bool{"velocity_estimation": true, "empiricism": true}
	for _, e := range dose.Entries {
		if e.DoseReason == DoseReasonExamPrepDrill && !allowed[e.Topic] {
			t.Errorf("drill entry topic = %q, not in coach recs", e.Topic)
		}
	}
}

// TestComposeDailyDoseWithCoach_CoachErrorFallsBackGracefully — when the
// coach RPC fails, the dose still composes via the vanilla algorithm. The
// learner's experience MUST not 5xx because of an upstream blip.
func TestComposeDailyDoseWithCoach_CoachErrorFallsBackGracefully(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{err: errors.New("coach down")}
	goal := &ExamPrepGoal{
		LearnerGCID: "g",
		ExamID:      "e",
		ExamDate:    now.AddDate(0, 0, 7),
		Retention: []ExamPrepTopicRetention{
			{Topic: "empiricism", Score: 0.20},
		},
	}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, coach, goal)

	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("dose size = %d, want %d (graceful fallback)",
			len(dose.Entries), DoseTargetSize)
	}
	// No drill entries when coach failed.
	if d := countByReason(dose.Entries, DoseReasonExamPrepDrill); d != 0 {
		t.Errorf("drill entries on coach error = %d, want 0", d)
	}
}

// TestComposeDailyDoseWithCoach_RespectsContextCancellation — context
// cancellation propagates to the coach call. The coach port receives the
// caller's context exactly so it can honour timeouts + deadlines.
func TestComposeDailyDoseWithCoach_RespectsContextCancellation(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	captured := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"empiricism"}},
	}
	goal := &ExamPrepGoal{
		LearnerGCID: "g",
		ExamID:      "e",
		ExamDate:    now.AddDate(0, 0, 7),
	}

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_ = ComposeDailyDoseWithCoach(ctx, DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, captured, goal)

	if len(captured.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(captured.calls))
	}
	if v := captured.calls[0].Ctx.Value(ctxKey{}); v != "marker" {
		t.Errorf("ctx not propagated; got %v", v)
	}
}

// TestComposeDailyDoseWithCoach_RecruitsFreshDrillsFromSeeds — when the
// vanilla dose has no curiosity entries that match the coach's
// recommended topics, the swap MUST recruit fresh atoms from the seed
// catalogue (Step 2 path in swapCuriosityForDrills).
func TestComposeDailyDoseWithCoach_RecruitsFreshDrillsFromSeeds(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()

	// Force the vanilla composer to fill curiosity from kanban + agile by
	// marking ONLY those topics as seen — velocity_estimation + empiricism
	// have NO seen-state, so they cannot be picked as curiosity entries.
	seenTopics := map[string]bool{"kanban": true, "agile": true}

	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{
			Topics: []string{"velocity_estimation"},
		},
	}
	goal := &ExamPrepGoal{
		LearnerGCID: "g",
		ExamID:      "e",
		ExamDate:    now.AddDate(0, 0, 7),
	}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds:      seeds,
		States:     map[string]SM2State{},
		SeenTopics: seenTopics,
		Now:        now,
	}, coach, goal)

	if d := countByReason(dose.Entries, DoseReasonExamPrepDrill); d < 1 {
		t.Errorf("expected ≥1 drill recruited from seeds, got %d", d)
	}
	for _, e := range dose.Entries {
		if e.DoseReason == DoseReasonExamPrepDrill && e.Topic != "velocity_estimation" {
			t.Errorf("drill topic = %q, want velocity_estimation", e.Topic)
		}
	}
}

// TestComposeDailyDoseWithCoach_EmptyTopicsKeepsVanilla — when the coach
// returns an empty Topics slice (e.g. learner already mastered all
// recommended topics), the swap is a no-op and the dose is the vanilla
// 2-1-2 mix unchanged.
func TestComposeDailyDoseWithCoach_EmptyTopicsKeepsVanilla(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	coach := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{}},
	}
	goal := &ExamPrepGoal{
		LearnerGCID: "g",
		ExamID:      "e",
		ExamDate:    now.AddDate(0, 0, 7),
	}

	dose := ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, coach, goal)

	if d := countByReason(dose.Entries, DoseReasonExamPrepDrill); d != 0 {
		t.Errorf("drill entries on empty topics = %d, want 0", d)
	}
	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}
}

// TestComposeDailyDoseWithCoach_AppliesDefaultThreshold — a goal with
// WeaknessThreshold=0 sends the canonical default (0.50) per UX flow
// VS05.WeaknessDrill instead of forwarding the zero unchanged.
func TestComposeDailyDoseWithCoach_AppliesDefaultThreshold(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	captured := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"velocity_estimation"}},
	}
	goal := &ExamPrepGoal{
		LearnerGCID:       "g",
		ExamID:            "e",
		ExamDate:          now.AddDate(0, 0, 7),
		WeaknessThreshold: 0, // forces default
	}

	_ = ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, captured, goal)

	if len(captured.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(captured.calls))
	}
	if got := captured.calls[0].Threshold; got != defaultWeaknessThreshold {
		t.Errorf("Threshold = %v, want default %v", got, defaultWeaknessThreshold)
	}
}

// TestComposeDailyDoseWithCoach_HonorsCustomThreshold — a non-zero goal
// threshold passes through verbatim.
func TestComposeDailyDoseWithCoach_HonorsCustomThreshold(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	captured := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"velocity_estimation"}},
	}
	goal := &ExamPrepGoal{
		LearnerGCID:       "g",
		ExamID:            "e",
		ExamDate:          now.AddDate(0, 0, 7),
		WeaknessThreshold: 0.30,
	}

	_ = ComposeDailyDoseWithCoach(context.Background(), DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, captured, goal)

	if len(captured.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(captured.calls))
	}
	if got := captured.calls[0].Threshold; got != 0.30 {
		t.Errorf("Threshold = %v, want 0.30", got)
	}
}

// TestComposeDailyDoseWithCoach_AllowsContextDeadlinePropagation —
// passing a deadline-bound ctx must be honoured by the port; the coach
// adapter is expected to propagate W3C traceparent on top.
func TestComposeDailyDoseWithCoach_AllowsContextDeadlinePropagation(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureExamPrepSeeds()
	captured := &fakeExamPrepCoach{
		resp: &ExamPrepCoachRecommendation{Topics: []string{"empiricism"}},
	}
	goal := &ExamPrepGoal{
		LearnerGCID: "g",
		ExamID:      "e",
		ExamDate:    now.AddDate(0, 0, 7),
	}
	deadline := time.Now().Add(2 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_ = ComposeDailyDoseWithCoach(ctx, DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	}, captured, goal)
	if len(captured.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(captured.calls))
	}
	if dl, ok := captured.calls[0].Ctx.Deadline(); !ok || !dl.Equal(deadline) {
		t.Errorf("deadline not propagated; got dl=%v ok=%v", dl, ok)
	}
}
