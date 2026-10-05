// cost_map_skill_invoke_test.go — CHO-2013 P1.B (R4-4): the three st2
// Skill-invoke action codes join the canonical in-memory cost map (spec §7:
// progress_mirror free · recap_scribe 5 · explain_anew 10). Authoritative
// pricing lives in chora_identity.mana_action_pricing (P0-seeded rows); this
// map is the consumption-side pre-flight affordability projection.
package companion

import "testing"

func TestLookupCost_SkillInvokeCodes(t *testing.T) {
	cases := map[string]int64{
		ActionCompanionSkillProgressMirror: 0,
		ActionCompanionSkillRecapScribe:    5,
		ActionCompanionSkillExplainAnew:    10,
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

func TestSkillInvokeCodes_CanonicalStrings(t *testing.T) {
	// The action-code strings are the spec-§7 `companion_skill_{key}` contract —
	// they MUST match the P0-seeded identity price rows byte-for-byte.
	cases := map[string]string{
		ActionCompanionSkillProgressMirror: "companion_skill_progress_mirror",
		ActionCompanionSkillRecapScribe:    "companion_skill_recap_scribe",
		ActionCompanionSkillExplainAnew:    "companion_skill_explain_anew",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("action code = %q, want %q", got, want)
		}
	}
}

func TestIsFreeAction_ProgressMirror(t *testing.T) {
	if !IsFreeAction(ActionCompanionSkillProgressMirror) {
		t.Error("progress_mirror invoke must be free (spec §7)")
	}
	if IsFreeAction(ActionCompanionSkillRecapScribe) || IsFreeAction(ActionCompanionSkillExplainAnew) {
		t.Error("recap_scribe / explain_anew must NOT be free")
	}
}
