// campaign_question_migration_test.go — CHO-2082 (WS-C3): content gate for
// migration 0079 (campaign question bank + atom_index cognitive_level).
// Mirrors campaign_migration_test.go's 0077 gate: the DDL files must carry
// the load-bearing clauses BEFORE any live apply (honest RED on a missing or
// hollow migration).
package pg

import (
	"strings"
	"testing"
)

const (
	campaignQuestionMigrationUp   = "0079_campaign_question_bank.up.sql"
	campaignQuestionMigrationDown = "0079_campaign_question_bank.down.sql"
)

func TestMigration0079_CampaignQuestionSetsDDL(t *testing.T) {
	sql := readMigration(t, campaignQuestionMigrationUp)
	assertContainsAll(t, campaignQuestionMigrationUp, sql, []string{
		"CREATE TABLE IF NOT EXISTS campaign_question_sets",
		"tenant_id",
		"learner_gcid",
		"concept_id",
		"concept_key",
		"rung",
		"retrieved_atom_ids",
		"generation_status",
		"assist_id",
		"requested_on",
		"questions_payload",
		"failure_reason",
		"deleted_at",
		"CHECK (rung BETWEEN 1 AND 6)",
		"CHECK (generation_status IN ('idle', 'requested', 'ready', 'failed'))",
		"questions_payload IS NOT NULL", // ready ⇒ payload present (no fabricated ready)
	})
}

func TestMigration0079_AtomIndexCognitiveLevel(t *testing.T) {
	sql := readMigration(t, campaignQuestionMigrationUp)
	assertContainsAll(t, campaignQuestionMigrationUp, sql, []string{
		"ALTER TABLE atom_index",
		"ADD COLUMN IF NOT EXISTS cognitive_level",
	})
}

func TestMigration0079_RLSAndIndexes(t *testing.T) {
	sql := readMigration(t, campaignQuestionMigrationUp)
	assertContainsAll(t, campaignQuestionMigrationUp, sql, []string{
		"ALTER TABLE campaign_question_sets ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE campaign_question_sets FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON campaign_question_sets",
		"current_setting('chora.tenant_id', true)::uuid",
		"uq_campaign_question_sets_live",
		"WHERE deleted_at IS NULL",
		"uq_campaign_question_sets_assist",
	})
	// Grants ride 9999_grant_app_roles.sql default privileges — never inline.
	if strings.Contains(sql, "GRANT ") {
		t.Fatalf("0079 must not carry inline GRANTs (9999 default-privileges idiom)")
	}
	// Additive-only migration: the NO-FORCE backfill toggle must not appear.
	if strings.Contains(campaignNormSQL(sql), "NO FORCE ROW LEVEL SECURITY") {
		t.Fatalf("0079 is additive-only; NO-FORCE toggle must not appear")
	}
}

func TestMigration0079_DownReverses(t *testing.T) {
	sql := readMigration(t, campaignQuestionMigrationDown)
	assertContainsAll(t, campaignQuestionMigrationDown, sql, []string{
		"DROP TABLE IF EXISTS campaign_question_sets",
		"ALTER TABLE atom_index",
		"DROP COLUMN IF EXISTS cognitive_level",
	})
}
