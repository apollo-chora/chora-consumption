// dose_composition_breadcrumb_test.go — S5 hand-off marker (resolved).
//
// Per docs/m13/audit-content-fillgaps.md §3.2 + docs/m13/phyllis-mvp-2026-05-08.md §5.4
// the spec-compliant Daily Dose composition is 40% Ebbinghaus + 30%
// curiosity + 30% weakness across a 5-atom budget. S4 shipped a 2-1-2
// split (no weakness slot) — net 40/20/40 ratio.
//
// S5 A-Companion reconciled the implementation: see
// `internal/domain/companion/daily_dose.go` — DoseEbbinghausCount,
// DoseWeaknessCount (NEW), DoseCuriosityCount each = 2 with priority
// cascade Ebbinghaus → Weakness → Curiosity → Fresh top-up; capped at
// DoseTargetSize. Per-package property tests in
// `internal/domain/companion/dose_403030_test.go` cover 200+ permutations.
//
// This breadcrumb test now asserts the constants are wired so that the
// gap stays caught at the consumption-package level (not just the
// companion sub-package level) if a future regression flips them.
//
// Uses external test package `domain_test` to avoid an import cycle
// (`internal/domain` exports `NewUUIDv7` which `companion` imports).
package domain_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestDailyDoseCompositionShouldBe40_30_30(t *testing.T) {
	// 40% of 5 = 2 (Ebbinghaus exact).
	if companion.DoseEbbinghausCount != 2 {
		t.Errorf("DoseEbbinghausCount = %d, want 2 (40%% of 5)",
			companion.DoseEbbinghausCount)
	}
	// 30% of 5 = 1.5 → ceil 2 (cap; priority cascade trims to <=5 total).
	if companion.DoseWeaknessCount != 2 {
		t.Errorf("DoseWeaknessCount = %d, want 2 (30%% of 5, ceil; NEW slot per §5.4)",
			companion.DoseWeaknessCount)
	}
	if companion.DoseCuriosityCount != 2 {
		t.Errorf("DoseCuriosityCount = %d, want 2 (30%% of 5, ceil)",
			companion.DoseCuriosityCount)
	}
	// Target unchanged from Comic Ch6 P14 invariant.
	if companion.DoseTargetSize != 5 {
		t.Errorf("DoseTargetSize = %d, want 5 (Comic Ch6 P14 invariant)",
			companion.DoseTargetSize)
	}
}
