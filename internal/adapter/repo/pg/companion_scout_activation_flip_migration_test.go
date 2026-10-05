// companion_scout_activation_flip_migration_test.go — kg_explore P2 wave C RELEASE:
// the 0085 migration flips EXACTLY seedspec.P2ScoutActivationOne (kg_explore)
// active=TRUE, as DATA (a standalone UPDATE), once its ADR-174 §8 grounded-
// reconcile eval gate has PASSED (owner-driven, post-deploy — the eval is
// author-only in-repo). This is the follow-up flip that releases the last of the
// P2 launch remainder (after 0073 sight + 0082 answerable). No other catalogue key
// may be smuggled into the UPDATE list (that would breach the §8 gate). HELD DARK
// until the owner runs the live gate — this test pins the migration's release
// contract, it does NOT apply it.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0085_ActivatesExactlyP2ScoutOne(t *testing.T) {
	up := readMigration(t, "0085_familiar_scout_activation.up.sql")

	// Activation is DATA not schema (spec §5 P1-delta #4): an UPDATE, never ALTER.
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0085 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0085 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// EXACTLY the eval-cleared scout Skill. 0085 is FROZEN at the bytes the live
	// lane applied (skill key fog_scout); 0110_companion_rename flips the live
	// row to kg_explore, so the pinned keys are compared through the pre-0110
	// name map.
	for _, key := range seedspec.P2ScoutActivationOne {
		if !strings.Contains(up, "'"+seedspec.LegacyPre0110(key)+"'") {
			t.Errorf("0085 up missing activation of %q", key)
		}
	}

	// No other catalogue key smuggled into the UPDATE list.
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P2ScoutActivationOne, e.SkillKey) {
			continue
		}
		if strings.Contains(up, "'"+seedspec.LegacyPre0110(e.SkillKey)+"'") {
			t.Errorf("0085 up references %q, which is NOT in P2ScoutActivationOne (ADR-174 gate breach)", e.SkillKey)
		}
	}

	// The down migration re-darks the exact same key (reversible).
	down := readMigration(t, "0085_familiar_scout_activation.down.sql")
	if !strings.Contains(strings.ToLower(down), "set active = false") {
		t.Errorf("0085 down must flip it back dark (SET active = FALSE)")
	}
	for _, key := range seedspec.P2ScoutActivationOne {
		if !strings.Contains(down, "'"+seedspec.LegacyPre0110(key)+"'") {
			t.Errorf("0085 down missing re-dark of %q", key)
		}
	}
}
