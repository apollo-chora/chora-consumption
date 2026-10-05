// lead_test.go: RED first for the LEAD COUNTERS that replace the lens
// constant (UX Track U, package B5; master plan section 4.1).
//
// The home ranks one list by (urgency, warmth) and needs the RAW signals, not
// a binary enum: a stored or latched mode is the "fixed UI per audience" shape
// ruling R2 forbids. These tests pin the four counters and the back-compat
// lens derived from them.
package goal

import (
	"testing"
	"time"
)

func leadGoal(status Status, updated time.Time) Goal {
	return Goal{
		GoalID: "g", TenantID: gTenant, LearnerGCID: gLearner,
		Kind: KindCuriosity, Status: status,
		CreatedAt: updated, UpdatedAt: updated,
	}
}

// A learner with no goals and no paths leads with curiosity and counts nothing.
// The zero Lead is the value the adapter emits when both reads come back empty,
// so it MUST be the safe default the FE already assumes.
func TestDeriveLead_Empty(t *testing.T) {
	l := DeriveLead(nil, nil)
	if l.ActiveGoals != 0 || l.ActiveCourseBoundPaths != 0 {
		t.Errorf("counts = %d/%d; want 0/0", l.ActiveGoals, l.ActiveCourseBoundPaths)
	}
	if l.LastCuriosityAt != nil || l.LastCourseAt != nil {
		t.Errorf("timestamps = %v/%v; want nil/nil", l.LastCuriosityAt, l.LastCourseAt)
	}
	if got := l.PrimaryLens(); got != LensCuriosity {
		t.Errorf("PrimaryLens = %q; want curiosity", got)
	}
	if got := (Lead{}).PrimaryLens(); got != LensCuriosity {
		t.Errorf("zero Lead PrimaryLens = %q; want curiosity", got)
	}
}

// ActiveGoals counts ACTIVE goals only; every non-deleted goal, whatever its
// status, still stamps the curiosity axis (an achieved goal touched an hour
// ago is curiosity activity, it is just not an open one).
func TestDeriveLead_ActiveGoalsAndCuriosityStamp(t *testing.T) {
	older := gNow.Add(-48 * time.Hour)
	newer := gNow.Add(-1 * time.Hour)
	l := DeriveLead([]Goal{
		leadGoal(StatusActive, older),
		leadGoal(StatusActive, older),
		leadGoal(StatusAchieved, newer),
		leadGoal(StatusRetired, older),
	}, nil)
	if l.ActiveGoals != 2 {
		t.Errorf("ActiveGoals = %d; want 2 (achieved + retired do not count)", l.ActiveGoals)
	}
	if l.LastCuriosityAt == nil || !l.LastCuriosityAt.Equal(newer) {
		t.Errorf("LastCuriosityAt = %v; want %v (the latest touch of ANY live goal)", l.LastCuriosityAt, newer)
	}
}

// A soft-deleted goal is invisible on both axes (ddd-enforcement #4).
func TestDeriveLead_IgnoresSoftDeletedGoals(t *testing.T) {
	del := gNow
	g := leadGoal(StatusActive, gNow)
	g.DeletedAt = &del
	l := DeriveLead([]Goal{g}, nil)
	if l.ActiveGoals != 0 {
		t.Errorf("ActiveGoals = %d; want 0 (soft-deleted)", l.ActiveGoals)
	}
	if l.LastCuriosityAt != nil {
		t.Errorf("LastCuriosityAt = %v; want nil (soft-deleted stamps nothing)", l.LastCuriosityAt)
	}
	if got := l.PrimaryLens(); got != LensCuriosity {
		t.Errorf("PrimaryLens = %q; want curiosity", got)
	}
}

// The cheapest credential predicate available in the same request: at least one
// live learning path bound to a course. A collection-derived study list and an
// ad-hoc path are NOT course-bound and must not flip the lead.
func TestDeriveLead_CourseBoundPathsDriveTheCredentialLens(t *testing.T) {
	t0 := gNow.Add(-6 * time.Hour)
	t1 := gNow.Add(-2 * time.Hour)
	l := DeriveLead(
		[]Goal{leadGoal(StatusActive, gNow.Add(-30 * time.Minute))},
		[]PathLead{
			{CourseBound: false, UpdatedAt: gNow},
			{CourseBound: true, UpdatedAt: t0},
			{CourseBound: true, UpdatedAt: t1},
		},
	)
	if l.ActiveCourseBoundPaths != 2 {
		t.Errorf("ActiveCourseBoundPaths = %d; want 2 (the un-bound path does not count)", l.ActiveCourseBoundPaths)
	}
	if l.LastCourseAt == nil || !l.LastCourseAt.Equal(t1) {
		t.Errorf("LastCourseAt = %v; want %v (latest COURSE-BOUND touch, not the study list)", l.LastCourseAt, t1)
	}
	if got := l.PrimaryLens(); got != LensCredential {
		t.Errorf("PrimaryLens = %q; want credential (a live course-bound path leads)", got)
	}
}

// Paths with no course binding leave the learner curiosity-led, and stamp
// nothing on the course axis.
func TestDeriveLead_UnboundPathsLeaveCuriosityLed(t *testing.T) {
	l := DeriveLead(
		[]Goal{leadGoal(StatusActive, gNow)},
		[]PathLead{{CourseBound: false, UpdatedAt: gNow}},
	)
	if l.ActiveCourseBoundPaths != 0 {
		t.Errorf("ActiveCourseBoundPaths = %d; want 0", l.ActiveCourseBoundPaths)
	}
	if l.LastCourseAt != nil {
		t.Errorf("LastCourseAt = %v; want nil", l.LastCourseAt)
	}
	if got := l.PrimaryLens(); got != LensCredential && got != LensCuriosity {
		t.Fatalf("PrimaryLens = %q; want a valid lens", got)
	}
	if got := l.PrimaryLens(); got != LensCuriosity {
		t.Errorf("PrimaryLens = %q; want curiosity", got)
	}
}

// The returned timestamps must be COPIES: an aliased pointer into the caller's
// slice would let a later mutation rewrite a value the handler already emitted.
func TestDeriveLead_TimestampsAreCopies(t *testing.T) {
	goals := []Goal{leadGoal(StatusActive, gNow)}
	paths := []PathLead{{CourseBound: true, UpdatedAt: gNow}}
	l := DeriveLead(goals, paths)
	goals[0].UpdatedAt = gNow.Add(72 * time.Hour)
	paths[0].UpdatedAt = gNow.Add(72 * time.Hour)
	if l.LastCuriosityAt == nil || !l.LastCuriosityAt.Equal(gNow) {
		t.Errorf("LastCuriosityAt = %v; want a copy pinned at %v", l.LastCuriosityAt, gNow)
	}
	if l.LastCourseAt == nil || !l.LastCourseAt.Equal(gNow) {
		t.Errorf("LastCourseAt = %v; want a copy pinned at %v", l.LastCourseAt, gNow)
	}
}

// A zero UpdatedAt is NOT a timestamp: a row written before the column existed
// must leave the axis unstamped rather than pin it at year 1, which would rank
// as infinitely stale and outrank every genuinely overdue card on the home.
func TestDeriveLead_ZeroTimestampsDoNotStampAnAxis(t *testing.T) {
	l := DeriveLead(
		[]Goal{leadGoal(StatusActive, time.Time{})},
		[]PathLead{{CourseBound: true, UpdatedAt: time.Time{}}},
	)
	if l.ActiveGoals != 1 || l.ActiveCourseBoundPaths != 1 {
		t.Fatalf("counts = %d/%d; want 1/1 (a zero stamp still counts the row)", l.ActiveGoals, l.ActiveCourseBoundPaths)
	}
	if l.LastCuriosityAt != nil {
		t.Errorf("LastCuriosityAt = %v; want nil for a zero UpdatedAt", l.LastCuriosityAt)
	}
	if l.LastCourseAt != nil {
		t.Errorf("LastCourseAt = %v; want nil for a zero UpdatedAt", l.LastCourseAt)
	}
}
