// curve_incubation_test.go — F-I1.1 (CHO-2088, ADR-228 incubation): a
// Stage-0 egg accrues growth_exp but MUST NEVER advance past Stage 0 via
// EXP. Only the explicit hatch ceremony (CommitHatch) leaves Stage 0.
//
// This closes the latent bug where StageForExp maps any EXP < 50 to Stage 1
// (post-hatch Baby), so a pre-hatch egg receiving its first award silently
// jumped to Stage 1 UNHATCHED. StateAfterAward is the SINGLE transition
// point (pg + inmem both call it), so the Stage-0 clamp lives here.
package growth_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestStateAfterAward_Stage0EggAccruesButNeverAdvances(t *testing.T) {
	// A fresh egg (Stage 0, 0 EXP) receiving its first EXP award: the EXP
	// accrues but the stage stays 0 — no silent jump to Stage 1.
	s := growth.StateAfterAward(0, 0, 10)
	if s.NewStage != 0 {
		t.Errorf("Stage-0 egg first award: NewStage = %d, want 0 (no advance)", s.NewStage)
	}
	if s.NewExp != 10 {
		t.Errorf("Stage-0 egg first award: NewExp = %d, want 10 (exp accrues)", s.NewExp)
	}
	if s.StageUp {
		t.Errorf("Stage-0 egg first award: StageUp = true, want false")
	}
	if len(s.CrossedStages) != 0 {
		t.Errorf("Stage-0 egg first award: CrossedStages = %v, want empty", s.CrossedStages)
	}
}

func TestStateAfterAward_Stage0EggStaysAtStage0PastThreshold(t *testing.T) {
	// An egg that has already accrued past the Stage-2 (50 EXP) curve
	// boundary STILL stays at Stage 0 — the curve simply does not apply to a
	// pre-hatch egg. It only ever leaves Stage 0 through the hatch ceremony.
	s := growth.StateAfterAward(0, 45, 60)
	if s.NewStage != 0 {
		t.Errorf("Stage-0 egg past curve boundary: NewStage = %d, want 0", s.NewStage)
	}
	if s.NewExp != 105 {
		t.Errorf("Stage-0 egg past curve boundary: NewExp = %d, want 105", s.NewExp)
	}
	if s.StageUp {
		t.Errorf("Stage-0 egg past curve boundary: StageUp = true, want false")
	}
}

func TestCrossesHatchThreshold(t *testing.T) {
	const threshold = 25
	cases := []struct {
		name                       string
		prevStage, prevExp, newExp int
		want                       bool
	}{
		{"crosses on this award", 0, 20, 30, true},
		{"lands exactly on threshold", 0, 24, 25, true},
		{"still below threshold", 0, 10, 20, false},
		{"already past threshold (one-shot)", 0, 30, 40, false},
		{"prev already at threshold", 0, 25, 40, false},
		{"hatched companion never stirs", 1, 20, 30, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := growth.CrossesHatchThreshold(tc.prevStage, tc.prevExp, tc.newExp, threshold); got != tc.want {
				t.Errorf("CrossesHatchThreshold(%d,%d,%d,%d) = %v, want %v",
					tc.prevStage, tc.prevExp, tc.newExp, threshold, got, tc.want)
			}
		})
	}
}

func TestCrossesHatchThreshold_DisabledWhenThresholdNonPositive(t *testing.T) {
	if growth.CrossesHatchThreshold(0, 0, 100, 0) {
		t.Errorf("threshold 0 must disable stirring")
	}
	if growth.CrossesHatchThreshold(0, 0, 100, -5) {
		t.Errorf("negative threshold must disable stirring")
	}
}

func TestStateAfterAward_HatchedCompanionStillAdvancesNormally(t *testing.T) {
	// Positive control: a hatched Companion (Stage 1) is unaffected by the
	// Stage-0 clamp — the ADR-149 curve applies from Stage 1 onward.
	s := growth.StateAfterAward(1, 40, 15) // crosses 50 → Stage 2
	if s.NewStage != 2 {
		t.Errorf("Stage-1 award crossing 50: NewStage = %d, want 2", s.NewStage)
	}
	if !s.StageUp {
		t.Errorf("Stage-1 award crossing 50: StageUp = false, want true")
	}
}
