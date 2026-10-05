// ritual_step_suspension_test.go: RED first for the adapter half of N13
// (ADR-257 D7, ADR-252 D5 and D6).
//
// A contained companion's turn comes back as an engine ERROR frame carrying the
// containment code. Today collectStepTurnResponse turns every error frame into a
// generic Go error, so the runner terminates the run as `failed` and the learner
// is told the product broke when in fact an operator paused their companion.
//
// ADR-252 D6 is explicit that the distinction must be carried by the ERROR and
// never inferred from an empty result, and calls that "the single most likely
// way to build this wrong". So these tests check the discriminator, not the
// emptiness.
package clients

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// errorFrameStream yields one engine error frame with the given code.
func errorFrameStream(code, msg string) <-chan ChatStreamFrame {
	ch := make(chan ChatStreamFrame, 1)
	b, _ := json.Marshal(map[string]string{"code": code, "message": msg})
	ch <- ChatStreamFrame{Type: ChatFrameError, Data: b}
	close(ch)
	return ch
}

// The caller-facing SSE spelling.
func TestCollectStepTurn_ContainmentCodeIsNotAGenericError(t *testing.T) {
	got, err := collectStepTurnResponse(errorFrameStream(companion.TurnErrCodeSuspended, "companion is suspended"))
	if err != nil {
		t.Fatalf("a containment denial is a terminal outcome, not a transport error: %v", err)
	}
	if !got.Suspended {
		t.Error("Suspended = false; the containment code must set it")
	}
	if got.Blocked {
		t.Error("Blocked = true; a containment denial is not an Armor content block")
	}
}

// The kennel's REJECTED spelling of the same fact. Two layers spell it
// differently and both must land on the same terminal.
func TestCollectStepTurn_KennelSpellingAlsoMaps(t *testing.T) {
	got, err := collectStepTurnResponse(errorFrameStream(companion.KennelErrCodeCompanionSuspended, "suspended"))
	if err != nil {
		t.Fatalf("kennel spelling must map too: %v", err)
	}
	if !got.Suspended {
		t.Errorf("Suspended = false for code %q", companion.KennelErrCodeCompanionSuspended)
	}
}

// Every OTHER error frame stays a fail-loud error. Widening this would turn a
// real outage into a "your companion is paused" message, which is worse than
// the defect being fixed.
func TestCollectStepTurn_OtherErrorCodesStillFailLoud(t *testing.T) {
	for _, code := range []string{"COMPANION_TURN_FAILED", "COMPANION_TURN_TIMEOUT", "INTERNAL", ""} {
		if _, err := collectStepTurnResponse(errorFrameStream(code, "boom")); err == nil {
			t.Errorf("code %q produced no error; only the containment code is a terminal", code)
		}
	}
}

// The executor carries the denial into the domain result, naming the companion.
func TestStepExecutor_SuspendedMapsToItsOwnTerminal(t *testing.T) {
	turn := &fakeStepTurn{resp: RitualStepTurnResponse{Suspended: true}}
	e := NewRitualStepExecutor(turn)
	res, err := e.ExecuteStep(context.Background(), stepInput())
	if err != nil {
		t.Fatalf("a containment denial is a terminal StepExecResult, not a Go error: %v", err)
	}
	if !res.Suspended {
		t.Error("res.Suspended = false; the denial was lost at the adapter boundary")
	}
	if res.Blocked {
		t.Error("res.Blocked = true; the two denials must stay distinct")
	}
	if res.SuspendedCompanionID != "fam-1" {
		t.Errorf("SuspendedCompanionID = %q; want the step's companion", res.SuspendedCompanionID)
	}
	// Even a paused step stamps its identity, like a blocked one does.
	if res.Stamp.StepIndex != 2 || res.Stamp.SkillKey != "explain_anew" {
		t.Errorf("paused step must still stamp identity: %+v", res.Stamp)
	}
}

// An ordinary turn is untouched: nothing about this change may make a healthy
// run look paused.
func TestStepExecutor_HealthyTurnIsNotSuspended(t *testing.T) {
	turn := &fakeStepTurn{resp: RitualStepTurnResponse{OutputText: "an answer"}}
	res, err := NewRitualStepExecutor(turn).ExecuteStep(context.Background(), stepInput())
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if res.Suspended || res.SuspendedCompanionID != "" {
		t.Errorf("a healthy turn reported a pause: %+v", res)
	}
}
