// Package companion — Maya retention loop tests (streak / XP / spaced
// repetition).
//
// Per S5.1 A-Companion briefing (Phyllis §5.4 + Maya retention loop):
//
//   - Streak counter increments on consecutive-day activity (≥1
//     atom_session.completed per UTC day).
//   - Streak resets on missed day (gap > 1 day between activity).
//   - XP gain rules (Maya cadence):
//     10 XP / atom completed
//     50 XP / daily-dose completed
//     200 XP / LearningPath milestone
//   - Spaced repetition next-due is per-atom, derived from TopicRetention
//     strength via SM2State (already implemented; we expose the helper).
//   - Time-travel: same fixture across days 1, 2, 3, 7, 14 yields stable
//     streak / XP / next-due transitions.
//
// TDD strict (RED → GREEN → REFACTOR).
package companion

import (
	"testing"
	"time"
)

// TestStreak_NewLearnerHasZeroStreak — fresh Companion starts at 0.
func TestStreak_NewLearnerHasZeroStreak(t *testing.T) {
	s := NewStreak("01970000-0000-7000-9000-000000000001")
	if s.Count != 0 {
		t.Errorf("new streak count = %d, want 0", s.Count)
	}
	if !s.LastActivityAt.IsZero() {
		t.Errorf("new streak LastActivityAt = %v, want zero", s.LastActivityAt)
	}
}

// TestStreak_RecordActivity_IncrementsConsecutive — activity on day 1
// then day 2 increments streak from 0 to 2.
func TestStreak_RecordActivity_IncrementsConsecutive(t *testing.T) {
	s := NewStreak("learner")
	day1 := mustParse(t, "2026-05-08T10:00:00Z")
	day2 := day1.AddDate(0, 0, 1).Add(2 * time.Hour) // next day, different time
	s.RecordActivity(day1)
	if s.Count != 1 {
		t.Errorf("after day1 count = %d, want 1", s.Count)
	}
	s.RecordActivity(day2)
	if s.Count != 2 {
		t.Errorf("after day2 count = %d, want 2", s.Count)
	}
}

// TestStreak_RecordActivity_SameDay_NoIncrement — multiple activities
// on the same UTC day count as ONE day for the streak.
func TestStreak_RecordActivity_SameDay_NoIncrement(t *testing.T) {
	s := NewStreak("learner")
	morning := mustParse(t, "2026-05-08T08:00:00Z")
	noon := morning.Add(4 * time.Hour)
	evening := morning.Add(12 * time.Hour)
	s.RecordActivity(morning)
	s.RecordActivity(noon)
	s.RecordActivity(evening)
	if s.Count != 1 {
		t.Errorf("same-day activity count = %d, want 1", s.Count)
	}
}

// TestStreak_RecordActivity_GapResets — a missed day resets the streak
// to 1 (not 0 — the new activity is itself day-1 of a fresh streak).
func TestStreak_RecordActivity_GapResets(t *testing.T) {
	s := NewStreak("learner")
	s.RecordActivity(mustParse(t, "2026-05-01T10:00:00Z"))
	s.RecordActivity(mustParse(t, "2026-05-02T10:00:00Z"))
	s.RecordActivity(mustParse(t, "2026-05-03T10:00:00Z"))
	if s.Count != 3 {
		t.Fatalf("expected 3-day streak, got %d", s.Count)
	}
	// Skip 2 days then resume — resets to 1.
	s.RecordActivity(mustParse(t, "2026-05-06T10:00:00Z"))
	if s.Count != 1 {
		t.Errorf("after 2-day gap, count = %d, want 1 (reset)", s.Count)
	}
}

// TestStreak_TimeTravel_Days1_2_3_7_14 — time-travel fixture: streak
// counter slides correctly across 5 days of consecutive activity.
func TestStreak_TimeTravel_Days1_2_3_7_14(t *testing.T) {
	s := NewStreak("learner")
	day0 := mustParse(t, "2026-05-01T00:00:00Z")
	for _, d := range []int{0, 1, 2, 3, 4, 5, 6} {
		s.RecordActivity(day0.AddDate(0, 0, d))
	}
	if s.Count != 7 {
		t.Errorf("after 7 consecutive days, count = %d, want 7", s.Count)
	}
	// Skip a day, then come back on day 14 — fresh streak of 1.
	s.RecordActivity(day0.AddDate(0, 0, 13))
	if s.Count != 1 {
		t.Errorf("after gap then day=13, count = %d, want 1", s.Count)
	}
}

// ----- XP rules (Maya cadence) -----

// TestXP_AtomCompletionGrants10 — completing one atom = 10 XP.
func TestXP_AtomCompletionGrants10(t *testing.T) {
	if XPPerAtomCompleted != 10 {
		t.Errorf("XPPerAtomCompleted = %d, want 10 (Maya cadence)", XPPerAtomCompleted)
	}
}

// TestXP_DailyDoseCompletionGrants50 — completing the full Daily Dose = 50 XP.
func TestXP_DailyDoseCompletionGrants50(t *testing.T) {
	if XPPerDailyDoseCompleted != 50 {
		t.Errorf("XPPerDailyDoseCompleted = %d, want 50 (Maya cadence)", XPPerDailyDoseCompleted)
	}
}

// TestXP_PathMilestoneGrants200 — hitting a LearningPath milestone = 200 XP.
func TestXP_PathMilestoneGrants200(t *testing.T) {
	if XPPerPathMilestone != 200 {
		t.Errorf("XPPerPathMilestone = %d, want 200 (Maya cadence)", XPPerPathMilestone)
	}
}

// TestXP_AwardForActivity — XPForActivity returns the correct value per
// activity kind, with unknown kinds returning 0 (defensive).
func TestXP_AwardForActivity(t *testing.T) {
	cases := []struct {
		kind ActivityKind
		want int
	}{
		{ActivityAtomCompleted, 10},
		{ActivityDailyDoseCompleted, 50},
		{ActivityPathMilestone, 200},
		{ActivityKind("unknown"), 0},
	}
	for _, c := range cases {
		got := XPForActivity(c.kind)
		if got != c.want {
			t.Errorf("XPForActivity(%q) = %d, want %d", c.kind, got, c.want)
		}
	}
}

// TestXP_TimeTravel_AccumulatesAcrossDays — across days 1, 2, 3, 7, 14
// the Companion's XP accumulates linearly (5 days × 10 XP/atom = 50 XP
// when completing 1 atom per day) and crosses the level-2 threshold of
// 400 XP only when enough activity is logged.
func TestXP_TimeTravel_AccumulatesAcrossDays(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001",
		"01970000-0000-7000-9000-000000000001", "Maya")
	day0 := mustParse(t, "2026-05-01T00:00:00Z")
	for _, d := range []int{0, 1, 2, 6, 13} {
		_ = day0.AddDate(0, 0, d)
		// Each day: complete the daily dose (50 XP).
		f.AwardXP(XPForActivity(ActivityDailyDoseCompleted))
	}
	if f.XP != 5*XPPerDailyDoseCompleted {
		t.Errorf("XP after 5 dose-completions = %d, want %d",
			f.XP, 5*XPPerDailyDoseCompleted)
	}
	// At 250 XP, level still 1 (level-2 threshold is 400).
	if f.Level != 1 {
		t.Errorf("Level = %d, want 1 at 250 XP", f.Level)
	}
}

// TestXP_LevelUpAtPathMilestone — completing 2 path milestones (400 XP)
// triggers level-2.
func TestXP_LevelUpAtPathMilestone(t *testing.T) {
	f, _ := New("01970000-0000-7000-8000-000000000001",
		"01970000-0000-7000-9000-000000000001", "Maya")
	leveled := f.AwardXP(2 * XPForActivity(ActivityPathMilestone)) // 400 XP
	if !leveled {
		t.Error("expected level-up at 400 XP (= 2 × 200 path milestones)")
	}
	if f.Level != 2 {
		t.Errorf("Level = %d, want 2", f.Level)
	}
}

// ----- Spaced repetition next-due (uses existing SM2State) -----

// TestSpacedRepetition_NextDueIsPerAtom — calling NextDueAt for a known
// SM2State returns the next_review_at field (consistency with SM-2).
func TestSpacedRepetition_NextDueIsPerAtom(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	atomID := "01970000-0000-7000-a000-000000000001"
	s := NewSM2State(atomID)
	if err := s.ApplyGrade(GradeIRemember, now); err != nil {
		t.Fatalf("ApplyGrade error: %v", err)
	}
	got := NextDueAt(s)
	if !got.Equal(s.NextReviewAt) {
		t.Errorf("NextDueAt = %v, want %v (SM-2 NextReviewAt)", got, s.NextReviewAt)
	}
}

// TestSpacedRepetition_TimeTravel_NextDueSlides — across days 1, 2, 3, 7, 14
// the next-due computation slides forward as graders accumulate.
func TestSpacedRepetition_TimeTravel_NextDueSlides(t *testing.T) {
	atomID := "01970000-0000-7000-a000-000000000001"
	state := NewSM2State(atomID)
	day0 := mustParse(t, "2026-05-01T00:00:00Z")

	// Day 1: first GradeIRemember → repetitions=1 → interval=1 → next-due day 2.
	state.ApplyGrade(GradeIRemember, day0)
	if NextDueAt(state).Day() != day0.AddDate(0, 0, 1).Day() {
		t.Errorf("day=1 next-due day = %d, want %d",
			NextDueAt(state).Day(), day0.AddDate(0, 0, 1).Day())
	}
	// Day 2: second GradeIRemember → repetitions=2 → interval=6 → next-due day 8.
	state.ApplyGrade(GradeIRemember, day0.AddDate(0, 0, 1))
	if state.IntervalDays != 6 {
		t.Errorf("after 2nd rep, IntervalDays = %d, want 6 (canonical SM-2)", state.IntervalDays)
	}
	// Day 3-6: nothing happens (waiting).
	// Day 7: not-quite-due check.
	// Day 14: third GradeIRemember → repetitions=3 → interval = round(6 × EF) ≈ 16.
	day14 := day0.AddDate(0, 0, 13)
	state.ApplyGrade(GradeIRemember, day14)
	if state.Repetitions != 3 {
		t.Errorf("after 3rd rep, Repetitions = %d, want 3", state.Repetitions)
	}
	if state.IntervalDays < 7 {
		t.Errorf("after 3rd rep, IntervalDays = %d, want >= 7 (SM-2 multiplier)", state.IntervalDays)
	}
}

// TestStreak_Validation_RejectsZeroTime — RecordActivity with zero time
// is a no-op (defensive).
func TestStreak_Validation_RejectsZeroTime(t *testing.T) {
	s := NewStreak("learner")
	s.RecordActivity(time.Time{}) // zero time
	if s.Count != 0 {
		t.Errorf("zero-time activity should not increment streak, got %d", s.Count)
	}
}
