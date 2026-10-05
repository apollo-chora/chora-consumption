// ritual_runner_outputs_test.go: RED first for ADR-257 section 6 clause 2, the
// clause the ADR itself names as "the clause most likely to be dropped under
// time pressure and the one carrying most of the value".
//
// Today a failed or blocked step returns early (`ritual_runner.go:296-305`) and
// the buffered outputs of every step that DID complete are discarded with the
// stack frame. Those are exactly what a learner needs to see where a run stopped:
// a run that says "stopped early" and shows nothing is indistinguishable from a
// run that never started.
package companion

import (
	"context"
	"testing"
)

// runFor executes a two-step ritual through the harness and returns the run.
func runFor(t *testing.T, h *runnerHarness) *RitualRun {
	t.Helper()
	run, err := newRunner(t, h).Run(context.Background(), runInput(publishedRitual(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return run
}

// Clause 1 through the runner: a completed run carries one output per stamp, in
// step order, matched to the Skill that produced it.
func TestRun_CompletedRunPersistsOneOutputPerStep(t *testing.T) {
	run := runFor(t, newHarness())
	if run.Status != RunStatusCompleted {
		t.Fatalf("status = %q; want completed", run.Status)
	}
	if len(run.StepOutputs) != len(run.Stamps) {
		t.Fatalf("outputs = %d, stamps = %d; want one output per stamp",
			len(run.StepOutputs), len(run.Stamps))
	}
	for i, o := range run.StepOutputs {
		if o.StepIndex != i {
			t.Errorf("outputs[%d].StepIndex = %d; want %d (step order is the contract)", i, o.StepIndex, i)
		}
		if o.SkillKey != run.Stamps[i].SkillKey {
			t.Errorf("outputs[%d].SkillKey = %q; want %q", i, o.SkillKey, run.Stamps[i].SkillKey)
		}
		if o.Text == "" {
			t.Errorf("outputs[%d].Text is empty; the fake executor returned output for this step", i)
		}
	}
}

// CLAUSE 2, the failure half. Step 0 completes, step 1 explodes. The run is
// failed and refunded, and it KEEPS step 0's output.
func TestRun_FailedRunKeepsTheOutputsOfCompletedSteps(t *testing.T) {
	h := newHarness()
	h.exec.failAt = 1
	run := runFor(t, h)

	if run.Status != RunStatusFailed {
		t.Fatalf("status = %q; want failed", run.Status)
	}
	if len(run.StepOutputs) != 1 {
		t.Fatalf("outputs = %d; want 1 (step 0 completed and its output is where the learner sees the run stop)", len(run.StepOutputs))
	}
	if run.StepOutputs[0].StepIndex != 0 || run.StepOutputs[0].Text == "" {
		t.Errorf("outputs[0] = %+v; want step 0's real output", run.StepOutputs[0])
	}
	// The economy is untouched by this change: a failed run still refunds.
	if run.ManaCharged != 0 {
		t.Errorf("manaCharged = %d; want 0 on a failed run", run.ManaCharged)
	}
	// And it still sinks nothing.
	if h.sink.calls != 0 {
		t.Errorf("sink called %d times on a failed run; want 0", h.sink.calls)
	}
}

// CLAUSE 2, the blocked half. An Armor block at step 1 keeps step 0's output for
// the same reason.
func TestRun_BlockedRunKeepsTheOutputsOfCompletedSteps(t *testing.T) {
	h := newHarness()
	h.exec.blockAt = 1
	run := runFor(t, h)

	if run.Status != RunStatusBlocked {
		t.Fatalf("status = %q; want blocked", run.Status)
	}
	if len(run.StepOutputs) != 1 {
		t.Fatalf("outputs = %d; want 1 (step 0 completed before the block)", len(run.StepOutputs))
	}
	if run.StepOutputs[0].SkillKey != "explain_anew" {
		t.Errorf("outputs[0].SkillKey = %q; want explain_anew", run.StepOutputs[0].SkillKey)
	}
}

// A run that fails at the very FIRST step has nothing to keep, and must say so
// with an empty list rather than a nil that a JSONB column would write as null.
func TestRun_FailureAtTheFirstStepPersistsAnEmptyList(t *testing.T) {
	h := newHarness()
	h.exec.failAt = 0
	run := runFor(t, h)

	if run.Status != RunStatusFailed {
		t.Fatalf("status = %q; want failed", run.Status)
	}
	if run.StepOutputs == nil {
		t.Fatal("StepOutputs is nil; want an empty slice so the column never stores null")
	}
	if len(run.StepOutputs) != 0 {
		t.Errorf("outputs = %d; want 0 (nothing completed)", len(run.StepOutputs))
	}
}

// The learner story of a stopped run now shows the steps that ran with what they
// produced, so "stopped early" has a where.
func TestRun_FailedRunStoryShowsWhereItStopped(t *testing.T) {
	h := newHarness()
	h.exec.failAt = 1
	run := runFor(t, h)

	story := LearnerRunStory(run, nil)
	if story.Status != "stopped early" {
		t.Fatalf("status copy = %q; want 'stopped early'", story.Status)
	}
	if len(story.Steps) == 0 {
		t.Fatal("story has no steps; a stopped run must still show what ran")
	}
	if story.Steps[0].Summary == "" {
		t.Error("step 0 has no summary; its output was captured and must be rendered")
	}
}
