// cost_map_seeker_invoke_test.go — P5 Far Sight (CHO-2017, ADR-231 D6): the
// Seeker grounded-egress meters. Each Seeker is metered ONCE at the gateway
// grounded-search egress carrying its OWN spec-§2.4 price (fact_check 40,
// web_research 80) — the metering is RE-HOMED from the fenced verify/research
// Invoke turn to the grounded egress (grounded.Query.ActionCode), NOT re-priced.
// Without a canonical cost the invoke runner's LookupCost pre-flight fail-loud
// 500s. Authoritative pricing lives in chora_identity.mana_action_pricing (mig
// 0031, meter_home defaults to gateway); this map is the affordability projection
// + the reported mana_charged.
package companion

import "testing"

func TestLookupCost_SeekerFactCheck(t *testing.T) {
	if ActionCompanionSkillFactCheck != "companion_skill_fact_check" {
		t.Fatalf("action code = %q, want the spec-§2.4 companion_skill_fact_check", ActionCompanionSkillFactCheck)
	}
	got, err := LookupCost(ActionCompanionSkillFactCheck)
	if err != nil {
		t.Fatalf("LookupCost(%s) = %v — an unregistered code bricks the runner with SKILL_PRICE_UNKNOWN", ActionCompanionSkillFactCheck, err)
	}
	if got != 40 {
		t.Errorf("LookupCost(fact_check) = %d, want 40 (spec §2.4)", got)
	}
	if IsFreeAction(ActionCompanionSkillFactCheck) {
		t.Error("fact_check is an external-egress grounded call — it must NOT be free")
	}
}

func TestLookupCost_SeekerWebResearch(t *testing.T) {
	if ActionCompanionSkillWebResearch != "companion_skill_web_research" {
		t.Fatalf("action code = %q, want the spec-§2.4 companion_skill_web_research", ActionCompanionSkillWebResearch)
	}
	got, err := LookupCost(ActionCompanionSkillWebResearch)
	if err != nil {
		t.Fatalf("LookupCost(%s) = %v — an unregistered code bricks the runner with SKILL_PRICE_UNKNOWN", ActionCompanionSkillWebResearch, err)
	}
	if got != 80 {
		t.Errorf("LookupCost(web_research) = %d, want 80 (spec §2.4 — the priciest Seeker)", got)
	}
	if IsFreeAction(ActionCompanionSkillWebResearch) {
		t.Error("web_research is an external-egress grounded call — it must NOT be free")
	}
}
