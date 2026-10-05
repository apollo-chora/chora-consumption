// curve_cover_test.go — branch-completion coverage for the pure curve helpers.
// Targets the out-of-range / unknown-input defensive arms that the happy-path
// tests in curve_test.go never reach. Black-box (package growth_test) to match
// the established convention.
package growth_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ExpPerEventForSource returns 0 for any non-canonical source (the `ok=false`
// arm). The happy-path table in curve_test.go only exercises known sources.
func TestExpPerEventForSource_UnknownReturnsZero(t *testing.T) {
	for _, s := range []string{"", "garbage", "atom_session_typo"} {
		if got := growth.ExpPerEventForSource(s); got != 0 {
			t.Errorf("ExpPerEventForSource(%q) = %d, want 0", s, got)
		}
	}
}

// StateAfterAward clamps a (defensively impossible) regression where the
// recomputed stage would be LOWER than the caller's prevStage. Feed an
// inflated prevStage with EXP that maps to a lower stage; the result must hold
// at prevStage and report no stage-up.
func TestStateAfterAward_DefensiveClampOnRegression(t *testing.T) {
	// prevStage=4 but cumulative EXP of 0 maps to StageForExp(0)=1 (< 4).
	out := growth.StateAfterAward(4, 0, 0)
	if out.NewStage != 4 {
		t.Errorf("NewStage = %d, want 4 (clamped to prevStage)", out.NewStage)
	}
	if out.StageUp {
		t.Errorf("StageUp should be false on a non-advancing/regressing award")
	}
	if out.NewExp != 0 {
		t.Errorf("NewExp = %d, want 0", out.NewExp)
	}
	if len(out.CrossedStages) != 0 {
		t.Errorf("CrossedStages = %v, want empty", out.CrossedStages)
	}
}

// LLMTierForStageAndMana returns the conservative flash-lite floor when the
// stage index is outside [0, len(llmMatrix)). curve_test.go only feeds valid
// stages 0..6.
func TestLLMTierForStageAndMana_StageOutOfRange(t *testing.T) {
	for _, stage := range []int{-1, 7, 100} {
		for _, tier := range []string{"basic", "standard", "premium", "garbage"} {
			if got := growth.LLMTierForStageAndMana(stage, tier); got != "flash-lite" {
				t.Errorf("LLMTierForStageAndMana(%d,%q) = %q, want flash-lite", stage, tier, got)
			}
		}
	}
}

// UnlockedToolsForStage returns an empty (non-nil) slice for out-of-range
// stages — the defensive arm not hit by the in-range table test.
func TestUnlockedToolsForStage_OutOfRange(t *testing.T) {
	for _, stage := range []int{-1, 7, 50} {
		got := growth.UnlockedToolsForStage(stage)
		if got == nil {
			t.Errorf("UnlockedToolsForStage(%d) = nil, want non-nil empty slice", stage)
		}
		if len(got) != 0 {
			t.Errorf("UnlockedToolsForStage(%d) = %v, want empty", stage, got)
		}
	}
}

// MemoryModeForStage falls back to "stateless" for out-of-range stages.
func TestMemoryModeForStage_OutOfRange(t *testing.T) {
	for _, stage := range []int{-1, 7, 99} {
		if got := growth.MemoryModeForStage(stage); got != "stateless" {
			t.Errorf("MemoryModeForStage(%d) = %q, want stateless", stage, got)
		}
	}
}
