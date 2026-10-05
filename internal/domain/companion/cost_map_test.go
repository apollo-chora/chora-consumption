// BE-USR-2 — Action-cost map tests per ADR-142 §4.
//
// The cost map is hardcoded with the canonical defaults from ADR-142 and
// looked up by action_code at debit time. Free actions (cost=0) skip the
// quoter call entirely so the demo flow never hits chora-identity for
// pure game mechanics.
//
// TDD strict — this file exists BEFORE cost_map.go.
package companion

import (
	"errors"
	"testing"
)

// TestLookupCost_KnownActionsReturnExpectedDefaults — verifies the
// canonical defaults from ADR-142 §4 action-cost matrix.
func TestLookupCost_KnownActionsReturnExpectedDefaults(t *testing.T) {
	cases := []struct {
		action string
		want   int64
	}{
		{ActionSummonCompanion, 0},
		{ActionDailyDoseDeterministic, 0},
		{ActionDailyDoseCoach, 10},
		{ActionAtomAuthoringAIAssist, 25},
		{ActionQuestionGeneration, 50},
		{ActionCompanionChat, 20},
		{ActionKnowledgeGraphTraversal, 100},
		{ActionBossChallengeAtomGen, 200},
		{ActionFullExamPrepCoach, 500},
	}
	for _, c := range cases {
		got, err := LookupCost(c.action)
		if err != nil {
			t.Errorf("LookupCost(%q) err = %v", c.action, err)
			continue
		}
		if got != c.want {
			t.Errorf("LookupCost(%q) = %d, want %d", c.action, got, c.want)
		}
	}
}

// TestLookupCost_UnknownActionReturnsError — an unknown action_code MUST
// fail loud rather than default to 0 (which would silently bypass mana).
func TestLookupCost_UnknownActionReturnsError(t *testing.T) {
	_, err := LookupCost("not_a_real_action")
	if err == nil {
		t.Fatal("LookupCost on unknown action: err = nil, want non-nil")
	}
	if !errors.Is(err, ErrUnknownActionCode) {
		t.Errorf("LookupCost err = %v, want ErrUnknownActionCode", err)
	}
}

// TestIsFreeAction_TrueForZeroCost — helper used by the dose composer to
// short-circuit before calling the quoter.
func TestIsFreeAction_TrueForZeroCost(t *testing.T) {
	if !IsFreeAction(ActionSummonCompanion) {
		t.Errorf("IsFreeAction(summon_companion) = false, want true")
	}
	if !IsFreeAction(ActionDailyDoseDeterministic) {
		t.Errorf("IsFreeAction(daily_dose_deterministic) = false, want true")
	}
	if IsFreeAction(ActionDailyDoseCoach) {
		t.Errorf("IsFreeAction(daily_dose_coach) = true, want false (cost=10)")
	}
	if IsFreeAction(ActionFullExamPrepCoach) {
		t.Errorf("IsFreeAction(full_exam_prep_coach) = true, want false (cost=500)")
	}
}

// TestIsFreeAction_UnknownActionDefaultsFalse — unknown codes are treated
// as paid (fail-safe) since they cannot be confirmed free.
func TestIsFreeAction_UnknownActionDefaultsFalse(t *testing.T) {
	if IsFreeAction("invented_action") {
		t.Errorf("IsFreeAction(invented_action) = true, want false (unknown)")
	}
}
