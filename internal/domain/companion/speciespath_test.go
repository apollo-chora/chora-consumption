// speciespath_test.go — ADR-218 D3 + ADR-228 D3: species Paths are
// platform-authored reference data validated FAIL-LOUD against the catalogue
// at seed/publish time. Bands (K per stage-up, editor-tunable defaults
// 1/3/4/6/7/6 at st1..st6 since the incubation arc split the former st2 K=4
// into a st1 band of 1 + a st2 band of 3) may never front-run a Skill's
// min_growth_stage floor. RED-first for CHO-2012 P0 / CHO-2089 F-I2.
package companion

import (
	"errors"
	"testing"
)

func tinyCatalogue() map[string]CatalogEntry {
	return map[string]CatalogEntry{
		"explain_anew": {SkillKey: "explain_anew", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
		"quiz_me":      {SkillKey: "quiz_me", SkillKind: SkillKindActive, MinGrowthStage: 3, SlotCost: 1},
		"web_research": {SkillKey: "web_research", SkillKind: SkillKindActive, MinGrowthStage: 5, SlotCost: 2},
	}
}

// tinyBands — a 3-entry catalogue with 1 unlock at st2, 1 at st3, 1 at st5.
var tinyBands = [7]int{0, 0, 1, 1, 0, 1, 0}

func TestUnlockStageForPositionDefaults(t *testing.T) {
	// Default bands 1/3/4/6/7/6 (ADR-228 D3) → position 1 st1 · 2-4 st2 ·
	// 5-8 st3 · 9-14 st4 · 15-21 st5 · 22-27 st6. The st1 band (K=1) is the
	// hatch-minted first Skill (progress_mirror); st2-on cumulative totals are
	// unchanged (4·8·14·21·27), so no Skill except progress_mirror moves.
	cases := map[int]int{1: 1, 2: 2, 4: 2, 5: 3, 8: 3, 9: 4, 14: 4, 15: 5, 21: 5, 22: 6, 27: 6}
	for pos, wantStage := range cases {
		if got := UnlockStageForPosition(pos, DefaultStageUnlockCounts); got != wantStage {
			t.Errorf("UnlockStageForPosition(%d) = %d, want %d", pos, got, wantStage)
		}
	}
}

func TestCumulativeUnlocksThroughStageDefaults(t *testing.T) {
	// ADR-228 D3: stage 1 now unlocks 1 (the st1 band); st2-on unchanged.
	cases := map[int]int{0: 0, 1: 1, 2: 4, 3: 8, 4: 14, 5: 21, 6: 27, 9: 27}
	for stage, want := range cases {
		if got := CumulativeUnlocksThroughStage(stage, DefaultStageUnlockCounts); got != want {
			t.Errorf("CumulativeUnlocksThroughStage(%d) = %d, want %d", stage, got, want)
		}
	}
}

// TestDefaultBandsHaveStageOneBandOfOne — ADR-228 D3: the incubation arc gives
// every species Path a stage-1 band of EXACTLY one Skill (K=1), so the hatch
// (stage 0→1) mints precisely the first Path entry. The former st2 K=4 splits
// into st1=1 + st2=3; the full 27-Skill catalogue is still reached by st6.
func TestDefaultBandsHaveStageOneBandOfOne(t *testing.T) {
	if got := DefaultStageUnlockCounts[1]; got != 1 {
		t.Errorf("st1 band = %d, want 1 (ADR-228 D3 hatch-mint band)", got)
	}
	if got := CumulativeUnlocksThroughStage(1, DefaultStageUnlockCounts); got != 1 {
		t.Errorf("cumulative through st1 = %d, want 1 (only the first entry mints at hatch)", got)
	}
	// st2-on cumulative totals are UNCHANGED by the split — proving no Skill
	// other than the first moves band (AC "no other skill touched").
	for stage, want := range map[int]int{2: 4, 3: 8, 4: 14, 5: 21, 6: 27} {
		if got := CumulativeUnlocksThroughStage(stage, DefaultStageUnlockCounts); got != want {
			t.Errorf("cumulative through st%d = %d, want %d (unchanged from pre-F-I2)", stage, got, want)
		}
	}
}

func TestValidatePathHappy(t *testing.T) {
	p := SpeciesPath{Species: "owl", Version: 1, Entries: []string{"explain_anew", "quiz_me", "web_research"}, Active: true}
	if err := ValidatePath(p, tinyCatalogue(), tinyBands); err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
}

func TestValidatePathFloorViolation(t *testing.T) {
	// web_research (min st5) at position 1 (band st2) front-runs its floor.
	p := SpeciesPath{Species: "owl", Version: 1, Entries: []string{"web_research", "quiz_me", "explain_anew"}, Active: true}
	err := ValidatePath(p, tinyCatalogue(), tinyBands)
	if !errors.Is(err, ErrPathFloorViolation) {
		t.Fatalf("floor front-run = %v, want ErrPathFloorViolation", err)
	}
}

func TestValidatePathUnknownSkill(t *testing.T) {
	p := SpeciesPath{Species: "owl", Version: 1, Entries: []string{"explain_anew", "quiz_me", "skill_tree_hack"}, Active: true}
	if err := ValidatePath(p, tinyCatalogue(), tinyBands); !errors.Is(err, ErrPathUnknownSkill) {
		t.Fatalf("unknown key = %v, want ErrPathUnknownSkill", err)
	}
}

func TestValidatePathDuplicateSkill(t *testing.T) {
	p := SpeciesPath{Species: "owl", Version: 1, Entries: []string{"explain_anew", "explain_anew", "quiz_me"}, Active: true}
	if err := ValidatePath(p, tinyCatalogue(), tinyBands); !errors.Is(err, ErrPathDuplicateSkill) {
		t.Fatalf("duplicate key = %v, want ErrPathDuplicateSkill", err)
	}
}

func TestValidatePathMissingSkill(t *testing.T) {
	// Every catalogue key must appear exactly once (paths reach the FULL
	// catalogue by Matured — GQ-6-f1 no exclusives).
	p := SpeciesPath{Species: "owl", Version: 1, Entries: []string{"explain_anew", "quiz_me"}, Active: true}
	if err := ValidatePath(p, tinyCatalogue(), tinyBands); !errors.Is(err, ErrPathMissingSkill) {
		t.Fatalf("missing key = %v, want ErrPathMissingSkill", err)
	}
}

func TestValidatePathBadSpecies(t *testing.T) {
	p := SpeciesPath{Species: "gryphon", Version: 1, Entries: []string{"explain_anew", "quiz_me", "web_research"}, Active: true}
	if err := ValidatePath(p, tinyCatalogue(), tinyBands); !errors.Is(err, ErrPathUnknownSpecies) {
		t.Fatalf("bad species = %v, want ErrPathUnknownSpecies", err)
	}
}
