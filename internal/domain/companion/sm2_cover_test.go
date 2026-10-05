package companion

import (
	"math"
	"testing"
)

// TestNewSM2State_BlankAtomIDDefensiveDefault — the constructor must never
// store an empty atom_id (a caller bug). It substitutes a sentinel rather
// than panicking, so a malformed call still yields a usable, EF=2.5 state.
func TestNewSM2State_BlankAtomIDDefensiveDefault(t *testing.T) {
	cases := []string{"", "   ", "\t\n"}
	for _, in := range cases {
		s := NewSM2State(in)
		if s.AtomID != "(unknown)" {
			t.Fatalf("NewSM2State(%q).AtomID = %q, want %q", in, s.AtomID, "(unknown)")
		}
		if s.EasinessFactor != 2.5 {
			t.Fatalf("default EF = %f, want 2.5", s.EasinessFactor)
		}
		if s.Repetitions != 0 || s.IntervalDays != 0 {
			t.Fatalf("fresh state must be zeroed, got reps=%d interval=%d", s.Repetitions, s.IntervalDays)
		}
	}
}

// TestNewSM2State_KeepsNonBlankAtomID — the trim guard must NOT mangle a real
// id (regression guard against trimming away legitimate ids).
func TestNewSM2State_KeepsNonBlankAtomID(t *testing.T) {
	const id = "01970000-0000-7000-a000-000000000009"
	if s := NewSM2State(id); s.AtomID != id {
		t.Fatalf("AtomID = %q, want %q", s.AtomID, id)
	}
}

// TestEbbinghausDecay_IntervalFlooredToOne reaches the maxInt(IntervalDays, 1)
// floor used in the stability calculation. A state graded "forgot" resets
// IntervalDays to 1; a freshly-constructed-then-overridden 0-interval state
// must still produce a finite decay (stability uses max(interval,1) so it
// never divides by zero).
func TestEbbinghausDecay_IntervalFlooredToOne(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	// IntervalDays explicitly 0 but reviewed in the past — forces the
	// maxInt(0,1) floor so stability = EF*1 (not EF*0 → divide-by-zero path).
	state := SM2State{
		AtomID:         "a",
		IntervalDays:   0,
		EasinessFactor: 2.5,
		LastReviewedAt: now.AddDate(0, 0, -3),
	}
	d := EbbinghausDecay(state, now)
	if math.IsNaN(d) || math.IsInf(d, 0) {
		t.Fatalf("decay invalid with 0 interval: %f", d)
	}
	if d <= 0 || d >= 1 {
		t.Fatalf("decay = %f, want strictly in (0,1) for a 3-day-old EF=2.5 card", d)
	}
}

// TestEbbinghausDecay_ZeroStabilityFullyDecayed — when stability collapses to
// <= 0 (EF 0), the function short-circuits to fully-decayed (1.0) rather than
// dividing by zero.
func TestEbbinghausDecay_ZeroStabilityFullyDecayed(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := SM2State{
		AtomID:         "a",
		IntervalDays:   5,
		EasinessFactor: 0, // stability = 0*max(5,1) = 0 → guard returns 1.0
		LastReviewedAt: now.AddDate(0, 0, -1),
	}
	if d := EbbinghausDecay(state, now); d != 1.0 {
		t.Fatalf("decay = %f, want 1.0 for zero-stability card", d)
	}
}

// TestEbbinghausDecay_FutureReviewClampsElapsed — a LastReviewedAt in the
// future (clock skew / replay) must clamp elapsed to 0, yielding ~0 decay
// rather than a negative exponent (which would push retention above 1).
func TestEbbinghausDecay_FutureReviewClampsElapsed(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	state := SM2State{
		AtomID:         "a",
		IntervalDays:   3,
		EasinessFactor: 2.5,
		LastReviewedAt: now.AddDate(0, 0, 2), // 2 days in the future
	}
	d := EbbinghausDecay(state, now)
	if d < 0 {
		t.Fatalf("decay = %f, must not be negative for future review", d)
	}
	if d > 1e-9 {
		t.Fatalf("decay = %f, want ~0 when elapsed clamps to 0", d)
	}
}

// TestQualityFor_MapsGradesAndDefaultsToZero pins the 3-button → SM-2
// quality mapping. The default arm (q=0 for any non-canonical grade) is the
// safe fail-closed value — ApplyGrade rejects invalid grades before reaching
// qualityFor, so this white-box call is the only way to characterise it, and
// it guards against a future caller wiring qualityFor without a prior
// IsValidGrade gate.
func TestQualityFor_MapsGradesAndDefaultsToZero(t *testing.T) {
	cases := []struct {
		g    Grade
		want int
	}{
		{GradeIRemember, 5},
		{GradeNotSure, 3},
		{GradeForgot, 0},
		{Grade("bogus"), 0},
		{Grade(""), 0},
	}
	for _, c := range cases {
		if got := qualityFor(c.g); got != c.want {
			t.Fatalf("qualityFor(%q) = %d, want %d", c.g, got, c.want)
		}
	}
}

// TestMaxInt_Branches directly covers both arms of the maxInt helper, which
// floors the SM-2 interval at 1 inside EbbinghausDecay.
func TestMaxInt_Branches(t *testing.T) {
	if got := maxInt(5, 1); got != 5 {
		t.Fatalf("maxInt(5,1) = %d, want 5", got)
	}
	if got := maxInt(0, 1); got != 1 {
		t.Fatalf("maxInt(0,1) = %d, want 1", got)
	}
	if got := maxInt(3, 3); got != 3 {
		t.Fatalf("maxInt(3,3) = %d, want 3", got)
	}
}

// TestApplyGrade_ThirdSuccessUsesEFInterval drives the third-repetition
// branch (interval = round(prev * EF)) that the canonical first/second-rep
// tests do not reach. After two successes interval is 6; the third multiplies
// by the (now-raised) EF, so the interval must exceed 6.
func TestApplyGrade_ThirdSuccessUsesEFInterval(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	s := NewSM2State("a")
	if err := s.ApplyGrade(GradeIRemember, now); err != nil { // rep1 → 1
		t.Fatal(err)
	}
	if err := s.ApplyGrade(GradeIRemember, now.AddDate(0, 0, 1)); err != nil { // rep2 → 6
		t.Fatal(err)
	}
	if s.IntervalDays != 6 {
		t.Fatalf("pre-condition interval = %d, want 6", s.IntervalDays)
	}
	efBefore := s.EasinessFactor
	if err := s.ApplyGrade(GradeIRemember, now.AddDate(0, 0, 7)); err != nil { // rep3 → round(6*EF)
		t.Fatal(err)
	}
	if s.Repetitions != 3 {
		t.Fatalf("Repetitions = %d, want 3", s.Repetitions)
	}
	want := int(math.Round(6 * efBefore))
	if s.IntervalDays != want {
		t.Fatalf("third-rep interval = %d, want round(6 * %f) = %d", s.IntervalDays, efBefore, want)
	}
	if s.IntervalDays <= 6 {
		t.Fatalf("third-rep interval = %d, must exceed the 6-day second-rep interval", s.IntervalDays)
	}
}
