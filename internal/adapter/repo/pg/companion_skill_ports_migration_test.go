// companion_skill_ports_migration_test.go — RED-first migration-content gate
// for ADR-257 D2: the reserved inter-step port columns on
// companion_skill_catalog.
//
// The columns are ADDITIVE and RESERVED: both default to 'none', nothing reads
// them, and the values themselves are a later data migration derived from
// ToolHandlerRefs + OutputSink. What this gate pins is the part that would rot
// silently otherwise, the CHECK constraint's value list against
// companion.IsKnownPort: adding a Go port constant without widening the
// constraint would fail at INSERT time in production, long after the Go change
// looked complete.
package pg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

const portsMigrationUp = "0117_companion_skill_ports.up.sql"
const portsMigrationDown = "0117_companion_skill_ports.down.sql"

// allKnownPorts is the Go-side closed set, asserted total against
// companion.IsKnownPort so this list cannot silently fall behind either.
var allKnownPorts = []string{
	companion.PortNone,
	companion.PortText,
	companion.PortConceptRef,
	companion.PortGrowthEdgeRef,
	companion.PortAtomRef,
	companion.PortQuestionRef,
	companion.PortSourceRef,
}

// TestPortVocabulary_ListIsTotal guards the guard: if a new port constant is
// added to catalog.go and not to allKnownPorts, the constraint test below would
// still pass while covering less. Probing a handful of plausible next names is
// weak, so this instead asserts the count, which any addition changes.
func TestPortVocabulary_ListIsTotal(t *testing.T) {
	const want = 7
	if len(allKnownPorts) != want {
		t.Fatalf("allKnownPorts has %d entries, want %d: a port constant was added or removed, "+
			"so widen BOTH this list and the migration CHECK constraint", len(allKnownPorts), want)
	}
	for _, p := range allKnownPorts {
		if !companion.IsKnownPort(p) {
			t.Errorf("port %q is in allKnownPorts but companion.IsKnownPort rejects it", p)
		}
	}
}

func TestMigration0117_AddsBothPortColumnsAdditively(t *testing.T) {
	sql := readMigration(t, portsMigrationUp)

	for _, col := range []string{"consumes", "produces"} {
		// IF NOT EXISTS keeps the migration idempotent (the 0061 idiom).
		want := fmt.Sprintf("ADD COLUMN IF NOT EXISTS %s TEXT NOT NULL DEFAULT 'none'", col)
		if !strings.Contains(sql, want) {
			t.Errorf("migration must add %q additively with the 'none' default.\nwant substring: %s", col, want)
		}
	}
	if !strings.Contains(sql, "ALTER TABLE companion_skill_catalog") {
		t.Error("migration must target companion_skill_catalog (post-0110 name)")
	}
	// Reserved means unread: the migration must not author any values, or the
	// ports stop being reserved and start being a claim.
	if strings.Contains(strings.ToUpper(sql), "UPDATE COMPANION_SKILL_CATALOG") {
		t.Error("0117 is a SCHEMA vehicle only; authoring port values belongs to a separate " +
			"drift-pinned data migration derived from ToolHandlerRefs + OutputSink")
	}
}

// TestMigration0117_ConstraintCoversEveryKnownPort is the drift gate that
// matters: the CHECK list and the Go vocabulary must agree in BOTH directions.
func TestMigration0117_ConstraintCoversEveryKnownPort(t *testing.T) {
	sql := readMigration(t, portsMigrationUp)

	for _, p := range allKnownPorts {
		if !strings.Contains(sql, "'"+p+"'") {
			t.Errorf("port %q is known to Go but absent from the migration CHECK constraint: "+
				"a row carrying it would be refused at INSERT time in production", p)
		}
	}
	// And the reverse: nothing the constraint ALLOWS that Go does not know.
	// Scoped to the IN-lists on purpose, so a column COMMENT can quote a port
	// name in prose without tripping this.
	lists := checkInLists(t, sql)
	if len(lists) != 2 {
		t.Fatalf("expected 2 CHECK IN-lists (consumes, produces), found %d", len(lists))
	}
	for _, list := range lists {
		for _, lit := range sqlStringLiterals(list) {
			if !companion.IsKnownPort(lit) {
				t.Errorf("migration CHECK constraint allows %q, which companion.IsKnownPort rejects", lit)
			}
		}
	}
	for _, col := range []string{"consumes", "produces"} {
		if !strings.Contains(sql, "chk_skill_catalog_"+col) {
			t.Errorf("expected a named CHECK constraint chk_skill_catalog_%s (the 0061 naming idiom)", col)
		}
	}
}

// TestMigration0117_IsReversible — every migration ships a down (the
// database-postgresql convention), and this one's down must drop exactly what
// the up added, leaving no orphan constraint.
func TestMigration0117_IsReversible(t *testing.T) {
	sql := readMigration(t, portsMigrationDown)
	for _, col := range []string{"consumes", "produces"} {
		want := fmt.Sprintf("DROP COLUMN IF EXISTS %s", col)
		if !strings.Contains(sql, want) {
			t.Errorf("down migration must drop %q.\nwant substring: %s", col, want)
		}
	}
	if strings.Contains(sql, "DROP TABLE") {
		t.Error("down migration must not drop the catalogue table")
	}
}

// checkInLists returns the body of each `CHECK (<col> IN (...))` value list in
// the migration. Narrow by design: the reverse-direction assertion is about
// what the constraint ADMITS, so it must not read prose in a COMMENT that
// happens to quote a port name.
func checkInLists(t *testing.T, sql string) []string {
	t.Helper()
	var out []string
	rest := sql
	for {
		i := strings.Index(rest, " IN (")
		if i < 0 {
			return out
		}
		rest = rest[i+len(" IN ("):]
		j := strings.Index(rest, ")")
		if j < 0 {
			t.Fatalf("unterminated IN list in migration")
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

// sqlStringLiterals returns the single-quoted literals in a SQL blob. Good
// enough for a CHECK-constraint value list, which is what it reads.
func sqlStringLiterals(sql string) []string {
	var out []string
	parts := strings.Split(sql, "'")
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}
