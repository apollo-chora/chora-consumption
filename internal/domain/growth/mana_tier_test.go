// mana_tier_test.go — domain spec for the config-validation helpers added for
// Task 2(b) (IsValidManaTier + DefaultManaTier). These let the adapter layer
// fail loud on a misconfigured COMPANION_DEFAULT_MANA_TIER rather than silently
// degrade every learner's Companion to basic.
package growth

import "testing"

func TestDefaultManaTier_IsBasic(t *testing.T) {
	if DefaultManaTier != "basic" {
		t.Fatalf("DefaultManaTier = %q, want basic", DefaultManaTier)
	}
	// The canonical default MUST itself be a valid tier (else config plumbing
	// that substitutes it would produce an invalid value).
	if !IsValidManaTier(DefaultManaTier) {
		t.Fatalf("DefaultManaTier %q is not a valid tier", DefaultManaTier)
	}
}

func TestIsValidManaTier_Canonical(t *testing.T) {
	cases := []struct {
		tier string
		want bool
	}{
		{"basic", true},
		{"standard", true},
		{"premium", true},
		{"", false},
		{"BASIC", false}, // case-sensitive
		{"Standard", false},
		{"gold", false},
		{"free", false},
		{"enterprise", false},
	}
	for _, c := range cases {
		if got := IsValidManaTier(c.tier); got != c.want {
			t.Errorf("IsValidManaTier(%q) = %v, want %v", c.tier, got, c.want)
		}
	}
}

// TestIsValidManaTier_MatchesMatrixColumns guards against drift: every tier
// accepted by IsValidManaTier MUST be a column the mana × growth matrix can
// price (else LLMTierForStageAndMana would silently fall back to basic).
func TestIsValidManaTier_MatchesMatrixColumns(t *testing.T) {
	for tier := range canonicalManaTiers {
		// Stage 6 matrix row carries every tier column.
		if _, ok := llmMatrix[StageMatured][tier]; !ok {
			t.Errorf("tier %q valid but absent from llmMatrix[Stage6] columns", tier)
		}
	}
}
