// seedspec_p2_activation_test.go — CHO-2014 P2 (Fledgling's Kit): pins the P2
// launch-remainder activation SET + its stage gating. The set is the release
// contract for the eventual active=TRUE migration (drift-tested like the P1
// three), but the flip itself is DEFERRED: these five carry no backing agent
// tool and no ADR-174 eval suite yet, so releasing them would bypass the §8
// eval gate — the same gating that held reminder_bell dark at P1.
package seedspec

import "testing"

// TestP2ActivationFive_ShapeAndGating asserts the P2 set is exactly the five
// launch-remainder Skills, each an active catalogue Skill gated at its spec
// stage (quiz_me/map_sight/weakness_sight st3; socratic_drill/kg_explore st4).
func TestP2ActivationFive_ShapeAndGating(t *testing.T) {
	byKey := CatalogueByKey()

	wantStage := map[string]int{
		"quiz_me":        3,
		"map_sight":      3,
		"weakness_sight": 3,
		"socratic_drill": 4,
		"kg_explore":     4,
	}
	if len(P2ActivationFive) != len(wantStage) {
		t.Fatalf("P2ActivationFive has %d keys, want %d", len(P2ActivationFive), len(wantStage))
	}
	seen := map[string]bool{}
	for _, key := range P2ActivationFive {
		if seen[key] {
			t.Errorf("duplicate key %q in P2ActivationFive", key)
		}
		seen[key] = true

		e, ok := byKey[key]
		if !ok {
			t.Fatalf("P2ActivationFive key %q absent from the catalogue", key)
		}
		if e.SkillKind != "active" {
			t.Errorf("%q kind = %q, want active (craft skills are never invoke-activated)", key, e.SkillKind)
		}
		want, tracked := wantStage[key]
		if !tracked {
			t.Errorf("%q is not an expected P2 launch-remainder Skill", key)
			continue
		}
		if e.MinGrowthStage != want {
			t.Errorf("%q min_growth_stage = %d, want %d", key, e.MinGrowthStage, want)
		}
	}
}

// TestP2ActivationFive_IsLaunchRemainder ties the P2 set to the canonical
// Launch-Seven set: P2 releases exactly LaunchSeven minus the P1-activated
// explain_anew and minus reminder_bell (deferred at P1 pending its
// notify.schedule tool). No un-launch Skill may be smuggled into the P2 set.
func TestP2ActivationFive_IsLaunchRemainder(t *testing.T) {
	// Every P2 key must be a Launch-Seven member.
	launch := map[string]bool{}
	for _, k := range LaunchSeven {
		launch[k] = true
	}
	for _, k := range P2ActivationFive {
		if !launch[k] {
			t.Errorf("P2ActivationFive key %q is not in LaunchSeven (un-launch release)", k)
		}
	}

	// The P2 set must be LaunchSeven \ {explain_anew, reminder_bell}.
	deferredOrEarlier := map[string]bool{"explain_anew": true, "reminder_bell": true}
	wantP2 := map[string]bool{}
	for _, k := range LaunchSeven {
		if !deferredOrEarlier[k] {
			wantP2[k] = true
		}
	}
	if len(wantP2) != len(P2ActivationFive) {
		t.Fatalf("launch-remainder has %d keys, P2ActivationFive has %d", len(wantP2), len(P2ActivationFive))
	}
	for _, k := range P2ActivationFive {
		if !wantP2[k] {
			t.Errorf("P2ActivationFive key %q is not in the launch-remainder set", k)
		}
	}

	// P2 must never re-activate a P1 Skill.
	p1 := map[string]bool{}
	for _, k := range P1ActivationThree {
		p1[k] = true
	}
	for _, k := range P2ActivationFive {
		if p1[k] {
			t.Errorf("P2ActivationFive key %q overlaps P1ActivationThree (double activation)", k)
		}
	}
}
