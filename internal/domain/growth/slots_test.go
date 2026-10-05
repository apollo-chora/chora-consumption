// slots_test.go — ADR-218 D2 (slots = +1 per growth stage) + D8 (evolution
// tier = derived presentation band, L6). RED-first for CHO-2012 P0.
package growth

import "testing"

// TestSlotsForStage pins the ADR-218 D2 cadence: 1 slot at Egg (stage 0),
// +1 per stage, 7 at Matured (stage 6). Supersedes the per-user-XP tier
// table (apprentice 1 / adept 3 / master 5 / sage 8).
func TestSlotsForStage(t *testing.T) {
	want := map[int]int{
		StageEgg:        1,
		StageBaby:       2,
		StageFledgling:  3,
		StageAwakened:   4,
		StageStructural: 5,
		StageTeen:       6,
		StageMatured:    7,
	}
	for stage, slots := range want {
		if got := SlotsForStage(stage); got != slots {
			t.Errorf("SlotsForStage(%d) = %d, want %d", stage, got, slots)
		}
	}
}

// TestSlotsForStageOutOfRange — defensive clamping: below Egg clamps to the
// Egg allowance, above Matured clamps to the Matured allowance.
func TestSlotsForStageOutOfRange(t *testing.T) {
	if got := SlotsForStage(-1); got != 1 {
		t.Errorf("SlotsForStage(-1) = %d, want 1", got)
	}
	if got := SlotsForStage(99); got != 7 {
		t.Errorf("SlotsForStage(99) = %d, want 7", got)
	}
}

// TestTierForStage pins L6's derived, never-shown presentation band:
// stages 0-1 apprentice, 2-3 adept, 4-5 master, 6 sage. The band is a pure
// function of growth_stage — the per-user XP feeder is retired (ADR-218 D8).
func TestTierForStage(t *testing.T) {
	want := map[int]string{
		0: "apprentice",
		1: "apprentice",
		2: "adept",
		3: "adept",
		4: "master",
		5: "master",
		6: "sage",
	}
	for stage, tier := range want {
		if got := TierForStage(stage); got != tier {
			t.Errorf("TierForStage(%d) = %q, want %q", stage, got, tier)
		}
	}
	// Out-of-range clamps to the nearest band, never an empty string.
	if got := TierForStage(-3); got != "apprentice" {
		t.Errorf("TierForStage(-3) = %q, want apprentice", got)
	}
	if got := TierForStage(42); got != "sage" {
		t.Errorf("TierForStage(42) = %q, want sage", got)
	}
}
