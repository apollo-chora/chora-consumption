// speciespath_nextunlocks_test.go — CHO-2030 (R3-9 named-tease): the
// next-stage unlock preview computed from the species Path bands. The
// Paths stay the single source of truth (option A of the R5-1 contract).
package companion

import (
	"errors"
	"testing"
)

func teasePath(entries ...string) *SpeciesPath {
	return &SpeciesPath{PathID: "p1", Species: "owl", Version: 1, Entries: entries, Active: true}
}

func teaseCatalogue(keys ...string) map[string]CatalogEntry {
	cat := make(map[string]CatalogEntry, len(keys))
	for i, k := range keys {
		active := i%2 == 0 // alternate to prove the flag is per-entry
		cat[k] = CatalogEntry{SkillKey: k, SkillKind: SkillKindActive, Active: active}
	}
	return cat
}

// TestNextUnlocksForStage_St0ReturnsStage1Band — ADR-228 D3: an egg (stage 0)
// previews its st1 band = the ONE Skill the hatch will mint (position 1).
func TestNextUnlocksForStage_St0ReturnsStage1Band(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 0, DefaultStageUnlockCounts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (st1 band K=1 — the hatch-minted first Skill)", len(got))
	}
	if got[0].SkillKey != "a" || got[0].UnlocksAtStage != 1 {
		t.Errorf("st1 preview = %q@%d, want a@1", got[0].SkillKey, got[0].UnlocksAtStage)
	}
}

// ADR-228 D3: with the st1 band carved out, stage 1's next-unlock preview is
// the st2 band of THREE (positions 2-4), not the pre-F-I2 four.
func TestNextUnlocksForStage_St1ReturnsStage2Band(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 1, DefaultStageUnlockCounts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (stage-2 band, positions 2-4)", len(got))
	}
	for i, want := range []string{"b", "c", "d"} {
		if got[i].SkillKey != want {
			t.Errorf("[%d] = %q, want %q", i, got[i].SkillKey, want)
		}
		if got[i].UnlocksAtStage != 2 {
			t.Errorf("[%d].UnlocksAtStage = %d, want 2", i, got[i].UnlocksAtStage)
		}
	}
	// The catalogue-activation flag rides each preview row (dormant tease):
	// teaseCatalogue sets active := i%2==0, so "b" (index 1) is inactive and
	// "c" (index 2) is active — the preview must mirror the catalogue per-row.
	if got[0].CatalogueActive || !got[1].CatalogueActive {
		t.Errorf("CatalogueActive flags = %v/%v, want false/true (mirror the catalogue)",
			got[0].CatalogueActive, got[1].CatalogueActive)
	}
}

func TestNextUnlocksForStage_St2ReturnsStage3Band(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 2, DefaultStageUnlockCounts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4 (stage-3 band)", len(got))
	}
	if got[0].SkillKey != "e" || got[3].SkillKey != "h" {
		t.Errorf("window = %q..%q, want e..h", got[0].SkillKey, got[3].SkillKey)
	}
}

func TestNextUnlocksForStage_TopStageEmpty(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 6, DefaultStageUnlockCounts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0 at the top stage", len(got))
	}
}

func TestNextUnlocksForStage_ClampsToPathLength(t *testing.T) {
	// Path shorter than the cumulative window — clamp, never panic.
	keys := []string{"a", "b", "c", "d", "e", "f"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 2, DefaultStageUnlockCounts)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 { // positions 5..8 exist only through 6
		t.Fatalf("len = %d, want 2 (clamped)", len(got))
	}
}

func TestNextUnlocksForStage_ZeroBandsDefault(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	got, err := NextUnlocksForStage(teasePath(keys...), teaseCatalogue(keys...), 1, [7]int{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Zero bands fall back to DefaultStageUnlockCounts → stage 1's next band is
	// the st2 band of 3 (ADR-228 D3, positions 2-4).
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (zero bands fall back to defaults)", len(got))
	}
}

func TestNextUnlocksForStage_UnknownKeyFailsLoud(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	cat := teaseCatalogue("a", "b", "c", "d") // "e" missing
	_, err := NextUnlocksForStage(teasePath(keys...), cat, 1, [7]int{0, 0, 5, 0, 0, 0, 0})
	if !errors.Is(err, ErrPathUnknownSkill) {
		t.Fatalf("err = %v, want ErrPathUnknownSkill", err)
	}
}

func TestNextUnlocksForStage_NilPathFailsLoud(t *testing.T) {
	if _, err := NextUnlocksForStage(nil, teaseCatalogue("a"), 1, [7]int{}); err == nil {
		t.Fatal("want error on nil path")
	}
}
