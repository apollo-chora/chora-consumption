// companion_suspension_projection_migration_test.go: content gate for
// migration 0112 (ADR-254 D11 advisory projection table). Mirrors the
// 0077/0079/0090 gate idiom: the DDL the adapter relies on must be present
// verbatim, and the RLS shape (ENABLE + FORCE + the platform-or-own-tenant
// policy with a NULLIF-safe cast) must not regress to the plain tenant_isolation
// template, which would hide every platform row from every tenant.
package pg

import "testing"

const (
	companionSuspensionProjectionMigrationUp   = "0112_companion_suspension_projection.up.sql"
	companionSuspensionProjectionMigrationDown = "0112_companion_suspension_projection.down.sql"
)

func TestMigration0112_CompanionSuspensionProjectionDDL(t *testing.T) {
	sql := readMigration(t, companionSuspensionProjectionMigrationUp)
	assertContainsAll(t, companionSuspensionProjectionMigrationUp, sql, []string{
		"BEGIN;",
		"CREATE TABLE companion_suspension_projection (",
		"suspension_id UUID PRIMARY KEY",
		"scope TEXT NOT NULL CHECK (scope IN ('platform', 'tenant'))",
		"tenant_id UUID",
		"skill_key TEXT CHECK (skill_key IS NULL OR length(skill_key) > 0)",
		"engaged BOOLEAN NOT NULL",
		"reason TEXT NOT NULL CHECK (length(reason) > 0)",
		"actor_gcid UUID NOT NULL",
		"source_version BIGINT NOT NULL CHECK (source_version >= 1)",
		"changed_at TIMESTAMPTZ NOT NULL",
		"last_event_id UUID NOT NULL",
		"projected_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"CONSTRAINT companion_suspension_projection_scope_tenant_chk CHECK ( (scope = 'platform' AND tenant_id IS NULL) OR (scope = 'tenant' AND tenant_id IS NOT NULL) )",
		"CREATE INDEX idx_companion_suspension_projection_engaged ON companion_suspension_projection (scope, tenant_id) WHERE engaged = TRUE",
		"COMMIT;",
	})
}

func TestMigration0112_RLSAdmitsPlatformRowsAndOwnTenant(t *testing.T) {
	sql := readMigration(t, companionSuspensionProjectionMigrationUp)
	assertContainsAll(t, companionSuspensionProjectionMigrationUp, sql, []string{
		"ALTER TABLE companion_suspension_projection ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE companion_suspension_projection FORCE ROW LEVEL SECURITY",
		"CREATE POLICY platform_or_own_tenant ON companion_suspension_projection FOR ALL USING ( tenant_id IS NULL OR tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid ) WITH CHECK ( tenant_id IS NULL OR tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid )",
	})
}

func TestMigration0112_DownDropsTable(t *testing.T) {
	sql := readMigration(t, companionSuspensionProjectionMigrationDown)
	assertContainsAll(t, companionSuspensionProjectionMigrationDown, sql, []string{
		"BEGIN;",
		"DROP TABLE IF EXISTS companion_suspension_projection;",
		"COMMIT;",
	})
}
