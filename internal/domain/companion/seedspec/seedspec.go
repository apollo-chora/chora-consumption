// Package seedspec is the Go mirror of the catalogue-seed SOURCE OF TRUTH
// `docs/FAMILIAR-SKILL-SPECS-2026-07-03.md` (Companion Growth & Grimoire CR,
// ADR-218/219/220): 24 active + 3 craft Skill sheets (§2/§3), the 5 species
// Paths (§4), and the seed conventions (§5).
//
// Consumption migration 0061 seeds companion_skill_catalog + species_paths
// from EXACTLY this data — the migration-content test regenerates the seed
// SQL from these fixtures and fails on any drift, and seedspec_test.go is
// the CI fail-loud Path validation gate (ADR-218 D3). Deviations at build
// time must update the spec doc in the same commit.
//
// P0 seeds everything DARK: every row Active=false (the ADR-174 eval gate
// flips the Launch 7 in P2), prompt_fragment_ref/eval_ref stay NULL until
// real registry fragments + golden suites exist (no fabricated pointers),
// params_schema seeds '{}' at P0; migration 0106 (CHO-2362) authors the real
// per-Skill schemas for every built Skill, generated from
// companion.SkillParamSheets via ParamsSchemaMigrationSQL (drift-pinned).
package seedspec

import "github.com/apollo-chora/chora-consumption/internal/domain/companion"

// CatalogueSize pins the P0 catalogue row count (24 active + 3 craft).
const CatalogueSize = 27

// LaunchSeven — the P2 activation set (CR GQ-9), seeded dark like the rest.
var LaunchSeven = []string{
	"explain_anew", "quiz_me", "socratic_drill", "map_sight",
	"weakness_sight", "reminder_bell", "kg_explore",
}

// P1ActivationThree — the st2 Skills the P1 evals flip active=TRUE (migration
// 0063; CHO-2013 P1.B, R4-1). reminder_bell (a Launch-Seven member) stays
// owned-dark until P2 with its notify.schedule tool. Activation is DATA not
// schema (spec §5 P1-delta #4): the P0 seed stays dark and 0063 is a
// standalone UPDATE — the seedspec Catalogue() is NOT re-flipped, so the 0061
// seed-drift gate stays green. The 0063 drift test pins the UPDATE list to
// this constant.
var P1ActivationThree = []string{"explain_anew", "recap_scribe", "progress_mirror"}

// P2ActivationFive — the CHO-2014 P2 "Fledgling's Kit" launch-remainder set
// (CR §5.5 "Launch 9" part 2, R3-3): quiz_me · map_sight · weakness_sight (st3)
// + socratic_drill · kg_explore (st4). These are LaunchSeven minus the
// P1-activated explain_anew and minus reminder_bell (held at P1 pending its
// notify.schedule tool).
//
// These five are seeded DARK and NOT yet released. Unlike the P1 three (whose
// profile.read + memory.note tools + ADR-174 eval suites shipped in P1.B),
// their backing agent tools (bank.query, kg.read_map, weakness.read,
// qgen.invoke; kg_explore's kg.suggest is not yet a companion-agent tool) and
// their per-Skill ADR-174 eval suites do NOT exist yet. Flipping active=TRUE
// before those land would release un-evaluated, tool-less Skills — bypassing
// the §8 eval gate and yielding an equippable-but-broken loadout — so the
// activation migration is DEFERRED (same gating that held reminder_bell dark
// at P1). This constant pins that eventual migration's UPDATE list so the
// drift test can forbid smuggling an un-launch key into the release.
var P2ActivationFive = []string{"quiz_me", "map_sight", "weakness_sight", "socratic_drill", "kg_explore"}

// P2SightActivationTwo — the FIRST P2 activation wave (CHO-2014 wave A): the two
// st3 "sight" Skills whose invoke-runner builders (buildWeaknessSightTurn /
// buildMapSightTurn), backing readers (weakness.read / kg.read_map), and ADR-174
// honest-empty/gap eval suites have landed. Both are SinkChat narration Skills —
// no answerable pipe needed. Deliberately a SUBSET of P2ActivationFive: quiz_me +
// socratic_drill stay dark pending the answerable quiz-pipe; kg_explore stays dark
// pending its suggestion-inbox write builder. Releasing those now would ship
// tool-less / un-evaluated Skills past the §8 ADR-174 eval gate. Migration 0073's
// UPDATE list is pinned to this constant by TestMigration0073_ActivatesExactlyP2SightTwo.
var P2SightActivationTwo = []string{"weakness_sight", "map_sight"}

// P2AnswerableActivationTwo — the SECOND P2 activation wave (CHO-2016 wave B):
// the two "answerable-pipe" Scholar Skills whose invoke-runner builders
// (buildQuizMeTurn / buildSocraticDrillTurn — RETRIEVE mode only), deterministic
// server-side item picks (weak / concept_ref / due), result_kind="answerable"
// discriminator + items[] channel have landed. quiz_me RETRIEVE draws from the
// learner's EXISTING atoms (generate is unbuilt — pre-declared at 25); the
// learner answers through the EXISTING server-graded session flow (no new submit
// path). Deliberately a SUBSET of P2ActivationFive: kg_explore stays dark pending
// its suggestion-inbox write builder. HELD DARK pending the §8 ADR-174 answerable
// eval gate — migration 0075 mirrors 0073's activation-vehicle structure but does
// NOT flip active=TRUE (the owner drives the eval gate + the flip). This constant
// pins the wave's release contract for the seedspec↔catalogue↔cost_map drift test.
var P2AnswerableActivationTwo = []string{"quiz_me", "socratic_drill"}

// P2ScoutActivationOne — the THIRD (and final) P2 activation wave (wave C): the
// one "suggestion-inbox scout" Weaver Skill, kg_explore (st4), whose GROUNDED-
// RECONCILE invoke-runner builder (buildKgExploreTurn — a deterministic
// map-adjacent topic pool → ONE fenced turn → edgescout.Reconcile hallucination
// floor → SinkSuggestionInbox write) has landed. Deliberately the COMPLEMENT of
// the sight + answerable waves: with kg_explore, the three P2 waves together
// release EXACTLY P2ActivationFive (sight {weakness_sight, map_sight} +
// answerable {quiz_me, socratic_drill} + scout {kg_explore}). HELD DARK pending the
// §8 ADR-174 scout eval gate — migration 0085 mirrors 0082's activation-vehicle
// structure (a standalone data UPDATE, idempotent, reversible, drift-tested) and
// flips active=TRUE only after the owner drives the live eval to a pass. This
// constant pins the wave's release contract for the seedspec↔catalogue↔cost_map
// drift test + the 0085 flip drift test.
var P2ScoutActivationOne = []string{"kg_explore"}

// P5SeekerActivationFactCheck — the FIRST P5 "Far Sight" activation wave
// (CHO-2017, ADR-220): the one Seeker Skill whose grounded-search invoke-runner
// builder has landed this slice, fact_check (st5, external_egress, sink=chat).
// fact_check verifies a learner claim against grounded, CITED web sources reached
// ONLY through chora-model-gateway's grounded-search surface (the single web
// egress; Armor + mana metering central). Deliberately EXACTLY fact_check — the
// sibling Seekers (web_research / source_reader) activate with their own builders
// + waves. HELD DARK pending the §8 ADR-174 external_egress eval gate (citation
// mandate + web-content injection block-rate 1.0); migration 0087 mirrors 0085's
// activation-vehicle structure (a standalone data UPDATE, idempotent, reversible,
// drift-tested) and flips active=TRUE only after the owner drives the live gate to
// a pass. This constant pins the wave's release contract for the
// seedspec↔catalogue↔cost_map drift test + the 0087 flip drift test.
var P5SeekerActivationFactCheck = []string{"fact_check"}

// P5SeekerActivationWebResearch — the SECOND P5 "Far Sight" activation wave
// (CHO-2017, ADR-220): the Seeker Skill whose grounded-search invoke-runner
// builder has landed this slice, web_research ("Far Sight", st5, external_egress,
// price 80). web_research researches a DIRECTION the learner points at against
// grounded, CITED web sources reached ONLY through chora-model-gateway's
// grounded-search surface (the single web egress) and writes a durable, cited
// RESEARCH NOTE to the companion's memory.
//
// ⚠ Owner ruling (2026-07-10): web_research has EXACTLY ONE output sink =
// memory_note. The spec §2.4 sheet listed a SECOND sink (map suggestion inbox via
// kg.suggest, provenance companion_suggested); that two-sink listing was a doc
// error. The 0061 seed + Catalogue() already carry the single memory_note sink;
// the map-suggestion candidate feed is DEFERRED (never built as a second sink —
// that would breach the closed-sink invariant, ADR-218 D9).
//
// Deliberately EXACTLY web_research — the remaining sibling Seeker (source_reader)
// activates with its own builder + wave. HELD DARK pending the §8 ADR-174
// external_egress eval gate (citation mandate + web-content injection block-rate
// 1.0); migration 0088 mirrors 0087's activation-vehicle structure (a standalone
// data UPDATE, idempotent, reversible, drift-tested) and flips active=TRUE only
// after the owner drives the live gate to a pass. This constant pins the wave's
// release contract for the seedspec↔catalogue↔cost_map drift test + the 0088 flip
// drift test.
var P5SeekerActivationWebResearch = []string{"web_research"}

// Catalogue returns the 27 catalogue rows in spec order (§2 family order,
// then §3 craft). Slice is fresh per call — callers may mutate.
func Catalogue() []companion.CatalogEntry {
	return []companion.CatalogEntry{
		// --- Scholar (7) — §2.1 ---
		{SkillKey: "explain_anew", Name: "Explain It Differently", SkillKind: companion.SkillKindActive, MinGrowthStage: 2, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_explain_anew", ToolHandlerRefs: []string{"atom.search", "atom.cite"}},
		{SkillKey: "quiz_me", Name: "Quick Quiz", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_quiz_me", ToolHandlerRefs: []string{"bank.query", "ebbinghaus_state", "qgen.invoke"}},
		{SkillKey: "worked_example", Name: "Worked Example", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_worked_example", ToolHandlerRefs: []string{"atom.search", "atom.cite"}},
		{SkillKey: "socratic_drill", Name: "Socratic Drill", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_socratic_drill", ToolHandlerRefs: []string{"bank.query"}},
		{SkillKey: "flashcard_forge", Name: "Flashcard Forge", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyGenerative, OutputSink: companion.SinkQuestionBank, PriceKey: "companion_skill_flashcard_forge", ToolHandlerRefs: []string{"qgen.invoke"}},
		{SkillKey: "step_checker", Name: "Step Checker", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_step_checker", ToolHandlerRefs: []string{"atom.cite"}},
		{SkillKey: "polyglot", Name: "Polyglot", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilyScholar, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_polyglot", ToolHandlerRefs: []string{"atom.search", "atom.cite"}},
		// --- Sight (5) — §2.2 ---
		{SkillKey: "map_sight", Name: "Map Sight", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilySight, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_map_sight", ToolHandlerRefs: []string{"kg.read_map"}},
		{SkillKey: "weakness_sight", Name: "Weakness Sight", SkillKind: companion.SkillKindActive, MinGrowthStage: 3, SlotCost: 1, Family: companion.FamilySight, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_weakness_sight", ToolHandlerRefs: []string{"weakness.read"}},
		{SkillKey: "progress_mirror", Name: "Progress Mirror", SkillKind: companion.SkillKindActive, MinGrowthStage: 1, SlotCost: 1, Family: companion.FamilySight, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_progress_mirror", ToolHandlerRefs: []string{"profile.read"}},
		{SkillKey: "recap_scribe", Name: "Recap Scribe", SkillKind: companion.SkillKindActive, MinGrowthStage: 2, SlotCost: 1, Family: companion.FamilySight, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkMemoryNote, PriceKey: "companion_skill_recap_scribe", ToolHandlerRefs: []string{}},
		{SkillKey: "photo_sight", Name: "Photo Sight", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilySight, PolicyClass: companion.PolicyGenerative, OutputSink: companion.SinkMemoryNote, PriceKey: "companion_skill_photo_sight", ToolHandlerRefs: []string{"doc.ingest"}},
		// --- Weaver (6) — §2.3 ---
		{SkillKey: "reminder_bell", Name: "Reminder Bell", SkillKind: companion.SkillKindActive, MinGrowthStage: 2, SlotCost: 1, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkNotification, PriceKey: "companion_skill_reminder_bell", ToolHandlerRefs: []string{"notify.schedule"}},
		{SkillKey: "path_weaver", Name: "Path Weaver", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 2, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_path_weaver", ToolHandlerRefs: []string{"path.draft", "weakness.read", "ebbinghaus_state"}},
		{SkillKey: "kg_explore", Name: "Knowledge Explorer", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyGenerative, OutputSink: companion.SinkSuggestionInbox, PriceKey: "companion_skill_kg_explore", ToolHandlerRefs: []string{"kg.suggest"}},
		{SkillKey: "goal_scribe", Name: "Goal Scribe", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_goal_scribe", ToolHandlerRefs: []string{"profile.read", "kg.read_map"}},
		{SkillKey: "atom_forge", Name: "Atom Forge", SkillKind: companion.SkillKindActive, MinGrowthStage: 6, SlotCost: 2, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyGenerative, OutputSink: companion.SinkQuestionBank, PriceKey: "companion_skill_atom_forge", ToolHandlerRefs: []string{"qgen.invoke"}},
		{SkillKey: "study_calendar", Name: "Study Calendar", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyWeaver, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkCalendarArtifact, PriceKey: "companion_skill_study_calendar", ToolHandlerRefs: []string{"ebbinghaus_state"}},
		// --- Seeker (3) — §2.4 ---
		{SkillKey: "web_research", Name: "Far Sight", SkillKind: companion.SkillKindActive, MinGrowthStage: 5, SlotCost: 2, Family: companion.FamilySeeker, PolicyClass: companion.PolicyExternalEgress, OutputSink: companion.SinkMemoryNote, PriceKey: "companion_skill_web_research", ToolHandlerRefs: []string{"gateway.grounded_search", "kg.suggest"}},
		{SkillKey: "source_reader", Name: "Source Reader", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilySeeker, PolicyClass: companion.PolicyExternalEgress, OutputSink: companion.SinkMemoryNote, PriceKey: "companion_skill_source_reader", ToolHandlerRefs: []string{"doc.ingest"}},
		{SkillKey: "fact_check", Name: "Fact Check", SkillKind: companion.SkillKindActive, MinGrowthStage: 5, SlotCost: 1, Family: companion.FamilySeeker, PolicyClass: companion.PolicyExternalEgress, OutputSink: companion.SinkChat, PriceKey: "companion_skill_fact_check", ToolHandlerRefs: []string{"gateway.grounded_search"}},
		// --- Companion (1) — §2.5 ---
		{SkillKey: "duel_second", Name: "Duel Second", SkillKind: companion.SkillKindActive, MinGrowthStage: 4, SlotCost: 1, Family: companion.FamilyCompanion, PolicyClass: companion.PolicyStandard, OutputSink: companion.SinkChat, PriceKey: "companion_skill_duel_second", ToolHandlerRefs: []string{"duel.read"}},
		// --- Habit (2) — §2.6, autonomy P6 ---
		{SkillKey: "dawn_briefing", Name: "Dawn Briefing", SkillKind: companion.SkillKindActive, MinGrowthStage: 5, SlotCost: 2, Family: companion.FamilyHabit, PolicyClass: companion.PolicyAutonomy, OutputSink: companion.SinkNotification, PriceKey: "companion_skill_dawn_briefing", ToolHandlerRefs: []string{"ebbinghaus_state", "profile.read", "kg.suggest"}},
		{SkillKey: "watchful_eye", Name: "Watchful Eye", SkillKind: companion.SkillKindActive, MinGrowthStage: 5, SlotCost: 2, Family: companion.FamilyHabit, PolicyClass: companion.PolicyAutonomy, OutputSink: companion.SinkSuggestionInbox, PriceKey: "companion_skill_watchful_eye", ToolHandlerRefs: []string{"kg.suggest"}},
		// --- Craft (3) — §3, slot-free designer grammar ---
		{SkillKey: "long_weaving", Name: "Long Weaving", SkillKind: companion.SkillKindCraft, MinGrowthStage: 5, SlotCost: 0, Family: companion.FamilyCraft, PolicyClass: companion.PolicyStandard, OutputSink: "", PriceKey: "", ToolHandlerRefs: []string{}},
		{SkillKey: "twin_rituals", Name: "Twin Rituals", SkillKind: companion.SkillKindCraft, MinGrowthStage: 5, SlotCost: 0, Family: companion.FamilyCraft, PolicyClass: companion.PolicyStandard, OutputSink: "", PriceKey: "", ToolHandlerRefs: []string{}},
		{SkillKey: "weave_mastery", Name: "Weave Mastery", SkillKind: companion.SkillKindCraft, MinGrowthStage: 6, SlotCost: 0, Family: companion.FamilyCraft, PolicyClass: companion.PolicyStandard, OutputSink: "", PriceKey: "", ToolHandlerRefs: []string{}},
	}
}

// CatalogueByKey returns the catalogue indexed by skill_key.
func CatalogueByKey() map[string]companion.CatalogEntry {
	entries := Catalogue()
	out := make(map[string]companion.CatalogEntry, len(entries))
	for _, e := range entries {
		out[e.SkillKey] = e
	}
	return out
}

// Paths returns the 5 species Paths (spec §4 table, read column-wise:
// 27 ordered skill keys per species; bands 1/3/4/6/7/6 at stages 1..6).
// ADR-228 D3 (F-I2): every Path OPENS on progress_mirror — the st1 band (K=1)
// that the EXP-gated hatch (stage 0→1) mints auto-equipped. The remaining st2
// trio and every later entry keep their pre-F-I2 relative order, so ONLY
// progress_mirror changed band (species differentiation still starts at st2).
func Paths() map[string][]string {
	return map[string][]string{
		"owl": {
			"progress_mirror", "explain_anew", "recap_scribe", "reminder_bell",
			"worked_example", "map_sight", "quiz_me", "weakness_sight",
			"step_checker", "socratic_drill", "source_reader", "flashcard_forge", "path_weaver", "polyglot",
			"fact_check", "web_research", "long_weaving", "photo_sight", "study_calendar", "goal_scribe", "twin_rituals",
			"watchful_eye", "dawn_briefing", "kg_explore", "duel_second", "atom_forge", "weave_mastery",
		},
		"fox": {
			"progress_mirror", "explain_anew", "recap_scribe", "reminder_bell",
			"map_sight", "quiz_me", "polyglot", "worked_example",
			"kg_explore", "goal_scribe", "weakness_sight", "photo_sight", "socratic_drill", "study_calendar",
			"web_research", "fact_check", "source_reader", "twin_rituals", "long_weaving", "path_weaver", "watchful_eye",
			"dawn_briefing", "flashcard_forge", "step_checker", "duel_second", "atom_forge", "weave_mastery",
		},
		"dragon": {
			"progress_mirror", "explain_anew", "reminder_bell", "recap_scribe",
			"quiz_me", "step_checker", "weakness_sight", "worked_example",
			"socratic_drill", "path_weaver", "goal_scribe", "flashcard_forge", "duel_second", "map_sight",
			"web_research", "fact_check", "dawn_briefing", "twin_rituals", "long_weaving", "photo_sight", "kg_explore",
			"watchful_eye", "source_reader", "study_calendar", "polyglot", "atom_forge", "weave_mastery",
		},
		"phoenix": {
			"progress_mirror", "recap_scribe", "reminder_bell", "explain_anew",
			"weakness_sight", "worked_example", "quiz_me", "step_checker",
			"photo_sight", "socratic_drill", "path_weaver", "flashcard_forge", "map_sight", "goal_scribe",
			"dawn_briefing", "watchful_eye", "long_weaving", "twin_rituals", "web_research", "study_calendar", "source_reader",
			"fact_check", "kg_explore", "duel_second", "polyglot", "atom_forge", "weave_mastery",
		},
		"penguin": {
			"progress_mirror", "reminder_bell", "recap_scribe", "explain_anew",
			"quiz_me", "worked_example", "map_sight", "polyglot",
			"study_calendar", "duel_second", "weakness_sight", "socratic_drill", "flashcard_forge", "goal_scribe",
			"dawn_briefing", "watchful_eye", "twin_rituals", "long_weaving", "path_weaver", "step_checker", "source_reader",
			"web_research", "fact_check", "photo_sight", "kg_explore", "atom_forge", "weave_mastery",
		},
	}
}
