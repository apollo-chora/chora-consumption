// companion_answerable_activation_flip_migration_test.go — CHO-2016 P2 wave B
// RELEASE: the 0082 migration flips EXACTLY seedspec.P2AnswerableActivationTwo
// (quiz_me + socratic_drill) active=TRUE, as DATA (a standalone UPDATE), now that
// their ADR-174 §8 answerable eval gate has PASSED (18d re-eval: quiz_me
// facts_groundedness 1.0, socratic_drill 0.909 — both clear the 0.80 floor;
// safety 1.0; adversarial block-rate 1.0). 0075 held them dark pending this gate;
// 0082 is the follow-up flip 0075's header promised. No other catalogue key —
// notably kg_explore, whose suggestion-inbox builder + eval have NOT landed — may
// be smuggled into the UPDATE list (that would breach the §8 gate).
package pg

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

func TestMigration0082_ActivatesExactlyP2AnswerableTwo(t *testing.T) {
	up := readMigration(t, "0082_familiar_answerable_activation_flip.up.sql")

	// Activation is DATA not schema (spec §5 P1-delta #4): an UPDATE, never ALTER.
	lower := strings.ToLower(up)
	if !strings.Contains(lower, "update familiar_skill_catalog") || !strings.Contains(lower, "set active = true") {
		t.Fatalf("0082 up must be an UPDATE ... SET active = TRUE; got:\n%s", up)
	}
	if strings.Contains(lower, "alter table") {
		t.Errorf("0082 must not ALTER schema — activation is data (spec §5 P1-delta #4)")
	}

	// EXACTLY the two eval-cleared answerable Skills — both present.
	for _, key := range seedspec.P2AnswerableActivationTwo {
		if !strings.Contains(up, "'"+key+"'") {
			t.Errorf("0082 up missing activation of %q", key)
		}
	}

	// No other catalogue key smuggled into the UPDATE list.
	for _, e := range seedspec.Catalogue() {
		if containsKey(seedspec.P2AnswerableActivationTwo, e.SkillKey) {
			continue
		}
		if strings.Contains(up, "IN (") && strings.Contains(up, "'"+e.SkillKey+"'") {
			t.Errorf("0082 up activates %q, which is NOT in P2AnswerableActivationTwo (ADR-174 gate breach)", e.SkillKey)
		}
	}

	// kg_explore stays dark until its own wave (builder + eval unbuilt).
	if strings.Contains(up, "'fog_scout'") {
		t.Errorf("0082 must NOT activate fog_scout — its suggestion-inbox builder/eval has not landed")
	}

	// The down migration re-darks the exact same two (reversible).
	down := readMigration(t, "0082_familiar_answerable_activation_flip.down.sql")
	if !strings.Contains(strings.ToLower(down), "set active = false") {
		t.Errorf("0082 down must flip the two back dark (SET active = FALSE)")
	}
	for _, key := range seedspec.P2AnswerableActivationTwo {
		if !strings.Contains(down, "'"+key+"'") {
			t.Errorf("0082 down missing re-dark of %q", key)
		}
	}
}
