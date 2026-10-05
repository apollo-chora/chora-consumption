// curve_innate_test.go — ADR-218 D1: the ADR-149 per-stage tool ladder
// becomes the INNATE KIT, capped at stage 4. Everything above (query_kg,
// score-at-st5, st6 meta-tools) moved to the equippable catalogue:
// query_kg → map_sight, suggest_atom_authoring → atom_forge,
// propose_kg_merge → kg_explore st6 extension (spec pack §1 + CR §5.5 notes).
// RED-first for CHO-2012 P0.
package growth

import (
	"reflect"
	"testing"
)

// TestInnateKitPlateausAtStructural — spec pack §1: the innate kit is
// complete at stage 4 (Structural); stages 5 and 6 add NO innate tools
// (higher-order powers are catalogue Skills occupying slots).
func TestInnateKitPlateausAtStructural(t *testing.T) {
	st4 := UnlockedToolsForStage(4)
	want := []string{"atom_search", "cite_atom", "ebbinghaus_state", "persona_lookup", "score_atom_for_learner"}
	if !reflect.DeepEqual(st4, want) {
		t.Fatalf("innate kit at stage 4 = %v, want %v", st4, want)
	}
	for _, stage := range []int{5, 6} {
		if got := UnlockedToolsForStage(stage); !reflect.DeepEqual(got, st4) {
			t.Errorf("innate kit at stage %d = %v, want the stage-4 plateau %v", stage, got, st4)
		}
	}
}

// TestInnateKitRetiredToolsGone — the catalogue-bound tool names must no
// longer appear anywhere in the innate ladder.
func TestInnateKitRetiredToolsGone(t *testing.T) {
	retired := map[string]string{
		"query_kg":               "map_sight (kg.read_map)",
		"suggest_atom_authoring": "atom_forge",
		"propose_kg_merge":       "kg_explore st6 extension",
	}
	for stage := 0; stage <= 6; stage++ {
		for _, tool := range UnlockedToolsForStage(stage) {
			if skill, gone := retired[tool]; gone {
				t.Errorf("stage %d innate kit still lists %q — moved to catalogue Skill %s (ADR-218 D1)", stage, tool, skill)
			}
		}
	}
}

// TestNewlyUnlockedToolsAtStageFive — no innate delta at st5/st6 any more.
func TestNewlyUnlockedToolsAtStageFive(t *testing.T) {
	if diff := NewlyUnlockedTools(4, 5); len(diff) != 0 {
		t.Errorf("NewlyUnlockedTools(4,5) = %v, want empty (innate kit plateaus at st4)", diff)
	}
	if diff := NewlyUnlockedTools(5, 6); len(diff) != 0 {
		t.Errorf("NewlyUnlockedTools(5,6) = %v, want empty", diff)
	}
	// score_atom_for_learner arrives with the stage-4 innate kit now.
	diff := NewlyUnlockedTools(3, 4)
	want := []string{"persona_lookup", "score_atom_for_learner"}
	if !reflect.DeepEqual(diff, want) {
		t.Errorf("NewlyUnlockedTools(3,4) = %v, want %v", diff, want)
	}
}
