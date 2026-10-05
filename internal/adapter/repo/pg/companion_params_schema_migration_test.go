// companion_params_schema_migration_test.go - CHO-2362: migration 0106 authors
// the per-Skill params_schema JSONB for the built Skills, as DATA (a standalone
// UPDATE per key - the 0063/0073 activation-vehicle pattern), leaving the 0061
// dark seed untouched. The up-file content is REGENERATED from the domain
// sheet table via seedspec.ParamsSchemaMigrationSQL(), so schema-vs-validator
// drift is impossible: any sheet edit without the migration (or vice versa)
// fails here.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0106_ParamsSchemaMatchesDomainSheets(t *testing.T) {
	up := readMigration(t, "0106_familiar_skill_params_schema.up.sql")

	// Regenerate from the domain table - the migration must carry EXACTLY the
	// generated statements (whitespace-normalised), nothing more.
	// 0106 is FROZEN at the bytes the live lane applied (pre-ADR-254 names);
	// 0110_companion_rename renames the live table + skill keys afterwards, so
	// the generated statements are compared through the pre-0110 name map.
	want := seedspec.LegacyPre0110(seedspec.ParamsSchemaMigrationSQL())
	if norm(up) != norm(want) {
		t.Errorf("0106 up drifts from seedspec.ParamsSchemaMigrationSQL():\n--- file ---\n%s\n--- generated ---\n%s", up, want)
	}

	// Data-not-schema: an UPDATE of params_schema only - never ALTER, never a
	// touch on active (this vehicle must not smuggle a release).
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set params_schema") {
		t.Fatalf("0106 up must UPDATE familiar_skill_catalog SET params_schema; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Error("0106 must not ALTER schema - params authoring is data")
	}
	if strings.Contains(lower, "active") {
		t.Error("0106 must not touch the active flag (ADR-174 release gate is a separate vehicle)")
	}

	// Every sheet-bearing skill is authored; no sheetless key smuggled in.
	for key := range companion.SkillParamSheets {
		if !strings.Contains(up, "'"+seedspec.LegacyPre0110(key)+"'") {
			t.Errorf("0106 up missing params_schema for %q", key)
		}
	}
	for _, e := range seedspec.Catalogue() {
		if _, ok := companion.SkillParamSheets[e.SkillKey]; ok {
			continue
		}
		if strings.Contains(up, "'"+seedspec.LegacyPre0110(e.SkillKey)+"'") {
			t.Errorf("0106 up authors %q, which has no domain sheet (no builder)", e.SkillKey)
		}
	}
}

func TestMigration0106_DownResetsToEmptyObject(t *testing.T) {
	down := readMigration(t, "0106_familiar_skill_params_schema.down.sql")
	lower := strings.ToLower(down)
	if !strings.Contains(lower, "set params_schema") || !strings.Contains(down, "'{}'") {
		t.Fatalf("0106 down must reset params_schema to '{}' for the authored keys; got:\n%s", down)
	}
	for key := range companion.SkillParamSheets {
		if !strings.Contains(down, "'"+seedspec.LegacyPre0110(key)+"'") {
			t.Errorf("0106 down missing reset of %q", key)
		}
	}
}

// norm collapses whitespace runs so formatting-only edits don't fail the
// regeneration equality (content drift still does).
func norm(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
