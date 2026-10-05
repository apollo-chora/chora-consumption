// Package topic_retention models the per-user, per-topic retention curve
// computed via the Ebbinghaus Forgetting Curve.
//
// Per docs/m13/phyllis-mvp-2026-05-08.md §5.4 + project_chora_concerns.md
// the retention formula is:
//
//	R(t) = e^(-t / S)
//
// where:
//
//   - t is the time elapsed since last review (in days)
//   - S is the per-user, per-topic strength (in days). Larger S = slower
//     forgetting curve.
//
// The aggregate captures one (gcid × topic_id) pair. Strength compounds on
// successful reviews (`Review(correct=true)`), shrinks on failed reviews,
// and is bounded by MinStrengthDays / MaxStrengthDays.
//
// Per ddd-enforcement #3 + #9: topic_id and gcid are opaque UUIDs without
// FK constraints (cross-domain references). UUIDv7 for the row id.
//
// Hexagonal: this is PURE domain — no time.Now() leaks, all clock-dependent
// calls take a `now` argument. No HTTP/Postgres imports here.
package topic_retention

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// MinStrengthDays / MaxStrengthDays bound the strength parameter.
//
//   - 0.5 day floor prevents pathological 0/negative strength after repeated
//     incorrect reviews.
//   - 365 day cap prevents runaway compounding after many corrects (an atom
//     reviewed 100× still benefits from a yearly nudge).
const (
	MinStrengthDays = 0.5
	MaxStrengthDays = 365.0

	// CorrectMultiplier multiplies the strength on a correct review (the
	// "compounding" growth factor). 2.0 doubles each time, capped at the
	// MaxStrengthDays ceiling.
	CorrectMultiplier = 2.0

	// IncorrectMultiplier multiplies the strength on an incorrect review.
	// 0.5 halves; floors at MinStrengthDays.
	IncorrectMultiplier = 0.5
)

// ErrInvalidScore is the sentinel returned by New for any validation failure.
var ErrInvalidScore = errors.New("topic_retention: invalid score")

// TopicScore is the per-(tenant_id, gcid, topic_id) retention aggregate.
type TopicScore struct {
	ScoreID        string
	TenantID       string
	GCID           string
	TopicID        string
	Strength       float64 // S in R(t) = e^(-t/S); units: days
	RetentionScore float64 // R snapshot at LastReviewedAt + UpdatedAt
	ReviewCount    int
	LastReviewedAt time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// New constructs a fresh TopicScore. Validates required fields + strength.
//
// strength must be > 0 (in days) — typical seed for a brand-new topic is
// 1.0 (1-day half-life). Use the same `now` for LastReviewedAt + UpdatedAt
// so a freshly-minted score reads R(0) = 1.0.
func New(tenantID, gcid, topicID string, now time.Time, strength float64) (*TopicScore, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidScore)
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, fmt.Errorf("%w: gcid required", ErrInvalidScore)
	}
	if strings.TrimSpace(topicID) == "" {
		return nil, fmt.Errorf("%w: topic_id required", ErrInvalidScore)
	}
	if strength <= 0 {
		return nil, fmt.Errorf("%w: strength must be > 0", ErrInvalidScore)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &TopicScore{
		ScoreID:        domain.NewUUIDv7(),
		TenantID:       tenantID,
		GCID:           gcid,
		TopicID:        topicID,
		Strength:       clampStrength(strength),
		RetentionScore: 1.0,
		LastReviewedAt: now,
		UpdatedAt:      now,
	}, nil
}

// RetentionAt evaluates R(t) = e^(-t/S) at the supplied wall-clock time.
//
// Returns 1.0 at exactly LastReviewedAt or before; decays exponentially
// after. Result is naturally bounded in (0, 1.0] for finite elapsed time
// (math.Exp(-positive) is always in (0, 1]).
func (s *TopicScore) RetentionAt(now time.Time) float64 {
	elapsedDays := now.Sub(s.LastReviewedAt).Hours() / 24.0
	if elapsedDays <= 0 {
		return 1.0
	}
	return math.Exp(-elapsedDays / s.Strength)
}

// DueThreshold is the retention floor below which a topic is "due for review"
// (review when < 60% retained). It is deliberately aligned with the daily-dose
// review cutoff: the dose picks atoms whose Ebbinghaus *decay* exceeds
// companion.DoseDecayThreshold (0.4), and decay == 1 − retention, so decay > 0.4
// ⟺ retention < 0.6. Sharing the 0.6 line keeps the KG-map ⏰ ("due") terrain
// telling the same story as what the learner's daily dose will actually drill,
// even though the two operate on different aggregates (per-topic TopicScore here
// vs the dose's per-atom SM2 state).
const DueThreshold = 0.6

// IsDueAt reports whether the topic has decayed past the due-for-review floor
// at the supplied wall-clock time. Strict: a score retained exactly AT the
// threshold is not yet due. Clock-injected (no time.Now leak) so callers stamp
// a single per-request `now` across a batch of topics.
func (s *TopicScore) IsDueAt(now time.Time) bool {
	return s.RetentionAt(now) < DueThreshold
}

// Review records one review event. Strength compounds on `correct=true`
// (×CorrectMultiplier, capped at MaxStrengthDays) and halves on
// `correct=false` (×IncorrectMultiplier, floored at MinStrengthDays).
//
// LastReviewedAt advances to `now`; RetentionScore snapshot resets to 1.0
// (we just reviewed). ReviewCount increments by one.
func (s *TopicScore) Review(correct bool, now time.Time) {
	if correct {
		s.Strength = clampStrength(s.Strength * CorrectMultiplier)
	} else {
		s.Strength = clampStrength(s.Strength * IncorrectMultiplier)
	}
	s.LastReviewedAt = now
	s.UpdatedAt = now
	s.RetentionScore = 1.0
	s.ReviewCount++
}

// SoftDelete marks the score deleted without removing it. Hard delete is
// forbidden per ddd-enforcement invariant #5.
func (s *TopicScore) SoftDelete(now time.Time) {
	s.DeletedAt = &now
	s.UpdatedAt = now
}

// clampStrength bounds strength in [MinStrengthDays, MaxStrengthDays].
func clampStrength(s float64) float64 {
	if s < MinStrengthDays {
		return MinStrengthDays
	}
	if s > MaxStrengthDays {
		return MaxStrengthDays
	}
	return s
}
