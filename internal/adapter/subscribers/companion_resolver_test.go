// companion_resolver_test.go — CHO-2012 (ADR-218 D7): EXP attribution
// resolves through Goal attachment. The goal-attached companion (most recent
// summon, ADR-212 D5 Goal.AttachCompanion) wins over roster order; the
// legacy oldest-roster read survives ONLY as the terminal fallback so EXP
// is never dropped for learners who have not summoned onto a map yet.
//
// F-I1.4 (CHO-2088, ADR-228 incubation): the resolver reads the roster
// (which carries growth stage) so a pre-hatch Stage-0 egg is EXCLUDED from
// BOTH fallbacks — an egg accrues XP ONLY via an exact goal attachment.
package subscribers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

type stubCompanionLister struct {
	roster []*companion.RosterEntry
	err    error
}

func (s *stubCompanionLister) ListRosterByOwner(_ context.Context, _, _ string) ([]*companion.RosterEntry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.roster, nil
}

type stubGoalLister struct {
	goals []*goal.Goal
	err   error
}

func (s *stubGoalLister) ListByLearner(_ context.Context, _, _ string) ([]*goal.Goal, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.goals, nil
}

// hatchedRoster builds a HATCHED (Stage-1 baby) roster entry — the default
// resolvable companion. rosterOf preserves slice order = created_at ASC.
func hatchedRoster(id string) *companion.RosterEntry {
	return &companion.RosterEntry{
		Instance: &companion.Instance{CompanionID: id, TenantID: "t1", OwnerGCID: "u1"},
		Growth:   &companion.GrowthSnapshot{Stage: 1, StageName: "baby"},
	}
}

// eggRoster builds a PRE-HATCH Stage-0 egg roster entry.
func eggRoster(id string) *companion.RosterEntry {
	return &companion.RosterEntry{
		Instance: &companion.Instance{CompanionID: id, TenantID: "t1", OwnerGCID: "u1"},
		Growth:   &companion.GrowthSnapshot{Stage: 0, StageName: "egg"},
	}
}

func rosterOf(entries ...*companion.RosterEntry) []*companion.RosterEntry { return entries }

func attachedGoal(goalID, companionID string, updatedAt time.Time) *goal.Goal {
	fid := companionID
	return &goal.Goal{GoalID: goalID, TenantID: "t1", LearnerGCID: "u1",
		AttachedCompanionID: &fid, UpdatedAt: updatedAt}
}

func TestResolve_GoalAttachedWinsOverRosterOrder(t *testing.T) {
	// Oldest roster row is fam-old; the learner's goal is bonded to
	// fam-new — attribution goes to fam-new (ADR-218 D7 replaces the
	// oldest-roster-row rule).
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-old"), hatchedRoster("fam-new"))},
		&stubGoalLister{goals: []*goal.Goal{
			attachedGoal("g1", "fam-new", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
		}},
	)
	got, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-new" {
		t.Errorf("resolved %q, want the goal-attached fam-new", got)
	}
}

func TestResolve_MostRecentSummonWins(t *testing.T) {
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-a"), hatchedRoster("fam-b"))},
		&stubGoalLister{goals: []*goal.Goal{
			attachedGoal("g-older", "fam-a", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
			attachedGoal("g-newer", "fam-b", time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)),
		}},
	)
	got, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-b" {
		t.Errorf("resolved %q, want most-recent summon fam-b", got)
	}
}

func TestResolve_NoAttachmentFallsBackToOldestRoster(t *testing.T) {
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-old"), hatchedRoster("fam-new"))},
		&stubGoalLister{goals: []*goal.Goal{
			{GoalID: "g-unbonded", TenantID: "t1", LearnerGCID: "u1"}, // no attachment
		}},
	)
	got, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-old" {
		t.Errorf("resolved %q, want the legacy oldest-roster fallback fam-old", got)
	}
}

func TestResolve_NoCompanionAtAll(t *testing.T) {
	r := subscribers.NewRepoCompanionResolver(&stubCompanionLister{}, &stubGoalLister{})
	if _, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1"); !errors.Is(err, subscribers.ErrNoCompanion) {
		t.Fatalf("err = %v, want ErrNoCompanion", err)
	}
}

func TestResolve_GoalListerErrorPropagates(t *testing.T) {
	// Fail-loud: a goals read error NACKs (Pub/Sub redelivers) rather than
	// silently mis-attributing to the roster fallback.
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-a"))},
		&stubGoalLister{err: errors.New("goals table on fire")},
	)
	if _, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1"); err == nil {
		t.Fatal("goal-lister error must propagate (redelivery), not silently fall back")
	}
}

func TestResolve_RosterErrorPropagates(t *testing.T) {
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{err: errors.New("roster on fire")},
		&stubGoalLister{},
	)
	if _, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1"); err == nil {
		t.Fatal("roster error must propagate, never a silent drop")
	}
}

func TestResolve_NilGoalListerLegacyBehaviour(t *testing.T) {
	// Constructed without a goal lister (defensive) the resolver keeps the
	// legacy oldest-roster semantics rather than erroring — EXP must flow.
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-old"), hatchedRoster("fam-new"))},
		nil,
	)
	got, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-old" {
		t.Errorf("resolved %q, want fam-old", got)
	}
}

func TestResolveForGoal_UsesThatGoalsAttachment(t *testing.T) {
	// Wire-ready goal-scoped resolution: when an upstream event carries a
	// goal context, THAT goal's bond wins even over a more recent summon.
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-a"), hatchedRoster("fam-b"))},
		&stubGoalLister{goals: []*goal.Goal{
			attachedGoal("g-target", "fam-a", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
			attachedGoal("g-other", "fam-b", time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)),
		}},
	)
	got, err := r.ResolveForGoal(context.Background(), "t1", "u1", "g-target")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-a" {
		t.Errorf("resolved %q, want g-target's fam-a", got)
	}

	// Unknown / unbonded goal falls through to the active-companion rule.
	got, err = r.ResolveForGoal(context.Background(), "t1", "u1", "g-missing")
	if err != nil {
		t.Fatalf("resolve fallthrough: %v", err)
	}
	if got != "fam-b" {
		t.Errorf("fallthrough resolved %q, want fam-b (most recent summon)", got)
	}
}

// ── F-I1.4 (ADR-228): pre-hatch eggs accrue nothing via the fallbacks ──────

func TestResolveForGoal_ExactAttachmentCreditsAnEgg(t *testing.T) {
	// An egg BONDED to the event's exact goal IS credited — this is the ONLY
	// path that credits a pre-hatch egg (bind-to-warm incubation).
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(eggRoster("fam-egg"))},
		&stubGoalLister{goals: []*goal.Goal{
			attachedGoal("g-egg", "fam-egg", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
		}},
	)
	got, err := r.ResolveForGoal(context.Background(), "t1", "u1", "g-egg")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-egg" {
		t.Errorf("exact-attachment resolved %q, want the bonded egg fam-egg", got)
	}
}

func TestResolve_UnboundEggAccruesNothing(t *testing.T) {
	// The learner owns ONLY an unbound Stage-0 egg. The terminal roster
	// fallback must NOT credit it — an unbound egg accrues nothing.
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(eggRoster("fam-egg"))},
		&stubGoalLister{},
	)
	if _, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1"); !errors.Is(err, subscribers.ErrNoCompanion) {
		t.Fatalf("unbound egg: err = %v, want ErrNoCompanion (no fallback credit)", err)
	}
}

func TestResolve_OldestRosterFallbackSkipsEggs(t *testing.T) {
	// Roster (created_at ASC) = [egg, hatched]. The terminal fallback must
	// skip the older egg and land on the first HATCHED companion.
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(eggRoster("fam-egg"), hatchedRoster("fam-hatched"))},
		&stubGoalLister{},
	)
	got, err := r.ResolveActiveCompanion(context.Background(), "t1", "u1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-hatched" {
		t.Errorf("resolved %q, want fam-hatched (egg skipped in fallback)", got)
	}
}

func TestResolve_MostRecentSummonSkipsEggAttachment(t *testing.T) {
	// The learner's MOST-recently-touched attachment is to an egg (bound to
	// its own goal g-egg); an older attachment is to a hatched companion. An
	// event for an UNRELATED unbound goal falls through to the active
	// companion — which must NOT be the egg (it only earns on g-egg's events).
	r := subscribers.NewRepoCompanionResolver(
		&stubCompanionLister{roster: rosterOf(hatchedRoster("fam-hatched"), eggRoster("fam-egg"))},
		&stubGoalLister{goals: []*goal.Goal{
			attachedGoal("g-hatched", "fam-hatched", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
			attachedGoal("g-egg", "fam-egg", time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)), // newer
		}},
	)
	got, err := r.ResolveForGoal(context.Background(), "t1", "u1", "g-unrelated")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fam-hatched" {
		t.Errorf("fallthrough resolved %q, want fam-hatched (egg's newer summon skipped)", got)
	}
}
