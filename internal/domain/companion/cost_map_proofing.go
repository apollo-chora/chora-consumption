// cost_map_proofing.go — CHO-2040 (R8-6): the Virgin Proofing Test composed
// runner's mana action code.
//
// DELIBERATELY a separate file from cost_map.go (which is under concurrent
// edit by the ceremony edge-scout session tonight): the code registers into
// the SAME canonicalCostMap via init(), so LookupCost / IsFreeAction see it
// identically to a literal map entry.
//
// Spec §1/§3 price_key = `proofing_test_gen`. PROVISIONAL consumption-side
// projection: 25 (the ai_assist authoring tier — the qgen batch charges once
// up-front at the consumption door via the reserve→settle/refund pattern; the
// AUTHORITATIVE price is the chora_identity mana_action_pricing row seeded by
// the matching identity migration, tenant-overridable via the ADR-178
// price-plan layer — the identity server authoritative-resolves on the
// units==0 debit).
package companion

// ActionProofingTestGen is the proofing-test generation action code
// (CHO-2040; spec §1 price_key). Reserved at the consumption door BEFORE the
// ai_assist.started.v2 publish; refunded on publish failure or crew refusal.
const ActionProofingTestGen = "proofing_test_gen"

func init() {
	canonicalCostMap[ActionProofingTestGen] = 25
}
