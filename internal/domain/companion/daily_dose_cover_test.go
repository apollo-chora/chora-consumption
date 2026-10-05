package companion

import "testing"

// TestBuildAtomBreakdown_CountsByReason verifies the read-only reason tally
// surfaced in the HTTP response + daily_dose.served.v1 event. The default arm
// (any non-Ebbinghaus/Weakness/Curiosity reason — including exam_prep_drill —
// counts as Fresh) is the catch-all that keeps the four buckets summing to the
// entry count.
func TestBuildAtomBreakdown_CountsByReason(t *testing.T) {
	entries := []DailyDoseEntry{
		{AtomID: "1", DoseReason: DoseReasonEbbinghausReview},
		{AtomID: "2", DoseReason: DoseReasonEbbinghausReview},
		{AtomID: "3", DoseReason: DoseReasonWeakness},
		{AtomID: "4", DoseReason: DoseReasonCuriosity},
		{AtomID: "5", DoseReason: DoseReasonFresh},
		{AtomID: "6", DoseReason: DoseReasonExamPrepDrill}, // default arm → Fresh
		{AtomID: "7", DoseReason: DoseReason("unmapped")},  // default arm → Fresh
	}
	b := BuildAtomBreakdown(entries)
	if b.Ebbinghaus != 2 {
		t.Fatalf("Ebbinghaus = %d, want 2", b.Ebbinghaus)
	}
	if b.Weakness != 1 {
		t.Fatalf("Weakness = %d, want 1", b.Weakness)
	}
	if b.Curiosity != 1 {
		t.Fatalf("Curiosity = %d, want 1", b.Curiosity)
	}
	// Fresh + exam_prep_drill + unmapped all land in the default Fresh bucket.
	if b.Fresh != 3 {
		t.Fatalf("Fresh = %d, want 3 (fresh + drill + unmapped via default arm)", b.Fresh)
	}
	// Invariant: the four buckets partition the entry slice.
	if total := b.Ebbinghaus + b.Weakness + b.Curiosity + b.Fresh; total != len(entries) {
		t.Fatalf("bucket total = %d, want %d (must partition entries)", total, len(entries))
	}
}

// TestBuildAtomBreakdown_Empty — a nil/empty entry slice yields a zero
// breakdown (no panic, all buckets 0).
func TestBuildAtomBreakdown_Empty(t *testing.T) {
	b := BuildAtomBreakdown(nil)
	if b != (DoseAtomBreakdown{}) {
		t.Fatalf("empty breakdown = %+v, want zero value", b)
	}
}

// TestPickEbbinghausSlots_SkipsAlreadyPicked covers the `picked[atom]` continue
// branch: an atom that a prior composition step already claimed must be
// skipped even when it is decay-eligible, so the same atom never appears twice
// in one dose.
func TestPickEbbinghausSlots_SkipsAlreadyPicked(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := []AtomSeed{
		{AtomID: "atom-A", Topic: "agile", Title: "A"},
		{AtomID: "atom-B", Topic: "agile", Title: "B"},
	}
	// Both atoms are stale (decay > 0.4): reviewed 30 days ago.
	states := map[string]SM2State{}
	for _, s := range seeds {
		st := NewSM2State(s.AtomID)
		st.ApplyGrade(GradeIRemember, now.AddDate(0, 0, -30))
		states[s.AtomID] = st
	}
	in := DailyDoseInput{Seeds: seeds, States: states, Now: now}

	// Pre-claim atom-A so the Ebbinghaus pass must skip it via the continue.
	picked := map[string]bool{"atom-A": true}
	entries := pickEbbinghausSlots(nil, picked, in, DoseEbbinghausCount)

	for _, e := range entries {
		if e.AtomID == "atom-A" {
			t.Fatalf("pre-picked atom-A was selected again: %+v", entries)
		}
	}
	// atom-B (the un-picked stale atom) should be selected.
	if len(entries) != 1 || entries[0].AtomID != "atom-B" {
		t.Fatalf("entries = %+v, want exactly [atom-B]", entries)
	}
	if entries[0].DoseReason != DoseReasonEbbinghausReview {
		t.Fatalf("reason = %q, want ebbinghaus_review", entries[0].DoseReason)
	}
}

// TestPickWeaknessSlots_SkipsAlreadyPickedAndRespectsCap covers the
// already-picked skip plus the weakest-first ordering of the weakness pass.
func TestPickWeaknessSlots_SkipsAlreadyPickedAndRespectsCap(t *testing.T) {
	now := mustParse(t, "2026-05-08T00:00:00Z")
	seeds := []AtomSeed{
		{AtomID: "atom-A", Topic: "agile", Title: "A"},
		{AtomID: "atom-B", Topic: "scrum", Title: "B"},
		{AtomID: "atom-C", Topic: "kanban", Title: "C"},
	}
	in := DailyDoseInput{
		Seeds:  seeds,
		States: map[string]SM2State{},
		Now:    now,
		TopicAccuracy: map[string]float64{
			"agile":  0.20, // weakest
			"scrum":  0.50,
			"kanban": 0.60,
		},
	}
	// Pre-claim the weakest topic's atom; the pass must skip it.
	picked := map[string]bool{"atom-A": true}
	entries := pickWeaknessSlots(nil, picked, in, DefaultWeaknessThreshold, DoseWeaknessCount)

	for _, e := range entries {
		if e.AtomID == "atom-A" {
			t.Fatalf("pre-picked atom-A selected again: %+v", entries)
		}
		if e.DoseReason != DoseReasonWeakness {
			t.Fatalf("reason = %q, want weakness", e.DoseReason)
		}
	}
	// scrum (0.50) is weaker than kanban (0.60), so atom-B must come first.
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (cap=%d)", len(entries), DoseWeaknessCount)
	}
	if entries[0].AtomID != "atom-B" {
		t.Fatalf("first weakness pick = %q, want atom-B (weakest remaining topic)", entries[0].AtomID)
	}
}
