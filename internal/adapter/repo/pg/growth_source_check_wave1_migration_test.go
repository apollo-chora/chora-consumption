// growth_source_check_wave1_migration_test.go — F-I3 (CHO-2090, ADR-228 D4)
// migration-content gate for 0089: the companion_growth_events.source CHECK
// widened with the three Wave-1 verified XP tokens. Fourth leg of the parity
// contract (curve.go canonicalSources + identity 0037 exp_source_def + the
// 0081 campaign check + THIS widening must agree). The down re-narrows to the
// 0081 vocabulary NOT VALID so Wave-1 ledger history survives a revert.
package pg

import (
	"strings"
	"testing"
)

const (
	growthSourceCheckWave1Up   = "0089_growth_source_check_wave1.up.sql"
	growthSourceCheckWave1Down = "0089_growth_source_check_wave1.down.sql"
)

func TestMigration0089_GrowthSourceCheckWave1(t *testing.T) {
	up := readMigration(t, growthSourceCheckWave1Up)
	assertContainsAll(t, growthSourceCheckWave1Up, up, []string{
		"DROP CONSTRAINT IF EXISTS familiar_growth_events_source_check",
		"ADD CONSTRAINT familiar_growth_events_source_check CHECK (source IN (",
		// The 11 legacy 0032 tokens survive verbatim.
		"'atom_session'", "'ebbinghaus_review'", "'hex_expand'", "'conv_turn'",
		"'daily_dose_open'", "'social_share'", "'social_reaction'",
		"'junction_accepted'", "'atom_authored'", "'admin_grant'", "'hatch_roll'",
		// The four WS-C5 campaign tokens survive verbatim.
		"'campaign_rung_cleared'", "'campaign_rung_refreshed'",
		"'campaign_node_won'", "'campaign_goal_sealed'",
		// The three ADR-228 D4 Wave-1 tokens (parity with curve.go + identity 0037).
		"'weakness_grown'", "'submission_graded'", "'module_completed'",
	})

	down := readMigration(t, growthSourceCheckWave1Down)
	assertContainsAll(t, growthSourceCheckWave1Down, down, []string{
		"DROP CONSTRAINT IF EXISTS familiar_growth_events_source_check",
		"ADD CONSTRAINT familiar_growth_events_source_check CHECK (source IN (",
		"NOT VALID",
		// The down re-narrows to the FULL 0081 list (campaign stays).
		"'campaign_goal_sealed'",
	})
	for _, waveToken := range []string{
		"'weakness_grown'", "'submission_graded'", "'module_completed'",
	} {
		if strings.Contains(down, waveToken) {
			t.Errorf("%s: down must re-narrow to the 0081 list — found %s", growthSourceCheckWave1Down, waveToken)
		}
	}
}
