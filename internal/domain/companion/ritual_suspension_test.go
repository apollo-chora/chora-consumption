// ritual_suspension_test.go: RED first for N13, the third terminal reason
// (ADR-257 D7, ADR-252 D5 and D6).
//
// StepExecResult.Blocked carries exactly one meaning today, an Armor block,
// rendered to the learner as "stopped for safety". ADR-252 introduces a second,
// different denial: a contained companion's turn is refused with
// FAILED_PRECONDITION and reason companion_suspended, and no mana is charged
// because the deny lands before the debit.
//
// Without a third reason a containment denial arrives as a generic step error
// and terminates the run as `failed`, which reads to a learner as a fault in the
// product rather than as a deliberate pause an operator applied. A peer step
// makes that materially worse, because the suspended companion may not be the
// one the learner is looking at, which is why the reason has to NAME it.
package companion

import (
	"context"
	"strings"
	"testing"
)

// suspendingExec refuses one step the way a contained gateway turn does.
type suspendingExec struct {
	*fakeStepExecutor
	suspendAt  int
	suspendCID string
}

func (s *suspendingExec) ExecuteStep(ctx context.Context, in StepExecInput) (StepExecResult, error) {
	if in.StepIndex == s.suspendAt {
		return StepExecResult{
			Suspended:            true,
			SuspendedCompanionID: s.suspendCID,
			Stamp:                StepStamp{StepIndex: in.StepIndex, SkillKey: in.SkillKey},
		}, nil
	}
	return s.fakeStepExecutor.ExecuteStep(ctx, in)
}

const suspendedPeerID = "01970000-0000-7000-8000-0000000000ff"

func suspendedRun(t *testing.T, at int) (*RitualRun, *runnerHarness) {
	t.Helper()
	h := newHarness()
	h.exec = newFakeExec()
	rr := newRunner(t, h)
	// Swap in the suspending executor through the same port the runner holds.
	rr.cfg.Steps = &suspendingExec{fakeStepExecutor: h.exec, suspendAt: at, suspendCID: suspendedPeerID}
	run, err := rr.Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return run, h
}

// A containment denial is its OWN terminal, not a failure.
func TestRun_ContainmentDenialIsItsOwnTerminalReason(t *testing.T) {
	run, _ := suspendedRun(t, 0)
	if run.Status == RunStatusFailed {
		t.Fatal("status = failed; a deliberate operator pause must not read as a product fault")
	}
	if run.Status == RunStatusBlocked {
		t.Fatal("status = blocked; that reason means an Armor block and has different learner copy")
	}
	if run.Status != RunStatusCompanionSuspended {
		t.Fatalf("status = %q; want companion_suspended", run.Status)
	}
}

// It NAMES the paused companion, because in a peer step it may not be the one
// the learner is looking at.
func TestRun_ContainmentDenialNamesThePausedCompanion(t *testing.T) {
	run, _ := suspendedRun(t, 0)
	if run.PausedCompanionID != suspendedPeerID {
		t.Errorf("PausedCompanionID = %q; want %q", run.PausedCompanionID, suspendedPeerID)
	}
}

// ADR-252 D5: no mana is charged, because the deny lands before the debit. The
// ritual economy reserves up front, so the reservation must be REFUNDED.
func TestRun_ContainmentDenialChargesNoMana(t *testing.T) {
	run, h := suspendedRun(t, 0)
	if run.ManaCharged != 0 {
		t.Errorf("manaCharged = %d; want 0 on a containment denial", run.ManaCharged)
	}
	if h.mana.refundCalls == 0 {
		t.Error("no refund issued; the ritual runner reserves up front, so a pre-debit deny must refund")
	}
	if h.sink.calls != 0 {
		t.Errorf("sink called %d times; a paused run sinks nothing", h.sink.calls)
	}
}

// The S8 clause-2 rule holds for this terminal too: a pause at step 1 keeps
// step 0's output.
func TestRun_ContainmentDenialKeepsEarlierStepOutputs(t *testing.T) {
	run, _ := suspendedRun(t, 1)
	if len(run.StepOutputs) != 1 {
		t.Fatalf("outputs = %d; want 1 (step 0 completed before the pause)", len(run.StepOutputs))
	}
}

// Learner copy: a designed pause, matching the vocabulary skipped_budget
// already established, and never the word for a fault.
func TestLearnerRunStory_PausedCopyIsNotAFault(t *testing.T) {
	run := &RitualRun{
		RitualName:        "Morning Review",
		Status:            RunStatusCompanionSuspended,
		PausedCompanionID: suspendedPeerID,
	}
	story := LearnerRunStory(run, nil)
	low := strings.ToLower(story.Status)
	if !strings.Contains(low, "paused") {
		t.Errorf("status copy = %q; want it to read as a pause", story.Status)
	}
	for _, faultWord := range []string{"fail", "error", "stopped early"} {
		if strings.Contains(low, faultWord) {
			t.Errorf("status copy = %q; must not read as a fault (%q)", story.Status, faultWord)
		}
	}
	if story.PausedCompanionID != suspendedPeerID {
		t.Errorf("story.PausedCompanionID = %q; want the id so the client can name the companion", story.PausedCompanionID)
	}
}

// The existing statuses keep their copy: this adds a reason, it does not
// renumber the others.
func TestFriendlyRunStatus_ExistingCopyUnchanged(t *testing.T) {
	cases := map[RunStatus]string{
		RunStatusCompleted: "done",
		RunStatusFailed:    "stopped early",
		RunStatusBlocked:   "stopped for safety",
		RunStatusRunning:   "running",
	}
	for st, want := range cases {
		if got := friendlyRunStatus(st); got != want {
			t.Errorf("friendlyRunStatus(%q) = %q; want %q", st, got, want)
		}
	}
	// skipped_budget is the vocabulary the new reason matches: a designed pause
	// that is not a fault. Its wording must stay a pause.
	if got := strings.ToLower(friendlyRunStatus(RunStatusSkippedBudget)); !strings.Contains(got, "paused") {
		t.Errorf("skipped_budget copy = %q; want it to read as a pause", got)
	}
}
