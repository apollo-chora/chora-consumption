package companion

// daily_dose_message_test.go — the dose nudge message reflects the ACTUAL
// entry count (no-debt directive 2026-06-10). The legacy static copy claimed
// "5 atoms" even when the real-universe dose composed fewer (or zero).

import (
	"strings"
	"testing"
	"time"
)

func TestDoseMessageFor_Counts(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "No atoms queued yet"},
		{1, "1 atom."},
		{3, "3 atoms."},
		{5, "5 atoms."},
	}
	for _, c := range cases {
		got := DoseMessageFor(c.n)
		if !strings.Contains(got, c.want) {
			t.Errorf("DoseMessageFor(%d) = %q, want it to contain %q", c.n, got, c.want)
		}
	}
}

func TestComposeDailyDose_MessageMatchesEntryCount(t *testing.T) {
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	seeds := []AtomSeed{
		{AtomID: "a1", Topic: "go", Title: "A1"},
		{AtomID: "a2", Topic: "go", Title: "A2"},
		{AtomID: "a3", Topic: "go", Title: "A3"},
	}
	dose := ComposeDailyDose(DailyDoseInput{Seeds: seeds, Now: now})
	if len(dose.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (fresh top-up over 3 seeds)", len(dose.Entries))
	}
	if dose.Message != DoseMessageFor(3) {
		t.Errorf("Message = %q, want DoseMessageFor(3) = %q", dose.Message, DoseMessageFor(3))
	}
}

func TestComposeDailyDose_EmptyUniverseMessage(t *testing.T) {
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	dose := ComposeDailyDose(DailyDoseInput{Seeds: nil, Now: now})
	if len(dose.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(dose.Entries))
	}
	if dose.Message != DoseMessageFor(0) {
		t.Errorf("Message = %q, want DoseMessageFor(0) = %q", dose.Message, DoseMessageFor(0))
	}
}

func TestSwapCuriosityForDrills_MessageStaysConsistentWithCount(t *testing.T) {
	dose := DailyDose{
		Message: DoseMessageFor(2),
		Entries: []DailyDoseEntry{
			{AtomID: "1", Topic: "agile", DoseReason: DoseReasonCuriosity},
			{AtomID: "2", Topic: "scrum", DoseReason: DoseReasonEbbinghausReview},
		},
	}
	out := swapCuriosityForDrills(dose, nil, []string{"agile"})
	if out.Message != DoseMessageFor(len(out.Entries)) {
		t.Errorf("Message = %q, want DoseMessageFor(%d) = %q",
			out.Message, len(out.Entries), DoseMessageFor(len(out.Entries)))
	}
}
