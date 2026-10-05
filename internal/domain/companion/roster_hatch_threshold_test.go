package companion_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// D3 (RED). The roster read and the single-companion growth read disagreed on
// the denominator a Stage-0 pod is warming towards.
//
// ExpToNextStage(0) resolves StageExpThresholds[1], which is 0 BY DESIGN: the
// hatch is an event, not a point on the growth curve, and a non-zero entry
// there would let stage-up arithmetic auto-hatch past the ceremony. The growth
// read special-cases stage 0 to the CONFIGURED incubation gate; the roster read
// did not, so /me/familiars said 0 while /me/familiars/{id}/growth said 25.
//
// The card believed the roster: a pod with 32 EXP rendered "Warming 32 / 0 EXP"
// on a bar pinned at 0 percent, behind a hatch CTA that could never free.
func TestExpToNextStageOrHatch(t *testing.T) {
	const configured = 25

	t.Run("a Stage-0 pod warms towards the configured hatch gate", func(t *testing.T) {
		if got := companion.ExpToNextStageOrHatch(0, configured); got != configured {
			t.Fatalf("stage 0: got %d, want the configured hatch gate %d", got, configured)
		}
	})

	t.Run("the gate is read from config, never recomputed as a constant", func(t *testing.T) {
		if got := companion.ExpToNextStageOrHatch(0, 40); got != 40 {
			t.Fatalf("stage 0 with an overridden gate: got %d, want 40", got)
		}
	})

	t.Run("every hatched stage still answers from the growth curve", func(t *testing.T) {
		for stage := 1; stage <= 6; stage++ {
			want := companion.ExpToNextStage(stage)
			if got := companion.ExpToNextStageOrHatch(stage, configured); got != want {
				t.Errorf("stage %d: got %d, want the curve value %d", stage, got, want)
			}
		}
	})

	t.Run("no configured gate leaves the curve value standing, never a fabricated one", func(t *testing.T) {
		if got := companion.ExpToNextStageOrHatch(0, 0); got != companion.ExpToNextStage(0) {
			t.Fatalf("stage 0 with no gate: got %d, want the curve value %d", got, companion.ExpToNextStage(0))
		}
	})
}
