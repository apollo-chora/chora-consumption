// roster_test.go — TDD spec for the `RosterEntry` value object + the
// stage-name/ExpToNextStage projection helpers introduced by debt #44.
//
// Per .claude/rules/development-execution.md TDD strict gate (85% domain
// coverage). These tests cover the public surface; the RosterEntry
// composition itself is a struct literal — its real coverage lives in
// the repo + handler layer tests.
package companion_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

func TestStageName_KnownStages(t *testing.T) {
	cases := []struct {
		stage int
		want  string
	}{
		{0, "egg"},
		{1, "baby"},
		{2, "fledgling"},
		{3, "awakened"},
		{4, "structural"},
		{5, "teen"},
		{6, "matured"},
	}
	for _, tc := range cases {
		if got := companion.StageName(tc.stage); got != tc.want {
			t.Errorf("StageName(%d) = %q; want %q", tc.stage, got, tc.want)
		}
	}
}

func TestStageName_OutOfRange_ReturnsUnknown(t *testing.T) {
	for _, stage := range []int{-1, 7, 100, -100} {
		if got := companion.StageName(stage); got != "unknown" {
			t.Errorf("StageName(%d) = %q; want \"unknown\"", stage, got)
		}
	}
}

func TestExpToNextStage_MatchesADR149Thresholds(t *testing.T) {
	cases := []struct {
		stage int
		want  int
	}{
		{0, 0},    // Stage 0 → 1 (hatch event, no EXP gate)
		{1, 50},   // Stage 1 → 2 (Fledgling)
		{2, 200},  // Stage 2 → 3 (Awakened / Aha-moment)
		{3, 500},  // Stage 3 → 4 (Structural)
		{4, 1200}, // Stage 4 → 5 (Teen)
		{5, 3000}, // Stage 5 → 6 (Matured)
	}
	for _, tc := range cases {
		if got := companion.ExpToNextStage(tc.stage); got != tc.want {
			t.Errorf("ExpToNextStage(%d) = %d; want %d", tc.stage, got, tc.want)
		}
	}
}

func TestExpToNextStage_AtMaturedStage_ReturnsZero(t *testing.T) {
	if got := companion.ExpToNextStage(6); got != 0 {
		t.Errorf("ExpToNextStage(6) = %d; want 0 (matured has no next stage)", got)
	}
}

func TestExpToNextStage_OutOfRange_ReturnsZero(t *testing.T) {
	for _, stage := range []int{-1, 7, 100} {
		if got := companion.ExpToNextStage(stage); got != 0 {
			t.Errorf("ExpToNextStage(%d) = %d; want 0", stage, got)
		}
	}
}

func TestRosterEntry_AllowsNilGrowthAndCosmetic(t *testing.T) {
	// Pre-0032 rows: growth columns may be unset. The entry must still
	// hold a valid Instance pointer and nil projections — the handler
	// will fall back to legacy ConfiguredRules JSONB derivation in that
	// case.
	inst, err := companion.NewInstance(
		"01970000-0000-7000-8000-000000000001",
		"01970000-0000-7000-9000-000000000001",
		"Newton", "math",
	)
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	entry := companion.RosterEntry{
		Instance: inst,
		Growth:   nil,
		Cosmetic: nil,
	}
	if entry.Instance == nil {
		t.Fatal("Instance pointer dropped")
	}
	if entry.Growth != nil {
		t.Error("Growth should be nil")
	}
	if entry.Cosmetic != nil {
		t.Error("Cosmetic should be nil")
	}
}

func TestRosterEntry_CarriesGrowthAndCosmetic(t *testing.T) {
	inst, _ := companion.NewInstance(
		"01970000-0000-7000-8000-000000000001",
		"01970000-0000-7000-9000-000000000001",
		"Eira", "cspo",
	)
	growth := &companion.GrowthSnapshot{
		Stage:            2,
		StageName:        "fledgling",
		Exp:              200,
		ExpToNextStage:   200,
		CurrentBreed:     "dragon",
		EffectiveLLMTier: "flash-lite",
		ResonantAtomID:   "00000000-0000-7000-8000-00000000a0a2",
	}
	cosmetic := &companion.CosmeticSnapshot{
		Shiny:  false,
		Rarity: "common",
	}
	entry := companion.RosterEntry{Instance: inst, Growth: growth, Cosmetic: cosmetic}
	if entry.Growth.Stage != 2 {
		t.Errorf("Growth.Stage = %d; want 2", entry.Growth.Stage)
	}
	if entry.Growth.CurrentBreed != "dragon" {
		t.Errorf("Growth.CurrentBreed = %q; want dragon", entry.Growth.CurrentBreed)
	}
	if entry.Cosmetic.Rarity != "common" {
		t.Errorf("Cosmetic.Rarity = %q; want common", entry.Cosmetic.Rarity)
	}
}
