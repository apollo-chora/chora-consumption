// Package atom_attempt - state-machine tests.
//
// State graph:
//
//	started → in_progress (first answer)
//	in_progress → in_progress (incorrect answer; hints_used++; max 3)
//	in_progress → completed (correct answer)
//	in_progress → abandoned (Abandon() OR 24hr timeout)
//	completed/abandoned terminal — any transition raises ErrInvalidTransition
//
// The clock is injectable (Clock interface) so 24hr-timeout tests are
// deterministic without time.Sleep.
package atom_attempt

import (
	"errors"
	"testing"
	"time"
)

const (
	tenantID = "01970000-0000-7000-8000-000000000001"
	gcid     = "01970000-0000-7000-9000-000000000001"
	atomID   = "01970000-0000-7000-a000-000000000001"
)

// fixedClock returns a deterministic time. Now() advances by adding the
// supplied offset on each call when used through tickClock.
type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { return c.t }

// TestStart creates a session in STARTED state.
func TestStart(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, err := Start(tenantID, gcid, atomID, clk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.SessionID == "" {
		t.Error("expected SessionID populated")
	}
	if s.Status != StatusStarted {
		t.Errorf("Status = %s, want %s", s.Status, StatusStarted)
	}
	if s.HintsUsed != 0 {
		t.Errorf("HintsUsed = %d, want 0", s.HintsUsed)
	}
	if s.AnswerCount != 0 {
		t.Errorf("AnswerCount = %d, want 0", s.AnswerCount)
	}
	if !s.StartedAt.Equal(clk.t) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, clk.t)
	}
}

func TestStart_RequiresTenant(t *testing.T) {
	if _, err := Start("", gcid, atomID, &fixedClock{t: time.Now()}); err == nil {
		t.Error("expected error")
	}
}

func TestStart_RequiresGCID(t *testing.T) {
	if _, err := Start(tenantID, "", atomID, &fixedClock{t: time.Now()}); err == nil {
		t.Error("expected error")
	}
}

func TestStart_RequiresAtom(t *testing.T) {
	if _, err := Start(tenantID, gcid, "", &fixedClock{t: time.Now()}); err == nil {
		t.Error("expected error")
	}
}

// TestSubmitAnswer_Incorrect transitions started → in_progress and bumps
// hints_used when a hint was used. Status remains in_progress on incorrect.
func TestSubmitAnswer_Incorrect(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)

	clk.t = clk.t.Add(30 * time.Second)
	res, err := s.SubmitAnswer("wrong-answer", true /*hint used*/, false /*correct?*/, clk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Status != StatusInProgress {
		t.Errorf("Status = %s, want %s", s.Status, StatusInProgress)
	}
	if s.HintsUsed != 1 {
		t.Errorf("HintsUsed = %d, want 1", s.HintsUsed)
	}
	if s.AnswerCount != 1 {
		t.Errorf("AnswerCount = %d, want 1", s.AnswerCount)
	}
	if res.Correct {
		t.Error("expected res.Correct=false")
	}
	if s.CompletedAt != nil {
		t.Error("expected CompletedAt nil while in_progress")
	}
}

// TestSubmitAnswer_Correct transitions in_progress → completed.
func TestSubmitAnswer_Correct(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)

	clk.t = clk.t.Add(60 * time.Second)
	if _, err := s.SubmitAnswer("correct", false, true, clk); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if s.Status != StatusCompleted {
		t.Errorf("Status = %s, want %s", s.Status, StatusCompleted)
	}
	if s.CompletedAt == nil {
		t.Error("expected CompletedAt populated")
	}
	if !s.CompletedAt.Equal(clk.t) {
		t.Errorf("CompletedAt = %v, want %v", s.CompletedAt, clk.t)
	}
}

// TestSubmitAnswer_HintsCappedAt3 enforces the max-3-hints rule.
func TestSubmitAnswer_HintsCappedAt3(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)

	for i := 0; i < 3; i++ {
		clk.t = clk.t.Add(30 * time.Second)
		if _, err := s.SubmitAnswer("wrong", true, false, clk); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if s.HintsUsed != 3 {
		t.Errorf("HintsUsed = %d, want 3", s.HintsUsed)
	}
	// 4th hint must error.
	clk.t = clk.t.Add(30 * time.Second)
	_, err := s.SubmitAnswer("wrong", true, false, clk)
	if !errors.Is(err, ErrHintsExceeded) {
		t.Errorf("4th hint err = %v, want ErrHintsExceeded", err)
	}
}

// TestSubmitAnswer_HintsCappedAllowsAnswerWithoutHint
// 4th answer WITHOUT a hint is fine.
func TestSubmitAnswer_HintsCappedAllowsAnswerWithoutHint(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	for i := 0; i < 3; i++ {
		clk.t = clk.t.Add(30 * time.Second)
		s.SubmitAnswer("wrong", true, false, clk)
	}
	clk.t = clk.t.Add(30 * time.Second)
	if _, err := s.SubmitAnswer("wrong-again", false, false, clk); err != nil {
		t.Errorf("answer without hint after 3 hints err = %v, want nil", err)
	}
	if s.HintsUsed != 3 {
		t.Errorf("HintsUsed unexpectedly bumped: %d", s.HintsUsed)
	}
}

// TestAbandon transitions in_progress → abandoned.
func TestAbandon(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Hour)
	if err := s.Abandon(clk); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if s.Status != StatusAbandoned {
		t.Errorf("Status = %s, want %s", s.Status, StatusAbandoned)
	}
	if s.AbandonedAt == nil {
		t.Error("expected AbandonedAt populated")
	}
}

// TestAbandon_AfterCompleted is invalid (terminal state).
func TestAbandon_AfterCompleted(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	s.SubmitAnswer("correct", false, true, clk)
	clk.t = clk.t.Add(time.Minute)
	if err := s.Abandon(clk); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("abandon after complete err = %v, want ErrInvalidTransition", err)
	}
}

// TestSubmitAnswer_AfterCompleted is invalid.
func TestSubmitAnswer_AfterCompleted(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	s.SubmitAnswer("correct", false, true, clk)
	clk.t = clk.t.Add(time.Minute)
	if _, err := s.SubmitAnswer("again", false, true, clk); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("submit after complete err = %v, want ErrInvalidTransition", err)
	}
}

// TestSubmitAnswer_AfterAbandoned is invalid.
func TestSubmitAnswer_AfterAbandoned(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	s.Abandon(clk)
	if _, err := s.SubmitAnswer("a", false, true, clk); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("submit after abandon err = %v, want ErrInvalidTransition", err)
	}
}

// TestExpireIfStale auto-abandons sessions older than 24hr.
func TestExpireIfStale(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	// fast-forward 24hr + 1s
	clk.t = clk.t.Add(24*time.Hour + time.Second)
	expired := s.ExpireIfStale(clk)
	if !expired {
		t.Error("expected expired=true")
	}
	if s.Status != StatusAbandoned {
		t.Errorf("Status = %s, want %s", s.Status, StatusAbandoned)
	}
}

// TestExpireIfStale_NotYet returns false within 24hr.
func TestExpireIfStale_NotYet(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(23 * time.Hour)
	if expired := s.ExpireIfStale(clk); expired {
		t.Error("expected expired=false at 23hr")
	}
	if s.Status != StatusStarted {
		t.Errorf("Status = %s, want %s", s.Status, StatusStarted)
	}
}

// TestExpireIfStale_NoOpOnTerminal does not move terminal states.
func TestExpireIfStale_NoOpOnTerminal(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	s.SubmitAnswer("correct", false, true, clk)
	clk.t = clk.t.Add(48 * time.Hour)
	if expired := s.ExpireIfStale(clk); expired {
		t.Error("expected expired=false on terminal session")
	}
	if s.Status != StatusCompleted {
		t.Errorf("Status mutated to %s on terminal", s.Status)
	}
}

// TestSecondAnswer_RemainsInProgress confirms in_progress → in_progress
// retransition stays in_progress (not bumped to a new state).
func TestSecondAnswer_RemainsInProgress(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	s.SubmitAnswer("wrong-1", false, false, clk)
	clk.t = clk.t.Add(time.Minute)
	s.SubmitAnswer("wrong-2", false, false, clk)
	if s.Status != StatusInProgress {
		t.Errorf("Status = %s, want %s", s.Status, StatusInProgress)
	}
	if s.AnswerCount != 2 {
		t.Errorf("AnswerCount = %d, want 2", s.AnswerCount)
	}
}
