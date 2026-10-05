// idempotent_answer_test.go — RED phase tests for idempotent SubmitAnswer.
//
// Per S4.2 done-criteria: "idempotent on answer_id" — the same (session,
// answer_id) submitted twice must not double-count, double-publish, or
// double-grade. The state machine returns the prior result for the
// duplicate submission.
package atom_attempt

import (
	"testing"
	"time"
)

func TestSubmitAnswerWithID_RecordsAnswerID(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)

	clk.t = clk.t.Add(time.Minute)
	res, err := s.SubmitAnswerWithID("ans-1", "wrong", false, false, clk)
	if err != nil {
		t.Fatalf("SubmitAnswerWithID: %v", err)
	}
	if res.Duplicate {
		t.Errorf("first submit Duplicate = true; want false")
	}
	if s.LastAnswerID != "ans-1" {
		t.Errorf("LastAnswerID = %q; want ans-1", s.LastAnswerID)
	}
}

func TestSubmitAnswerWithID_DuplicateReturnsPriorResult(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)

	clk.t = clk.t.Add(time.Minute)
	r1, err := s.SubmitAnswerWithID("ans-1", "first", false, true, clk)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if r1.Duplicate {
		t.Errorf("first Duplicate = true")
	}

	// Replay the same answer_id (network retry simulation).
	r2, err := s.SubmitAnswerWithID("ans-1", "first", false, true, clk)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !r2.Duplicate {
		t.Errorf("replay Duplicate = false; want true")
	}
	if r2.Correct != r1.Correct {
		t.Errorf("replay Correct = %v; want %v (deterministic)", r2.Correct, r1.Correct)
	}
	if s.AnswerCount != 1 {
		t.Errorf("AnswerCount = %d; want 1 (no double-count)", s.AnswerCount)
	}
}

func TestSubmitAnswerWithID_DifferentIDIsNotDuplicate(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	if _, err := s.SubmitAnswerWithID("ans-1", "wrong", false, false, clk); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Minute)
	r, err := s.SubmitAnswerWithID("ans-2", "right", false, true, clk)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if r.Duplicate {
		t.Errorf("ans-2 Duplicate = true; want false (different id)")
	}
	if s.AnswerCount != 2 {
		t.Errorf("AnswerCount = %d; want 2", s.AnswerCount)
	}
}

func TestSubmitAnswerWithID_BlankIDFallsBackToNonIdempotent(t *testing.T) {
	clk := &fixedClock{t: time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)}
	s, _ := Start(tenantID, gcid, atomID, clk)
	clk.t = clk.t.Add(time.Minute)
	r1, err := s.SubmitAnswerWithID("", "x", false, false, clk)
	if err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Minute)
	r2, err := s.SubmitAnswerWithID("", "y", false, false, clk)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Duplicate || r2.Duplicate {
		t.Errorf("blank id should never be duplicate")
	}
	if s.AnswerCount != 2 {
		t.Errorf("AnswerCount = %d; want 2", s.AnswerCount)
	}
}
