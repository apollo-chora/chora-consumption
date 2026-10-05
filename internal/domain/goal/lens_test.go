// lens_test.go: the DERIVED dashboard lead (ADR-204 §3, amended by ADR-214 §4).
// Under ADR-214 the self-completing credential kinds are REMOVED, so NO goal
// KIND drives the credential lens: goals alone always project to curiosity, and
// the credential lead lives on the learner's course-bound learning paths
// (lead_test.go). The lead is NEVER stored: a stored mode-enum would be a
// hidden toggle, violating the integrative-UI mandate.
package goal

import (
	"testing"
	"time"
)

func lensGoal(kind Kind, status Status) Goal {
	g := Goal{
		GoalID: "g", TenantID: gTenant, LearnerGCID: gLearner,
		Kind: kind, Status: status, CreatedAt: gNow, UpdatedAt: gNow,
	}
	if kind.RequiresTarget() {
		g.ChoraTargetRef = strptr(gTarget)
	}
	return g
}

// Goals ALONE never lead with credential: there is no goal-Kind credential
// trigger any more (ADR-214 §3/§4). The credential lead comes from the
// course-bound paths, which this derivation is not given.
func TestPrimaryLens_GoalsAloneAlwaysProjectToCuriosity(t *testing.T) {
	cases := []struct {
		name  string
		goals []Goal
	}{
		{"empty", nil},
		{"active curiosity", []Goal{lensGoal(KindCuriosity, StatusActive)}},
		{"active theme_mastery", []Goal{lensGoal(KindThemeMastery, StatusActive)}},
		{"active edge", []Goal{lensGoal(KindEdge, StatusActive)}},
		{"mixed personal goals", []Goal{lensGoal(KindCuriosity, StatusActive), lensGoal(KindThemeMastery, StatusAchieved)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveLead(tc.goals, nil).PrimaryLens(); got != LensCuriosity {
				t.Errorf("PrimaryLens = %q; want curiosity (no goal KIND leads with credential)", got)
			}
		})
	}
}

func TestPrimaryLens_IgnoresSoftDeleted(t *testing.T) {
	g := lensGoal(KindThemeMastery, StatusActive)
	del := gNow.Add(time.Hour)
	g.DeletedAt = &del
	if got := DeriveLead([]Goal{g}, nil).PrimaryLens(); got != LensCuriosity {
		t.Errorf("PrimaryLens over a soft-deleted goal = %q; want curiosity", got)
	}
}

func TestRequiresTarget(t *testing.T) {
	if KindCuriosity.RequiresTarget() {
		t.Error("curiosity must NOT require a target")
	}
	for _, k := range []Kind{KindThemeMastery, KindEdge} {
		if !k.RequiresTarget() {
			t.Errorf("kind %q must require a target", k)
		}
	}
}
