// dose_goal_scope_test.go - WS-3: goal-scoped Daily Dose (the owner ask "in a
// goal, if the user selects daily dose, the dose relating to the goal should
// appear first").
//
// The contract these tests pin, in the order the design argues it:
//
//  1. OPTIONAL - a nil GoalScope composes byte-identically to the pre-scope
//     composer. This is the backward-compatibility proof.
//  2. BIAS, NOT FILTER - a goal with thin (or zero) material still composes a
//     full DoseTargetSize dose from the learner-wide universe.
//  3. SM-2 IS NOT OVERRIDDEN - goal affinity reorders WITHIN the pool each slot
//     was already eligible to draw from. It never promotes a not-due atom into
//     the Ebbinghaus review slot, and it never drops a due one from the dose.
//  4. THE CAMPAIGN AXIS IS UNTOUCHED - campaign picks keep their count and their
//     march metadata whether or not a goal scope is present.
//  5. THE WEAKNESS SLOT IS UNTOUCHED - ADR-224's subject spread is an explicit
//     anti-monoculture invariant and a goal is roughly a subject, so biasing the
//     weakness slot toward one goal would silently repeal it.
//
// TDD strict (RED first): every test below fails to COMPILE before
// dose_goal_scope.go lands (DailyDoseInput has no GoalScope field), which is the
// RED state for a new-field feature.
package companion

import (
	"sort"
	"testing"
	"time"
)

var goalScopeNow = time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)

// goalSeeds is a two-subject universe: three "fractions" atoms (the goal's
// material) and four "photosynthesis" atoms (everything else). AtomIDs are
// ordered so the deterministic atom_id-ascending fills are predictable.
func goalSeeds() []AtomSeed {
	return []AtomSeed{
		{AtomID: "atom-01", Topic: "photosynthesis", Title: "Chloroplasts"},
		{AtomID: "atom-02", Topic: "fractions", Title: "Common denominators"},
		{AtomID: "atom-03", Topic: "photosynthesis", Title: "Light reactions"},
		{AtomID: "atom-04", Topic: "fractions", Title: "Mixed numbers"},
		{AtomID: "atom-05", Topic: "photosynthesis", Title: "Calvin cycle"},
		{AtomID: "atom-06", Topic: "fractions", Title: "Improper fractions"},
		{AtomID: "atom-07", Topic: "photosynthesis", Title: "Stomata"},
	}
}

// fractionsScope is the goal scope for a "Fractions" goal: it names one atom
// explicitly (the precise, concept-bound signal) and one topic slug (the broad
// signal).
func fractionsScope() *GoalScopeInput {
	return &GoalScopeInput{
		GoalID:       "goal-fractions",
		AtomIDs:      []string{"atom-06"},
		ConceptSlugs: []string{"fractions"},
	}
}

func entryIDs(d DailyDose) []string {
	out := make([]string, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, e.AtomID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGoalScope_NilScope_ComposesExactlyAsBefore is proof #1: with no goal the
// composer must produce the pre-scope result, entry for entry, reason for
// reason. The expected list is the CURRENT algorithm's output (curiosity over
// the active-path topics, then fresh top-up by atom_id) written out literally so
// any drift in the unscoped path fails here rather than silently shipping.
func TestGoalScope_NilScope_ComposesExactlyAsBefore(t *testing.T) {
	in := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
	}
	if in.GoalScope != nil {
		t.Fatalf("GoalScope default = %v, want nil (the unscoped dose is the default)", in.GoalScope)
	}

	got := ComposeDailyDose(in)
	if len(got.Entries) != DoseTargetSize {
		t.Fatalf("unscoped dose size = %d, want %d", len(got.Entries), DoseTargetSize)
	}
	want := []string{"atom-01", "atom-02", "atom-03", "atom-04", "atom-05"}
	if !equalStrings(entryIDs(got), want) {
		t.Fatalf("unscoped dose = %v, want %v (atom_id-ascending curiosity fill, unchanged)", entryIDs(got), want)
	}
	for i, e := range got.Entries {
		if i < DoseCuriosityCount {
			if e.DoseReason != DoseReasonCuriosity {
				t.Fatalf("entry %d reason = %q, want %q", i, e.DoseReason, DoseReasonCuriosity)
			}
			continue
		}
		if e.DoseReason != DoseReasonFresh {
			t.Fatalf("entry %d reason = %q, want %q", i, e.DoseReason, DoseReasonFresh)
		}
	}
}

// TestGoalScope_GoalMaterialLeadsTheDose is the owner ask: with a goal scope the
// goal's atoms occupy the FRONT of the dose.
func TestGoalScope_GoalMaterialLeadsTheDose(t *testing.T) {
	in := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
		GoalScope:        fractionsScope(),
	}

	got := ComposeDailyDose(in)
	if len(got.Entries) != DoseTargetSize {
		t.Fatalf("scoped dose size = %d, want %d", len(got.Entries), DoseTargetSize)
	}
	// All three fractions atoms are in the universe, so all three must lead.
	lead := entryIDs(got)[:3]
	wantLead := map[string]bool{"atom-02": true, "atom-04": true, "atom-06": true}
	for i, id := range lead {
		if !wantLead[id] {
			t.Fatalf("dose position %d = %q, want a fractions atom (goal material leads); full dose = %v",
				i, id, entryIDs(got))
		}
	}
	// The explicitly bound atom outranks a mere topic match.
	if lead[0] != "atom-06" {
		t.Fatalf("dose position 0 = %q, want %q (concept-bound atom outranks a topic-slug match)", lead[0], "atom-06")
	}
}

// TestGoalScope_ThinGoal_DoesNotStuntTheDose is proof #2: one matching atom must
// not shrink the dose. The goal atom leads; the rest of the budget fills from the
// learner-wide universe.
func TestGoalScope_ThinGoal_DoesNotStuntTheDose(t *testing.T) {
	in := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
		GoalScope: &GoalScopeInput{
			GoalID:  "goal-thin",
			AtomIDs: []string{"atom-05"}, // exactly ONE atom, no topic slugs
		},
	}

	got := ComposeDailyDose(in)
	if len(got.Entries) != DoseTargetSize {
		t.Fatalf("thin-goal dose size = %d, want %d (degrade to the learner-wide universe, never stunt)",
			len(got.Entries), DoseTargetSize)
	}
	if entryIDs(got)[0] != "atom-05" {
		t.Fatalf("thin-goal dose[0] = %q, want %q (the one goal atom still leads)", entryIDs(got)[0], "atom-05")
	}
}

// TestGoalScope_GoalWithNoMaterial_DegeneratesToUnscoped is the extreme of proof
// #2: a goal whose material is absent from the universe entirely must compose
// the unscoped dose, unchanged.
func TestGoalScope_GoalWithNoMaterial_DegeneratesToUnscoped(t *testing.T) {
	base := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
	}
	scoped := base
	scoped.GoalScope = &GoalScopeInput{
		GoalID:       "goal-empty",
		AtomIDs:      []string{"atom-does-not-exist"},
		ConceptSlugs: []string{"astrophysics"},
	}

	unscopedDose := ComposeDailyDose(base)
	scopedDose := ComposeDailyDose(scoped)
	if !equalStrings(entryIDs(unscopedDose), entryIDs(scopedDose)) {
		t.Fatalf("empty-goal dose = %v, want the unscoped %v (a goal with no material changes nothing)",
			entryIDs(scopedDose), entryIDs(unscopedDose))
	}
	if len(scopedDose.Entries) != DoseTargetSize {
		t.Fatalf("empty-goal dose size = %d, want %d", len(scopedDose.Entries), DoseTargetSize)
	}
}

// TestGoalScope_DoesNotPromoteANotDueAtomIntoTheReviewSlot is proof #3a: the
// Ebbinghaus slot's ELIGIBILITY rule (decay > DoseDecayThreshold) is untouched.
// A freshly-reviewed goal atom is not due, so it must never be tagged
// ebbinghaus_review no matter how relevant it is to the goal.
func TestGoalScope_DoesNotPromoteANotDueAtomIntoTheReviewSlot(t *testing.T) {
	// atom-06 (goal material) was reviewed moments ago: not due.
	// atom-01 (off-goal) is long overdue: due.
	states := map[string]SM2State{
		"atom-06": {Repetitions: 3, IntervalDays: 30, EasinessFactor: 2.5, LastReviewedAt: goalScopeNow.Add(-1 * time.Hour)},
		"atom-01": {Repetitions: 1, IntervalDays: 1, EasinessFactor: 2.5, LastReviewedAt: goalScopeNow.Add(-40 * 24 * time.Hour)},
	}
	in := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           states,
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
		GoalScope:        fractionsScope(),
	}

	got := ComposeDailyDose(in)
	for _, e := range got.Entries {
		if e.AtomID == "atom-06" && e.DoseReason == DoseReasonEbbinghausReview {
			t.Fatalf("atom-06 served as %q, want NOT a review pick (it is not due; goal affinity must not override the forgetting curve)",
				e.DoseReason)
		}
	}
	// The genuinely-due off-goal atom must still be in the dose as a review pick.
	var sawDueReview bool
	for _, e := range got.Entries {
		if e.AtomID == "atom-01" && e.DoseReason == DoseReasonEbbinghausReview {
			sawDueReview = true
		}
	}
	if !sawDueReview {
		t.Fatalf("dose = %v, want the overdue off-goal atom-01 still served as a review pick (goal scoping never drops what is due)",
			entryIDs(got))
	}
}

// TestGoalScope_ReordersWithinTheDuePoolOnly is proof #3b: among atoms that are
// ALL genuinely due, goal material is SELECTED into the capped review slot first.
//
// The slot cap is what makes this a selection test rather than a display test.
// Three atoms are due but only DoseEbbinghausCount fit: the two off-goal atoms
// are more decayed, so a decay-only rule leaves the goal's due atom out of the
// review slot entirely. The final presentation reorder cannot rescue an atom that
// was never picked, so this fails if the universe partition is removed.
func TestGoalScope_ReordersWithinTheDuePoolOnly(t *testing.T) {
	due := func(daysAgo int) SM2State {
		return SM2State{
			Repetitions: 1, IntervalDays: 1, EasinessFactor: 2.5,
			LastReviewedAt: goalScopeNow.Add(-time.Duration(daysAgo) * 24 * time.Hour),
		}
	}
	states := map[string]SM2State{
		"atom-01": due(60), // off-goal, most decayed
		"atom-03": due(50), // off-goal, next most decayed
		"atom-02": due(5),  // GOAL material, least decayed but still due
	}
	base := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           states,
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
	}
	scoped := base
	scoped.GoalScope = fractionsScope()

	reviewIDs := func(d DailyDose) []string {
		out := []string{}
		for _, e := range d.Entries {
			if e.DoseReason == DoseReasonEbbinghausReview {
				out = append(out, e.AtomID)
			}
		}
		return out
	}
	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	// Positive control on the UNSCOPED path: decay alone fills the slot with the
	// two off-goal atoms and the goal's due atom does not make the cut.
	unscoped := reviewIDs(ComposeDailyDose(base))
	if !equalStrings(unscoped, []string{"atom-01", "atom-03"}) {
		t.Fatalf("unscoped review picks = %v, want [atom-01 atom-03] (most-decayed first)", unscoped)
	}
	if contains(unscoped, "atom-02") {
		t.Fatalf("unscoped review picks = %v, want the goal atom EXCLUDED (else this test cannot detect the bias)", unscoped)
	}

	got := reviewIDs(ComposeDailyDose(scoped))
	if len(got) != DoseEbbinghausCount {
		t.Fatalf("scoped review picks = %v, want %d (the slot cap is unchanged)", got, DoseEbbinghausCount)
	}
	if !contains(got, "atom-02") {
		t.Fatalf("scoped review picks = %v, want the goal's DUE atom selected into the capped slot", got)
	}
	if !contains(got, "atom-01") {
		t.Fatalf("scoped review picks = %v, want the most-decayed atom still served (the curve keeps the remaining slot)", got)
	}
}

// TestGoalScope_LeavesTheCampaignSlotIntact is proof #4. The campaign march is
// elected by a seeded daily RNG shared with the ANSWER side; a goal scope must
// not re-elect it, drop it, or strip its march metadata, even when the elected
// campaign belongs to a DIFFERENT goal than the one being browsed.
func TestGoalScope_LeavesTheCampaignSlotIntact(t *testing.T) {
	campaign := &CampaignInput{
		GoalID:           "goal-photosynthesis", // NOT the browsed goal
		ConceptID:        "concept-photo",
		ConceptKey:       "photosynthesis",
		ConceptLabel:     "Photosynthesis",
		Rung:             2,
		CandidateAtomIDs: []string{"atom-01", "atom-03"},
	}
	base := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
		ComposerV2:       true,
		Campaign:         campaign,
	}
	scoped := base
	scoped.GoalScope = fractionsScope()

	unscoped := ComposeDailyDose(base)
	got := ComposeDailyDose(scoped)

	countCampaign := func(d DailyDose) int {
		n := 0
		for _, e := range d.Entries {
			if e.DoseReason == DoseReasonCampaign {
				n++
			}
		}
		return n
	}
	if countCampaign(got) != countCampaign(unscoped) {
		t.Fatalf("scoped campaign picks = %d, want the unscoped %d (the goal scope must not fight the campaign axis)",
			countCampaign(got), countCampaign(unscoped))
	}
	if countCampaign(got) != DoseCampaignCount {
		t.Fatalf("campaign picks = %d, want %d", countCampaign(got), DoseCampaignCount)
	}
	for _, e := range got.Entries {
		if e.DoseReason != DoseReasonCampaign {
			continue
		}
		if e.CampaignGoalID != campaign.GoalID || e.CampaignConceptID != campaign.ConceptID || e.CampaignRung != campaign.Rung {
			t.Fatalf("campaign entry %q lost its march metadata: %+v", e.AtomID, e)
		}
	}
}

// TestGoalScope_LeavesTheWeaknessSlotIntact is proof #5. The weakness slot is
// owned by Growth-Edge shakiness (v1) and ADR-224's subject spread (v2). A goal
// is roughly a subject, so goal-biasing this slot would repeal the very
// invariant ADR-224 exists to enforce. The picks must be identical either way.
func TestGoalScope_LeavesTheWeaknessSlotIntact(t *testing.T) {
	edges := []GrowthEdgeInput{
		{EdgeID: "e1", ConceptKey: "photosynthesis", Category: "Science", Strength: 0.95, CachedDrillAtomIDs: []string{"atom-01"}},
		{EdgeID: "e2", ConceptKey: "fractions", Category: "Mathematics", Strength: 0.60, CachedDrillAtomIDs: []string{"atom-02"}},
	}
	base := DailyDoseInput{
		Seeds:            goalSeeds(),
		States:           map[string]SM2State{},
		ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
		Now:              goalScopeNow,
		GrowthEdges:      edges,
	}
	scoped := base
	scoped.GoalScope = fractionsScope()

	// Compare the SELECTION, not the position: the goal scope's final pass
	// reorders the whole dose for presentation, so a weakness pick may legitimately
	// move up the card list. What must not change is WHICH atoms the slot chose.
	weaknessIDs := func(d DailyDose) []string {
		out := []string{}
		for _, e := range d.Entries {
			if e.DoseReason == DoseReasonWeakness {
				out = append(out, e.AtomID)
			}
		}
		sort.Strings(out)
		return out
	}
	unscoped := weaknessIDs(ComposeDailyDose(base))
	got := weaknessIDs(ComposeDailyDose(scoped))
	if !equalStrings(unscoped, got) {
		t.Fatalf("scoped weakness picks = %v, want the unscoped %v (ADR-224 subject spread is not a goal-scoping surface)",
			got, unscoped)
	}
	if len(unscoped) == 0 {
		t.Fatalf("weakness picks = %v, want a non-empty control (an all-empty comparison proves nothing)", unscoped)
	}
}

// TestGoalScope_IsDeterministic - the same scoped input composes the same dose.
func TestGoalScope_IsDeterministic(t *testing.T) {
	build := func() DailyDoseInput {
		return DailyDoseInput{
			Seeds:            goalSeeds(),
			States:           map[string]SM2State{},
			ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
			Now:              goalScopeNow,
			GoalScope:        fractionsScope(),
		}
	}
	first := entryIDs(ComposeDailyDose(build()))
	second := entryIDs(ComposeDailyDose(build()))
	if !equalStrings(first, second) {
		t.Fatalf("scoped dose is not deterministic: %v then %v", first, second)
	}
}

// TestGoalScope_WorksWithComposerV2OffAndOn is the darkness proof.
//
// Composer v2 (ADR-224, DOSE_COMPOSER_V2_ENABLED) is OFF in production. If goal
// scoping were expressed inside v2's weighted samplers it would inherit that
// darkness and never run for a real learner. The universe partition sits OUTSIDE
// the v1/v2 branch and delegates to whichever selector is active, so the goal's
// material leads in BOTH configurations. Asserting both is what stops a future
// refactor from quietly moving the bias inside the v2-only path.
func TestGoalScope_WorksWithComposerV2OffAndOn(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		in := DailyDoseInput{
			Seeds:            goalSeeds(),
			States:           map[string]SM2State{},
			ActivePathTopics: map[string]bool{"photosynthesis": true, "fractions": true},
			Now:              goalScopeNow,
			GoalScope:        fractionsScope(),
			ComposerV2:       v2,
			RandSeed:         DoseSeed("2026-07-21", "learner-1"),
		}
		got := ComposeDailyDose(in)
		if len(got.Entries) != DoseTargetSize {
			t.Fatalf("ComposerV2=%v: dose size = %d, want %d", v2, len(got.Entries), DoseTargetSize)
		}
		if got.Entries[0].AtomID != "atom-06" {
			t.Fatalf("ComposerV2=%v: dose[0] = %q, want the concept-bound goal atom %q. Goal scoping must NOT depend on the v2 flag (it is dark in prod)",
				v2, got.Entries[0].AtomID, "atom-06")
		}
		lead := entryIDs(got)[:3]
		for i, id := range lead {
			if fractionsScope().affinity(id, topicOf(goalSeeds(), id)) == goalAffinityNone {
				t.Fatalf("ComposerV2=%v: dose position %d = %q is not goal material; dose = %v", v2, i, id, entryIDs(got))
			}
		}
	}
}

// topicOf resolves a seed's topic by atom id (test helper).
func topicOf(seeds []AtomSeed, atomID string) string {
	for _, s := range seeds {
		if s.AtomID == atomID {
			return s.Topic
		}
	}
	return ""
}
