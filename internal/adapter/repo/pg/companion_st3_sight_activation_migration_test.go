// companion_st3_sight_activation_migration_test.go — CHO-2014 P2 wave A: the 0073
// activation migration must flip EXACTLY seedspec.P2SightActivationTwo
// (weakness_sight + map_sight) active, as DATA (a standalone UPDATE), leaving the
// dark seed untouched. The un-ready P2 remainder (quiz_me/socratic_drill/
// kg_explore) must NOT be smuggled in — their invoke-runner builders / ADR-174
// eval suites have not landed, so releasing them would breach the §8 eval gate.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0073_ActivatesExactlyP2SightTwo(t *testing.T) {
	up := readMigration(t, "0073_familiar_st3_sight_activation.up.sql")

	// Activation is DATA not schema (spec §5 P1-delta #4): an UPDATE, never ALTER.
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0073 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0073 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// EXACTLY the two ready sight Skills — both present.
	for _, key := range seedspec.P2SightActivationTwo {
		if !strings.Contains(up, "'"+key+"'") {
			t.Errorf("0073 up missing activation of %q", key)
		}
	}

	// No other catalogue key smuggled into the UPDATE list (a stray key would
	// silently release an un-evaluated Skill — the ADR-174 gate breach).
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P2SightActivationTwo, e.SkillKey) {
			continue
		}
		if strings.Contains(up, "IN (") && strings.Contains(up, "'"+e.SkillKey+"'") {
			t.Errorf("0073 up activates %q, which is NOT in P2SightActivationTwo (ADR-174 gate breach)", e.SkillKey)
		}
	}

	// The un-ready P2 remainder stays dark until its own wave.
	for _, key := range []string{"quiz_me", "socratic_drill", "fog_scout"} {
		if strings.Contains(up, "'"+key+"'") {
			t.Errorf("0073 must NOT activate %q — its builder/eval has not landed (CHO-2014 wave)", key)
		}
	}

	// The down migration reverses the exact same two.
	down := readMigration(t, "0073_familiar_st3_sight_activation.down.sql")
	if !strings.Contains(strings.ToLower(down), "set active = false") {
		t.Errorf("0073 down must flip the two back dark (SET active = FALSE)")
	}
	for _, key := range seedspec.P2SightActivationTwo {
		if !strings.Contains(down, "'"+key+"'") {
			t.Errorf("0073 down missing re-dark of %q", key)
		}
	}
}

func TestP2SightActivationTwo_IsStage3ActiveSightSkills(t *testing.T) {
	byKey := seedspec.CatalogueByKey()
	for _, key := range seedspec.P2SightActivationTwo {
		e, ok := byKey[key]
		if !ok {
			t.Fatalf("P2SightActivationTwo key %q absent from the catalogue", key)
		}
		if e.MinGrowthStage != 3 {
			t.Errorf("%q min_growth_stage = %d, want 3 (st3 sight-activation set)", key, e.MinGrowthStage)
		}
		if e.SkillKind != "active" {
			t.Errorf("%q kind = %q, want active (craft skills are never invoke-activated)", key, e.SkillKind)
		}
	}
	if len(seedspec.P2SightActivationTwo) != 2 {
		t.Errorf("P2SightActivationTwo has %d keys, want 2", len(seedspec.P2SightActivationTwo))
	}
	// Each wave must be a subset of the eventual full P2 launch set.
	for _, key := range seedspec.P2SightActivationTwo {
		if !containsKey(seedspec.P2ActivationFive, key) {
			t.Errorf("%q not in P2ActivationFive — activation waves must subset the launch set", key)
		}
	}
}
