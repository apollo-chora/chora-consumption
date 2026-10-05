// paramsgen.go - CHO-2362: generates the 0106 params_schema authoring
// migration from the domain sheet table (companion.SkillParamSheets). The
// checked-in migration file must equal this output (whitespace-normalised) -
// the pg migration-content test regenerates and compares, so the validator,
// the catalogue seed, and the FE schema can never drift apart. Same
// data-not-schema vehicle as the activation migrations (0063/0073).
package seedspec

import (
	"fmt"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ParamsSchemaMigrationSQL renders the FULL content of
// 0106_familiar_skill_params_schema.up.sql: one UPDATE per sheet-bearing
// skill, sorted by skill_key for determinism. Data-only - no ALTER, and the
// release flag is deliberately never touched.
func ParamsSchemaMigrationSQL() string {
	keys := make([]string, 0, len(companion.SkillParamSheets))
	for k := range companion.SkillParamSheets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("-- 0106_familiar_skill_params_schema.up.sql - CHO-2362 skills editor.\n")
	b.WriteString("-- Authors the per-Skill params_schema JSONB (spec pack §2 sheets, §5.5\n")
	b.WriteString("-- params model) for every Skill with an invoke-runner builder. DATA only:\n")
	b.WriteString("-- generated from the domain sheet table (seedspec.ParamsSchemaMigrationSQL,\n")
	b.WriteString("-- drift-pinned by the pg migration-content test); the release flag is a\n")
	b.WriteString("-- separate vehicle and is not touched here.\n\n")
	for _, k := range keys {
		schema := companion.SkillParamsSchemaJSON(k)
		fmt.Fprintf(&b, "UPDATE companion_skill_catalog\nSET params_schema = '%s'::jsonb\nWHERE skill_key = '%s';\n\n", schema, k)
	}
	return b.String()
}

// ParamsSchemaDownSQL renders the matching down-file: reset the authored keys
// to the 0061 dark-seed '{}'.
func ParamsSchemaDownSQL() string {
	keys := make([]string, 0, len(companion.SkillParamSheets))
	for k := range companion.SkillParamSheets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("-- 0106_familiar_skill_params_schema.down.sql - reset the authored\n")
	b.WriteString("-- params_schema rows to the 0061 dark-seed empty object.\n\n")
	b.WriteString("UPDATE companion_skill_catalog\nSET params_schema = '{}'::jsonb\nWHERE skill_key IN (\n")
	for i, k := range keys {
		sep := ","
		if i == len(keys)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    '%s'%s\n", k, sep)
	}
	b.WriteString(");\n")
	return b.String()
}
