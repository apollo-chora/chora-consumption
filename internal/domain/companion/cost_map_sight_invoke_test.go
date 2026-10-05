// cost_map_sight_invoke_test.go — CHO-2014 P2 "Fledgling's Kit" wave A: the two
// activated sight Skill-invoke action codes join the canonical cost map (spec §7:
// weakness_sight 15 · map_sight 0 pure-retrieval). Without these, the invoke
// runner's LookupCost pre-flight fail-loud 500s a live Skill (SKILL_PRICE_UNKNOWN).
package companion

import "testing"

func TestLookupCost_SightSkillInvokeCodes(t *testing.T) {
	cases := map[string]int64{
		ActionCompanionSkillWeaknessSight: 15, // spec §7
		ActionCompanionSkillMapSight:      0,  // spec §7 (pure retrieval)
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

func TestSightSkillInvokeCodes_CanonicalStrings(t *testing.T) {
	// The action-code strings are the spec-§7 `companion_skill_{key}` contract.
	if ActionCompanionSkillWeaknessSight != "companion_skill_weakness_sight" {
		t.Errorf("weakness_sight action code = %q, want companion_skill_weakness_sight", ActionCompanionSkillWeaknessSight)
	}
	if ActionCompanionSkillMapSight != "companion_skill_map_sight" {
		t.Errorf("map_sight action code = %q, want companion_skill_map_sight", ActionCompanionSkillMapSight)
	}
}

func TestIsFreeAction_SightSkills(t *testing.T) {
	if !IsFreeAction(ActionCompanionSkillMapSight) {
		t.Error("map_sight invoke must be free (spec §7 pure retrieval)")
	}
	if IsFreeAction(ActionCompanionSkillWeaknessSight) {
		t.Error("weakness_sight must NOT be free (spec §7 = 15)")
	}
}
