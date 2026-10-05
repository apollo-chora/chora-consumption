package pg

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeScan returns a scan func that copies vals into the dest pointers by
// position (reflect-set), so scanCompanionInstanceRow can be exercised without a
// live DB. Arity mismatch is a loud error — which is exactly the RED signal
// when the scan target list has not yet grown to include the persona columns.
func fakeScan(vals ...any) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != len(vals) {
			return &scanArityError{want: len(vals), got: len(dest)}
		}
		for i, d := range dest {
			reflect.ValueOf(d).Elem().Set(reflect.ValueOf(vals[i]))
		}
		return nil
	}
}

type scanArityError struct{ want, got int }

func (e *scanArityError) Error() string {
	return "fakeScan: dest arity mismatch"
}

// TestScanCompanionInstanceRow_MapsPersonaColumns proves the by-id / list scan
// carries the new guidance_note + persona_version through to the aggregate
// (CHO-2015 B1 persistence). Column order MUST mirror loadCompanionInstanceSQL /
// listCompanionInstancesByOwnerSQL: the persona columns sit right after
// configured_rules and before created_at.
func TestScanCompanionInstanceRow_MapsPersonaColumns(t *testing.T) {
	now := time.Now().UTC()
	var deleted *time.Time
	inst, err := scanCompanionInstanceRow(fakeScan(
		"fam-1",                       // companion_id
		"ten-1",                       // tenant_id
		"gcid-1",                      // owner_gcid
		"Ember",                       // name
		"math",                        // specialization
		"adept",                       // evolution_tier
		3,                             // skill_slots_unlocked
		2000,                          // memory_context_capacity
		"a summary",                   // persona_summary
		[]byte(`{"tone":"socratic"}`), // configured_rules
		"be kind and concise",         // guidance_note   ← NEW
		4,                             // persona_version ← NEW
		now,                           // created_at
		now,                           // updated_at
		deleted,                       // deleted_at
	))
	if err != nil {
		t.Fatalf("scanCompanionInstanceRow: %v", err)
	}
	if inst.GuidanceNote != "be kind and concise" {
		t.Errorf("guidance_note not scanned: %q", inst.GuidanceNote)
	}
	if inst.PersonaVersion != 4 {
		t.Errorf("persona_version not scanned: %d", inst.PersonaVersion)
	}
	if inst.ConfiguredRules["tone"] != "socratic" {
		t.Errorf("configured_rules still scanned: %v", inst.ConfiguredRules)
	}
}

// TestCompanionInstanceSQL_PersistsPersonaColumns guards that every read + write
// template projects/persists the persona columns (a missed template = a scan
// arity panic or a silently-dropped save).
func TestCompanionInstanceSQL_PersistsPersonaColumns(t *testing.T) {
	for name, sql := range map[string]string{
		"insert":      insertCompanionInstanceSQL,
		"upsert":      upsertCompanionInstanceSQL,
		"load":        loadCompanionInstanceSQL,
		"listByOwner": listCompanionInstancesByOwnerSQL,
	} {
		for _, col := range []string{"guidance_note", "persona_version"} {
			if !strings.Contains(sql, col) {
				t.Errorf("%s SQL missing column %q", name, col)
			}
		}
	}
}
