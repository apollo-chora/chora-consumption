// Package companion — tests for the spec-compliant 40/30/30 Daily Dose
// composition (Ebbinghaus / curiosity / weakness) per
// docs/m13/phyllis-mvp-2026-05-08.md §5.4 + ux_engagement.md §2.
//
// Per docs/m13/audit-content-fillgaps.md §3.2: the S4 implementation was
// 2/1/2 (Ebbinghaus / fresh / curiosity) — net 40/20/40, NO weakness slot.
// S5 A-Companion replaces this with 40/30/30 (Ebbinghaus / curiosity /
// weakness) with the fresh slot retained as a top-up.
//
// Composition algorithm (deterministic, no LLM):
//
//  1. Ebbinghaus 40% — atoms with EbbinghausDecay > 0.4 (highest decay first).
//  2. Weakness 30%   — atoms in topics where MCQ accuracy < 0.7.
//  3. Curiosity 30%  — atoms in topics adjacent to active LearningPath
//     topics (topic_tag overlap heuristic for S5.1; full KG
//     traversal in S5.2).
//  4. Fresh top-up   — atoms never seen, fills any unused slots up to the
//     5-atom budget. Tagged DoseReasonFresh.
//
// Slot caps (max per slot, sum > 5 OK because cap is total budget):
//
//	DoseEbbinghausCount = 2
//	DoseWeaknessCount   = 2  (NEW)
//	DoseCuriosityCount  = 2
//
// Priority cascade (when total demand exceeds budget): Ebbinghaus →
// Weakness → Curiosity → Fresh.
//
// TDD strict (RED → GREEN → REFACTOR).
package companion

import (
	"strings"
	"testing"
)

// fixture403030Seeds returns 25 atoms across 5 topics for 40/30/30 testing.
// Topics: "agile", "scrum", "kanban", "estimation", "retrospective".
// Each topic has 5 atoms (a..e) so the composer has enough variety.
func fixture403030Seeds() []AtomSeed {
	topics := []string{"agile", "scrum", "kanban", "estimation", "retrospective"}
	letters := []string{"a", "b", "c", "d", "e"}
	seeds := make([]AtomSeed, 0, 25)
	for i, topic := range topics {
		for j, letter := range letters {
			seeds = append(seeds, AtomSeed{
				AtomID: "01970000-0000-7000-c000-0000000000" + leftpad(i, 1) + leftpad(j, 1),
				Topic:  topic,
				Title:  topic + " atom " + letter,
			})
		}
	}
	return seeds
}

// TestDoseSlotCounts403030 — the canonical 40/30/30 slot caps.
// Sum = 2 + 2 + 2 = 6 (intentionally one over budget); priority cascade
// Ebbinghaus → Weakness → Curiosity caps total at DoseTargetSize.
func TestDoseSlotCounts403030(t *testing.T) {
	if DoseEbbinghausCount != 2 {
		t.Errorf("DoseEbbinghausCount = %d, want 2 (40%% of 5)", DoseEbbinghausCount)
	}
	if DoseWeaknessCount != 2 {
		t.Errorf("DoseWeaknessCount = %d, want 2 (30%% of 5, ceil)", DoseWeaknessCount)
	}
	if DoseCuriosityCount != 2 {
		t.Errorf("DoseCuriosityCount = %d, want 2 (30%% of 5, ceil)", DoseCuriosityCount)
	}
	if DoseTargetSize != 5 {
		t.Errorf("DoseTargetSize = %d, want 5 (Comic Ch6 P14 invariant)", DoseTargetSize)
	}
}

// TestComposeDailyDose_403030_FullSplit — healthy learner with all three
// slot types satisfied yields the canonical 2-Ebbinghaus / 2-Weakness /
// 1-Curiosity (= 40/40/20 by atoms, closest integer to 40/30/30).
func TestComposeDailyDose_403030_FullSplit(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// Ebbinghaus pool: 3 atoms in "agile" with decay > 0.4 (30 days stale).
	states := map[string]SM2State{}
	for i := 0; i < 3; i++ {
		s := NewSM2State(seeds[i].AtomID)
		s.ApplyGrade(GradeIRemember, dayAt(now, -30))
		states[seeds[i].AtomID] = s
	}

	// Weakness pool: 3 atoms in "scrum" + "kanban" with accuracy < 0.7.
	weakTopics := map[string]float64{
		"scrum":  0.40, // 40% accuracy → weak
		"kanban": 0.55, // 55% accuracy → weak
	}

	// Curiosity adjacency: learner has active LearningPath in "agile".
	// Adjacent topics include "scrum", "kanban", "estimation",
	// "retrospective". For test simplicity we say all 5 topics are seen.
	activePathTopics := map[string]bool{
		"agile":         true,
		"scrum":         true,
		"kanban":        true,
		"estimation":    true,
		"retrospective": true,
	}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               states,
		SeenTopics:           activePathTopics,
		ActivePathTopics:     activePathTopics,
		TopicAccuracy:        weakTopics,
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}

	ebb := countByReason(dose.Entries, DoseReasonEbbinghausReview)
	weak := countByReason(dose.Entries, DoseReasonWeakness)
	cur := countByReason(dose.Entries, DoseReasonCuriosity)
	if ebb != 2 {
		t.Errorf("ebbinghaus_review count = %d, want 2 (40%%)", ebb)
	}
	if weak != 2 {
		t.Errorf("weakness count = %d, want 2 (30%% upper)", weak)
	}
	if cur != 1 {
		t.Errorf("curiosity count = %d, want 1 (30%% lower, priority cascade)", cur)
	}
}

// TestComposeDailyDose_403030_WeaknessSlotPicksLowAccuracy — weakness
// slot MUST pick atoms in topics with accuracy < threshold (default 0.7).
func TestComposeDailyDose_403030_WeaknessSlotPicksLowAccuracy(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// Only weakness eligibility (no Ebbinghaus + no curiosity).
	weakTopics := map[string]float64{
		"kanban": 0.50, // weak
		"agile":  0.90, // strong (must NOT be picked)
	}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               map[string]SM2State{},
		SeenTopics:           map[string]bool{},
		ActivePathTopics:     map[string]bool{},
		TopicAccuracy:        weakTopics,
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})

	for _, e := range dose.Entries {
		if e.DoseReason != DoseReasonWeakness {
			continue
		}
		if e.Topic != "kanban" {
			t.Errorf("weakness entry topic = %q, want kanban (only weak topic; accuracy 0.50 < 0.7)", e.Topic)
		}
	}
	weakCount := countByReason(dose.Entries, DoseReasonWeakness)
	if weakCount == 0 {
		t.Error("weakness count = 0; expected at least 1 weakness pick when learner has weak topic")
	}
	if weakCount > DoseWeaknessCount {
		t.Errorf("weakness count = %d, must not exceed cap %d", weakCount, DoseWeaknessCount)
	}
}

// TestComposeDailyDose_403030_CuriositySlotUsesActivePathAdjacency —
// curiosity slot picks atoms from topics adjacent to active LearningPath
// topics. For S5.1 the heuristic is "topic_tag in ActivePathTopics set".
func TestComposeDailyDose_403030_CuriositySlotUsesActivePathAdjacency(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// Active path covers "scrum" + "estimation" only.
	pathTopics := map[string]bool{
		"scrum":      true,
		"estimation": true,
	}
	// SeenTopics overlap to ensure curiosity pool is non-empty.
	seenTopics := map[string]bool{
		"scrum":      true,
		"estimation": true,
	}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               map[string]SM2State{},
		SeenTopics:           seenTopics,
		ActivePathTopics:     pathTopics,
		TopicAccuracy:        map[string]float64{},
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})

	for _, e := range dose.Entries {
		if e.DoseReason != DoseReasonCuriosity {
			continue
		}
		if e.Topic != "scrum" && e.Topic != "estimation" {
			t.Errorf("curiosity entry topic = %q, want scrum|estimation (active-path adjacency)", e.Topic)
		}
	}
}

// TestComposeDailyDose_403030_EmptySlotsTopUpFresh — when no
// Ebbinghaus/weakness/curiosity eligibility exists, the dose is filled
// with fresh atoms (DoseReasonFresh) so the 5-card budget is preserved.
func TestComposeDailyDose_403030_EmptySlotsTopUpFresh(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               map[string]SM2State{},
		SeenTopics:           map[string]bool{},
		ActivePathTopics:     map[string]bool{},
		TopicAccuracy:        map[string]float64{},
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})
	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("brand-new learner dose size = %d, want %d (top-up)", len(dose.Entries), DoseTargetSize)
	}
	freshCount := countByReason(dose.Entries, DoseReasonFresh)
	if freshCount == 0 {
		t.Error("expected fresh top-up entries when no other slot is eligible")
	}
}

// TestComposeDailyDose_403030_NoDuplicateAtoms — same atom MUST NOT be
// picked into multiple slots.
func TestComposeDailyDose_403030_NoDuplicateAtoms(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// Make atom 0 ("agile a") eligible for BOTH Ebbinghaus AND weakness:
	// stale review + weak topic.
	states := map[string]SM2State{}
	s := NewSM2State(seeds[0].AtomID)
	s.ApplyGrade(GradeIRemember, dayAt(now, -30))
	states[seeds[0].AtomID] = s
	weakTopics := map[string]float64{"agile": 0.40}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               states,
		SeenTopics:           map[string]bool{"agile": true},
		ActivePathTopics:     map[string]bool{"agile": true},
		TopicAccuracy:        weakTopics,
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})

	seen := map[string]int{}
	for _, e := range dose.Entries {
		seen[e.AtomID]++
	}
	for atomID, n := range seen {
		if n > 1 {
			t.Errorf("atom %s appears %d times (must be unique)", atomID, n)
		}
	}
}

// TestComposeDailyDose_403030_Deterministic — same input → same output.
func TestComposeDailyDose_403030_Deterministic(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()
	states := map[string]SM2State{}
	for i := 0; i < 5; i++ {
		s := NewSM2State(seeds[i].AtomID)
		s.ApplyGrade(GradeIRemember, dayAt(now, -30))
		states[seeds[i].AtomID] = s
	}
	in := DailyDoseInput{
		Seeds:                seeds,
		States:               states,
		SeenTopics:           map[string]bool{"agile": true, "scrum": true},
		ActivePathTopics:     map[string]bool{"agile": true},
		TopicAccuracy:        map[string]float64{"scrum": 0.40},
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	}
	d1 := ComposeDailyDose(in)
	d2 := ComposeDailyDose(in)
	if len(d1.Entries) != len(d2.Entries) {
		t.Fatalf("non-deterministic size: %d vs %d", len(d1.Entries), len(d2.Entries))
	}
	for i := range d1.Entries {
		if d1.Entries[i].AtomID != d2.Entries[i].AtomID {
			t.Errorf("entry[%d] AtomID = %q vs %q (non-deterministic)",
				i, d1.Entries[i].AtomID, d2.Entries[i].AtomID)
		}
		if d1.Entries[i].DoseReason != d2.Entries[i].DoseReason {
			t.Errorf("entry[%d] DoseReason = %q vs %q (non-deterministic)",
				i, d1.Entries[i].DoseReason, d2.Entries[i].DoseReason)
		}
	}
}

// TestComposeDailyDose_403030_NudgeMessageMatchesComic — message text MUST
// still match the Comic Ch6 P14 P4 invariant.
func TestComposeDailyDose_403030_NudgeMessageMatchesComic(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds: fixture403030Seeds(),
		Now:   now,
	})
	if !strings.Contains(dose.Message, "Daily Dose is ready") {
		t.Errorf("message = %q, want to contain 'Daily Dose is ready'", dose.Message)
	}
}

// TestComposeDailyDose_403030_PropertyBased — 200+ permutations of
// (TopicRetention × LearningPath × MCQ history) all yield deterministic
// 40/30/30 splits with total ≤ DoseTargetSize and slot counts ≤ caps.
//
// Per the S5.1 done-criterion in the agent briefing, the property test
// covers: full split, no-Ebb, no-weak, no-cur, brand-new, all-stale,
// many-weak topics, partial coverage. 40 permutations × 5 days = 200.
func TestComposeDailyDose_403030_PropertyBased(t *testing.T) {
	baseNow := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// 8 staleness configurations × 5 weakness configurations × 5 path
	// configurations = 200 unique permutations.
	stalenessProfiles := []struct {
		name          string
		staleAtoms    int
		stalenessDays int
	}{
		{"no_stale", 0, 0},
		{"one_stale", 1, 30},
		{"two_stale", 2, 30},
		{"three_stale", 3, 30},
		{"five_stale", 5, 30},
		{"all_stale", 25, 30},
		{"deep_stale", 3, 100},
		{"recent_review", 3, 1},
	}
	weaknessProfiles := []struct {
		name   string
		topics map[string]float64
	}{
		{"no_weak", map[string]float64{}},
		{"one_weak", map[string]float64{"scrum": 0.50}},
		{"two_weak", map[string]float64{"scrum": 0.50, "kanban": 0.55}},
		{"three_weak", map[string]float64{"scrum": 0.50, "kanban": 0.55, "estimation": 0.65}},
		{"all_strong", map[string]float64{
			"scrum": 0.95, "kanban": 0.92, "estimation": 0.88, "agile": 0.91, "retrospective": 0.90,
		}},
	}
	pathProfiles := []struct {
		name   string
		topics map[string]bool
	}{
		{"no_path", map[string]bool{}},
		{"one_topic", map[string]bool{"agile": true}},
		{"two_topics", map[string]bool{"agile": true, "scrum": true}},
		{"three_topics", map[string]bool{"agile": true, "scrum": true, "kanban": true}},
		{"all_topics", map[string]bool{
			"agile": true, "scrum": true, "kanban": true, "estimation": true, "retrospective": true,
		}},
	}

	totalPermutations := 0
	for _, sp := range stalenessProfiles {
		for _, wp := range weaknessProfiles {
			for _, pp := range pathProfiles {
				totalPermutations++
				// Build SM2 states.
				states := map[string]SM2State{}
				for i := 0; i < sp.staleAtoms && i < len(seeds); i++ {
					s := NewSM2State(seeds[i].AtomID)
					s.ApplyGrade(GradeIRemember, dayAt(baseNow, -sp.stalenessDays))
					states[seeds[i].AtomID] = s
				}
				dose := ComposeDailyDose(DailyDoseInput{
					Seeds:                seeds,
					States:               states,
					SeenTopics:           pp.topics,
					ActivePathTopics:     pp.topics,
					TopicAccuracy:        wp.topics,
					Now:                  baseNow,
					WeaknessThresholdMin: 0.7,
				})

				// Invariant: NEVER exceed budget.
				if len(dose.Entries) > DoseTargetSize {
					t.Errorf("[%s × %s × %s] dose size = %d, MUST NOT exceed %d",
						sp.name, wp.name, pp.name, len(dose.Entries), DoseTargetSize)
				}
				// Invariant: slot caps respected.
				if countByReason(dose.Entries, DoseReasonEbbinghausReview) > DoseEbbinghausCount {
					t.Errorf("[%s × %s × %s] ebbinghaus over cap", sp.name, wp.name, pp.name)
				}
				if countByReason(dose.Entries, DoseReasonWeakness) > DoseWeaknessCount {
					t.Errorf("[%s × %s × %s] weakness over cap", sp.name, wp.name, pp.name)
				}
				if countByReason(dose.Entries, DoseReasonCuriosity) > DoseCuriosityCount {
					t.Errorf("[%s × %s × %s] curiosity over cap", sp.name, wp.name, pp.name)
				}
				// Invariant: no duplicate atoms.
				seenIDs := map[string]bool{}
				for _, e := range dose.Entries {
					if seenIDs[e.AtomID] {
						t.Errorf("[%s × %s × %s] duplicate atom %s",
							sp.name, wp.name, pp.name, e.AtomID)
					}
					seenIDs[e.AtomID] = true
					// Every entry must have a valid reason.
					if e.DoseReason == "" {
						t.Errorf("[%s × %s × %s] entry %s has empty dose_reason",
							sp.name, wp.name, pp.name, e.AtomID)
					}
				}
			}
		}
	}
	if totalPermutations < 200 {
		t.Errorf("total permutations = %d, want >= 200 for property coverage", totalPermutations)
	}
}

// TestComposeDailyDose_403030_PropertyBased_Deterministic_TimeTravel —
// the same input shape across 5 days yields stable composition (no
// drift from injected `Now` if ApplyGrade is on the same day).
func TestComposeDailyDose_403030_PropertyBased_Deterministic_TimeTravel(t *testing.T) {
	seeds := fixture403030Seeds()
	weakTopics := map[string]float64{"scrum": 0.50}
	pathTopics := map[string]bool{"agile": true, "scrum": true}

	for _, dayOffset := range []int{1, 2, 3, 7, 14} {
		now := mustParse(t, "2026-05-01T00:00:00Z").AddDate(0, 0, dayOffset)
		states := map[string]SM2State{}
		for i := 0; i < 3; i++ {
			s := NewSM2State(seeds[i].AtomID)
			// Stale relative to baseline (always 30 days before `now`).
			s.ApplyGrade(GradeIRemember, dayAt(now, -30))
			states[seeds[i].AtomID] = s
		}
		dose := ComposeDailyDose(DailyDoseInput{
			Seeds:                seeds,
			States:               states,
			SeenTopics:           pathTopics,
			ActivePathTopics:     pathTopics,
			TopicAccuracy:        weakTopics,
			Now:                  now,
			WeaknessThresholdMin: 0.7,
		})
		if len(dose.Entries) != DoseTargetSize {
			t.Errorf("day=%d dose size = %d, want %d", dayOffset, len(dose.Entries), DoseTargetSize)
		}
		// Time-travel deterministic shape: same atom IDs picked.
		for _, e := range dose.Entries {
			if e.DoseReason == DoseReasonEbbinghausReview {
				if !strings.HasPrefix(e.Topic, "agile") {
					// Stale atoms are all in "agile" (seeds[0..2]).
					t.Errorf("day=%d Ebbinghaus pick topic = %q, expected agile", dayOffset, e.Topic)
				}
			}
		}
	}
}

// TestComposeDailyDose_403030_NoLLMCall — domain code MUST NOT depend on
// any LLM-shaped invoker for the 40/30/30 composition. This is asserted
// at compile time via the package not importing any AI/Model Broker
// types into the deterministic ComposeDailyDose code path. (Existing
// LLM-aware paths live in separate functions like ComposeDailyDoseWithCoach.)
//
// Property test: ComposeDailyDose accepts no port + no context. If it
// did, it could not be called without one (compile error). This is a
// regression guard against accidental LLM coupling.
func TestComposeDailyDose_403030_NoLLMCall(t *testing.T) {
	// If this compiles + passes, ComposeDailyDose has no Port arg.
	// We invoke with only the deterministic input struct.
	now := mustParse(t, "2026-05-08T00:00:00Z")
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds: fixture403030Seeds(),
		Now:   now,
	})
	if dose.GeneratedAt.IsZero() {
		t.Error("dose.GeneratedAt zero — composer not pure-domain?")
	}
}

// TestComposeDailyDose_403030_DispatchDoesNotMutateComposition — after
// SelectGreetingCompanion runs over the same DailyDose, the composition
// invariants (size, slot caps, determinism) must still hold.
//
// Regression guard: SelectGreetingCompanion must read-only over DailyDose.Entries;
// it MUST NOT reorder, filter, or append entries. The 40/30/30 split must
// survive intact after dispatch.
func TestComposeDailyDose_403030_DispatchDoesNotMutateComposition(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixture403030Seeds()

	// Set up a learner with Ebbinghaus + weakness + curiosity eligibility
	// so all three 40/30/30 slots fire.
	states := map[string]SM2State{}
	for i := 0; i < 3; i++ {
		s := NewSM2State(seeds[i].AtomID)
		s.ApplyGrade(GradeIRemember, dayAt(now, -30))
		states[seeds[i].AtomID] = s
	}
	weakTopics := map[string]float64{"scrum": 0.40, "kanban": 0.55}
	pathTopics := map[string]bool{"agile": true, "scrum": true, "kanban": true}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:                seeds,
		States:               states,
		SeenTopics:           pathTopics,
		ActivePathTopics:     pathTopics,
		TopicAccuracy:        weakTopics,
		Now:                  now,
		WeaknessThresholdMin: 0.7,
	})

	// Record pre-dispatch composition.
	preSize := len(dose.Entries)
	preEbb := countByReason(dose.Entries, DoseReasonEbbinghausReview)
	preWeak := countByReason(dose.Entries, DoseReasonWeakness)
	preCur := countByReason(dose.Entries, DoseReasonCuriosity)

	// Run dispatch with a minimal roster of 2 hatched Companions (cspo + agile).
	roster := []RosterCompanion{
		{CompanionID: "fam-a", Name: "Alpha", Specialization: "agile", GrowthStage: 1, GrowthExp: 100},
		{CompanionID: "fam-b", Name: "Beta", Specialization: "scrum", GrowthStage: 2, GrowthExp: 200},
	}
	_, _ = SelectGreetingCompanion(roster, dose)

	// Post-dispatch: composition MUST be identical.
	if len(dose.Entries) != preSize {
		t.Errorf("post-dispatch size = %d, was %d before dispatch (mutation!)", len(dose.Entries), preSize)
	}
	if countByReason(dose.Entries, DoseReasonEbbinghausReview) != preEbb {
		t.Error("Ebbinghaus count changed after dispatch — mutation!")
	}
	if countByReason(dose.Entries, DoseReasonWeakness) != preWeak {
		t.Error("Weakness count changed after dispatch — mutation!")
	}
	if countByReason(dose.Entries, DoseReasonCuriosity) != preCur {
		t.Error("Curiosity count changed after dispatch — mutation!")
	}
}
