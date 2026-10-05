// skill_params.go - CHO-2362 (skills editor): the per-Skill param sheets for
// every Skill with an invoke-runner builder, transcribed from the spec pack
// docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §2 sheets + §5.5 params model. This
// table is the SINGLE validation authority: the single-invoke adapter derives
// its closed specs from it, the ritual publish/run gates validate step values
// against it, the 0106 catalogue seed is generated from it (drift-pinned), and
// the loadout response renders it for the FE editor. One source, no dual
// authority.
//
// Ritual semantics deliberately differ from invoke in ONE way: params stay
// OPTIONAL in ritual steps (absent → the Skill runs on its ritual-context
// default behaviour - the walked-live status quo). Required-ness is an
// invoke-path contract; the schema still carries it so the invoke adapter and
// the FE's "point this Skill" nudge stay truthful.
package companion

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SkillParamKind is the closed set of param value shapes (spec §5.5).
type SkillParamKind string

const (
	SkillParamEnum          SkillParamKind = "enum"
	SkillParamInt           SkillParamKind = "int"
	SkillParamText          SkillParamKind = "text"
	SkillParamConceptRef    SkillParamKind = "concept_ref"
	SkillParamGrowthEdgeRef SkillParamKind = "growth_edge_ref"
)

// SkillParam is one bounded parameter on a Skill sheet. Defaults are wire
// strings (params ride as strings; the FE parses for its controls).
type SkillParam struct {
	Name     string
	Kind     SkillParamKind
	Values   []string // enum only - closed membership
	Min, Max int      // int only - inclusive bounds
	MaxLen   int      // text only - rune cap (Armor-INSPECTed upstream)
	Default  string   // "" = no default
	Required bool     // invoke-path contract; a nudge (never a block) in rituals
}

// SkillParamSheets - ordered sheets for the built Skills, keyed by skill_key.
// A key appears here IFF the skill has an invoke-runner builder: a sheet on an
// unbuilt skill would render an editor for machinery that cannot run, and a
// builder without a sheet leaves its params surface unclosed (the sheet-vs-
// builder parity test pins both directions).
var SkillParamSheets = map[string][]SkillParam{
	"progress_mirror": {
		{Name: "window", Kind: SkillParamEnum, Values: []string{"week", "month", "all"}, Default: "all"},
	},
	"recap_scribe": {
		{Name: "session", Kind: SkillParamEnum, Values: []string{"current", "last"}, Default: "current"},
	},
	"explain_anew": {
		// target accepts a concept ref OR an atom ref on the wire (same UUID
		// shape); the editor offers the learner's concept graph.
		{Name: "target", Kind: SkillParamConceptRef, Required: true},
		{Name: "style", Kind: SkillParamEnum, Values: []string{"analogy", "story", "eli5", "contrast", "visual"}, Default: "analogy"},
		{Name: "length", Kind: SkillParamEnum, Values: []string{"short", "full"}, Default: "short"},
	},
	"weakness_sight": {
		{Name: "edge", Kind: SkillParamGrowthEdgeRef}, // empty = auto-top edge
	},
	"map_sight": {
		{Name: "focus", Kind: SkillParamConceptRef}, // empty = resonant centre
	},
	"quiz_me": {
		{Name: "scope", Kind: SkillParamEnum, Values: []string{"weak", "concept_ref", "due"}, Default: "weak"},
		{Name: "count", Kind: SkillParamInt, Min: 3, Max: 5, Default: "3"},
		{Name: "mode", Kind: SkillParamEnum, Values: []string{"retrieve", "generate"}, Default: "retrieve"},
		{Name: "concept", Kind: SkillParamConceptRef},
	},
	"socratic_drill": {
		{Name: "scope", Kind: SkillParamEnum, Values: []string{"weak", "concept_ref"}, Default: "weak"},
		{Name: "rounds", Kind: SkillParamInt, Min: 3, Max: 7, Default: "3"},
		{Name: "concept", Kind: SkillParamConceptRef},
	},
	"kg_explore": {
		{Name: "count", Kind: SkillParamInt, Min: 3, Max: 5, Default: "3"},
	},
	"fact_check": {
		{Name: "claim", Kind: SkillParamText, MaxLen: 200, Required: true},
	},
	"web_research": {
		// direction also accepts a concept ref UUID (36 chars fit the cap);
		// resolution happens in the builder.
		{Name: "direction", Kind: SkillParamText, MaxLen: 120, Required: true},
		{Name: "depth", Kind: SkillParamEnum, Values: []string{"survey", "deep"}, Default: "survey"},
	},
}

// ErrRitualStepParamsInvalid is the named sentinel for every param-value
// rejection - mapped to 422 at the HTTP boundary alongside the other grammar
// errors.
var ErrRitualStepParamsInvalid = errors.New("companion.ritual: step params invalid")

// ValidateSkillParamValues checks the PRESENT params of one step against the
// skill's sheet: unknown keys, non-string shapes, enum membership, int bounds,
// rune-capped text, and UUID-shaped refs all fail loud (these values ride into
// the prompt - the surface stays closed). Absent params and empty strings are
// fine (absent-equivalent). A skill with no sheet accepts NO params at all.
func ValidateSkillParamValues(skillKey string, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	sheet, ok := SkillParamSheets[skillKey]
	if !ok {
		return fmt.Errorf("%w: skill %q accepts no params", ErrRitualStepParamsInvalid, skillKey)
	}
	byName := make(map[string]SkillParam, len(sheet))
	for _, p := range sheet {
		byName[p.Name] = p
	}
	// Deterministic error order for a stable 422 detail.
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		p, known := byName[name]
		if !known {
			return fmt.Errorf("%w: unknown param %q for skill %q", ErrRitualStepParamsInvalid, name, skillKey)
		}
		if err := validateParamValue(p, params[name]); err != nil {
			return fmt.Errorf("%w: param %q of skill %q %s", ErrRitualStepParamsInvalid, name, skillKey, err)
		}
	}
	return nil
}

// validateParamValue checks one value against its sheet entry. Returns a plain
// (unwrapped) description - the caller attaches the sentinel + context.
func validateParamValue(p SkillParam, v any) error {
	// Ints tolerate JSON numbers (a decoded body hands float64) alongside the
	// wire-canonical numeric string.
	if p.Kind == SkillParamInt {
		n, err := paramInt(v)
		if err != nil {
			return err
		}
		if n == nil { // empty string - absent-equivalent
			return nil
		}
		if *n < p.Min || *n > p.Max {
			return fmt.Errorf("must be between %d and %d", p.Min, p.Max)
		}
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("must be a string")
	}
	s = strings.TrimSpace(s)
	if s == "" { // absent-equivalent
		return nil
	}
	switch p.Kind {
	case SkillParamEnum:
		for _, allowed := range p.Values {
			if s == allowed {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v", p.Values)
	case SkillParamText:
		if utf8.RuneCountInString(s) > p.MaxLen {
			return fmt.Errorf("must be at most %d characters", p.MaxLen)
		}
		return nil
	case SkillParamConceptRef, SkillParamGrowthEdgeRef:
		if !paramUUIDShaped(s) {
			return fmt.Errorf("must be a UUID ref")
		}
		return nil
	default:
		return fmt.Errorf("has an unknown kind %q", p.Kind)
	}
}

// paramInt coerces an int param value: numeric string, or a WHOLE JSON number.
// (nil, nil) means absent-equivalent (empty string).
func paramInt(v any) (*int, error) {
	switch n := v.(type) {
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return nil, nil
		}
		i, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("must be a whole number")
		}
		return &i, nil
	case float64:
		i := int(n)
		if float64(i) != n {
			return nil, fmt.Errorf("must be a whole number")
		}
		return &i, nil
	case int:
		i := n
		return &i, nil
	default:
		return nil, fmt.Errorf("must be a whole number")
	}
}

// paramUUIDShaped mirrors the invoke adapter's ref discipline: hyphenated
// 8-4-4-4-12 hex.
func paramUUIDShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// SkillParamsSchemaJSON renders one skill's sheet as the deterministic JSONB
// object the 0106 seed, the loadout response, and the FE editor all share:
// {"<name>":{"type":…,"values":[…],"min":…,"max":…,"maxLen":…,"default":…,
// "required":true}} in sheet order, only set fields emitted. Keys are chosen
// camel-safe (the CompanionBridge camelises response bodies recursively - every
// key here survives that unchanged). Returns nil for a sheetless skill.
func SkillParamsSchemaJSON(skillKey string) []byte {
	sheet, ok := SkillParamSheets[skillKey]
	if !ok {
		return nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range sheet {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:{%q:%q", p.Name, "type", string(p.Kind))
		if p.Kind == SkillParamEnum {
			b.WriteString(`,"values":[`)
			for j, v := range p.Values {
				if j > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%q", v)
			}
			b.WriteByte(']')
		}
		if p.Kind == SkillParamInt {
			fmt.Fprintf(&b, `,"min":%d,"max":%d`, p.Min, p.Max)
		}
		if p.Kind == SkillParamText {
			fmt.Fprintf(&b, `,"maxLen":%d`, p.MaxLen)
		}
		if p.Default != "" {
			fmt.Fprintf(&b, `,"default":%q`, p.Default)
		}
		if p.Required {
			b.WriteString(`,"required":true`)
		}
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return []byte(b.String())
}
