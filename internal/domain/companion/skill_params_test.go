// skill_params_test.go - RED-first for CHO-2362 (skills editor: schema-driven
// per-step Skill params). Exercises the domain param-sheet table for the 11
// built Skills (the single validation authority the invoke adapter, the ritual
// publish/run gates, the 0106 catalogue seed and the FE schema all derive
// from), the value-validation gate, and the deterministic JSONB rendering.
//
// These reference symbols that do NOT exist yet (SkillParamSheets,
// ValidateSkillParamValues, SkillParamsSchemaJSON, ErrRitualStepParamsInvalid)
// - the package fails to COMPILE, which is the RED signal (house convention).
package companion

import (
	"errors"
	"strings"
	"testing"
)

func TestSkillParamSheetsCoverExactlyTheBuiltSkills(t *testing.T) {
	// Every skill with an invoke builder carries a sheet…
	for _, key := range []string{
		"progress_mirror", "recap_scribe", "explain_anew",
		"weakness_sight", "map_sight",
		"quiz_me", "socratic_drill", "kg_explore",
		"fact_check", "web_research",
	} {
		if _, ok := SkillParamSheets[key]; !ok {
			t.Errorf("SkillParamSheets missing built skill %q", key)
		}
	}
	// …and no sheet exists for a skill without a builder (a sheet on an
	// unbuilt skill would render an editor for machinery that cannot run).
	for key := range SkillParamSheets {
		switch key {
		case "progress_mirror", "recap_scribe", "explain_anew",
			"weakness_sight", "map_sight",
			"quiz_me", "socratic_drill", "kg_explore",
			"fact_check", "web_research":
		default:
			t.Errorf("SkillParamSheets carries %q, which has no invoke builder", key)
		}
	}
}

func TestSkillParamSheetSpotChecks(t *testing.T) {
	find := func(key, name string) SkillParam {
		t.Helper()
		for _, p := range SkillParamSheets[key] {
			if p.Name == name {
				return p
			}
		}
		t.Fatalf("sheet %q has no param %q", key, name)
		return SkillParam{}
	}

	// explain_anew - target is the invoke-required concept/atom ref; style +
	// length are closed enums with defaults (spec §2.1).
	target := find("explain_anew", "target")
	if target.Kind != SkillParamConceptRef || !target.Required {
		t.Errorf("explain_anew.target = %+v, want required concept_ref", target)
	}
	style := find("explain_anew", "style")
	if style.Kind != SkillParamEnum || len(style.Values) != 5 || style.Default != "analogy" {
		t.Errorf("explain_anew.style = %+v, want 5-value enum default analogy", style)
	}

	// quiz_me - count is a bounded int 3..5 default 3 (spec §2.2).
	count := find("quiz_me", "count")
	if count.Kind != SkillParamInt || count.Min != 3 || count.Max != 5 || count.Default != "3" {
		t.Errorf("quiz_me.count = %+v, want int 3..5 default 3", count)
	}

	// socratic_drill - rounds 3..7 (spec §2.3); scope has NO "due".
	rounds := find("socratic_drill", "rounds")
	if rounds.Kind != SkillParamInt || rounds.Min != 3 || rounds.Max != 7 {
		t.Errorf("socratic_drill.rounds = %+v, want int 3..7", rounds)
	}
	scope := find("socratic_drill", "scope")
	for _, v := range scope.Values {
		if v == "due" {
			t.Error("socratic_drill.scope must not offer 'due' (invoke contract)")
		}
	}

	// weakness_sight - edge is an OPTIONAL growth-edge ref (empty = auto-top).
	edge := find("weakness_sight", "edge")
	if edge.Kind != SkillParamGrowthEdgeRef || edge.Required {
		t.Errorf("weakness_sight.edge = %+v, want optional growth_edge_ref", edge)
	}

	// fact_check / web_research - bounded free text (Armor-INSPECTed upstream).
	claim := find("fact_check", "claim")
	if claim.Kind != SkillParamText || claim.MaxLen != 200 || !claim.Required {
		t.Errorf("fact_check.claim = %+v, want required text maxLen 200", claim)
	}
	direction := find("web_research", "direction")
	if direction.Kind != SkillParamText || direction.MaxLen != 120 || !direction.Required {
		t.Errorf("web_research.direction = %+v, want required text maxLen 120", direction)
	}
}

func TestValidateSkillParamValues(t *testing.T) {
	uuid := "0198c0de-0000-7000-8000-000000000001"

	cases := []struct {
		name    string
		skill   string
		params  map[string]any
		wantErr bool
	}{
		{"nil params fine", "explain_anew", nil, false},
		{"empty params fine", "explain_anew", map[string]any{}, false},
		{"good enum", "explain_anew", map[string]any{"style": "story"}, false},
		{"bad enum", "explain_anew", map[string]any{"style": "sarcastic"}, true},
		{"unknown key", "explain_anew", map[string]any{"tone": "warm"}, true},
		{"good ref", "explain_anew", map[string]any{"target": uuid}, false},
		{"bad ref shape", "explain_anew", map[string]any{"target": "not-a-uuid"}, true},
		{"empty string is absent", "explain_anew", map[string]any{"style": ""}, false},
		{"int as string in range", "quiz_me", map[string]any{"count": "4"}, false},
		{"int as json number in range", "quiz_me", map[string]any{"count": float64(5)}, false},
		{"int out of range", "quiz_me", map[string]any{"count": "9"}, true},
		{"int junk", "quiz_me", map[string]any{"count": "many"}, true},
		{"int fractional json number", "quiz_me", map[string]any{"count": 3.5}, true},
		{"text within cap", "fact_check", map[string]any{"claim": "The Moon has no atmosphere."}, false},
		{"text over cap by runes", "fact_check", map[string]any{"claim": strings.Repeat("é", 201)}, true},
		{"text multibyte at cap", "fact_check", map[string]any{"claim": strings.Repeat("é", 200)}, false},
		{"non-string enum value", "explain_anew", map[string]any{"style": 7}, true},
		{"params on a sheetless skill", "flashcard_forge", map[string]any{"count": "5"}, true},
		{"no params on a sheetless skill fine", "flashcard_forge", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSkillParamValues(tc.skill, tc.params)
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if err != nil && !errors.Is(err, ErrRitualStepParamsInvalid) {
				t.Errorf("err = %v, want ErrRitualStepParamsInvalid sentinel", err)
			}
		})
	}
}

func TestSkillParamsSchemaJSONDeterministic(t *testing.T) {
	// explain_anew renders in sheet order with only the set fields - the exact
	// bytes the 0106 seed and the loadout response carry (drift-pinned).
	want := `{"target":{"type":"concept_ref","required":true},` +
		`"style":{"type":"enum","values":["analogy","story","eli5","contrast","visual"],"default":"analogy"},` +
		`"length":{"type":"enum","values":["short","full"],"default":"short"}}`
	got := string(SkillParamsSchemaJSON("explain_anew"))
	if got != want {
		t.Errorf("explain_anew schema JSON:\n got %s\nwant %s", got, want)
	}

	// A bounded int renders min/max; defaults stay strings (wire params are
	// strings - the FE parses for its stepper).
	wantQuiz := `{"scope":{"type":"enum","values":["weak","concept_ref","due"],"default":"weak"},` +
		`"count":{"type":"int","min":3,"max":5,"default":"3"},` +
		`"mode":{"type":"enum","values":["retrieve","generate"],"default":"retrieve"},` +
		`"concept":{"type":"concept_ref"}}`
	if got := string(SkillParamsSchemaJSON("quiz_me")); got != wantQuiz {
		t.Errorf("quiz_me schema JSON:\n got %s\nwant %s", got, wantQuiz)
	}

	// A sheetless skill renders nothing (the response omits the field).
	if b := SkillParamsSchemaJSON("flashcard_forge"); b != nil {
		t.Errorf("sheetless skill must render nil schema, got %s", b)
	}
}
