// campaign_reveal_migration_test.go — WS-C4 (CHO-2083, ADR-227 D2 fog gating)
// migration-content gate for 0080: the free-on-win reveal LEDGER.
//
// The reveal consumer folds chora.consumption.campaign.node_won.v1 into
// exactly ONE free fog-reveal per node win, idempotent under Pub/Sub
// redelivery. The ledger is the redelivery-safe claim record:
//
//   - campaign_reveal_ledger — one live row per (tenant, learner, goal,
//     concept): the semantic ≤1-reveal-per-node-win bound. node_won_event_id
//     carries a second unique (the belt to the semantic braces per the
//     ADR-227 addendum), request_id + published_at record the mark-published
//     step so a crash between claim and publish is recoverable.
//
// Mirrors campaign_question_migration_test.go's 0079 gate: the DDL files must
// carry the load-bearing clauses BEFORE any live apply (honest RED on a
// missing or hollow migration). 0056 idiom: RLS ENABLE+FORCE + tenant_isolation
// on chora.tenant_id, soft-delete, live-row partial indexes, UUIDv7 minted in
// Go (gen_random_uuid() only as DB fallback), grants via 9999.
package pg

import (
	"strings"
	"testing"
)

const (
	campaignRevealMigrationUp   = "0080_campaign_reveal_ledger.up.sql"
	campaignRevealMigrationDown = "0080_campaign_reveal_ledger.down.sql"
)

func TestMigration0080_CampaignRevealLedgerDDL(t *testing.T) {
	sql := readMigration(t, campaignRevealMigrationUp)
	assertContainsAll(t, campaignRevealMigrationUp, sql, []string{
		"CREATE TABLE IF NOT EXISTS campaign_reveal_ledger",
		"id UUID PRIMARY KEY DEFAULT gen_random_uuid()",
		"tenant_id UUID NOT NULL",
		"learner_gcid UUID NOT NULL",
		"goal_id UUID NOT NULL",
		"concept_id UUID NOT NULL",
		"node_won_event_id UUID NOT NULL",
		"request_id UUID",
		"published_at TIMESTAMPTZ",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"deleted_at TIMESTAMPTZ",
	})
}

func TestMigration0080_RLSAndIndexes(t *testing.T) {
	sql := readMigration(t, campaignRevealMigrationUp)
	assertContainsAll(t, campaignRevealMigrationUp, sql, []string{
		// RLS trio (0056 idiom; FORCE so even the table owner is filtered).
		"ALTER TABLE campaign_reveal_ledger ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE campaign_reveal_ledger FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation ON campaign_reveal_ledger",
		"USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)",
		// The semantic ≤1-reveal-per-node-win bound.
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_reveal_ledger_live ON campaign_reveal_ledger (tenant_id, learner_gcid, goal_id, concept_id) WHERE deleted_at IS NULL",
		// The event-id dedupe belt (ADR-227 addendum).
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_reveal_ledger_event ON campaign_reveal_ledger (node_won_event_id) WHERE deleted_at IS NULL",
	})
	// Grants ride 9999_grant_app_roles.sql default privileges — never inline.
	if strings.Contains(sql, "GRANT ") {
		t.Fatalf("0080 must not carry inline GRANTs (9999 default-privileges idiom)")
	}
	// Additive-only migration: the NO-FORCE backfill toggle must not appear.
	if strings.Contains(campaignNormSQL(sql), "NO FORCE ROW LEVEL SECURITY") {
		t.Fatalf("0080 is additive-only; NO-FORCE toggle must not appear")
	}
}

func TestMigration0080_DownReverses(t *testing.T) {
	sql := readMigration(t, campaignRevealMigrationDown)
	assertContainsAll(t, campaignRevealMigrationDown, sql, []string{
		"DROP TABLE IF EXISTS campaign_reveal_ledger",
	})
}

func TestMigration0080_Transactional(t *testing.T) {
	for _, name := range []string{campaignRevealMigrationUp, campaignRevealMigrationDown} {
		sql := readMigration(t, name)
		if !strings.Contains(sql, "BEGIN;") || !strings.Contains(sql, "COMMIT;") {
			t.Errorf("%s must be wrapped in BEGIN;/COMMIT;", name)
		}
	}
}
