// cost_map_proofing_cover_test.go — CHO-2040 (R8-6): the proofing-test
// composed runner's mana action code registration.
//
// The code is registered from cost_map_proofing.go via init() into the SAME
// canonicalCostMap — a SEPARATE file on purpose: cost_map.go is under
// concurrent edit by the ceremony edge-scout session tonight.
package companion

import "testing"

func TestProofingTestGen_ActionCodeRegistered(t *testing.T) {
	if ActionProofingTestGen != "proofing_test_gen" {
		t.Fatalf("action code = %q, want proofing_test_gen (spec §1/§3 price_key)", ActionProofingTestGen)
	}
	cost, err := LookupCost(ActionProofingTestGen)
	if err != nil {
		t.Fatalf("LookupCost(proofing_test_gen): %v — an unregistered code 500s the runner", err)
	}
	if cost != 25 {
		t.Fatalf("provisional cost = %d, want 25", cost)
	}
	if IsFreeAction(ActionProofingTestGen) {
		t.Fatalf("proofing_test_gen must NOT be free")
	}
}
