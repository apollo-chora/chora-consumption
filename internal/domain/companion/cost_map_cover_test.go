package companion

import "testing"

// TestChatTurnActionCodeForTier_MapsTierToActionCode exercises all three
// branches of the tier→action-code map (ADR-154 D3). The "default → basic"
// branch is the safety fallback for unknown / empty tiers — asserting it
// guards against a regression that would silently price an unknown plan at a
// higher tier (an over-charge bug) or fail loud where it should degrade.
func TestChatTurnActionCodeForTier_MapsTierToActionCode(t *testing.T) {
	cases := []struct {
		name string
		tier string
		want string
	}{
		{"premium", "premium", ActionCompanionChatTurnPremium},
		{"standard", "standard", ActionCompanionChatTurnStandard},
		{"basic", "basic", ActionCompanionChatTurnBasic},
		{"unknown defaults to basic", "enterprise", ActionCompanionChatTurnBasic},
		{"empty defaults to basic", "", ActionCompanionChatTurnBasic},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChatTurnActionCodeForTier(c.tier); got != c.want {
				t.Fatalf("ChatTurnActionCodeForTier(%q) = %q, want %q", c.tier, got, c.want)
			}
		})
	}
}

// TestChatTurnActionCodeForTier_ResolvedCodesArePriced is a cross-check that
// every action code the resolver can emit is registered in the canonical cost
// map. A new tier added to the resolver without a matching cost-map entry
// would make LookupCost fail loud at debit time — this catches that drift at
// the unit level where the per-tier prices are also asserted (5/15/30).
func TestChatTurnActionCodeForTier_ResolvedCodesArePriced(t *testing.T) {
	want := map[string]int64{
		"premium":  30,
		"standard": 15,
		"basic":    5,
	}
	for tier, wantCost := range want {
		code := ChatTurnActionCodeForTier(tier)
		got, err := LookupCost(code)
		if err != nil {
			t.Fatalf("LookupCost(%q) for tier %q: %v", code, tier, err)
		}
		if got != wantCost {
			t.Fatalf("tier %q -> %q cost = %d, want %d", tier, code, got, wantCost)
		}
	}
}
