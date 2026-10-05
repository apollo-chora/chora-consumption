package companion

import "testing"

// drillCount counts exam-prep-drill entries in a dose.
func drillCount(d DailyDose) int {
	n := 0
	for _, e := range d.Entries {
		if e.DoseReason == DoseReasonExamPrepDrill {
			n++
		}
	}
	return n
}

func reasonCount(d DailyDose, r DoseReason) int {
	n := 0
	for _, e := range d.Entries {
		if e.DoseReason == r {
			n++
		}
	}
	return n
}

// TestSwapCuriosityForDrills_NoRecTopicsPassthrough covers the early-return
// guard: with no coach-recommended topics the dose is returned unchanged.
func TestSwapCuriosityForDrills_NoRecTopicsPassthrough(t *testing.T) {
	dose := DailyDose{
		Message: DoseMessageFor(2),
		Entries: []DailyDoseEntry{
			{AtomID: "1", Topic: "agile", DoseReason: DoseReasonCuriosity},
			{AtomID: "2", Topic: "scrum", DoseReason: DoseReasonEbbinghausReview},
		},
	}
	out := swapCuriosityForDrills(dose, nil, nil)
	if len(out.Entries) != len(dose.Entries) {
		t.Fatalf("entries changed with empty recTopics: %d vs %d", len(out.Entries), len(dose.Entries))
	}
	if drillCount(out) != 0 {
		t.Fatalf("drills appeared with empty recTopics: %d", drillCount(out))
	}
}

// TestSwapCuriosityForDrills_RetagsCuriosityInPlace — Step 1a: a curiosity
// entry whose topic matches a recTopic is retagged to exam_prep_drill,
// preserving its deterministically-selected atom_id. Ebbinghaus entries are
// untouched.
func TestSwapCuriosityForDrills_RetagsCuriosityInPlace(t *testing.T) {
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "ebb", Topic: "agile", DoseReason: DoseReasonEbbinghausReview},
			{AtomID: "cur", Topic: "velocity", DoseReason: DoseReasonCuriosity},
		},
	}
	out := swapCuriosityForDrills(dose, nil, []string{"velocity"})

	if drillCount(out) != 1 {
		t.Fatalf("drills = %d, want 1 (retagged curiosity)", drillCount(out))
	}
	// Ebbinghaus must survive untouched.
	if reasonCount(out, DoseReasonEbbinghausReview) != 1 {
		t.Fatal("ebbinghaus entry was disturbed by the swap")
	}
	// The retagged atom keeps its original id.
	var found bool
	for _, e := range out.Entries {
		if e.DoseReason == DoseReasonExamPrepDrill {
			if e.AtomID != "cur" {
				t.Fatalf("drill atom_id = %q, want preserved 'cur'", e.AtomID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("retagged drill entry missing")
	}
}

// TestSwapCuriosityForDrills_RetagsFreshWhenCuriosityAbsent — Step 1b: when
// the vanilla composer placed coach-relevant atoms in Fresh top-up slots
// (no curiosity-eligible topics), the swap retags those Fresh entries as
// drills. Non-matching Fresh entries pass through unchanged.
func TestSwapCuriosityForDrills_RetagsFreshWhenCuriosityAbsent(t *testing.T) {
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "ebb", Topic: "agile", DoseReason: DoseReasonEbbinghausReview},
			{AtomID: "fresh-match", Topic: "velocity", DoseReason: DoseReasonFresh},
			{AtomID: "fresh-other", Topic: "kanban", DoseReason: DoseReasonFresh},
		},
	}
	out := swapCuriosityForDrills(dose, nil, []string{"velocity"})

	if drillCount(out) != 1 {
		t.Fatalf("drills = %d, want 1 (fresh-match retagged)", drillCount(out))
	}
	// fresh-other (non-matching topic) must remain a Fresh entry.
	var sawFreshOther bool
	for _, e := range out.Entries {
		if e.AtomID == "fresh-other" {
			if e.DoseReason != DoseReasonFresh {
				t.Fatalf("fresh-other reason = %q, want fresh (unmatched topic untouched)", e.DoseReason)
			}
			sawFreshOther = true
		}
		if e.AtomID == "fresh-match" && e.DoseReason != DoseReasonExamPrepDrill {
			t.Fatalf("fresh-match reason = %q, want exam_prep_drill", e.DoseReason)
		}
	}
	if !sawFreshOther {
		t.Fatal("non-matching fresh entry was dropped")
	}
}

// TestSwapCuriosityForDrills_LeftoverCuriosityPadsBudget — Step 3 padding: a
// curiosity entry with NO recTopic match is leftover, and when the drill
// count is under the DoseCuriosityCount budget the leftover fills the
// remaining slot rather than being dropped.
func TestSwapCuriosityForDrills_LeftoverCuriosityPadsBudget(t *testing.T) {
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "cur-match", Topic: "velocity", DoseReason: DoseReasonCuriosity},
			{AtomID: "cur-other", Topic: "agile", DoseReason: DoseReasonCuriosity},
		},
	}
	// Only one recTopic match → one drill; budget DoseCuriosityCount(2) leaves
	// one slot for the leftover curiosity entry (no seed catalogue to draw
	// extra drills from, so the leftover must pad).
	out := swapCuriosityForDrills(dose, nil, []string{"velocity"})

	if drillCount(out) != 1 {
		t.Fatalf("drills = %d, want 1", drillCount(out))
	}
	if c := reasonCount(out, DoseReasonCuriosity); c != 1 {
		t.Fatalf("leftover curiosity = %d, want 1 (padded into the budget)", c)
	}
	// drills(1) + curiosity(1) must not exceed the DoseCuriosityCount budget.
	if drillCount(out)+reasonCount(out, DoseReasonCuriosity) > DoseCuriosityCount {
		t.Fatalf("drills+curiosity exceeds budget %d", DoseCuriosityCount)
	}
}

// TestSwapCuriosityForDrills_ExcessCuriosityDropped — Step 3 drop: when the
// drill count already fills the DoseCuriosityCount budget, leftover curiosity
// entries are dropped (not stacked) to preserve the canonical mix.
func TestSwapCuriosityForDrills_ExcessCuriosityDropped(t *testing.T) {
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "cur-m1", Topic: "velocity", DoseReason: DoseReasonCuriosity},
			{AtomID: "cur-m2", Topic: "empiricism", DoseReason: DoseReasonCuriosity},
			{AtomID: "cur-extra", Topic: "agile", DoseReason: DoseReasonCuriosity},
		},
	}
	// Two recTopic matches → 2 drills == DoseCuriosityCount budget; the
	// non-matching curiosity entry must be dropped.
	out := swapCuriosityForDrills(dose, nil, []string{"velocity", "empiricism"})

	if drillCount(out) != DoseCuriosityCount {
		t.Fatalf("drills = %d, want %d", drillCount(out), DoseCuriosityCount)
	}
	if c := reasonCount(out, DoseReasonCuriosity); c != 0 {
		t.Fatalf("leftover curiosity = %d, want 0 (budget full → dropped)", c)
	}
	for _, e := range out.Entries {
		if e.AtomID == "cur-extra" {
			t.Fatal("excess curiosity 'cur-extra' should have been dropped")
		}
	}
}

// TestSwapCuriosityForDrills_DrawsFromSeedCatalogue — Step 2: when in-dose
// curiosity/fresh entries do not fill the drill budget, additional atoms are
// pulled from the seed catalogue for recTopics not already in the dose,
// deterministically by atom_id ascending within a topic.
func TestSwapCuriosityForDrills_DrawsFromSeedCatalogue(t *testing.T) {
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "ebb", Topic: "agile", DoseReason: DoseReasonEbbinghausReview},
		},
	}
	seeds := []AtomSeed{
		{AtomID: "vel-b", Topic: "velocity", Title: "vb"},
		{AtomID: "vel-a", Topic: "velocity", Title: "va"}, // lower id → picked first
		{AtomID: "agile-x", Topic: "agile", Title: "ax"},
	}
	out := swapCuriosityForDrills(dose, seeds, []string{"velocity"})

	if drillCount(out) == 0 {
		t.Fatal("expected drills drawn from seed catalogue")
	}
	// Deterministic: vel-a (lower id) must be the first velocity drill.
	for _, e := range out.Entries {
		if e.DoseReason == DoseReasonExamPrepDrill {
			if e.AtomID != "vel-a" {
				t.Fatalf("first catalogue drill = %q, want vel-a (atom_id ascending)", e.AtomID)
			}
			break
		}
	}
}

// TestSwapCuriosityForDrills_RestoresFreshUpToBudget — Step 4: leftover Fresh
// entries are restored after drills so the 5-card budget is met, and the
// restore halts at DoseTargetSize (the >= break). We pack the dose so the
// budget boundary is exercised.
func TestSwapCuriosityForDrills_RestoresFreshUpToBudget(t *testing.T) {
	// 3 ebbinghaus (kept) + 1 matching fresh (→ drill) + 3 non-matching fresh
	// leftovers. After drills the total is 4; only 1 leftover fresh fits
	// before DoseTargetSize(5) is reached → the >= break drops the rest.
	dose := DailyDose{
		Entries: []DailyDoseEntry{
			{AtomID: "ebb1", Topic: "agile", DoseReason: DoseReasonEbbinghausReview},
			{AtomID: "ebb2", Topic: "scrum", DoseReason: DoseReasonEbbinghausReview},
			{AtomID: "ebb3", Topic: "kanban", DoseReason: DoseReasonEbbinghausReview},
			{AtomID: "fresh-match", Topic: "velocity", DoseReason: DoseReasonFresh},
			{AtomID: "fresh-1", Topic: "x1", DoseReason: DoseReasonFresh},
			{AtomID: "fresh-2", Topic: "x2", DoseReason: DoseReasonFresh},
			{AtomID: "fresh-3", Topic: "x3", DoseReason: DoseReasonFresh},
		},
	}
	out := swapCuriosityForDrills(dose, nil, []string{"velocity"})

	if len(out.Entries) > DoseTargetSize {
		t.Fatalf("dose size = %d, must never exceed DoseTargetSize %d", len(out.Entries), DoseTargetSize)
	}
	if len(out.Entries) != DoseTargetSize {
		t.Fatalf("dose size = %d, want exactly %d (3 ebb + 1 drill + 1 restored fresh)", len(out.Entries), DoseTargetSize)
	}
	if drillCount(out) != 1 {
		t.Fatalf("drills = %d, want 1", drillCount(out))
	}
	if reasonCount(out, DoseReasonEbbinghausReview) != 3 {
		t.Fatal("ebbinghaus entries must be preserved")
	}
	// Exactly one fresh leftover restored (the other two dropped at the break).
	if f := reasonCount(out, DoseReasonFresh); f != 1 {
		t.Fatalf("restored fresh = %d, want 1 (budget break dropped the rest)", f)
	}
}
