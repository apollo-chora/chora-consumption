// campaign_migration_test.go — WS-C0 (CHO-2079, ADR-227 D16) migration-content gate.
//
// Pins the 0077 DDL contract every later campaign WS (C1 ladder/pacing, C4
// reveal gating, C5 XP, C6 merge/split) relies on:
//
//   - campaign_node_progress — per-(tenant, learner, concept) ladder state:
//     rungs_cleared 0-6 with a positionally-aligned rung_cleared_at audit
//     array, won_at permanent ratchet (D9: only legal at 6/6), and
//     last_advance_date backing the D7 one-rung-per-day pacing gate.
//   - concept_node_lineage — D14 merge/split ancestry on BOTH axes (node
//     UUIDs + concept_key slugs) so the C5 no-re-award rule and the C6
//     join-repairs can walk lineage.
//   - goals.focus_concept_id — the D11 campaign focus pointer (nullable,
//     cross-aggregate UUID, no FK).
//
// All tables follow the 0056 idiom: RLS ENABLE+FORCE + tenant_isolation
// policy on chora.tenant_id, soft-delete, live-row partial indexes, UUIDv7
// minted in Go (gen_random_uuid() only as DB fallback), grants via 9999.
package pg

import (
	"regexp"
	"strings"
	"testing"
)

const (
	campaignMigrationUp   = "0077_familiar_campaign.up.sql"
	campaignMigrationDown = "0077_familiar_campaign.down.sql"
)

// campaignNormSQL collapses all whitespace runs to single spaces so DDL
// assertions are stable against column alignment.
func campaignNormSQL(sql string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(sql, " ")
}

func assertContainsAll(t *testing.T, name, sql string, musts []string) {
	t.Helper()
	norm := campaignNormSQL(sql)
	for _, m := range musts {
		if !strings.Contains(norm, m) {
			t.Errorf("%s missing required DDL fragment:\n  %s", name, m)
		}
	}
}

func TestMigration0077_CampaignNodeProgressDDL(t *testing.T) {
	sql := readMigration(t, campaignMigrationUp)
	assertContainsAll(t, campaignMigrationUp, sql, []string{
		"CREATE TABLE IF NOT EXISTS campaign_node_progress",
		"id UUID PRIMARY KEY DEFAULT gen_random_uuid()",
		"tenant_id UUID NOT NULL",
		"learner_gcid UUID NOT NULL",
		"concept_id UUID NOT NULL",
		"rungs_cleared SMALLINT NOT NULL DEFAULT 0",
		"rung_cleared_at TIMESTAMPTZ[] NOT NULL DEFAULT '{}'",
		"won_at TIMESTAMPTZ",
		"last_advance_date DATE",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"deleted_at TIMESTAMPTZ",
		// Ladder invariants — fail-loud in the schema, not just in Go.
		"CONSTRAINT campaign_progress_rungs_range CHECK (rungs_cleared BETWEEN 0 AND 6)",
		"CONSTRAINT campaign_progress_rung_audit_aligned CHECK (cardinality(rung_cleared_at) = rungs_cleared)",
		"CONSTRAINT campaign_progress_won_is_full_ladder CHECK (won_at IS NULL OR rungs_cleared = 6)",
	})
}

func TestMigration0077_ConceptNodeLineageDDL(t *testing.T) {
	sql := readMigration(t, campaignMigrationUp)
	assertContainsAll(t, campaignMigrationUp, sql, []string{
		"CREATE TABLE IF NOT EXISTS concept_node_lineage",
		"operation VARCHAR(16) NOT NULL",
		"CHECK (operation IN ('merge', 'split'))",
		"from_concept_id UUID NOT NULL",
		"to_concept_id UUID NOT NULL",
		"from_concept_key TEXT NOT NULL",
		"to_concept_key TEXT NOT NULL",
		"CONSTRAINT concept_node_lineage_no_self CHECK (from_concept_id <> to_concept_id)",
	})
}

func TestMigration0077_RLSAndIndexes(t *testing.T) {
	sql := readMigration(t, campaignMigrationUp)
	assertContainsAll(t, campaignMigrationUp, sql, []string{
		// RLS trio per table (0056 idiom; FORCE so even the table owner is filtered).
		"ALTER TABLE campaign_node_progress ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE campaign_node_progress FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON campaign_node_progress",
		"ALTER TABLE concept_node_lineage ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE concept_node_lineage FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON concept_node_lineage",
		"USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)",
		// One live ladder row per (tenant, learner, concept).
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_node_progress_live ON campaign_node_progress (tenant_id, learner_gcid, concept_id) WHERE deleted_at IS NULL",
		// Lineage walked both directions by C5/C6.
		"CREATE INDEX IF NOT EXISTS idx_concept_node_lineage_from ON concept_node_lineage (tenant_id, learner_gcid, from_concept_id) WHERE deleted_at IS NULL",
		"CREATE INDEX IF NOT EXISTS idx_concept_node_lineage_to ON concept_node_lineage (tenant_id, learner_gcid, to_concept_id) WHERE deleted_at IS NULL",
	})
	if strings.Contains(sql, "GRANT ") {
		t.Errorf("%s must not carry inline GRANTs — 9999_grant_app_roles.sql owns grants via ALTER DEFAULT PRIVILEGES", campaignMigrationUp)
	}
}

func TestMigration0077_GoalsFocusConceptColumn(t *testing.T) {
	sql := readMigration(t, campaignMigrationUp)
	assertContainsAll(t, campaignMigrationUp, sql, []string{
		"ALTER TABLE goals",
		"ADD COLUMN IF NOT EXISTS focus_concept_id UUID",
	})
	// Nullable pointer, additive only — a backfill would need the NO FORCE
	// toggle txn (0074 idiom) and is deliberately absent here.
	if strings.Contains(campaignNormSQL(sql), "NO FORCE ROW LEVEL SECURITY") {
		t.Errorf("%s must not toggle NO FORCE — 0077 is additive with no backfill", campaignMigrationUp)
	}
}

func TestMigration0077_DownReverses(t *testing.T) {
	sql := readMigration(t, campaignMigrationDown)
	assertContainsAll(t, campaignMigrationDown, sql, []string{
		"ALTER TABLE goals DROP COLUMN IF EXISTS focus_concept_id",
		"DROP TABLE IF EXISTS concept_node_lineage",
		"DROP TABLE IF EXISTS campaign_node_progress",
	})
	if strings.Contains(campaignNormSQL(sql), "DROP TABLE IF EXISTS goals") {
		t.Errorf("%s must never drop the goals table", campaignMigrationDown)
	}
}

func TestMigration0077_Transactional(t *testing.T) {
	for _, name := range []string{campaignMigrationUp, campaignMigrationDown} {
		sql := readMigration(t, name)
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Errorf("%s must be wrapped in BEGIN;/COMMIT;", name)
		}
	}
}
