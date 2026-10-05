// Package companion — SM-2 spaced-repetition algorithm for the Daily Dose.
//
// Implements a small variant of the SM-2 (SuperMemo 2) algorithm from
// Wozniak (1985), adapted for the 3-button feedback grade (i_remember /
// not_sure / forgot) used by the Comic Ch6 P14 Daily Dose UX rather than
// the 0–5 quality scale of the canonical SM-2.
//
// Mapping:
//
//	"i_remember" → quality 5 (perfect recall)        → bump interval
//	"not_sure"   → quality 3 (correct after pause)    → modest interval bump
//	"forgot"     → quality 0 (complete failure)        → reset to day 1
//
// SM-2 update rule (per the canonical algorithm):
//
//	If quality < 3:
//	    interval = 1 day, repetitions = 0
//	    (EF unchanged — failure does not punish ease beyond this)
//	Else:
//	    repetitions++
//	    if repetitions == 1: interval = 1
//	    elif repetitions == 2: interval = 6
//	    else: interval = round(prev_interval * EF)
//	    EF = EF + (0.1 - (5 - q) * (0.08 + (5 - q) * 0.02))
//	    if EF < 1.3: EF = 1.3
//
// The default initial easiness factor (EF) is 2.5 — straight from SM-2.
//
// CRITICAL: this is a pure-domain function. No persistence, no events; the
// caller is expected to update its TopicRetention store and emit events.
package companion

import (
	"errors"
	"math"
	"strings"
	"time"
)

// Grade is the user's 3-button feedback for a Daily Dose card.
type Grade string

const (
	// GradeIRemember = perfect recall (canonical SM-2 q=5).
	GradeIRemember Grade = "i_remember"
	// GradeNotSure = recalled but with effort (canonical SM-2 q=3).
	GradeNotSure Grade = "not_sure"
	// GradeForgot = failed to recall (canonical SM-2 q=0).
	GradeForgot Grade = "forgot"
)

// IsValidGrade returns true if g is one of the 3 canonical grades.
func IsValidGrade(g Grade) bool {
	return g == GradeIRemember || g == GradeNotSure || g == GradeForgot
}

// SM2State is the per-atom-per-learner state mutated by each grade.
//
// In production this lives in chora_consumption.topic_retention; for the MVP
// in-memory adapter it lives in the CompanionRepo / DailyDoseRepo composite
// state. Cross-domain references (atom_id) are UUIDs without FK per
// ddd-enforcement #3.
type SM2State struct {
	AtomID         string
	Repetitions    int       // canonical SM-2 n
	IntervalDays   int       // canonical SM-2 I (days until next review)
	EasinessFactor float64   // canonical SM-2 EF (>= 1.3)
	LastReviewedAt time.Time // when this card was last graded
	NextReviewAt   time.Time // when this card is next due
}

// NewSM2State returns a fresh SM-2 state for an atom (never reviewed).
// Default EF = 2.5 (canonical SM-2 starting value).
func NewSM2State(atomID string) SM2State {
	if strings.TrimSpace(atomID) == "" {
		// Defensive — caller bug, but never panic.
		atomID = "(unknown)"
	}
	return SM2State{
		AtomID:         atomID,
		Repetitions:    0,
		IntervalDays:   0,
		EasinessFactor: 2.5,
	}
}

// ApplyGrade mutates s in-place per SM-2 update rules and returns the new
// state plus an error if grade is invalid.
//
// The clock is injected so tests can be deterministic (now := time.Now() in
// production callers; fixed time in tests).
func (s *SM2State) ApplyGrade(g Grade, now time.Time) error {
	if !IsValidGrade(g) {
		return errors.New("sm2: invalid grade")
	}

	q := qualityFor(g)
	s.LastReviewedAt = now

	if q < 3 {
		// Failure path: reset interval to 1 day; repetitions to 0.
		s.Repetitions = 0
		s.IntervalDays = 1
		// EF unchanged on failure (per canonical SM-2 — failures don't
		// punish EF beyond resetting interval).
	} else {
		// Success path: bump repetitions and recompute interval.
		s.Repetitions++
		switch s.Repetitions {
		case 1:
			s.IntervalDays = 1
		case 2:
			s.IntervalDays = 6
		default:
			s.IntervalDays = int(math.Round(float64(s.IntervalDays) * s.EasinessFactor))
			if s.IntervalDays < 1 {
				s.IntervalDays = 1
			}
		}
		// Update EF per canonical formula. Floor at 1.3.
		qf := float64(q)
		s.EasinessFactor = s.EasinessFactor + (0.1 - (5.0-qf)*(0.08+(5.0-qf)*0.02))
		if s.EasinessFactor < 1.3 {
			s.EasinessFactor = 1.3
		}
	}

	s.NextReviewAt = now.AddDate(0, 0, s.IntervalDays)
	return nil
}

func qualityFor(g Grade) int {
	switch g {
	case GradeIRemember:
		return 5
	case GradeNotSure:
		return 3
	case GradeForgot:
		return 0
	default:
		return 0
	}
}

// EbbinghausDecay returns the retention probability R(t) for a card last
// reviewed `t` ago, with stability `S` (in days). Implements the Ebbinghaus
// Forgetting Curve primitive named in CLAUDE.md:
//
//	R(t) = exp(-t / S)
//
// `S` is taken from the SM2State's EasinessFactor scaled by IntervalDays.
// A larger S = stronger memory = slower decay.
//
// Used by Daily Dose composition to pick atoms whose decay has fallen below
// the review threshold (decay > 0.4 per spec).
func EbbinghausDecay(state SM2State, now time.Time) float64 {
	if state.LastReviewedAt.IsZero() {
		// Never reviewed — fully decayed (forces fresh-pick eligibility).
		return 1.0
	}
	stability := state.EasinessFactor * float64(maxInt(state.IntervalDays, 1))
	if stability <= 0 {
		return 1.0
	}
	elapsed := now.Sub(state.LastReviewedAt).Hours() / 24.0 // days
	if elapsed < 0 {
		elapsed = 0
	}
	retention := math.Exp(-elapsed / stability)
	// Decay is the inverse of retention: higher decay = more forgotten.
	return 1.0 - retention
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
