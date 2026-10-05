// campaign_counter_migration_test.go — WS-C1 (CHO-2080) migration-content
// gate for 0078: the two goal/ladder state columns the domain core needs
// that 0077 did not carry.
//
//   - campaign_node_progress.current_rung_correct — the D6 rung-clear rule
//     (CAMPAIGN_RUNG_CLEAR_CORRECT corrects at the CURRENT rung) needs a
//     persistent counter across sessions/days; wrong answers never reset it
//     (calendar time is the anti-farm defence, D7).
//   - goals.campaign_sealed_at — seal HISTORY (D3), distinct from
//     personal_completed_at which the bare ADR-213 PATCH can set: re-seal
//     gating (>= N new wins + weekly cap) reads this timestamp.
//
// Reuses the 0077 helpers (readMigration / assertContainsAll) — same package.
package pg

import (
	"strings"
	"testing"
)

const (
	campaignCounterMigrationUp   = "0078_campaign_counter_and_seal.up.sql"
	campaignCounterMigrationDown = "0078_campaign_counter_and_seal.down.sql"
)

func TestMigration0078_CounterAndSealColumns(t *testing.T) {
	sql := readMigration(t, campaignCounterMigrationUp)
	assertContainsAll(t, campaignCounterMigrationUp, sql, []string{
		"ALTER TABLE campaign_node_progress",
		"ADD COLUMN IF NOT EXISTS current_rung_correct SMALLINT NOT NULL DEFAULT 0",
		"CONSTRAINT campaign_progress_counter_nonneg CHECK (current_rung_correct >= 0)",
		"ALTER TABLE goals",
		"ADD COLUMN IF NOT EXISTS campaign_sealed_at TIMESTAMPTZ",
	})
	if strings.Contains(sql, "GRANT ") {
		t.Errorf("%s must not carry inline GRANTs (9999 owns grants)", campaignCounterMigrationUp)
	}
	// Additive only — both columns are fresh state with defaults; a backfill
	// (and its NO-FORCE toggle) must not appear.
	if strings.Contains(campaignNormSQL(sql), "NO FORCE ROW LEVEL SECURITY") {
		t.Errorf("%s must not toggle NO FORCE — additive, no backfill", campaignCounterMigrationUp)
	}
}

func TestMigration0078_DownReverses(t *testing.T) {
	sql := readMigration(t, campaignCounterMigrationDown)
	assertContainsAll(t, campaignCounterMigrationDown, sql, []string{
		"ALTER TABLE goals DROP COLUMN IF EXISTS campaign_sealed_at",
		"ALTER TABLE campaign_node_progress DROP COLUMN IF EXISTS current_rung_correct",
	})
	if strings.Contains(campaignNormSQL(sql), "DROP TABLE") {
		t.Errorf("%s must only drop columns, never tables", campaignCounterMigrationDown)
	}
}

func TestMigration0078_Transactional(t *testing.T) {
	for _, name := range []string{campaignCounterMigrationUp, campaignCounterMigrationDown} {
		sql := readMigration(t, name)
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Errorf("%s must be wrapped in BEGIN;/COMMIT;", name)
		}
	}
}
