// cost_map_edge_scout_first_test.go — CHO-2040 (owner ruling R8-1): the FIRST
// ceremony edge-scout run per goal is FREE; re-runs stay at the paid key.
//
// Zero-cost mechanism GROUNDING: the canonical map has always carried explicit
// zero-cost entries (summon_companion / daily_dose_deterministic /
// companion_skill_progress_mirror = 0), and chora_identity.mana_action_pricing
// CHECKs mana_cost >= 0 (zero rows exist: companion_skill_quiz_me et al.). The
// only "never free" assertion in this package (cost_map_edge_scout_test.go)
// binds the RE-RUN code alone — so the owner-ruled free first run is a SECOND
// registered code at an explicit 0, not a mutation of the paid entry. Unknown
// codes keep hard-failing via LookupCost (silent bypass = abuse vector).
//
// The `_first` code is still STAMPED on the engine turn so the model-gateway
// meters it (IMDA D3: never an un-metered turn) — it just debits 0.
package companion

import "testing"

func TestLookupCost_CeremonyEdgeScoutFirstRunFree(t *testing.T) {
	if ActionCompanionCeremonyEdgeScoutFirst != "companion_ceremony_edge_scout_first" {
		t.Fatalf("action code = %q, want the locked companion_ceremony_edge_scout_first",
			ActionCompanionCeremonyEdgeScoutFirst)
	}
	cost, err := LookupCost(ActionCompanionCeremonyEdgeScoutFirst)
	if err != nil {
		t.Fatalf("LookupCost(%s) = %v — an unregistered code bricks the first-run path with EDGE_SCOUT_PRICE_UNKNOWN",
			ActionCompanionCeremonyEdgeScoutFirst, err)
	}
	if cost != 0 {
		t.Fatalf("cost = %d, want exactly 0 (owner ruling R8-1: first run per goal is free)", cost)
	}
	if !IsFreeAction(ActionCompanionCeremonyEdgeScoutFirst) {
		t.Fatalf("first-run code must report as a free action")
	}
}

func TestLookupCost_CeremonyEdgeScoutRerunStaysPaid(t *testing.T) {
	// The R8-1 ruling prices RE-RUNS at 25 — the free first run must never
	// bleed into the paid key (two distinct codes, distinct prices).
	if ActionCompanionCeremonyEdgeScoutFirst == ActionCompanionCeremonyEdgeScout {
		t.Fatalf("first-run and re-run codes must be distinct")
	}
	cost, err := LookupCost(ActionCompanionCeremonyEdgeScout)
	if err != nil {
		t.Fatalf("LookupCost(re-run) = %v", err)
	}
	if cost != 25 {
		t.Fatalf("re-run cost = %d, want the R8-1 locked 25", cost)
	}
}
