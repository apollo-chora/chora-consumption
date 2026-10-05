// Package atom_attempt - explicit state-machine AtomAttempt (extension scope).
//
// This is distinct from the older skeleton `session` package which models
// a simpler completed/score lifecycle. This package introduces:
//
//   - Status enum (started / in_progress / completed / abandoned)
//   - Hint counter capped at 3
//   - Submit-answer transitions (correct → completed; incorrect → in_progress)
//   - Abandon() and 24hr-timeout (ExpireIfStale) flows
//   - Injectable Clock for deterministic tests
//
// All transitions enforce the state machine in CLAUDE.md briefing.
//
// Per ddd-enforcement: atom_id is a cross-aggregate UUID reference (no FK
// across DBs). UUIDv7 for SessionID. Soft delete (DeletedAt) supported.
package atom_attempt

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Status is the AtomAttempt lifecycle state.
type Status string

const (
	StatusStarted    Status = "started"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusAbandoned  Status = "abandoned"
)

// MaxHints is the per-session cap on hint usage. 4th hint = ErrHintsExceeded.
const MaxHints = 3

// SessionTimeout is the 24-hour idle-window after which a session in a
// non-terminal state auto-abandons.
const SessionTimeout = 24 * time.Hour

// Errors.
var (
	ErrInvalidTransition = errors.New("atom_session: invalid state transition")
	ErrHintsExceeded     = errors.New("atom_session: hints cap of 3 exceeded")
)

// Clock is an injectable time source — tests pass a fixed clock for
// deterministic 24hr-timeout coverage.
type Clock interface {
	Now() time.Time
}

// realClock returns time.Now().UTC() — used by tests/handlers when no
// fixed clock is provided.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// SystemClock is the production clock; falls back to wall time.
var SystemClock Clock = realClock{}

// AtomAttempt captures one learner's state-machine attempt at one atom.
type AtomAttempt struct {
	SessionID    string
	TenantID     string
	LearnerGCID  string
	AtomID       string
	Status       Status
	HintsUsed    int
	AnswerCount  int
	LastAnswer   string
	LastAnswerID string // idempotency-key per submission (S4.2)
	LastResult   SubmitResult
	StartedAt    time.Time
	CompletedAt  *time.Time
	AbandonedAt  *time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// SubmitResult is returned from SubmitAnswer for handler-side telemetry.
type SubmitResult struct {
	Correct   bool
	Status    Status
	Duplicate bool // S4.2 — true when this was an idempotent retry of a prior answer_id
}

// Start creates a session in STARTED state. Validates required IDs.
func Start(tenantID, learnerGCID, atomID string, clk Clock) (*AtomAttempt, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("atom_session: tenant_id required")
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return nil, errors.New("atom_session: learner_gcid required")
	}
	if strings.TrimSpace(atomID) == "" {
		return nil, errors.New("atom_session: atom_id required")
	}
	if clk == nil {
		clk = SystemClock
	}
	now := clk.Now()
	return &AtomAttempt{
		SessionID:   domain.NewUUIDv7(),
		TenantID:    tenantID,
		LearnerGCID: learnerGCID,
		AtomID:      atomID,
		Status:      StatusStarted,
		StartedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// SubmitAnswer transitions the session per the answer outcome.
//
// Transitions:
//
//	started      → in_progress (incorrect)
//	started      → completed   (correct)
//	in_progress  → in_progress (incorrect; hints_used++)
//	in_progress  → completed   (correct)
//	completed    → ErrInvalidTransition
//	abandoned    → ErrInvalidTransition
//
// Hints rule: if hintUsed=true and HintsUsed already == MaxHints, returns
// ErrHintsExceeded (no state change). Submitting an answer WITHOUT a hint
// is always allowed (in non-terminal states).
func (s *AtomAttempt) SubmitAnswer(answerText string, hintUsed bool, correct bool, clk Clock) (SubmitResult, error) {
	return s.SubmitAnswerWithID("", answerText, hintUsed, correct, clk)
}

// SubmitAnswerWithID is the idempotent variant of SubmitAnswer. When
// answerID is non-empty AND matches the most recent LastAnswerID, the
// call is a no-op replay (returns the prior LastResult with
// Duplicate=true). When answerID is blank, behaviour is identical to
// SubmitAnswer (no idempotency tracking).
//
// S4.2 done-criteria: "idempotent on answer_id" — supports network retry
// patterns at the player layer without double-publishing the
// `chora.consumption.atom_session.completed.v1` event.
func (s *AtomAttempt) SubmitAnswerWithID(answerID, answerText string, hintUsed bool, correct bool, clk Clock) (SubmitResult, error) {
	// Idempotent replay path — only when an explicit answer_id is supplied
	// AND it matches the previous one. We allow this even on terminal
	// states so a retry of the completing-answer is a clean no-op.
	if answerID != "" && answerID == s.LastAnswerID {
		dup := s.LastResult
		dup.Duplicate = true
		return dup, nil
	}
	if s.Status == StatusCompleted || s.Status == StatusAbandoned {
		return SubmitResult{}, ErrInvalidTransition
	}
	if hintUsed && s.HintsUsed >= MaxHints {
		return SubmitResult{}, ErrHintsExceeded
	}
	if clk == nil {
		clk = SystemClock
	}
	if hintUsed {
		s.HintsUsed++
	}
	s.AnswerCount++
	s.LastAnswer = answerText
	s.LastAnswerID = answerID
	now := clk.Now()
	s.UpdatedAt = now

	if correct {
		s.Status = StatusCompleted
		s.CompletedAt = &now
	} else {
		s.Status = StatusInProgress
	}
	res := SubmitResult{Correct: correct, Status: s.Status}
	s.LastResult = res
	return res, nil
}

// Abandon transitions the session into ABANDONED state. Terminal — calling
// after completed/abandoned returns ErrInvalidTransition.
func (s *AtomAttempt) Abandon(clk Clock) error {
	if s.Status == StatusCompleted || s.Status == StatusAbandoned {
		return ErrInvalidTransition
	}
	if clk == nil {
		clk = SystemClock
	}
	now := clk.Now()
	s.Status = StatusAbandoned
	s.AbandonedAt = &now
	s.UpdatedAt = now
	return nil
}

// ExpireIfStale auto-abandons the session if more than SessionTimeout has
// elapsed since StartedAt, but only when the session is in a non-terminal
// state. Returns true if the session was expired by this call.
func (s *AtomAttempt) ExpireIfStale(clk Clock) bool {
	if s.Status == StatusCompleted || s.Status == StatusAbandoned {
		return false
	}
	if clk == nil {
		clk = SystemClock
	}
	if clk.Now().Sub(s.StartedAt) <= SessionTimeout {
		return false
	}
	now := clk.Now()
	s.Status = StatusAbandoned
	s.AbandonedAt = &now
	s.UpdatedAt = now
	return true
}
