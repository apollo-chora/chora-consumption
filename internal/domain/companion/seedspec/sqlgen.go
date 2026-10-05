// sqlgen.go — deterministic SQL emission for the 0061 migration seeds.
//
// The migration file embeds the generated blocks between BEGIN/END markers;
// the migration-content test regenerates them from the Go fixtures and
// fails on ANY byte drift — the single-source-of-truth gate between
// seedspec (the spec-pack mirror) and the applied DDL. Descriptions come
// from each sheet's "What" line (IMDA D2: the catalogue page discloses what
// a Skill does).
package seedspec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// skillDescriptions — one learner-language line per Skill (spec sheet
// "What" lines, condensed).
var skillDescriptions = map[string]string{
	"explain_anew":    "Re-explains a concept through analogies and styles tuned to your interests",
	"quiz_me":         "3-5 question micro-quiz pointed at a concept, due items, or weak spots",
	"worked_example":  "Step-by-step walkthrough of a problem type from your theme",
	"socratic_drill":  "Question-first drilling from existing bank questions; never reveals early",
	"flashcard_forge": "Generates flashcards from a concept into your own bank",
	"step_checker":    "Verifies your worked steps and points at the FIRST wrong step",
	"polyglot":        "Re-teaches a concept bilingually in a language you choose",
	"map_sight":       "Answers grounded in YOUR map, resonance-aware, honest about gaps",
	"weakness_sight":  "Explains WHY you are weak somewhere plus concrete next steps",
	"progress_mirror": "Narrates your verified profile: courses, scores, certs, trends",
	"recap_scribe":    "Turns a study session into a short memory note you can see and steer",
	"photo_sight":     "Reads a snapped page of homework or notes into weakness signals and a note",
	"reminder_bell":   "Schedules due-review nudges at Ebbinghaus-optimal times",
	"path_weaver":     "Composes a personalised practice plan from weak, due, and goal",
	"kg_explore":      "Scouts new concepts and edges toward a direction you point",
	"goal_scribe":     "Proposes next-goal candidates at graduation",
	"atom_forge":      "Authors fresh practice atoms on demand into your own bank",
	"study_calendar":  "Exports your review schedule as calendar entries",
	"web_research":    "Grounded web search in a direction you point; returns a cited note",
	"source_reader":   "Reads a URL or document you give it into theme knowledge, with citations",
	"fact_check":      "Verifies a claim with cited sources",
	"duel_second":     "Pre- and post-duel coaching off your duel history",
	"dawn_briefing":   "Prepares a morning digest before you arrive (consented, budgeted)",
	"watchful_eye":    "Watches for new atoms in your theme and refreshes suggestions",
	"long_weaving":    "Craft: raises the Ritual step cap from 5 to 8 for this companion",
	"twin_rituals":    "Craft: raises the enabled-Rituals quota from 2 to 4",
	"weave_mastery":   "Craft: unlocks the interactive node canvas and branching Ritual grammar",
}

// sqlQuote escapes a literal for a single-quoted SQL string.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sqlStringOrNull renders a nullable text column.
func sqlStringOrNull(s string) string {
	if s == "" {
		return "NULL"
	}
	return sqlQuote(s)
}

// CatalogueSeedSQL emits the idempotent companion_skill_catalog v2 seed
// (ON CONFLICT (skill_key) DO NOTHING so post-release `active` flips and
// operator edits survive re-apply).
func CatalogueSeedSQL() string {
	var b strings.Builder
	b.WriteString("INSERT INTO companion_skill_catalog\n")
	b.WriteString("    (skill_key, name, description, tool_handler_ref, skill_kind,\n")
	b.WriteString("     min_growth_stage, slot_cost, family, policy_class, output_sink,\n")
	b.WriteString("     price_key, params_schema, active)\n")
	b.WriteString("VALUES\n")
	entries := Catalogue()
	for i, e := range entries {
		refs, _ := json.Marshal(e.ToolHandlerRefs)
		b.WriteString(fmt.Sprintf("    (%s, %s, %s,\n     %s, %s, %d, %d, %s, %s, %s, %s, '{}'::jsonb, FALSE)",
			sqlQuote(e.SkillKey),
			sqlQuote(e.Name),
			sqlQuote(skillDescriptions[e.SkillKey]),
			sqlQuote(string(refs)),
			sqlQuote(string(e.SkillKind)),
			e.MinGrowthStage,
			e.SlotCost,
			sqlQuote(e.Family),
			sqlQuote(e.PolicyClass),
			sqlStringOrNull(e.OutputSink),
			sqlStringOrNull(e.PriceKey),
		))
		if i < len(entries)-1 {
			b.WriteString(",\n")
		}
	}
	b.WriteString("\nON CONFLICT (skill_key) DO NOTHING;")
	return b.String()
}

// SpeciesPathsSeedSQL emits the idempotent 5-hero species_paths seed
// (version 1, active). ON CONFLICT targets the one-active-per-species
// partial unique index.
func SpeciesPathsSeedSQL() string {
	paths := Paths()
	species := make([]string, 0, len(paths))
	for s := range paths {
		species = append(species, s)
	}
	sort.Strings(species)

	var b strings.Builder
	b.WriteString("INSERT INTO species_paths (species, version, entries, active)\nVALUES\n")
	for i, s := range species {
		entries, _ := json.Marshal(paths[s])
		b.WriteString(fmt.Sprintf("    (%s, 1, %s::jsonb, TRUE)", sqlQuote(s), sqlQuote(string(entries))))
		if i < len(species)-1 {
			b.WriteString(",\n")
		}
	}
	b.WriteString("\nON CONFLICT (species) WHERE active DO NOTHING;")
	return b.String()
}

// PathFloorValidation re-validates every seed Path against the seed
// catalogue under the default bands — called by the CI seed test AND
// available to future authoring tooling (ADR-218 D3 fail-loud gate).
func PathFloorValidation() error {
	catalogue := CatalogueByKey()
	for s, entries := range Paths() {
		p := companion.SpeciesPath{Species: s, Version: 1, Entries: entries, Active: true}
		if err := companion.ValidatePath(p, catalogue, companion.DefaultStageUnlockCounts); err != nil {
			return err
		}
	}
	return nil
}
