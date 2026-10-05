// curve_test.go — RED phase for the EXP curve + stage thresholds.
//
// ADR-149 §"The 7 stages" cumulative thresholds:
//
//	Stage 0 = 0 (Egg)
//	Stage 1 = 0 (Baby, post-hatch)
//	Stage 2 = 50
//	Stage 3 = 200
//	Stage 4 = 500
//	Stage 5 = 1200
//	Stage 6 = 3000
package growth_test

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestStageForExp_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		exp  int
		want int
	}{
		{"zero is stage 0 or 1 depending on hatched", 0, 1},
		{"just below stage 2", 49, 1},
		{"exactly stage 2", 50, 2},
		{"just below stage 3", 199, 2},
		{"exactly stage 3", 200, 3},
		{"between 3 and 4", 250, 3},
		{"exactly stage 4", 500, 4},
		{"exactly stage 5", 1200, 5},
		{"just below stage 6", 2999, 5},
		{"exactly stage 6", 3000, 6},
		{"far beyond 6", 999999, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := growth.StageForExp(c.exp)
			if got != c.want {
				t.Errorf("StageForExp(%d) = %d, want %d", c.exp, got, c.want)
			}
		})
	}
}

func TestNextThreshold(t *testing.T) {
	cases := []struct {
		stage int
		want  int
	}{
		{0, 0}, // egg has no growth-exp curve
		{1, 50},
		{2, 200},
		{3, 500},
		{4, 1200},
		{5, 3000},
		{6, 0}, // maxed out
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			got := growth.NextThreshold(c.stage)
			if got != c.want {
				t.Errorf("NextThreshold(%d) = %d, want %d", c.stage, got, c.want)
			}
		})
	}
}

func TestStageName(t *testing.T) {
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
		{99, ""},
		{-1, ""},
	}
	for _, c := range cases {
		got := growth.StageName(c.stage)
		if got != c.want {
			t.Errorf("StageName(%d) = %q, want %q", c.stage, got, c.want)
		}
	}
}

func TestDailyCapForSource(t *testing.T) {
	// Per ADR-149 §EXP economy table.
	cases := []struct {
		source string
		want   int
	}{
		{"atom_session", 30},
		{"ebbinghaus_review", 15},
		{"hex_expand", 12},
		{"conv_turn", 10},
		{"daily_dose_open", 2},
		{"social_share", 16},
		{"social_reaction", 10},
		{"junction_accepted", 15},
		{"atom_authored", 40},
		{"admin_grant", 0}, // 0 = no cap; admin pathway
		{"hatch_roll", 0},
		{"unknown_source", -1},
	}
	for _, c := range cases {
		got := growth.DailyCapForSource(c.source)
		if got != c.want {
			t.Errorf("DailyCapForSource(%q) = %d, want %d", c.source, got, c.want)
		}
	}
}

func TestExpPerEventForSource(t *testing.T) {
	// Per ADR-149 §EXP economy. Returns EXP/event for the canonical happy-path
	// per source (when the AwardExp delta is not explicitly supplied by the
	// runtime — subscribers use these defaults).
	cases := []struct {
		source string
		want   int
	}{
		{"atom_session", 3},
		{"ebbinghaus_review", 5},
		{"hex_expand", 4},
		{"conv_turn", 2},
		{"daily_dose_open", 2},
		{"social_share", 8},
		{"social_reaction", 1},
		{"junction_accepted", 15},
		{"atom_authored", 20},
		{"admin_grant", 0},
		{"hatch_roll", 0},
	}
	for _, c := range cases {
		got := growth.ExpPerEventForSource(c.source)
		if got != c.want {
			t.Errorf("ExpPerEventForSource(%q) = %d, want %d", c.source, got, c.want)
		}
	}
}

func TestClampDelta(t *testing.T) {
	// Source has cap=30. Already awarded 27. Request 5 → clamped to 3.
	clamped, capHit := growth.ClampDelta("atom_session", 27, 5)
	if clamped != 3 {
		t.Errorf("clamped = %d, want 3", clamped)
	}
	if !capHit {
		t.Errorf("capHit should be true")
	}

	// Already at cap.
	clamped, capHit = growth.ClampDelta("atom_session", 30, 1)
	if clamped != 0 {
		t.Errorf("clamped = %d, want 0", clamped)
	}
	if !capHit {
		t.Errorf("capHit should be true (already at cap)")
	}

	// Well below cap, no clamp.
	clamped, capHit = growth.ClampDelta("atom_session", 10, 3)
	if clamped != 3 {
		t.Errorf("clamped = %d, want 3 (no clamp)", clamped)
	}
	if capHit {
		t.Errorf("capHit should be false")
	}

	// Unknown source returns no clamp (defensive — caller still gets the
	// full delta but the subscriber path filters unknowns out earlier).
	clamped, capHit = growth.ClampDelta("unknown", 10, 100)
	if clamped != 100 {
		t.Errorf("unknown source clamped = %d, want 100", clamped)
	}
	if capHit {
		t.Errorf("unknown source capHit should be false")
	}

	// admin_grant + hatch_roll (cap=0) means no cap — full delta passes.
	clamped, _ = growth.ClampDelta("admin_grant", 9999, 50)
	if clamped != 50 {
		t.Errorf("admin_grant cap=0 clamped = %d, want 50", clamped)
	}
}

func TestIsValidSource(t *testing.T) {
	valid := []string{
		"atom_session", "ebbinghaus_review", "hex_expand", "conv_turn",
		"daily_dose_open", "social_share", "social_reaction",
		"junction_accepted", "atom_authored", "admin_grant", "hatch_roll",
	}
	for _, s := range valid {
		if !growth.IsValidSource(s) {
			t.Errorf("IsValidSource(%q) = false, want true", s)
		}
	}
	if growth.IsValidSource("garbage") {
		t.Errorf("IsValidSource(garbage) = true, want false")
	}
	if growth.IsValidSource("") {
		t.Errorf("IsValidSource(empty) = true, want false")
	}
}

func TestStateAfterAward(t *testing.T) {
	// Starting at stage 1, 40 EXP, award 15 → crosses 50 → stage 2.
	s := growth.StateAfterAward(1, 40, 15)
	if s.NewStage != 2 {
		t.Errorf("NewStage = %d, want 2", s.NewStage)
	}
	if !s.StageUp {
		t.Errorf("StageUp should be true")
	}
	if s.NewExp != 55 {
		t.Errorf("NewExp = %d, want 55", s.NewExp)
	}

	// No stage-up.
	s = growth.StateAfterAward(2, 60, 5)
	if s.NewStage != 2 {
		t.Errorf("NewStage = %d, want 2", s.NewStage)
	}
	if s.StageUp {
		t.Errorf("StageUp should be false")
	}
	if s.NewExp != 65 {
		t.Errorf("NewExp = %d, want 65", s.NewExp)
	}

	// Multiple boundaries crossed in one award (rare but possible — Stage 2
	// → 4 by awarding 500 to a Stage 2 Companion with 50 EXP).
	s = growth.StateAfterAward(2, 50, 450)
	if s.NewStage != 4 {
		t.Errorf("multi-cross NewStage = %d, want 4", s.NewStage)
	}
	if !s.StageUp {
		t.Errorf("multi-cross StageUp should be true")
	}
}

func TestLLMTierForStageAndMana(t *testing.T) {
	// Per ADR-149 §"Mana × Growth LLM matrix":
	//   stages         0     1     2      3      4     5     6
	// basic         lite  lite  lite  lite¹  lite  lite  lite²
	// standard      lite  lite  lite  flash³ flash flash pro
	// premium       lite  lite  flash flash  flash pro   pro
	cases := []struct {
		stage    int
		manaTier string
		want     string
	}{
		{0, "basic", "flash-lite"},
		{6, "basic", "flash-lite"},
		{3, "basic", "flash-lite"},
		{3, "standard", "flash"},
		{6, "standard", "pro"},
		{2, "premium", "flash"},
		{5, "premium", "pro"},
		{6, "premium", "pro"},
		// Unknown mana tier → conservative basic.
		{3, "garbage", "flash-lite"},
	}
	for _, c := range cases {
		got := growth.LLMTierForStageAndMana(c.stage, c.manaTier)
		if got != c.want {
			t.Errorf("LLMTierForStageAndMana(%d,%q) = %q, want %q",
				c.stage, c.manaTier, got, c.want)
		}
	}
}

func TestPreviewLLMTierForAhaMoment(t *testing.T) {
	// Per ADR-149: Stage-3 Source Revelation preview LLM tier — Basic gets
	// flash (one notch up), Standard + Premium get pro.
	cases := []struct {
		manaTier string
		want     string
	}{
		{"basic", "flash"},
		{"standard", "pro"},
		{"premium", "pro"},
		{"garbage", "flash"},
	}
	for _, c := range cases {
		got := growth.PreviewLLMTierForAhaMoment(c.manaTier)
		if got != c.want {
			t.Errorf("PreviewLLMTierForAhaMoment(%q) = %q, want %q",
				c.manaTier, got, c.want)
		}
	}
}

func TestUnlockedToolsForStage(t *testing.T) {
	// Per ADR-149 §"The 7 stages" tools column.
	cases := []struct {
		stage int
		// must-contain tools (order not asserted; superset OK)
		musts []string
	}{
		{0, []string{}},
		{1, []string{"cite_atom"}},
		{2, []string{"cite_atom", "atom_search"}},
		{3, []string{"cite_atom", "atom_search", "ebbinghaus_state"}},
		{4, []string{"cite_atom", "atom_search", "ebbinghaus_state", "persona_lookup", "score_atom_for_learner"}},
		// ADR-218 D1 (CHO-2012): the innate kit plateaus at stage 4 —
		// query_kg + the st6 meta-tools moved to the equippable catalogue
		// (map_sight / atom_forge / kg_explore st6 ext). curve_innate_test.go
		// asserts the retired names are gone.
		{5, []string{"score_atom_for_learner"}},
		{6, []string{"score_atom_for_learner"}},
	}
	for _, c := range cases {
		got := growth.UnlockedToolsForStage(c.stage)
		set := make(map[string]bool, len(got))
		for _, t := range got {
			set[t] = true
		}
		for _, m := range c.musts {
			if !set[m] {
				t.Errorf("stage %d missing tool %q (got %v)", c.stage, m, got)
			}
		}
	}
}

func TestNewlyUnlockedTools(t *testing.T) {
	// Stage 2 → 3 unlocks ebbinghaus_state (delta only).
	got := growth.NewlyUnlockedTools(2, 3)
	found := false
	for _, t := range got {
		if t == "ebbinghaus_state" {
			found = true
		}
	}
	if !found {
		t.Errorf("Stage 2→3 should newly-unlock ebbinghaus_state, got %v", got)
	}
}

func TestMemoryModeForStage(t *testing.T) {
	cases := []struct {
		stage int
		want  string
	}{
		{0, "stateless"},
		{1, "rolling-1"},
		{2, "rolling-3"},
		{3, "rolling-7"},
		{4, "rolling-21-rag"},
		{5, "vector-rag-all"},
		{6, "full-procedural"},
	}
	for _, c := range cases {
		got := growth.MemoryModeForStage(c.stage)
		if got != c.want {
			t.Errorf("MemoryModeForStage(%d) = %q, want %q", c.stage, got, c.want)
		}
	}
}
