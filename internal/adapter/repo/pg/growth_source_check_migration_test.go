// growth_source_check_migration_test.go — WS-C5 (CHO-2084, ADR-227 D10)
// migration-content gate for 0081: the companion_growth_events.source CHECK
// widened with the four campaign conquest tokens.
//
// 0032 seeded the ledger CHECK as the defensive belt behind the app-layer
// canonical enum; the WS-C5 live smoke (2026-07-09) proved the belt bites —
// campaign awards died 23514 until this widening. The token list here is
// the third leg of the parity contract (curve.go canonicalSources +
// identity 0036 exp_source_def + THIS check must all carry the same four
// campaign tokens). The down re-narrows NOT VALID so append-only campaign
// ledger history survives a revert.
package pg

import (
	"strings"
	"testing"
)

const (
	growthSourceCheckMigrationUp   = "0081_growth_source_check_campaign.up.sql"
	growthSourceCheckMigrationDown = "0081_growth_source_check_campaign.down.sql"
)

func TestMigration0081_GrowthSourceCheckCampaign(t *testing.T) {
	up := readMigration(t, growthSourceCheckMigrationUp)
	assertContainsAll(t, growthSourceCheckMigrationUp, up, []string{
		"DROP CONSTRAINT IF EXISTS familiar_growth_events_source_check",
		"ADD CONSTRAINT familiar_growth_events_source_check CHECK (source IN (",
		// The 11 legacy 0032 tokens must survive the widening verbatim.
		"'atom_session'", "'ebbinghaus_review'", "'hex_expand'", "'conv_turn'",
		"'daily_dose_open'", "'social_share'", "'social_reaction'",
		"'junction_accepted'", "'atom_authored'", "'admin_grant'", "'hatch_roll'",
		// The four WS-C5 campaign conquest tokens (parity with curve.go +
		// identity 0036).
		"'campaign_rung_cleared'", "'campaign_rung_refreshed'",
		"'campaign_node_won'", "'campaign_goal_sealed'",
	})

	down := readMigration(t, growthSourceCheckMigrationDown)
	assertContainsAll(t, growthSourceCheckMigrationDown, down, []string{
		"DROP CONSTRAINT IF EXISTS familiar_growth_events_source_check",
		"ADD CONSTRAINT familiar_growth_events_source_check CHECK (source IN (",
		"NOT VALID",
	})
	for _, campaignToken := range []string{
		"'campaign_rung_cleared'", "'campaign_rung_refreshed'",
		"'campaign_node_won'", "'campaign_goal_sealed'",
	} {
		if strings.Contains(down, campaignToken) {
			t.Errorf("%s: down must re-narrow to the legacy list — found %s", growthSourceCheckMigrationDown, campaignToken)
		}
	}
}
