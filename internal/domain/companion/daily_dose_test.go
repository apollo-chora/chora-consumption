package companion

import (
	"strings"
	"testing"
	"time"
)

// dayAt is a small helper for relative-day timestamps in tests.
func dayAt(now time.Time, d int) time.Time {
	return now.AddDate(0, 0, d)
}

// fixtureSeeds returns 20 atoms across 5 topics (Comic Ch6 P14 spec invariant).
func fixtureSeeds() []AtomSeed {
	topics := []string{"agile", "scrum", "kanban", "estimation", "retrospective"}
	seeds := make([]AtomSeed, 0, 20)
	letters := []string{"a", "b", "c", "d"}
	for i, topic := range topics {
		for j, letter := range letters {
			seeds = append(seeds, AtomSeed{
				AtomID: "01970000-0000-7000-a000-0000000000" + leftpad(i, 1) + leftpad(j, 1),
				Topic:  topic,
				Title:  topic + " atom " + letter,
			})
		}
	}
	return seeds
}

// leftpad converts an int 0..15 to a single hex digit; for fixture IDs only.
func leftpad(n, width int) string {
	const hex = "0123456789abcdef"
	if width != 1 || n < 0 || n > 15 {
		// Defensive — fixture data should never trigger this.
		return "0"
	}
	return string(hex[n])
}

// TestComposeDailyDose_LegacyEbbinghausCuriosity_StillPicks — legacy
// fixtureSeeds-driven test (no TopicAccuracy + no ActivePathTopics).
// Validates the back-compat path: with no weakness data the composer
// falls back to Ebbinghaus + Curiosity (via SeenTopics) + Fresh top-up.
//
// Renamed from TestComposeDailyDose_2_1_2_Mix to reflect the S5.1
// semantics shift: the canonical 2-1-2 split is replaced by 40/30/30
// (Ebbinghaus / weakness / curiosity); see dose_403030_test.go for the
// canonical assertions.
func TestComposeDailyDose_LegacyEbbinghausCuriosity_StillPicks(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()

	// Build SM-2 state for first 4 atoms — 3 are very stale (decay > 0.4),
	// 1 is fresh review (decay < 0.4).
	states := map[string]SM2State{}
	staleAtoms := []string{seeds[0].AtomID, seeds[1].AtomID, seeds[2].AtomID}
	for _, atomID := range staleAtoms {
		s := NewSM2State(atomID)
		s.ApplyGrade(GradeIRemember, dayAt(now, -30)) // 30 days ago → fully decayed
		states[atomID] = s
	}
	// Recent review — should NOT qualify as Ebbinghaus.
	s := NewSM2State(seeds[3].AtomID)
	s.ApplyGrade(GradeIRemember, dayAt(now, 0))
	states[seeds[3].AtomID] = s

	// Learner has seen "agile" + "scrum" topics (drives curiosity picks
	// via the legacy SeenTopics fallback when ActivePathTopics is empty).
	seen := map[string]bool{"agile": true, "scrum": true}

	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:      seeds,
		States:     states,
		SeenTopics: seen,
		Now:        now,
	})

	if len(dose.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want %d", len(dose.Entries), DoseTargetSize)
	}

	ebb := countByReason(dose.Entries, DoseReasonEbbinghausReview)
	cur := countByReason(dose.Entries, DoseReasonCuriosity)
	weak := countByReason(dose.Entries, DoseReasonWeakness)

	if ebb != DoseEbbinghausCount {
		t.Errorf("ebbinghaus_review count = %d, want %d", ebb, DoseEbbinghausCount)
	}
	// Without TopicAccuracy data the weakness pool is empty.
	if weak != 0 {
		t.Errorf("weakness count = %d, want 0 (no TopicAccuracy data)", weak)
	}
	if cur != DoseCuriosityCount {
		t.Errorf("curiosity count = %d, want %d", cur, DoseCuriosityCount)
	}
}

// TestComposeDailyDose_5CardBudget — total entries NEVER exceed 5.
func TestComposeDailyDose_5CardBudget(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	states := map[string]SM2State{}
	for _, seed := range seeds {
		s := NewSM2State(seed.AtomID)
		s.ApplyGrade(GradeIRemember, dayAt(now, -100)) // very stale
		states[seed.AtomID] = s
	}
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds:  seeds,
		States: states,
		Now:    now,
	})
	if len(dose.Entries) > DoseTargetSize {
		t.Errorf("dose size = %d, MUST NOT exceed %d (Comic Ch6 P14 invariant)",
			len(dose.Entries), DoseTargetSize)
	}
}

// TestComposeDailyDose_DoseReasonTagged — every entry MUST carry a non-empty
// dose_reason for UI rendering.
func TestComposeDailyDose_DoseReasonTagged(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	})
	for i, e := range dose.Entries {
		if e.DoseReason == "" {
			t.Errorf("entry[%d] dose_reason empty", i)
		}
		if e.DoseReason != DoseReasonEbbinghausReview &&
			e.DoseReason != DoseReasonFresh &&
			e.DoseReason != DoseReasonCuriosity {
			t.Errorf("entry[%d] dose_reason = %q, want one of {ebbinghaus_review, fresh, curiosity}", i, e.DoseReason)
		}
	}
}

// TestComposeDailyDose_FreshLearnerStillGets5 — even a brand-new learner
// (no states, no seen topics) gets the full 5-card budget filled (top-up).
func TestComposeDailyDose_FreshLearnerStillGets5(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds: seeds,
		Now:   now,
	})
	if len(dose.Entries) != DoseTargetSize {
		t.Errorf("brand-new learner dose size = %d, want %d (top-up)",
			len(dose.Entries), DoseTargetSize)
	}
}

// TestComposeDailyDose_NudgeMessageMatchesComic — the message text MUST match
// the Comic Ch6 P14 P4 invariant ("Daily Dose is ready" + "memory's fading").
func TestComposeDailyDose_NudgeMessageMatchesComic(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	dose := ComposeDailyDose(DailyDoseInput{
		Seeds: fixtureSeeds(),
		Now:   now,
	})
	if !strings.Contains(dose.Message, "Daily Dose is ready") {
		t.Errorf("message = %q, want to contain 'Daily Dose is ready'", dose.Message)
	}
	if !strings.Contains(dose.Message, "5 atoms") {
		t.Errorf("message = %q, want to contain '5 atoms'", dose.Message)
	}
	if !strings.Contains(dose.Message, "memory's fading") {
		t.Errorf("message = %q, want to contain 'memory's fading'", dose.Message)
	}
}

// TestComposeDailyDose_Deterministic — same input → same output.
func TestComposeDailyDose_Deterministic(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := fixtureSeeds()
	in := DailyDoseInput{Seeds: seeds, Now: now}

	d1 := ComposeDailyDose(in)
	d2 := ComposeDailyDose(in)
	if len(d1.Entries) != len(d2.Entries) {
		t.Fatalf("different sizes: %d vs %d", len(d1.Entries), len(d2.Entries))
	}
	for i := range d1.Entries {
		if d1.Entries[i].AtomID != d2.Entries[i].AtomID {
			t.Errorf("entry[%d] AtomID = %q vs %q (non-deterministic)",
				i, d1.Entries[i].AtomID, d2.Entries[i].AtomID)
		}
	}
}

// TestSeedTopicCount — sanity check the fixture has the canonical 5 topics.
func TestSeedTopicCount(t *testing.T) {
	seeds := fixtureSeeds()
	if SeedTopicCount(seeds) != 5 {
		t.Errorf("topic count = %d, want 5 (canonical mock seed)", SeedTopicCount(seeds))
	}
}
