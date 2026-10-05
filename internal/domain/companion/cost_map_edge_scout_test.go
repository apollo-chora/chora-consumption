// cost_map_edge_scout_test.go — CHO-2040 (CR §8 R7-3): the ceremony
// edge-scout price key. LookupCost fails-loud on unregistered codes (the
// invoke machinery 500s SKILL_PRICE_UNKNOWN), so the composed runner's code
// MUST be registered in the consumption-side canonical map. The authoritative
// per-tenant price lives in chora_identity.mana_action_pricing — that seed is
// an owner decision and is NOT minted here.
package companion

import "testing"

func TestLookupCost_CeremonyEdgeScoutRegistered(t *testing.T) {
	if ActionCompanionCeremonyEdgeScout != "companion_ceremony_edge_scout" {
		t.Fatalf("action code = %q, want the locked companion_ceremony_edge_scout", ActionCompanionCeremonyEdgeScout)
	}
	cost, err := LookupCost(ActionCompanionCeremonyEdgeScout)
	if err != nil {
		t.Fatalf("LookupCost(%s) = %v — an unregistered code bricks the runner with SKILL_PRICE_UNKNOWN", ActionCompanionCeremonyEdgeScout, err)
	}
	if cost <= 0 {
		t.Fatalf("cost = %d, want > 0 (one extraction turn is a metered LLM action, never free)", cost)
	}
	if IsFreeAction(ActionCompanionCeremonyEdgeScout) {
		t.Fatalf("edge-scout must not short-circuit as a free action")
	}
}
