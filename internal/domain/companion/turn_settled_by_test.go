package companion

import (
	"testing"
	"time"
)

// AUDIT-G5. A reaped turn and an answered turn are BOTH terminal and BOTH carry
// a completed_at, so `status` alone cannot tell them apart at the point the
// audit needs to. These tests pin the distinction the gate reads.

func acceptedTurnForSettleTest(t *testing.T) *Turn {
	t.Helper()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	return &Turn{
		TurnID:      "01a02e7d-3166-7d05-89cd-c53e3c616eb7",
		TenantID:    "11111111-1111-7111-8111-111111111111",
		OwnerGCID:   "00000000-0000-7000-8000-000000001999",
		Kind:        TurnKindGreeting,
		Lane:        TurnLaneCompanionTurn,
		Status:      TurnStatusAccepted,
		RequestedAt: now,
		DeadlineAt:  now.Add(130 * time.Second),
	}
}

func TestTurnTimeout_StampsSettledByTimeout(t *testing.T) {
	turn := acceptedTurnForSettleTest(t)
	reapedAt := turn.DeadlineAt.Add(time.Second)

	if !turn.Timeout(reapedAt) {
		t.Fatal("Timeout returned false on an accepted turn")
	}
	if turn.Status != TurnStatusTimeout {
		t.Fatalf("status = %q, want %q", turn.Status, TurnStatusTimeout)
	}
	// The assertion that matters: a reaper must SIGN its work.
	if turn.SettledBy != TurnSettledByTimeout {
		t.Fatalf("SettledBy = %q, want %q: a reaped turn that does not say so is indistinguishable from an answered one", turn.SettledBy, TurnSettledByTimeout)
	}
	// A reaped turn was never answered, so it must not carry an answerer.
	if turn.GeneratedByModelID != "" {
		t.Fatalf("GeneratedByModelID = %q, want empty on a reaped turn", turn.GeneratedByModelID)
	}
	if turn.LateCompletionAt != nil {
		t.Fatal("LateCompletionAt must stay nil until a result actually arrives late")
	}
}

func TestTurnComplete_StampsSettledByCompletion(t *testing.T) {
	turn := acceptedTurnForSettleTest(t)

	if err := turn.Complete(TurnResult{
		WireStatus:         TurnWireStatusOK,
		GeneratedByModelID: "gemini-2.5-flash",
		Raw:                []byte(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if turn.Status != TurnStatusCompleted {
		t.Fatalf("status = %q, want %q", turn.Status, TurnStatusCompleted)
	}
	if turn.SettledBy != TurnSettledByCompletion {
		t.Fatalf("SettledBy = %q, want %q", turn.SettledBy, TurnSettledByCompletion)
	}
}

// The discriminator the two audit instances actually need: the reaped turn and
// the answered turn must not be confusable by any single field the gate reads.
func TestTurnSettledBy_ReapedAndAnsweredAreNotConfusable(t *testing.T) {
	reaped := acceptedTurnForSettleTest(t)
	reaped.Timeout(reaped.DeadlineAt.Add(time.Second))

	answered := acceptedTurnForSettleTest(t)
	if err := answered.Complete(TurnResult{
		WireStatus:         TurnWireStatusOK,
		GeneratedByModelID: "gemini-2.5-flash",
		Raw:                []byte(`{"text":"hello"}`),
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if reaped.SettledBy == answered.SettledBy {
		t.Fatalf("reaped and answered both settled_by %q: the distinction the audit gates on does not exist", reaped.SettledBy)
	}
	// Both are terminal and both carry completed_at, which is precisely why
	// status and completed_at cannot be the discriminator.
	if !reaped.IsTerminal() || !answered.IsTerminal() {
		t.Fatal("expected both turns terminal")
	}
	if reaped.CompletedAt == nil || answered.CompletedAt == nil {
		t.Fatal("expected both turns to carry completed_at; if they did not, settled_by would be unnecessary")
	}
}

// Reaped at the deadline, answered a second later. The coordinator's stated
// real case, and the one the old code discarded: Complete refused it with
// ErrTurnAlreadyTerminal and the store returned nil, so the answer was ACKed
// and lost while turn.go's own docstring claimed it was recorded.
func TestTurnRecordLateCompletion_KeepsTheEvidenceWithoutFlippingStatus(t *testing.T) {
	turn := acceptedTurnForSettleTest(t)
	reapedAt := turn.DeadlineAt.Add(time.Second)
	turn.Timeout(reapedAt)

	lateAt := reapedAt.Add(time.Second)
	ok := turn.RecordLateCompletion(TurnResult{
		WireStatus:         TurnWireStatusOK,
		GeneratedByModelID: "gemini-2.5-flash",
		CompletedAt:        lateAt,
		Raw:                []byte(`{"text":"too late"}`),
	}, lateAt)
	if !ok {
		t.Fatal("RecordLateCompletion returned false for a genuine late arrival")
	}

	// The history stays true: the caller WAS answered with a timeout.
	if turn.Status != TurnStatusTimeout {
		t.Fatalf("status = %q, want %q: a late answer must not rewrite what the caller was told", turn.Status, TurnStatusTimeout)
	}
	if turn.SettledBy != TurnSettledByTimeout {
		t.Fatalf("SettledBy = %q, want %q", turn.SettledBy, TurnSettledByTimeout)
	}
	// But the answer is no longer lost.
	if turn.LateCompletionAt == nil {
		t.Fatal("LateCompletionAt is nil: the late answer was discarded, which is the AUDIT-G5 defect")
	}
	if !turn.LateCompletionAt.Equal(lateAt.UTC()) {
		t.Fatalf("LateCompletionAt = %v, want %v", turn.LateCompletionAt, lateAt.UTC())
	}
	if turn.GeneratedByModelID != "gemini-2.5-flash" {
		t.Fatalf("GeneratedByModelID = %q: the late answerer must be named", turn.GeneratedByModelID)
	}
	// Result stays nil deliberately. Writing it would make a reaped turn read as
	// a completed one to anything projecting from Result.
	if turn.Result != nil {
		t.Fatalf("Result = %s, want nil: a late answer is evidence, not a completion", turn.Result)
	}
}

func TestTurnRecordLateCompletion_RefusesOpenTurnAndSecondRecord(t *testing.T) {
	open := acceptedTurnForSettleTest(t)
	if open.RecordLateCompletion(TurnResult{WireStatus: TurnWireStatusOK}, time.Now()) {
		t.Fatal("recorded lateness on a turn that is still open")
	}

	turn := acceptedTurnForSettleTest(t)
	turn.Timeout(turn.DeadlineAt.Add(time.Second))
	first := turn.DeadlineAt.Add(2 * time.Second)
	if !turn.RecordLateCompletion(TurnResult{WireStatus: TurnWireStatusOK}, first) {
		t.Fatal("first late record refused")
	}
	// A redelivery of the same completion must not move the recorded time, or
	// the evidence drifts every time Pub/Sub retries.
	if turn.RecordLateCompletion(TurnResult{WireStatus: TurnWireStatusOK}, first.Add(time.Minute)) {
		t.Fatal("second late record accepted: a redelivery would overwrite the evidence")
	}
	if !turn.LateCompletionAt.Equal(first.UTC()) {
		t.Fatalf("LateCompletionAt moved to %v on redelivery, want %v", turn.LateCompletionAt, first.UTC())
	}
}
