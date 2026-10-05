// companion_answerable_activation_migration_test.go — CHO-2016 P2 wave B: the
// 0075 migration is the answerable pipe's activation VEHICLE, but it must HOLD
// quiz_me + socratic_drill DARK — their ADR-174 §8 answerable eval suite has not
// passed, so a flip to active=TRUE would release un-evaluated LLM Skills (the §8
// breach). This guards that 0075 mirrors 0073's data-UPDATE structure yet never
// activates, references exactly seedspec.P2AnswerableActivationTwo, and neither
// direction flips active=TRUE.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0075_HoldsAnswerableDark(t *testing.T) {
	up := readMigration(t, "0075_familiar_answerable_activation.up.sql")
	// Inspect EXECUTABLE SQL only — the header comment legitimately mentions
	// "active=TRUE" in prose (explaining the deferred flip), so the guards must
	// run on the statements, not the commentary.
	upSQL := stripSQLComments(up)

	// Activation is DATA not schema (spec §5 P1-delta #4): an UPDATE, never ALTER.
	if !strings.Contains(strings.ToLower(upSQL), "update familiar_skill_catalog") {
		t.Fatalf("0075 up must be an UPDATE on familiar_skill_catalog; got SQL:\n%s", upSQL)
	}
	if strings.Contains(strings.ToLower(upSQL), "alter table") {
		t.Errorf("0075 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// MUST NOT flip active=TRUE: the answerable eval gate has not passed, so a
	// stray activation would release un-evaluated LLM Skills (the ADR-174 §8
	// breach). Whitespace-insensitive so "active = true" / "active=true" both hit.
	if activatesTrue(upSQL) {
		t.Fatalf("0075 must NOT activate answerable skills (eval gate unmet — held dark); got SQL:\n%s", upSQL)
	}

	// References EXACTLY the answerable held-dark set — both present, no stray key.
	for _, key := range seedspec.P2AnswerableActivationTwo {
		if !strings.Contains(upSQL, "'"+key+"'") {
			t.Errorf("0075 up missing %q from the held-dark set", key)
		}
	}
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P2AnswerableActivationTwo, e.SkillKey) {
			continue
		}
		if strings.Contains(upSQL, "'"+e.SkillKey+"'") {
			t.Errorf("0075 up references %q, which is NOT in P2AnswerableActivationTwo", e.SkillKey)
		}
	}

	// The down migration also never activates on rollback.
	downSQL := stripSQLComments(readMigration(t, "0075_familiar_answerable_activation.down.sql"))
	if activatesTrue(downSQL) {
		t.Errorf("0075 down must NOT activate on rollback (symmetric dark-hold)")
	}
	for _, key := range seedspec.P2AnswerableActivationTwo {
		if !strings.Contains(downSQL, "'"+key+"'") {
			t.Errorf("0075 down missing %q", key)
		}
	}
}

// stripSQLComments removes `--` line comments (whole-line and inline) so a drift
// guard inspects executable SQL rather than the header commentary.
func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// activatesTrue reports whether the SQL sets active = TRUE (whitespace-insensitive).
func activatesTrue(sql string) bool {
	flat := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(sql), " ", ""), "\n", "")
	return strings.Contains(flat, "active=true")
}
