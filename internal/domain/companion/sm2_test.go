package companion

import (
	"math"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

// TestSM2_IRememberBumpsInterval verifies "i_remember" increments repetitions
// and pushes the next-review-date forward.
func TestSM2_IRememberBumpsInterval(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := NewSM2State("01970000-0000-7000-a000-000000000001")
	if err := state.ApplyGrade(GradeIRemember, now); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if state.Repetitions != 1 {
		t.Errorf("Repetitions = %d, want 1", state.Repetitions)
	}
	if state.IntervalDays != 1 {
		t.Errorf("IntervalDays = %d, want 1 (first repetition)", state.IntervalDays)
	}
	if !state.NextReviewAt.After(now) {
		t.Errorf("NextReviewAt = %v should be after %v", state.NextReviewAt, now)
	}
}

// TestSM2_SecondRepetitionInterval6 — canonical SM-2: 2nd success → interval 6 days.
func TestSM2_SecondRepetitionInterval6(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := NewSM2State("a")
	state.ApplyGrade(GradeIRemember, now)                  // rep 1, interval 1
	state.ApplyGrade(GradeIRemember, now.AddDate(0, 0, 1)) // rep 2, interval 6
	if state.Repetitions != 2 {
		t.Errorf("Repetitions = %d, want 2", state.Repetitions)
	}
	if state.IntervalDays != 6 {
		t.Errorf("IntervalDays = %d, want 6 (second repetition canonical)", state.IntervalDays)
	}
}

// TestSM2_ForgotResetsToDayOne — "forgot" must reset interval to 1 day.
func TestSM2_ForgotResetsToDayOne(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := NewSM2State("a")
	// Build up some repetitions first.
	state.ApplyGrade(GradeIRemember, now)
	state.ApplyGrade(GradeIRemember, now.AddDate(0, 0, 1))
	if state.Repetitions != 2 {
		t.Fatalf("preload Repetitions = %d, want 2", state.Repetitions)
	}
	// Now forget.
	state.ApplyGrade(GradeForgot, now.AddDate(0, 0, 7))
	if state.Repetitions != 0 {
		t.Errorf("Repetitions after forgot = %d, want 0 (reset)", state.Repetitions)
	}
	if state.IntervalDays != 1 {
		t.Errorf("IntervalDays after forgot = %d, want 1 (reset to day 1)", state.IntervalDays)
	}
}

// TestSM2_NotSureIsModestSuccess — "not_sure" maps to q=3, so it counts as a
// success (repetitions++) but penalises EF more than i_remember.
func TestSM2_NotSureIsModestSuccess(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	a := NewSM2State("a")
	b := NewSM2State("b")

	a.ApplyGrade(GradeIRemember, now)
	b.ApplyGrade(GradeNotSure, now)

	if a.Repetitions != 1 || b.Repetitions != 1 {
		t.Errorf("expected both to bump repetitions (a=%d b=%d)", a.Repetitions, b.Repetitions)
	}
	if b.EasinessFactor >= a.EasinessFactor {
		t.Errorf("not_sure EF (%f) should be lower than i_remember EF (%f)",
			b.EasinessFactor, a.EasinessFactor)
	}
}

// TestSM2_EFFloor — EF must never drop below 1.3.
func TestSM2_EFFloor(t *testing.T) {
	state := NewSM2State("a")
	state.EasinessFactor = 1.31
	now := mustParse(t, "2026-05-08T00:00:00Z")
	// not_sure with EF on the floor edge.
	state.ApplyGrade(GradeNotSure, now)
	if state.EasinessFactor < 1.3 {
		t.Errorf("EasinessFactor = %f, want >= 1.3 (floor)", state.EasinessFactor)
	}
}

// TestSM2_InvalidGradeRejected — only the 3 canonical grades are accepted.
func TestSM2_InvalidGradeRejected(t *testing.T) {
	state := NewSM2State("a")
	now := mustParse(t, "2026-05-08T00:00:00Z")
	if err := state.ApplyGrade(Grade("bogus"), now); err == nil {
		t.Error("expected error for invalid grade")
	}
}

// TestEbbinghausDecay_NeverReviewed — never-reviewed → fully decayed.
func TestEbbinghausDecay_NeverReviewed(t *testing.T) {
	state := SM2State{AtomID: "a"} // zero LastReviewedAt
	now := mustParse(t, "2026-05-08T00:00:00Z")
	d := EbbinghausDecay(state, now)
	if d != 1.0 {
		t.Errorf("decay = %f, want 1.0 for never-reviewed", d)
	}
}

// TestEbbinghausDecay_FreshlyReviewed — just-reviewed → near 0 decay.
func TestEbbinghausDecay_FreshlyReviewed(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := NewSM2State("a")
	state.ApplyGrade(GradeIRemember, now)
	d := EbbinghausDecay(state, now)
	if d > 0.05 {
		t.Errorf("decay = %f, want near 0 immediately after review", d)
	}
}

// TestEbbinghausDecay_StaleAfterDays — decay grows monotonically over time.
func TestEbbinghausDecay_StaleAfterDays(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := NewSM2State("a")
	state.ApplyGrade(GradeIRemember, now)
	d1 := EbbinghausDecay(state, now.AddDate(0, 0, 5))
	d10 := EbbinghausDecay(state, now.AddDate(0, 0, 30))
	if d10 <= d1 {
		t.Errorf("decay@30d (%f) should be > decay@5d (%f)", d10, d1)
	}
	if math.IsNaN(d10) || math.IsInf(d10, 0) {
		t.Errorf("decay@30d invalid: %f", d10)
	}
}

// TestIsValidGrade — exhaustive 3-button check.
func TestIsValidGrade(t *testing.T) {
	if !IsValidGrade(GradeIRemember) || !IsValidGrade(GradeNotSure) || !IsValidGrade(GradeForgot) {
		t.Error("expected all 3 canonical grades to validate")
	}
	if IsValidGrade(Grade("maybe")) {
		t.Error("non-canonical grade accepted")
	}
}
