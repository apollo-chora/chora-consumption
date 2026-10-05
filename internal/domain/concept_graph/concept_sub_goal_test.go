package conceptgraph

import (
	"errors"
	"testing"
	"time"
)

// ADR-247 D1: a per-node sub-goal is the learner's stated objective for the
// concept ("mastery here means ..."). Setting it stamps learner_authored by
// default and touches UpdatedAt.
func TestConceptNode_SetSubGoal_SetsAndTrims(t *testing.T) {
	c := mustConcept(t)
	later := cNow.Add(time.Hour)
	if err := c.SetSubGoal("  explain why story points are relative, not hours  ", "", later); err != nil {
		t.Fatalf("SetSubGoal: unexpected error: %v", err)
	}
	if c.SubGoal != "explain why story points are relative, not hours" {
		t.Errorf("SubGoal = %q; want trimmed value", c.SubGoal)
	}
	if c.SubGoalProvenance != ProvenanceLearnerAuthored {
		t.Errorf("SubGoalProvenance = %q; want learner_authored (default)", c.SubGoalProvenance)
	}
	if !c.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v; want touched to %v", c.UpdatedAt, later)
	}
}

// An explicit provenance (a Companion-suggested sub-goal, a follow-on lane) is
// honoured when valid.
func TestConceptNode_SetSubGoal_HonoursExplicitProvenance(t *testing.T) {
	c := mustConcept(t)
	if err := c.SetSubGoal("derive the area under a curve", ProvenanceCompanionSuggestedAccepted, cNow); err != nil {
		t.Fatalf("SetSubGoal: unexpected error: %v", err)
	}
	if c.SubGoalProvenance != ProvenanceCompanionSuggestedAccepted {
		t.Errorf("SubGoalProvenance = %q; want companion_suggested_accepted", c.SubGoalProvenance)
	}
}

// An empty/whitespace text CLEARS the sub-goal and its provenance (the learner
// deletes their objective).
func TestConceptNode_SetSubGoal_ClearsOnEmpty(t *testing.T) {
	c := mustConcept(t)
	if err := c.SetSubGoal("temporary", "", cNow); err != nil {
		t.Fatalf("seed sub-goal: %v", err)
	}
	if err := c.SetSubGoal("   ", "", cNow.Add(time.Minute)); err != nil {
		t.Fatalf("clear sub-goal: %v", err)
	}
	if c.SubGoal != "" || c.SubGoalProvenance != "" {
		t.Errorf("empty text must clear sub-goal + provenance; got %q / %q", c.SubGoal, c.SubGoalProvenance)
	}
}

// A soft-deleted node refuses edits (fail-loud, mirrors Rename/AttachAtom).
func TestConceptNode_SetSubGoal_DeletedRefuses(t *testing.T) {
	c := mustConcept(t)
	c.SoftDelete(cNow)
	if err := c.SetSubGoal("x", "", cNow); !errors.Is(err, ErrConceptDeleted) {
		t.Fatalf("SetSubGoal on a deleted node must be ErrConceptDeleted; got %v", err)
	}
}

// An unknown provenance is rejected (never persist a malformed provenance).
func TestConceptNode_SetSubGoal_InvalidProvenance(t *testing.T) {
	c := mustConcept(t)
	if err := c.SetSubGoal("x", Provenance("bogus"), cNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provenance must be ErrInvalid; got %v", err)
	}
}
