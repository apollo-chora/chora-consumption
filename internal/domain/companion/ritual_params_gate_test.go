// ritual_params_gate_test.go - RED-first for CHO-2362: the param VALUE gate
// on the ritual grammar. Publish and run share ONE rule - params that are
// PRESENT must be valid (unknown key / bad enum / bad ref shape / over-long
// text fail loud); params stay OPTIONAL in rituals (absent → the Skill's
// ritual-context default behaviour, the walked-live status quo). Required-ness
// is an invoke-path contract, deliberately NOT enforced here - enforcing it at
// publish would retro-brick every param-less ritual the designer has already
// published.
package companion

import (
	"context"
	"errors"
	"testing"
)

func TestValidateStepsRejectsBadParamValue(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	bad := []RitualStep{{SkillKey: "explain_anew", Params: map[string]any{"style": "sarcastic"}}}
	err := r.ValidateSteps(bad, ritualTestCaps())
	if err == nil {
		t.Fatal("a step with an invalid enum value must fail validation")
	}
	if !errors.Is(err, ErrRitualStepParamsInvalid) {
		t.Errorf("err = %v, want ErrRitualStepParamsInvalid", err)
	}
}

func TestValidateStepsAcceptsAbsentAndValidParams(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	ok := []RitualStep{
		{SkillKey: "explain_anew", Params: map[string]any{}}, // param-less: the live idiom
		{SkillKey: "quiz_me", Params: map[string]any{"scope": "weak", "count": "4"}},
	}
	if err := r.ValidateSteps(ok, ritualTestCaps()); err != nil {
		t.Fatalf("valid params must pass, got %v", err)
	}
}

func TestPublishRejectsBadParamValueAndPersistsNothing(t *testing.T) {
	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkChat)
	bad := []RitualStep{{SkillKey: "quiz_me", Params: map[string]any{"count": "9"}}}
	if _, err := r.Publish(bad, "allow", ritualTestCaps(), fixedClock()); !errors.Is(err, ErrRitualStepParamsInvalid) {
		t.Fatalf("publish with an out-of-range int must fail with the params sentinel, got %v", err)
	}
	if len(r.Revisions) != 0 || r.CurrentRevision != 0 {
		t.Error("a rejected publish must append nothing")
	}
}

// Run-time twin of the Ruling-#6 equipped re-check: a published revision whose
// step params fail VALUE validation at run time (e.g. the catalogue tightened,
// or an API-authored revision carried junk into the prompt surface) fails loud
// BEFORE any spend - no reserve, no steps, no run record.
func TestRunFailsLoudOnInvalidStepParams(t *testing.T) {
	h := newHarness()
	rr := newRunner(t, h)

	r, _ := NewRitual("t-1", "fam-1", "R", TriggerManual, SinkQuestionBank)
	caps := ritualTestCaps()
	good := []RitualStep{{SkillKey: "explain_anew", Params: map[string]any{"style": "story"}}, {SkillKey: "flashcard_forge", Params: map[string]any{}}}
	if _, err := r.Publish(good, "allow", caps, fixedClock()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Corrupt the frozen revision in memory to simulate a pre-gate revision
	// carrying an invalid value (the gate did not exist when it was authored).
	r.Revisions[0].Steps[0].Params = map[string]any{"style": "sarcastic"}

	run, err := rr.Run(context.Background(), runInput(r))
	if err == nil {
		t.Fatal("run with invalid step params must fail loud (Go error)")
	}
	if !errors.Is(err, ErrRitualStepParamsInvalid) {
		t.Errorf("err = %v, want ErrRitualStepParamsInvalid", err)
	}
	if run != nil {
		t.Errorf("no run record on a pre-flight guard failure, got %+v", run)
	}
	if h.mana.reserveCalls != 0 || len(h.exec.calls) != 0 || h.sink.calls != 0 {
		t.Error("pre-flight guard must not reserve, execute, or sink")
	}
}
