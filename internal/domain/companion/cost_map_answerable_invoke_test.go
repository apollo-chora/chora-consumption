// cost_map_answerable_invoke_test.go — CHO-2016 quiz_me + socratic_drill
// "answerable-pipe" (wave B): the answerable Skill-invoke action codes join the
// canonical cost map (spec §7: quiz_me retrieve 0 · quiz_me generate 25 pre-declared ·
// socratic_drill 15). Mirrors chora_identity migration 0031 (already seeded).
// Without these, the invoke runner's LookupCost pre-flight fail-loud 500s a live
// Skill (SKILL_PRICE_UNKNOWN).
package companion

import "testing"

func TestLookupCost_AnswerableSkillInvokeCodes(t *testing.T) {
	cases := map[string]int64{
		ActionCompanionSkillQuizMe:        0,  // spec §7 (retrieve — pure retrieval practice)
		ActionCompanionSkillQuizMeGen:     25, // spec §7 (generate — pre-declared for a later wave)
		ActionCompanionSkillSocraticDrill: 15, // spec §7
	}
	for code, want := range cases {
		got, err := LookupCost(code)
		if err != nil {
			t.Errorf("LookupCost(%q) error: %v", code, err)
			continue
		}
		if got != want {
			t.Errorf("LookupCost(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestAnswerableSkillInvokeCodes_CanonicalStrings(t *testing.T) {
	// The action-code strings are the spec-§7 `companion_skill_{key}` contract
	// (quiz_me generate uses the `_gen` suffix, mirroring identity 0031).
	cases := map[string]string{
		ActionCompanionSkillQuizMe:        "companion_skill_quiz_me",
		ActionCompanionSkillQuizMeGen:     "companion_skill_quiz_me_gen",
		ActionCompanionSkillSocraticDrill: "companion_skill_socratic_drill",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("action code = %q, want %q", got, want)
		}
	}
}

func TestIsFreeAction_AnswerableSkills(t *testing.T) {
	if !IsFreeAction(ActionCompanionSkillQuizMe) {
		t.Error("quiz_me retrieve must be free (spec §7 pure retrieval)")
	}
	if IsFreeAction(ActionCompanionSkillQuizMeGen) {
		t.Error("quiz_me generate must NOT be free (spec §7 = 25)")
	}
	if IsFreeAction(ActionCompanionSkillSocraticDrill) {
		t.Error("socratic_drill must NOT be free (spec §7 = 15)")
	}
}
