// companion_capability_migration_test.go — CHO-2012 migration-content gate.
//
// Regenerates the marker-delimited seed blocks in 0061 from the seedspec Go
// fixtures (the spec-pack mirror) and fails on ANY byte drift — the
// migration can never disagree with the validated fixture. Also pins the
// structural DDL the code relies on (v2 columns, species_paths, grants
// loadout columns, the FORCE-RLS toggle around the data re-base).
package pg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func readMigration(t *testing.T, name string) string {
	t.Helper()
	// Package dir = internal/adapter/repo/pg → migrations sit 4 levels up.
	path := filepath.Join("..", "..", "..", "..", "migrations", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func extractBlock(t *testing.T, sql, beginMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(sql, beginMarker)
	end := strings.Index(sql, endMarker)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("markers %q / %q not found in migration", beginMarker, endMarker)
	}
	block := sql[start+len(beginMarker) : end]
	return strings.TrimSpace(block)
}

func TestMigration0061_CatalogueSeedMatchesSeedspec(t *testing.T) {
	sql := readMigration(t, "0061_familiar_capability_catalogue_v2.up.sql")
	got := extractBlock(t, sql,
		"-- BEGIN GENERATED CATALOGUE SEED (seedspec.CatalogueSeedSQL — do not hand-edit)",
		"-- END GENERATED CATALOGUE SEED")
	// 0061 is FROZEN at the bytes the live lane applied (pre-ADR-254 names);
	// 0110_companion_rename renames the live objects + seeded values afterwards,
	// so the generated block is compared through the pre-0110 name map.
	want := strings.TrimSpace(seedspec.LegacyPre0110(seedspec.CatalogueSeedSQL()))
	if got != want {
		t.Errorf("catalogue seed block drifted from seedspec.CatalogueSeedSQL().\nRegenerate the block (see seedspec/sqlgen.go).\n--- migration ---\n%.400s\n--- seedspec ---\n%.400s", got, want)
	}
}

func TestMigration0061_SpeciesPathsSeedMatchesSeedspec(t *testing.T) {
	sql := readMigration(t, "0061_familiar_capability_catalogue_v2.up.sql")
	got := extractBlock(t, sql,
		"-- BEGIN GENERATED SPECIES PATHS SEED (seedspec.SpeciesPathsSeedSQL — do not hand-edit)",
		"-- END GENERATED SPECIES PATHS SEED")
	want := strings.TrimSpace(seedspec.LegacyPre0110(seedspec.SpeciesPathsSeedSQL()))
	if got != want {
		t.Errorf("species paths seed block drifted from seedspec.SpeciesPathsSeedSQL().\n--- migration ---\n%.400s\n--- seedspec ---\n%.400s", got, want)
	}
}

func TestMigration0061_SeedsValidateAgainstFloors(t *testing.T) {
	// The D3 fail-loud gate: what 0061 seeds must validate.
	if err := seedspec.PathFloorValidation(); err != nil {
		t.Fatalf("seeded species paths violate catalogue floors: %v", err)
	}
}

func TestMigration0061_StructuralDDL(t *testing.T) {
	sql := readMigration(t, "0061_familiar_capability_catalogue_v2.up.sql")
	musts := []string{
		// v2 columns the pg adapters SELECT.
		"ADD COLUMN IF NOT EXISTS skill_kind TEXT NOT NULL DEFAULT 'active'",
		"ADD COLUMN IF NOT EXISTS min_growth_stage SMALLINT NOT NULL DEFAULT 2",
		"ADD COLUMN IF NOT EXISTS slot_cost SMALLINT NOT NULL DEFAULT 1",
		"ADD COLUMN IF NOT EXISTS policy_class TEXT NOT NULL DEFAULT 'standard'",
		"ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT FALSE",
		// species_paths + one-active-per-species.
		"CREATE TABLE IF NOT EXISTS species_paths",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_species_paths_active",
		// grants loadout columns.
		"ADD COLUMN IF NOT EXISTS equipped BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS unlocked_via TEXT",
		"ADD COLUMN IF NOT EXISTS unlocked_at_stage SMALLINT",
		// data re-base + the WS-4 FORCE-RLS lesson.
		"ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY",
		"SET skill_slots_unlocked = LEAST(growth_stage + 1, 7)",
		"ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY",
	}
	for _, m := range musts {
		if !strings.Contains(sql, m) {
			t.Errorf("0061 missing required statement: %q", m)
		}
	}
	// The re-base toggle must RESTORE FORCE after the UPDATE.
	noForce := strings.Index(sql, "NO FORCE ROW LEVEL SECURITY")
	restore := strings.LastIndex(sql, "FORCE ROW LEVEL SECURITY")
	if !(noForce >= 0 && restore > noForce) {
		t.Error("FORCE RLS not restored after the data re-base")
	}
}

func TestMigration0061_DownRevertsStructures(t *testing.T) {
	sql := readMigration(t, "0061_familiar_capability_catalogue_v2.down.sql")
	musts := []string{
		"DROP TABLE IF EXISTS species_paths",
		"DROP COLUMN IF EXISTS equipped",
		"DROP COLUMN IF EXISTS skill_kind",
		"DROP COLUMN IF EXISTS active",
	}
	for _, m := range musts {
		if !strings.Contains(sql, m) {
			t.Errorf("0061 down missing: %q", m)
		}
	}
}
