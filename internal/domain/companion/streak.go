// Package companion — Maya retention loop (streak / XP cadence / next-due).
//
// Per S5.1 A-Companion briefing + ux_engagement.md §2 the engagement loop
// has three deterministic primitives that live in the Companion domain:
//
//	Streak  — consecutive UTC-day activity counter (resets on miss).
//	XP cadence — fixed XP rewards by activity kind:
//	   10 XP / atom completed       (Maya: tiny constant feedback)
//	   50 XP / daily-dose completed  (Maya: visit-the-shop reward)
//	  200 XP / LearningPath milestone (Maya: chapter-end celebration)
//	Spaced repetition next-due — already in SM2State; this file exposes
//	   a convenience accessor so the surface code never reaches into the
//	   SM-2 internal struct.
//
// All three are pure-domain. No persistence, no events; the caller
// (typically the HTTP handler / subscriber) is responsible for storage.
//
// Spec invariant: streak + XP are deterministic functions of activity
// timestamps + kinds. Replaying the same input MUST yield the same state.
//
// CRITICAL: per memory `feedback_companion_vs_agent` the Companion entity
// is a domain construct — NO LLM imports. The mana-metered LLM-personalised
// greeting belongs in greeting.go (separate concern, port-driven).
package companion

import (
	"strings"
	"time"
)

// ActivityKind enumerates the events that earn XP under the Maya cadence.
type ActivityKind string

const (
	// ActivityAtomCompleted = one AtomAttempt transitioned to "completed".
	ActivityAtomCompleted ActivityKind = "atom_completed"
	// ActivityDailyDoseCompleted = the learner finished all 5 atoms in a Daily Dose.
	ActivityDailyDoseCompleted ActivityKind = "daily_dose_completed"
	// ActivityPathMilestone = a LearningPath milestone (any of: 25%, 50%,
	// 75%, completion). Surface code is responsible for deduplication via
	// idempotency keys.
	ActivityPathMilestone ActivityKind = "path_milestone"
)

// XP cadence constants — the canonical Maya rewards.
const (
	// XPPerAtomCompleted is the XP awarded when an AtomAttempt transitions
	// to "completed". Calibrated so that a 5-atom Daily Dose pre-dose-bonus
	// equals 50 XP (1 level-2 in 8 doses).
	XPPerAtomCompleted = 10
	// XPPerDailyDoseCompleted is the bonus XP for finishing the full
	// 5-atom Daily Dose card stack (in addition to per-atom XP).
	XPPerDailyDoseCompleted = 50
	// XPPerPathMilestone is the XP awarded on a LearningPath milestone
	// (chapter celebration cadence).
	XPPerPathMilestone = 200
)

// XPForActivity returns the XP awarded for an activity kind. Unknown
// kinds return 0 (defensive — surface code may pass user-supplied
// strings that drift from the enum).
func XPForActivity(kind ActivityKind) int {
	switch kind {
	case ActivityAtomCompleted:
		return XPPerAtomCompleted
	case ActivityDailyDoseCompleted:
		return XPPerDailyDoseCompleted
	case ActivityPathMilestone:
		return XPPerPathMilestone
	default:
		return 0
	}
}

// Streak is the per-learner consecutive-day activity counter.
//
// Invariants:
//
//   - Count >= 0
//   - Count == 0 only when LastActivityAt is zero (never touched).
//   - LastActivityAt is always normalised to UTC midnight (the day-bucket).
//
// Cross-domain reference: LearnerGCID is a UUIDv7 with no FK constraint
// (per ddd-enforcement #3); validation is the caller's responsibility.
type Streak struct {
	LearnerGCID    string    `json:"learner_gcid"`
	Count          int       `json:"count"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// NewStreak constructs a fresh streak for a learner. Count starts at 0.
func NewStreak(learnerGCID string) *Streak {
	return &Streak{
		LearnerGCID: strings.TrimSpace(learnerGCID),
		Count:       0,
	}
}

// RecordActivity advances the streak based on UTC-day comparison:
//
//   - First activity ever (LastActivityAt zero) → Count = 1.
//   - Same UTC day as last activity → no change.
//   - Next UTC day (gap == 1) → Count++.
//   - Gap > 1 day → Count reset to 1 (the new activity is day-1 of a
//     fresh streak, NOT zero — the learner showed up today).
//
// A zero `now` is rejected silently (defensive against caller bugs).
func (s *Streak) RecordActivity(now time.Time) {
	if now.IsZero() {
		return
	}
	currentDay := startOfDayUTC(now)
	if s.LastActivityAt.IsZero() {
		s.Count = 1
		s.LastActivityAt = currentDay
		return
	}
	lastDay := startOfDayUTC(s.LastActivityAt)
	switch dayDelta := daysBetween(lastDay, currentDay); {
	case dayDelta == 0:
		// Same day — no change to Count.
		return
	case dayDelta == 1:
		s.Count++
	default:
		// Gap >= 2 days — reset to 1 (fresh streak starting today).
		s.Count = 1
	}
	s.LastActivityAt = currentDay
}

// startOfDayUTC truncates a timestamp to UTC midnight (00:00:00).
func startOfDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// daysBetween returns the integer count of days between two UTC-midnight
// timestamps. Both inputs MUST already be at UTC midnight.
func daysBetween(a, b time.Time) int {
	hours := b.Sub(a).Hours()
	return int(hours / 24)
}

// NextDueAt returns the next-review timestamp for an SM-2 state. Used by
// surface code to render the "Next review" pill on the Daily Dose card
// stack without exposing the SM-2 struct shape outside this package.
func NextDueAt(state SM2State) time.Time {
	return state.NextReviewAt
}
