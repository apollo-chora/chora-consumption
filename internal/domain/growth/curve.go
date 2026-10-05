// Package growth implements the ADR-149 Companion Growth Stage Model — a
// per-Companion 7-stage axis driven by EXP from 7 source topics, bounded by
// the user's mana_tier ceiling per ADR-142.
//
// This file owns the pure curve mechanics: stage thresholds, per-source EXP
// per event + daily caps, the mana × growth LLM matrix, per-stage tool sets,
// memory modes, and stage-up decision rules.
//
// All values come from configuration tables hardcoded against ADR-149 §"EXP
// economy" + §"The 7 stages" + §"Mana × Growth LLM matrix". No env reads —
// per `feedback_no_inline_config`, env-aware glue lives in cmd/server.
package growth

import "sort"

// Canonical breed-neutral stage names per ADR-149.
const (
	StageEgg        = 0 // pre-hatch
	StageBaby       = 1
	StageFledgling  = 2
	StageAwakened   = 3
	StageStructural = 4
	StageTeen       = 5
	StageMatured    = 6
)

// stageThresholds is the cumulative EXP needed to ENTER each stage. Index =
// stage. Stage 0 + Stage 1 both sit at 0 (Stage 1 is "post-hatch baby"; Stage
// 0 is the pre-hatch egg — the curve only applies after hatching).
var stageThresholds = [7]int{
	0,    // 0 Egg
	0,    // 1 Baby (post-hatch)
	50,   // 2 Fledgling
	200,  // 3 Awakened
	500,  // 4 Structural
	1200, // 5 Teen
	3000, // 6 Matured
}

// stageNames maps stage index → canonical breed-neutral name.
var stageNames = [7]string{
	"egg", "baby", "fledgling", "awakened", "structural", "teen", "matured",
}

// SourceCampaignRungRefreshed is the reduced re-clear source (ADR-227 D10).
// Named because AwardExp carries a source-specific rule for it: a refresher
// never warms an UNHATCHED pod (CHO-2239 — the farming seam).
const SourceCampaignRungRefreshed = "campaign_rung_refreshed"

// dailyCapsBySource — ADR-149 §EXP economy. 0 = no daily cap.
//
// ADR-218 D6: this map (with expPerEventBySource) is the DOCUMENTED
// FALLBACK only. The live award path resolves rules via the ExpRuler port
// (identity ExpRuleResolver, parity-seeded by identity migration 0030);
// see exp_rules.go. Edit BOTH sides or the parity suites fail.
var dailyCapsBySource = map[string]int{
	"atom_session":      30,
	"ebbinghaus_review": 15,
	"hex_expand":        12,
	"conv_turn":         10,
	"daily_dose_open":   2,
	"social_share":      16,
	"social_reaction":   10,
	"junction_accepted": 15,
	"atom_authored":     40,
	"admin_grant":       0, // no cap
	"hatch_roll":        0, // audit only

	// ADR-227 D10 / WS-C5 (CHO-2084) campaign conquest sources — parity
	// with identity migration 0036 (exp_rules_test.go pins both sides).
	"campaign_rung_cleared":   32,
	"campaign_rung_refreshed": 12,
	"campaign_node_won":       75,
	"campaign_goal_sealed":    120,

	// ADR-228 D4 Wave 1 / F-I3 (CHO-2090) — parity with identity migration
	// 0037 (exp_rules_test.go pins both sides).
	"weakness_grown":    30,
	"submission_graded": 60,
	"module_completed":  45,
}

// expPerEventBySource — ADR-149 §EXP economy "EXP/event" column. Default
// award amount when the runtime doesn't supply an explicit delta. Subscribers
// use these defaults; AwardExpRequest.requested_delta MAY override.
var expPerEventBySource = map[string]int{
	"atom_session":      3, // correct answer; subscribers map incorrect → 1
	"ebbinghaus_review": 5,
	"hex_expand":        4,
	"conv_turn":         2,
	"daily_dose_open":   2,
	"social_share":      8,
	"social_reaction":   1,
	"junction_accepted": 15,
	"atom_authored":     20,
	"admin_grant":       0, // explicit delta required
	"hatch_roll":        0, // audit-only event

	// ADR-227 D10 campaign values (identity 0036 parity): first-clear
	// small-flat, refresher re-clear reduced, node won medium at the win
	// moment, goal sealed tier-S (the additional ~1/week spacing is a code
	// semantic in the campaign XP subscriber — daily caps only here).
	"campaign_rung_cleared":   8,
	"campaign_rung_refreshed": 3,
	"campaign_node_won":       25,
	"campaign_goal_sealed":    120,

	// ADR-228 D4 Wave 1 values (identity 0037 parity): "the power of a
	// little bit" — an edge recovered (tier B), an R+ assessment graded
	// (tier A, flat regardless of score — L16 no difficulty scaling), a W7
	// module completed (tier B, the structural completion bonus).
	"weakness_grown":    10,
	"submission_graded": 30,
	"module_completed":  15,
}

// canonicalSources is the validation set for the `source` field on
// AwardExpRequest and companion_growth_events.source.
var canonicalSources = map[string]bool{
	"atom_session":      true,
	"ebbinghaus_review": true,
	"hex_expand":        true,
	"conv_turn":         true,
	"daily_dose_open":   true,
	"social_share":      true,
	"social_reaction":   true,
	"junction_accepted": true,
	"atom_authored":     true,
	"admin_grant":       true,
	"hatch_roll":        true,

	// ADR-227 D10 / WS-C5 campaign conquest vocabulary. Verified events
	// only (L16): fed exclusively by the domain-emitted campaign.* topics.
	"campaign_rung_cleared":   true,
	"campaign_rung_refreshed": true,
	"campaign_node_won":       true,
	"campaign_goal_sealed":    true,

	// ADR-228 D4 Wave 1 / F-I3 vocabulary. Verified events only (L16):
	// weakness.grown.v1 (consumption outbox) · submission.graded.v1
	// (delivery, binary proto) · module_progress.completed.v1 (delivery).
	"weakness_grown":    true,
	"submission_graded": true,
	"module_completed":  true,
}

// IsValidSource returns true if s is a canonical ADR-149 source token.
func IsValidSource(s string) bool { return canonicalSources[s] }

// DailyCapForSource returns the per-day per-source EXP cap. 0 = no cap.
// Returns -1 for unknown sources so callers can reject defensively.
func DailyCapForSource(source string) int {
	if cap, ok := dailyCapsBySource[source]; ok {
		return cap
	}
	return -1
}

// ExpPerEventForSource returns the default EXP delta per event for the
// supplied source. Returns 0 for unknown sources.
func ExpPerEventForSource(source string) int {
	if v, ok := expPerEventBySource[source]; ok {
		return v
	}
	return 0
}

// ClampDelta applies the per-source daily cap to a requested delta given the
// running count already awarded today. Returns (clampedDelta, capHit) where
// capHit=true means the cap influenced the result (either it was hit during
// this call OR already reached).
//
// cap=0 → no clamp (admin_grant / hatch_roll path).
// Unknown source → no clamp; caller is responsible for filtering earlier.
func ClampDelta(source string, alreadyAwardedToday, requestedDelta int) (int, bool) {
	cap, ok := dailyCapsBySource[source]
	if !ok {
		return requestedDelta, false
	}
	if cap <= 0 {
		// No daily cap (admin_grant / hatch_roll).
		return requestedDelta, false
	}
	if alreadyAwardedToday >= cap {
		return 0, true
	}
	remaining := cap - alreadyAwardedToday
	if requestedDelta <= remaining {
		// Would-be-equal hits the cap; surface capHit even if delta fits exactly.
		return requestedDelta, requestedDelta >= remaining
	}
	return remaining, true
}

// StageForExp returns the current stage given a cumulative EXP. A Companion
// at 0 EXP is at Stage 1 (post-hatch Baby) — Stage 0 (Egg) is gated by
// hatched_at being NULL, not by EXP.
func StageForExp(exp int) int {
	stage := 1
	for i := 2; i < len(stageThresholds); i++ {
		if exp >= stageThresholds[i] {
			stage = i
		} else {
			break
		}
	}
	return stage
}

// NextThreshold returns the cumulative EXP needed to enter the NEXT stage
// from the given current stage. 0 = caller is at max (Stage 6) OR at Stage 0
// (no growth curve until hatched).
func NextThreshold(stage int) int {
	if stage < 0 || stage >= len(stageThresholds)-1 {
		return 0
	}
	return stageThresholds[stage+1]
}

// StageName returns the canonical breed-neutral stage name. Returns empty
// string for out-of-range.
func StageName(stage int) string {
	if stage < 0 || stage >= len(stageNames) {
		return ""
	}
	return stageNames[stage]
}

// AwardOutcome captures the post-award projection for a Companion.
type AwardOutcome struct {
	PreviousStage int
	NewStage      int
	NewExp        int
	StageUp       bool
	// CrossedStages lists ALL stages crossed in the award (length > 1 only
	// when an unusual single award crosses multiple thresholds).
	CrossedStages []int
}

// StateAfterAward computes the post-award state given a starting stage,
// starting cumulative EXP, and a clamped delta. Idempotent + pure.
func StateAfterAward(prevStage, prevExp, clampedDelta int) AwardOutcome {
	newExp := prevExp + clampedDelta
	newStage := StageForExp(newExp)
	// If prevStage > newStage (shouldn't happen — growth is monotonic) clamp
	// to prevStage. Defence-in-depth.
	if newStage < prevStage {
		newStage = prevStage
	}
	// F-I1.1 (CHO-2088, ADR-228 incubation): a Stage-0 egg accrues
	// growth_exp but NEVER advances past Stage 0 via EXP — only the explicit
	// hatch ceremony (Repository.CommitHatch) leaves Stage 0. StageForExp
	// maps any EXP < 50 to Stage 1 (post-hatch Baby), so without this clamp a
	// pre-hatch egg receiving its first award silently jumped to Stage 1
	// UNHATCHED (no hatched_at, no species). This is the SINGLE award
	// transition point — both pg.AwardExpTx and inmem.AwardExpTx delegate the
	// stage decision here — so clamping closes the bug for every award path.
	if prevStage == StageEgg {
		newStage = StageEgg
	}
	out := AwardOutcome{
		PreviousStage: prevStage,
		NewStage:      newStage,
		NewExp:        newExp,
		StageUp:       newStage > prevStage,
	}
	if out.StageUp {
		for s := prevStage + 1; s <= newStage; s++ {
			out.CrossedStages = append(out.CrossedStages, s)
		}
	}
	return out
}

// llmMatrix lookup: [stage][manaTier] → effective tier. ADR-149 §"Mana × Growth".
var llmMatrix = [7]map[string]string{
	// Stage 0 (Egg) — flash-lite across all tiers
	{"basic": "flash-lite", "standard": "flash-lite", "premium": "flash-lite"},
	// Stage 1 (Baby)
	{"basic": "flash-lite", "standard": "flash-lite", "premium": "flash-lite"},
	// Stage 2 (Fledgling)
	{"basic": "flash-lite", "standard": "flash-lite", "premium": "flash"},
	// Stage 3 (Awakened) — Basic stays lite; Standard + Premium jump to flash
	{"basic": "flash-lite", "standard": "flash", "premium": "flash"},
	// Stage 4 (Structural)
	{"basic": "flash-lite", "standard": "flash", "premium": "flash"},
	// Stage 5 (Teen)
	{"basic": "flash-lite", "standard": "flash", "premium": "pro"},
	// Stage 6 (Matured)
	{"basic": "flash-lite", "standard": "pro", "premium": "pro"},
}

// DefaultManaTier is the canonical safe-default mana tier (lowest plan) used
// when no per-user subscription tier is resolved. Centralised here (the
// domain owns the mana × growth matrix) so config-plumbing in the adapter
// layer doesn't re-hardcode the literal.
const DefaultManaTier = "basic"

// canonicalManaTiers is the validation set for mana tier strings, derived
// from the mana × growth matrix columns (per ADR-142 plan tiers). Used by
// IsValidManaTier so config plumbing (COMPANION_DEFAULT_MANA_TIER) can fail
// loud on an unknown tier rather than silently degrading to basic.
var canonicalManaTiers = map[string]bool{
	"basic":    true,
	"standard": true,
	"premium":  true,
}

// IsValidManaTier reports whether t is one of the canonical ADR-142 plan
// tiers (basic / standard / premium). Empty string is NOT valid — callers
// substitute DefaultManaTier explicitly when a value is unset.
func IsValidManaTier(t string) bool {
	return canonicalManaTiers[t]
}

// LLMTierForStageAndMana returns the effective LLM tier (mana-bounded).
// Unknown mana tier → conservative basic.
// Stage out-of-range → flash-lite.
func LLMTierForStageAndMana(stage int, manaTier string) string {
	if stage < 0 || stage >= len(llmMatrix) {
		return "flash-lite"
	}
	m := llmMatrix[stage]
	if t, ok := m[manaTier]; ok {
		return t
	}
	return m["basic"]
}

// PreviewLLMTierForAhaMoment returns the LLM tier used during the Stage-3
// Source Revelation 24-hour preview window. Per ADR-149: Basic gets flash
// (one notch up), Standard + Premium get pro.
func PreviewLLMTierForAhaMoment(manaTier string) string {
	switch manaTier {
	case "standard", "premium":
		return "pro"
	default:
		return "flash"
	}
}

// toolsByStage lists the CUMULATIVE INNATE KIT at each stage (ADR-218 D1:
// the ADR-149 ladder is the innate kit — retrieval/core-loop primitives
// arriving automatically with stage). The kit PLATEAUS at stage 4: every
// higher-order power is an equippable catalogue Skill occupying slots
// (spec pack docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §1). Former ladder
// entries moved to the catalogue: query_kg → map_sight (kg.read_map),
// suggest_atom_authoring → atom_forge, propose_kg_merge → kg_explore's st6
// extension. NewlyUnlockedTools computes deltas.
var toolsByStage = [7][]string{
	0: {},
	1: {"cite_atom"},
	2: {"cite_atom", "atom_search"},
	3: {"cite_atom", "atom_search", "ebbinghaus_state"},
	4: {"cite_atom", "atom_search", "ebbinghaus_state", "persona_lookup",
		"score_atom_for_learner"},
	5: {"cite_atom", "atom_search", "ebbinghaus_state", "persona_lookup",
		"score_atom_for_learner"},
	6: {"cite_atom", "atom_search", "ebbinghaus_state", "persona_lookup",
		"score_atom_for_learner"},
}

// UnshippedLadderTools declares the innate ladder names whose backing agent
// tools do NOT ship (ADR-249 A1a, owner-ruled 2026-08-07: cite_atom is the
// agent's only tool). The ladder above keeps them as product design
// (stage-up vocabulary, FE unlock copy), but the allowed_tools merge
// (companion_p1b_tools.go) omits them so the agent's runtime allowlist
// carries only tools that exist. Delivery returns via the ADR-249 Option B
// consumption-mediated callback or an equippable catalogue Skill; remove a
// name here in the SAME change that ships its tool. The agent-side twin of
// this declaration is growthstageplugin config.yaml `unshipped_tools`.
var UnshippedLadderTools = map[string]bool{
	"atom_search":            true,
	"ebbinghaus_state":       true,
	"persona_lookup":         true,
	"score_atom_for_learner": true,
}

// UnlockedToolsForStage returns the cumulative tool set at the given stage.
// Returns an empty slice for out-of-range.
func UnlockedToolsForStage(stage int) []string {
	if stage < 0 || stage >= len(toolsByStage) {
		return []string{}
	}
	// Copy to defend against caller mutation.
	out := make([]string, len(toolsByStage[stage]))
	copy(out, toolsByStage[stage])
	sort.Strings(out)
	return out
}

// NewlyUnlockedTools returns tools available at `to` that were NOT available
// at `from`. Used to populate the CompanionStageUp event's newly_unlocked_tools
// delta.
func NewlyUnlockedTools(from, to int) []string {
	have := make(map[string]bool)
	for _, t := range UnlockedToolsForStage(from) {
		have[t] = true
	}
	var diff []string
	for _, t := range UnlockedToolsForStage(to) {
		if !have[t] {
			diff = append(diff, t)
		}
	}
	sort.Strings(diff)
	return diff
}

// memoryModes per ADR-149 §"The 7 stages" memory column.
var memoryModes = [7]string{
	"stateless",       // 0
	"rolling-1",       // 1
	"rolling-3",       // 2
	"rolling-7",       // 3
	"rolling-21-rag",  // 4
	"vector-rag-all",  // 5
	"full-procedural", // 6
}

// MemoryModeForStage returns the canonical memory mode string for a stage.
func MemoryModeForStage(stage int) string {
	if stage < 0 || stage >= len(memoryModes) {
		return "stateless"
	}
	return memoryModes[stage]
}
