// companion_web_research_activation_flip_migration_test.go — P5 Far Sight
// (CHO-2017) web_research RELEASE: the 0088 migration flips EXACTLY
// seedspec.P5SeekerActivationWebResearch (web_research) active=TRUE, as DATA (a
// standalone UPDATE), once its ADR-174 §8 external_egress eval gate has PASSED
// (owner-driven, post-deploy — the eval is author-only in-repo). No other
// catalogue key may be smuggled into the UPDATE list (that would breach the §8
// gate). HELD DARK until the owner runs the live gate — this test pins the
// migration's release contract, it does NOT apply it.
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0088_ActivatesExactlyP5SeekerWebResearch(t *testing.T) {
	up := readMigration(t, "0088_familiar_seeker_web_research_activation.up.sql")

	// Activation is DATA not schema (spec §5 P1-delta #4): an UPDATE, never ALTER.
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0088 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0088 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// EXACTLY the eval-cleared Seeker Skill.
	for _, key := range seedspec.P5SeekerActivationWebResearch {
		if !strings.Contains(up, "'"+key+"'") {
			t.Errorf("0088 up missing activation of %q", key)
		}
	}

	// No other catalogue key smuggled into the UPDATE list — in particular the
	// fact_check + source_reader siblings must not ride this wave.
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P5SeekerActivationWebResearch, e.SkillKey) {
			continue
		}
		if strings.Contains(up, "'"+e.SkillKey+"'") {
			t.Errorf("0088 up references %q, which is NOT in P5SeekerActivationWebResearch (ADR-174 gate breach)", e.SkillKey)
		}
	}

	// The down migration re-darks the exact same key (reversible).
	down := readMigration(t, "0088_familiar_seeker_web_research_activation.down.sql")
	if !strings.Contains(strings.ToLower(down), "set active = false") {
		t.Errorf("0088 down must flip it back dark (SET active = FALSE)")
	}
	for _, key := range seedspec.P5SeekerActivationWebResearch {
		if !strings.Contains(down, "'"+key+"'") {
			t.Errorf("0088 down missing re-dark of %q", key)
		}
	}
}
