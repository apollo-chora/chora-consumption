// paramsgen_test.go — shape tests for the CHO-2362 params_schema authoring
// generators (ParamsSchemaMigrationSQL / ParamsSchemaDownSQL). These are
// pure deterministic renderers; the pg migration-content test additionally
// regenerates and diffs against the checked-in migration, so here we pin the
// structural promises: one UPDATE per sheet-bearing skill on the up side,
// one IN-list reset on the down side, keys sorted, and the release flag never
// touched on either side.
package seedspec

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestParamsSchemaMigrationSQL_OneUpdatePerSkill — every sheet-bearing skill
// gets exactly one UPDATE, keys are sorted for determinism, every authored
// JSONB is non-empty, and the release flag is untouched.
func TestParamsSchemaMigrationSQL_OneUpdatePerSkill(t *testing.T) {
	if len(companion.SkillParamSheets) == 0 {
		t.Fatal("SkillParamSheets is empty — paramsgen has nothing to author")
	}
	sql := ParamsSchemaMigrationSQL()
	for key, sheet := range companion.SkillParamSheets {
		if !strings.Contains(sql, "WHERE skill_key = '"+key+"';") {
			t.Errorf("migration SQL missing UPDATE for skill_key %q", key)
		}
		if len(sheet) == 0 {
			t.Errorf("skill %q has an empty sheet — nothing authored", key)
		}
		schema := string(companion.SkillParamsSchemaJSON(key))
		if schema == "" || schema == "{}" {
			t.Errorf("skill %q authored an empty params_schema (%q)", key, schema)
		}
	}
	// The data-only promoter must not contain a SET clause touching the release
	// flag (prose in the header comment is fine — we match the assignment).
	if strings.Contains(sql, "SET release") || strings.Contains(sql, "release =") ||
		strings.Contains(sql, "is_released =") {
		t.Error("migration SQL must never touch the release flag")
	}
}

// TestParamsSchemaMigrationSQL_DeterministicOutput — two invocations must
// produce byte-identical SQL (sorted keys ⇒ stable output).
func TestParamsSchemaMigrationSQL_DeterministicOutput(t *testing.T) {
	if ParamsSchemaMigrationSQL() != ParamsSchemaMigrationSQL() {
		t.Fatal("ParamsSchemaMigrationSQL is not deterministic across calls")
	}
}

// TestParamsSchemaDownSQL_ResetsEverySheetBackToEmptyObject — the down side
// resets every authored skill to the 0061 dark-seed '{}' with one IN-list,
// and never sets a release flag.
func TestParamsSchemaDownSQL_ResetsEverySheetBackToEmptyObject(t *testing.T) {
	sql := ParamsSchemaDownSQL()
	if !strings.Contains(sql, "params_schema = '{}'::jsonb") {
		t.Error("down migration must reset params_schema to the empty object")
	}
	if !strings.Contains(sql, "WHERE skill_key IN (") {
		t.Error("down migration must use a single IN-list reset")
	}
	for key := range companion.SkillParamSheets {
		if !strings.Contains(sql, "'"+key+"'") {
			t.Errorf("down migration missing skill_key %q from the IN-list", key)
		}
	}
	if strings.Contains(sql, "release =") || strings.Contains(sql, "is_released =") {
		t.Error("down migration must never touch the release flag")
	}
}

// TestParamsSchemaDownSQL_DeterministicOutput — same stability guarantee as
// the up side.
func TestParamsSchemaDownSQL_DeterministicOutput(t *testing.T) {
	if ParamsSchemaDownSQL() != ParamsSchemaDownSQL() {
		t.Fatal("ParamsSchemaDownSQL is not deterministic across calls")
	}
}
