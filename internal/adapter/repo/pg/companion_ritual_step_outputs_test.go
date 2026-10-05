package pg

// companion_ritual_step_outputs_test.go: the one rule the S8 column write has to
// hold that no PREPARE can check for us.
//
// step_outputs is NOT NULL DEFAULT '[]'. A Go nil slice marshals to JSON `null`,
// which the column would refuse, so every terminal that never entered the step
// loop (skipped_budget, a stale-sweep reclaim) would fail its write. The domain
// normalises nil at terminate; this is the adapter's own guard, because the
// adapter is what actually hands the value to Postgres.

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestMarshalStepOutputs_NilBecomesEmptyArrayNeverNull(t *testing.T) {
	b, err := marshalStepOutputs(nil)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("nil marshalled to %s; want [] (the column is NOT NULL and would refuse null)", b)
	}
}

func TestMarshalStepOutputs_EmptySliceIsAlsoAnEmptyArray(t *testing.T) {
	b, err := marshalStepOutputs([]companion.StepOutput{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("empty slice marshalled to %s; want []", b)
	}
}

func TestMarshalStepOutputs_EntriesKeepTheSnakeCaseWireShape(t *testing.T) {
	b, err := marshalStepOutputs([]companion.StepOutput{
		{StepIndex: 0, StepID: "s-a", SkillKey: "explain_anew", Text: "hello", Truncated: true},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `[{"step_index":0,"step_id":"s-a","skill_key":"explain_anew","text":"hello","truncated":true}]`
	if string(b) != want {
		t.Errorf("wire = %s\nwant   %s", b, want)
	}
}
