// service_internal_cover_test.go — white-box (package growth) coverage for the
// unexported helpers maxOutputTokensForStageAndTier and clampStage. These are
// pure functions reachable only from inside the package, so a white-box test is
// the only way to assert their branch outcomes.
package growth

import (
	"testing"
)

// maxOutputTokensForStageAndTier returns min(stageCap, tierCap). The happy-path
// projectState calls only ever surface the flash-lite tier; this table walks
// every tier arm plus the stage-cap-wins vs tier-cap-wins split.
func TestMaxOutputTokensForStageAndTier_Branches(t *testing.T) {
	cases := []struct {
		name    string
		stage   int
		llmTier string
		want    int
	}{
		// Stage 6 cap = 2000. tier cap is the binding constraint for the lower tiers.
		{"flash-lite caps at 1200 below stage cap", 6, "flash-lite", 1200},
		{"flash caps at 1600 below stage cap", 6, "flash", 1600},
		{"flash-reasoning caps at 1800 below stage cap", 6, "flash-reasoning", 1800},
		{"pro tier cap 2000 equals stage 6 cap", 6, "pro", 2000},
		// Stage 0 cap = 100 wins over every tier cap (stageCap < tierCap arm).
		{"stage cap wins at stage 0 with pro", 0, "pro", 100},
		{"stage cap wins at stage 0 with flash", 0, "flash", 100},
		// Unknown tier → default tierCap 1000; stage 6 cap 2000 → 1000 binds.
		{"unknown tier defaults to 1000", 6, "mystery", 1000},
		// Default tier (1000) vs a small stage cap (stage 1 = 600) → stage wins.
		{"unknown tier but small stage cap", 1, "mystery", 600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maxOutputTokensForStageAndTier(c.stage, c.llmTier); got != c.want {
				t.Errorf("maxOutputTokensForStageAndTier(%d,%q) = %d, want %d",
					c.stage, c.llmTier, got, c.want)
			}
		})
	}
}

// clampStage pins the stage index into [0, 6]. The in-range projectState calls
// never feed negative or >6 stages; assert both saturating arms.
func TestClampStage_Branches(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, 0},
		{-1, 0},
		{0, 0},
		{3, 3},
		{6, 6},
		{7, 6},
		{100, 6},
	}
	for _, c := range cases {
		if got := clampStage(c.in); got != c.want {
			t.Errorf("clampStage(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// CHO-2225: TestDefaultIDFactory_ShapeAndUniqueness is deliberately GONE along
// with defaultIDFactory itself. It asserted the id carried an "evt-" prefix —
// pinning the defect as the spec, so minting a correct UUIDv7 would have turned
// it RED. event_id is a mandatory UUIDv7 envelope field; the composition root
// now injects domain.NewUUIDv7 and NewService refuses a nil NewID outright, so
// there is no in-package factory left to cover.
