// companion_answerable_activation_reassert_migration_test.go — W0-F2b.
//
// 0096 REPAIRS AN ORDERING DEFECT, it does not make a new release decision.
//
// The defect: 0075 (which holds quiz_me + socratic_drill DARK pending their
// ADR-174 §8 eval) has NO row in chora_runner_schema_migrations, while 0082
// (the eval-passed flip that ACTIVATES them) HAS one. runner.sh decides
// pending-vs-applied solely from that table, so on its next run it APPLIES the
// unrecorded 0075 (active=FALSE) and SKIPS the recorded 0082 — the older file's
// intent silently overwrites the newer applied one. Two live, eval-gate-passed,
// learner-facing Skills go dark, with zero errors and a green SUCCESS report.
//
// 0096 re-asserts 0082's decision AFTER 0075 in sort order. THE ORDERING IS THE
// FIX: renumber 0096 below 0075 and the repair runs before the damage and does
// nothing. TestMigration0096_SortsAfterTheMigrationItRepairs pins that.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

const (
	reassertUp   = "0096_familiar_answerable_activation_reassert.up.sql"
	reassertDown = "0096_familiar_answerable_activation_reassert.down.sql"
)

// sqlOnly strips `--` line comments, so an assertion about what a migration DOES
// cannot be satisfied — or broken — by what its header SAYS. These headers
// necessarily quote the very statements they exist to warn against (0096's down
// explains why a `SET active = FALSE` would be wrong), and a raw-text grep reads
// that prose as the deed. Assert on the executable SQL; assert on the prose
// separately, and deliberately.
func sqlOnly(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func TestMigration0096_ReassertsExactlyP2AnswerableTwo(t *testing.T) {
	up := sqlOnly(readMigration(t, reassertUp))
	lower := strings.ToLower(up)

	// Activation is DATA, not schema (spec §5 P1-delta #4) — mirrors 0082.
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0096 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0096 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// EXACTLY the two eval-cleared answerable Skills that 0082 released.
	for _, key := range seedspec.P2AnswerableActivationTwo {
		if !strings.Contains(up, "'"+key+"'") {
			t.Errorf("0096 up missing re-assertion of %q", key)
		}
	}

	// No other catalogue key smuggled in. A repair migration is a tempting place
	// to quietly widen an activation list — that would breach the ADR-174 §8 gate
	// exactly as it would in 0082.
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P2AnswerableActivationTwo, e.SkillKey) {
			continue
		}
		if strings.Contains(up, "'"+e.SkillKey+"'") {
			t.Errorf("0096 up activates %q, which is NOT in P2AnswerableActivationTwo (ADR-174 §8 gate breach)", e.SkillKey)
		}
	}

	// kg_explore is out of scope — NOT dark. 0075's and 0082's headers say its
	// builder + eval "have not landed"; that was true when they were written and
	// is false now (0085_familiar_scout_activation released it; live: active =
	// true). The assertion stands, but on SCOPE: kg_explore's activation is owned
	// by 0085, and a repair for the answerable pair must not restate another
	// wave's decision. Asserting it "stays dark" would be a lie that passes.
	if strings.Contains(up, "'fog_scout'") {
		t.Errorf("0096 must NOT touch fog_scout — its activation is owned by 0085; " +
			"a repair scoped to P2AnswerableActivationTwo may not widen its list")
	}
}

// The whole mechanism is that 0096 runs AFTER the file it repairs. If a future
// renumber puts it below 0075, the repair fires before the damage and the Skills
// go dark again — silently, because nothing errors.
func TestMigration0096_SortsAfterTheMigrationItRepairs(t *testing.T) {
	const repaired = "0075_familiar_answerable_activation.up.sql"

	// Positive control: the file it repairs must still exist and must still hold
	// the two dark IN ITS EXECUTABLE SQL. If someone "fixed" 0075 by editing it
	// instead of superseding it, this repair is dead weight and the reader
	// deserves to know.
	held := sqlOnly(readMigration(t, repaired))
	if !strings.Contains(strings.ToLower(held), "set active = false") {
		t.Fatalf("%s no longer holds the answerable Skills dark — 0096 supersedes it; "+
			"if 0075 was rewritten, delete 0096 rather than leave a repair for a defect that moved", repaired)
	}

	if reassertUp <= repaired {
		t.Fatalf("0096 (%s) must sort AFTER the migration it repairs (%s) — "+
			"the ordering IS the fix; runner.sh applies pending files in sort order",
			reassertUp, repaired)
	}
}

// The down must NOT re-dark. Reversing a repair would restore the defect: two
// Skills dark despite a PASSED eval gate. The activation decision lives in 0082
// — roll THAT back if they should be dark.
func TestMigration0096_DownIsADeliberateNoOp(t *testing.T) {
	raw := readMigration(t, reassertDown)

	// Negative, on the EXECUTABLE SQL only. The header necessarily quotes the
	// statement it exists to forbid; a raw grep would read that warning as the
	// deed and fail an otherwise-correct file.
	if strings.Contains(strings.ToLower(sqlOnly(raw)), "set active = false") {
		t.Errorf("0096 down must NOT re-dark the answerable Skills — that restores the defect. " +
			"The activation decision belongs to 0082; reverse it there.")
	}

	// Positive controls, on the PROSE. Without these, the negative above is an
	// absence assertion that a zero-byte file would satisfy perfectly — and a
	// silently-empty down is exactly the shape of defect this migration exists
	// to repair.
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "0082") {
		t.Errorf("0096 down must explain that the activation decision lives in 0082 "+
			"and that this no-op is deliberate; got:\n%s", raw)
	}
	if !strings.Contains(lower, "no-op") {
		t.Errorf("0096 down must state it is an intentional no-op; got:\n%s", raw)
	}
}
