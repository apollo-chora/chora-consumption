// companion_rename_migration_test.go - ADR-254 D9 migration-content gate for
// 0110_companion_rename: the catalog-driven live rename of every familiar-named
// object / stored value to the Companion vocabulary the renamed service reads.
//
// The historical files 0001..0109 are FROZEN (the runner tracker is
// filename-keyed; see seedspec/legacy_names.go), so this file is the ONLY place
// the seedspec legacy-name map is applied to a live database. The gate pins:
//
//   - the file is one transaction (BEGIN ... COMMIT) and carries no
//     data-destroying DDL (assert-not-destructive.sh would refuse it);
//   - every LegacyNamesBefore0110 value pair is rewritten as DATA (skill_key
//     fog_scout -> kg_explore, price_key familiar_skill_* -> companion_skill_*,
//     fog_scout tokens inside species_paths / ritual steps / ritual run stamps);
//   - the generic catalog passes exist for constraints, indexes, triggers,
//     functions (+ bodies), relations, columns (tables AND indexes), enum types,
//     enum values and policies, all recorded in companion_rename_0110_log;
//   - the CHECK constraints carrying a familiar value are recreated around the
//     provenance update; memory scope keys, review payload JSON keys, pending
//     outbox topics and idempotency keys are rewritten;
//   - it ENDS by asserting nothing familiar-named remains (fail-loud, not warn);
//   - the down replays the log in reverse, inverts every value map, and drops
//     the log; and it does not touch the kg_explore display name (owner copy
//     decision pending) nor the pre-existing 'companion' Skill family.
//
// The behavioural proof (up on a PG 18 built from 0001..0109: zero familiar
// leftovers; down: pg_dump -s identical to the pre-rename dump; fresh build
// with 0110 in the series: identical to the upgraded schema) was run by hand on
// 2026-08-23 and is recorded in the WP-D handoff; this gate keeps the file
// honest afterwards.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

const (
	companionRenameUp   = "0110_companion_rename.up.sql"
	companionRenameDown = "0110_companion_rename.down.sql"
)

func TestMigration0110_TransactionalAndNonDestructive(t *testing.T) {
	for _, name := range []string{companionRenameUp, companionRenameDown} {
		sql := stripSQLComments(readMigration(t, name))
		if !strings.HasPrefix(strings.TrimSpace(sql), "BEGIN;") || !strings.HasSuffix(strings.TrimSpace(sql), "COMMIT;") {
			t.Errorf("%s must be one BEGIN ... COMMIT transaction (the runner applies with psql -f, not -1)", name)
		}
		lower := strings.ToLower(sql)
		for _, bad := range []string{"drop table ", "drop schema", "drop database", "truncate", "drop column", "drop type"} {
			// the down drops ONLY the audit log it created; every other drop is refused
			if strings.Contains(lower, bad) && !(name == companionRenameDown && bad == "drop table " && strings.Count(lower, "drop table ") == 1 && strings.Contains(lower, "drop table companion_rename_0110_log")) {
				t.Errorf("%s carries %q: a rename must not destroy data (assert-not-destructive.sh refuses it)", name, bad)
			}
		}
		if strings.Contains(lower, "set local lock_timeout") == false {
			t.Errorf("%s must SET LOCAL lock_timeout so a rename fails loud instead of queueing behind a long query", name)
		}
	}
}

func TestMigration0110_RenamesEveryLegacyName(t *testing.T) {
	up := stripSQLComments(readMigration(t, companionRenameUp))

	// Value pairs from the single source of truth (seedspec.LegacyNamesBefore0110).
	var (
		priceOld, priceNew, keyOld, keyNew string
	)
	for _, p := range seedspec.LegacyNamesBefore0110 {
		switch p.New {
		case "companion_skill_kg_explore":
			priceOld, priceNew = p.Old, p.New
		case "kg_explore":
			keyOld, keyNew = p.Old, p.New
		}
	}
	if priceOld == "" || keyOld == "" {
		t.Fatalf("seedspec.LegacyNamesBefore0110 lost the fog_scout pairs: %+v", seedspec.LegacyNamesBefore0110)
	}
	musts := []string{
		// skill catalogue keys
		"UPDATE companion_skill_catalog",
		"SET price_key = 'companion_skill_' || substr(price_key, length('familiar_skill_') + 1)",
		"SET skill_key = '" + keyNew + "'",
		"price_key = '" + priceNew + "'",
		"WHERE skill_key = '" + keyOld + "'",
		// the fog_scout token inside JSONB (species paths, ritual steps, run stamps)
		`UPDATE species_paths`,
		`replace(entries::text, '"` + keyOld + `"', '"` + keyNew + `"')::jsonb`,
		`UPDATE companion_ritual_revisions`,
		`replace(steps::text, '"` + keyOld + `"', '"` + keyNew + `"')::jsonb`,
		`UPDATE companion_ritual_runs`,
		`replace(decision_stamp::text, '"` + keyOld + `"', '"` + keyNew + `"')::jsonb`,
		// memory scope keys, provenance, review payload, outbox, idempotency keys
		"SET scope_key = 'companion:' || substr(scope_key, length('familiar:') + 1)",
		"UPDATE concept_nodes SET provenance = 'companion_suggested_accepted' WHERE provenance = 'familiar_suggested_accepted'",
		"UPDATE concept_edges SET provenance = 'companion_suggested_accepted' WHERE provenance = 'familiar_suggested_accepted'",
		`replace(replace(review_payload::text, '"familiar_id":', '"companion_id":'), '"familiar":', '"companion":')::jsonb`,
		"UPDATE outbox_events",
		"WHERE status IN ('pending', 'failed')",
		"UPDATE idempotency_keys",
		// the generic catalog passes (one per object class) + the audit log
		"CREATE TABLE IF NOT EXISTS companion_rename_0110_log",
		"RENAME CONSTRAINT %I TO %I",
		"ALTER INDEX %I RENAME TO %I",
		"ALTER TRIGGER %I ON %s RENAME TO %I",
		"ALTER FUNCTION %I(%s) RENAME TO %I",
		"pg_get_functiondef(p.oid)",
		"ALTER TABLE %I RENAME TO %I",
		"ALTER TABLE %I RENAME COLUMN %I TO %I",
		"AND c.relkind IN ('r', 'p', 'v', 'm', 'i', 'I')", // index attributes too
		"ALTER TYPE %I RENAME TO %I",
		"ALTER TYPE %I RENAME VALUE %L TO %L",
		"ALTER POLICY %I ON %s RENAME TO %I",
		"AND c.contype = 'c'",
		"pg_get_constraintdef(c.oid) ILIKE '%familiar%'",
		"ALTER TABLE %s ADD CONSTRAINT %I %s",
		"COMMENT ON COLUMN %I.%I IS %L",
		// fail-loud end state
		"RAISE EXCEPTION '0110_companion_rename: familiar leftovers after rename: %', leftovers",
	}
	assertContainsAll(t, companionRenameUp, up, musts)

	// The display name follows the owner ruling (2026-08-23): 'Fog Scout' ->
	// 'Knowledge Explorer' rides the same UPDATE as the key flip, and the down
	// restores the frozen seed's name.
	if !strings.Contains(up, "name      = 'Knowledge Explorer'") {
		t.Error("0110 up must rename the kg_explore display name to 'Knowledge Explorer' (owner ruling)")
	}
	if !strings.Contains(stripSQLComments(readMigration(t, companionRenameDown)), "name      = 'Fog Scout'") {
		t.Error("0110 down must restore the display name 'Fog Scout'")
	}
	// Not touched on purpose.
	if strings.Contains(up, "chk_skill_catalog_family") {
		t.Error("0110 must not touch the pre-existing 'companion' Skill family CHECK")
	}
}

func TestMigration0110_DownReplaysTheLogAndInvertsValues(t *testing.T) {
	down := stripSQLComments(readMigration(t, companionRenameDown))
	musts := []string{
		"FROM companion_rename_0110_log",
		"ORDER BY id DESC",
		"WHEN 'constraint' THEN",
		"WHEN 'index' THEN",
		"WHEN 'trigger' THEN",
		"WHEN 'function' THEN",
		"WHEN 'functiondef' THEN",
		"WHEN 'relation' THEN",
		"WHEN 'column' THEN",
		"WHEN 'type' THEN",
		"WHEN 'enumvalue' THEN",
		"WHEN 'policy' THEN",
		"WHEN 'comment_column' THEN",
		"RAISE EXCEPTION '0110_companion_rename down: unknown log kind % (id %)', r.kind, r.id",
		// value maps inverted
		"SET skill_key = 'fog_scout'",
		"price_key = 'familiar_skill_fog_scout'",
		"SET price_key = 'familiar_skill_' || substr(price_key, length('companion_skill_') + 1)",
		`replace(entries::text, '"kg_explore"', '"fog_scout"')::jsonb`,
		"SET scope_key = 'familiar:' || substr(scope_key, length('companion:') + 1)",
		"UPDATE concept_nodes SET provenance = 'familiar_suggested_accepted' WHERE provenance = 'companion_suggested_accepted'",
		"DROP TABLE companion_rename_0110_log",
	}
	assertContainsAll(t, companionRenameDown, down, musts)
}
