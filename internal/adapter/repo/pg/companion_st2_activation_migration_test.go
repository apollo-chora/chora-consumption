// companion_st2_activation_migration_test.go — CHO-2013 P1.B (R4-1): the 0063
// activation migration must flip EXACTLY seedspec.P1ActivationThree active,
// as DATA (a standalone UPDATE), leaving the 0061 dark seed untouched.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0063_ActivatesExactlyP1Three(t *testing.T) {
	up := readMigration(t, "0063_familiar_st2_three_activation.up.sql")

	// It must be a data UPDATE that sets active = TRUE — never an ALTER (spec
	// §5 P1-delta #4: activation is DATA not schema).
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0063 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0063 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// The UPDATE targets EXACTLY the P1 three — every one present, and no other
	// catalogue key smuggled in (a stray key would silently release an
	// un-evaluated Skill — the ADR-174 gate breach this test guards).
	for _, key := range seedspec.P1ActivationThree {
		if !strings.Contains(up, "'"+key+"'") {
			t.Errorf("0063 up missing activation of %q", key)
		}
	}
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P1ActivationThree, e.SkillKey) {
			continue
		}
		// Any non-P1 key appearing in the UPDATE list is a release breach.
		if strings.Contains(up, "IN (") && strings.Contains(up, "'"+e.SkillKey+"'") {
			t.Errorf("0063 up activates %q, which is NOT in P1ActivationThree (ADR-174 gate breach)", e.SkillKey)
		}
	}

	// reminder_bell stays owned-dark until P2 (R4-1) — it MUST NOT be in 0063.
	if strings.Contains(up, "'reminder_bell'") {
		t.Errorf("0063 must NOT activate reminder_bell — deferred to P2 (R4-1)")
	}

	// The down migration reverses the exact same three.
	down := readMigration(t, "0063_familiar_st2_three_activation.down.sql")
	if !strings.Contains(strings.ToLower(down), "set active = false") {
		t.Errorf("0063 down must flip the three back dark (SET active = FALSE)")
	}
	for _, key := range seedspec.P1ActivationThree {
		if !strings.Contains(down, "'"+key+"'") {
			t.Errorf("0063 down missing re-dark of %q", key)
		}
	}
}

func TestP1ActivationThree_IsCraftFreeActiveSkills(t *testing.T) {
	byKey := seedspec.CatalogueByKey()
	for _, key := range seedspec.P1ActivationThree {
		e, ok := byKey[key]
		if !ok {
			t.Fatalf("P1ActivationThree key %q absent from the catalogue", key)
		}
		// ADR-228 D3 (F-I2): progress_mirror is now the st1 hatch-minted Skill;
		// the other two P1-activation Skills stay st2. All three remain
		// craft-free ACTIVE Skills — the invariant this test really guards.
		wantStage := 2
		if key == "progress_mirror" {
			wantStage = 1
		}
		if e.MinGrowthStage != wantStage {
			t.Errorf("%q min_growth_stage = %d, want %d", key, e.MinGrowthStage, wantStage)
		}
		if e.SkillKind != "active" {
			t.Errorf("%q kind = %q, want active (craft skills are never invoke-activated)", key, e.SkillKind)
		}
	}
	if len(seedspec.P1ActivationThree) != 3 {
		t.Errorf("P1ActivationThree has %d keys, want 3", len(seedspec.P1ActivationThree))
	}
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}
