// BE-USR-2 — Action-cost map per ADR-142 §4.
//
// Hardcoded canonical defaults; tunable via mana_action_pricing config
// table (chora_identity DB) without code change once the BE-USR-1 wave
// lands the migration. For now the consumption + broker-gateway sides
// look up costs from this in-memory map and forward to chora-identity's
// DeductMana RPC, which authoritative-resolves cost when units==0.
//
// Free actions (cost=0) are short-circuited by IsFreeAction so the dose
// composer + broker middleware never RPC-out for pure game mechanics.
package companion

// Action codes canonical to chora-consumption + chora-model-broker-gateway.
// Mirrored from ADR-142 §4. New codes MUST be added here AND in the
// chora-identity mana_action_pricing config table.
const (
	// Free actions (cost=0).
	ActionSummonCompanion        = "summon_companion"
	ActionDailyDoseDeterministic = "daily_dose_deterministic"

	// Medium actions.
	ActionDailyDoseCoach        = "daily_dose_coach"
	ActionAtomAuthoringAIAssist = "atom_authoring_ai_assist"
	ActionQuestionGeneration    = "question_generation"
	ActionCompanionChat         = "companion_chat"
	// ActionAIAssist is the broker-gateway alias for ad-hoc LLM calls
	// (e.g., generic "AI Assist" toolbar action). Same medium tier as
	// AtomAuthoringAIAssist.
	ActionAIAssist = "ai_assist"

	// ADR-154 — tier-aware Companion conversational chat turn action codes.
	// Authoritative pricing lives in chora_identity.mana_action_pricing (rows
	// seeded by migration 0010_mana_action_pricing_companion_chat_tiers.up.sql);
	// the entries below are the in-memory canonical fallback used by
	// LookupCost when chora-identity is unreachable.
	ActionCompanionChatTurnBasic    = "companion_chat_turn_basic"
	ActionCompanionChatTurnStandard = "companion_chat_turn_standard"
	ActionCompanionChatTurnPremium  = "companion_chat_turn_premium"

	// Heavy actions.
	ActionKnowledgeGraphTraversal = "knowledge_graph_traversal"
	ActionBossChallengeAtomGen    = "boss_challenge_atom_gen"

	// Premium actions.
	ActionFullExamPrepCoach = "full_exam_prep_coach"

	// ADR-205 D6 / WS-4 (CHO-1956) — Growth-Edge premium upload analysis. The
	// upload door reserves this on accept (reserve-on-accept); the ai-kernel
	// weakness-analyser crew settles on success / refunds on fail. Authoritative
	// pricing lives in chora_identity (migration 0028, tenant-overridable via the
	// ADR-178 price-plan layer); the entry below is the canonical in-memory
	// display fallback. The auto-derived edge path never debits this.
	ActionWeaknessAnalysis = "weakness_analysis"

	// CHO-2013 P1.B (R4-4) — single-step Skill invoke action codes, spec §7
	// contract `companion_skill_{key}`. The invoke runner stamps the code onto
	// the engine turn so the model-gateway debits ONCE (ADR-177); these
	// entries are the consumption-side pre-flight affordability projection
	// (identity mana_action_pricing rows were P0-seeded).
	ActionCompanionSkillProgressMirror = "companion_skill_progress_mirror"
	ActionCompanionSkillRecapScribe    = "companion_skill_recap_scribe"
	ActionCompanionSkillExplainAnew    = "companion_skill_explain_anew"

	// CHO-2040 (CR §8 R7-3) — the ceremony edge-scout COMPOSED runner (not a
	// catalogue Skill row; the ceremony is the acquisition moment). One crawl
	// + one fenced extraction turn per invocation; the runner stamps this code
	// on the turn and the model-gateway debits ONCE (ADR-177 — the runner
	// never debits locally). The virgin fallback path (no weaknesses + no
	// graded comments) skips the turn entirely and charges nothing.
	ActionCompanionCeremonyEdgeScout = "companion_ceremony_edge_scout"

	// CHO-2040 (owner ruling R8-1) — the FIRST ceremony edge-scout run per
	// (tenant, goal, learner) is FREE; re-runs stay on the paid code above.
	// This is an EXPLICIT zero-cost entry (the map has always carried them:
	// summon_companion / daily_dose_deterministic / progress_mirror), scoped to
	// this code by the ceremony_edge_scout_runs ledger — the runner stamps it
	// ONLY when no run row exists, and records the row as part of the same
	// successful flow. The turn still rides the model-gateway with this code
	// stamped so metering sees it (IMDA D3: never an un-metered LLM turn) —
	// the gateway resolves the chora_identity mana_action_pricing row
	// (seeded 0, migration 0032) and debits 0. Unknown codes keep hard-failing
	// via LookupCost; the "never free" invariant in cost_map_edge_scout_test.go
	// binds the RE-RUN code only.
	ActionCompanionCeremonyEdgeScoutFirst = "companion_ceremony_edge_scout_first"

	// CHO-2014 P2 "Fledgling's Kit" wave A — spec §7 skill-invoke action codes
	// for the two activated sight Skills (migration 0073). The
	// `companion_skill_{key}` contract mirrors the P1-three above. The remaining
	// P2 codes register with their own waves: kg_explore=20, quiz_me=0
	// (`_gen`=25), socratic_drill=15 (spec §7).
	ActionCompanionSkillWeaknessSight = "companion_skill_weakness_sight"
	ActionCompanionSkillMapSight      = "companion_skill_map_sight"

	// CHO-2016 quiz_me + socratic_drill "answerable-pipe" (wave B) — spec §7
	// skill-invoke action codes. quiz_me RETRIEVE is free (draws from the
	// learner's EXISTING atoms — no generation); quiz_me GENERATE is pre-declared
	// at 25 for a later wave (the qgen.invoke crew is unbuilt this wave, so the
	// runner rejects mode=generate with a 4xx). socratic_drill is 15. All three
	// rows are ALREADY seeded gateway-side in chora_identity mana_action_pricing
	// (migration 0031); these entries are the consumption-side pre-flight
	// affordability projection + the LookupCost fail-loud floor (ADR-177 — the
	// gateway is the sole debiter).
	ActionCompanionSkillQuizMe        = "companion_skill_quiz_me"
	ActionCompanionSkillQuizMeGen     = "companion_skill_quiz_me_gen"
	ActionCompanionSkillSocraticDrill = "companion_skill_socratic_drill"

	// kg_explore (Weaver, st4) "suggestion-inbox" scout — spec §7 skill-invoke
	// action code, price 20. ONE fenced grounded-reconcile turn over the learner's
	// map-adjacent topic pool; the runner stamps this code on the turn so the
	// model-gateway debits ONCE (ADR-177 — the runner never debits locally). This
	// entry is the consumption-side pre-flight affordability projection + the
	// LookupCost fail-loud floor (the authoritative chora_identity
	// mana_action_pricing row is seeded gateway-side with the activation wave).
	ActionCompanionSkillKgExplore = "companion_skill_kg_explore"

	// P5 Far Sight (CHO-2017, ADR-220/231) — the Seeker grounded-egress meters.
	// Each Seeker carries its OWN price (spec §2.4: fact_check 40, web_research 80)
	// and is metered ONCE, at the gateway grounded-search egress: the
	// grounded_search_gateway_client stamps this code onto the GroundedSearch RPC
	// (grounded.Query.ActionCode) and the gateway debits it there (ADR-231 D6, the
	// sole meter per ADR-177; priced in chora_identity mana_action_pricing mig
	// 0031, meter_home defaults to gateway). The metering is RE-HOMED from the
	// fenced verify/research Invoke turn to the grounded egress — the fenced turn is
	// un-metered (no double-debit) — but the per-skill PRICE is preserved (NOT
	// flattened). These entries are the consumption-side affordability projection +
	// the reported mana_charged. fact_check/web_research stay DARK until their §8
	// eval passes (migs 0087/0088).
	ActionCompanionSkillFactCheck   = "companion_skill_fact_check"
	ActionCompanionSkillWebResearch = "companion_skill_web_research"
)

// canonicalCostMap holds the in-memory defaults. Replace the lookup
// implementation when chora-identity's mana_action_pricing table is
// readable via GetActionPricing RPC.
var canonicalCostMap = map[string]int64{
	ActionSummonCompanion:        0,
	ActionDailyDoseDeterministic: 0,
	ActionDailyDoseCoach:         10,
	ActionAtomAuthoringAIAssist:  25,
	ActionAIAssist:               25,
	ActionQuestionGeneration:     50,
	ActionCompanionChat:          20,
	// ADR-154 — tier-aware chat turns: basic=5, standard=15, premium=30.
	ActionCompanionChatTurnBasic:    5,
	ActionCompanionChatTurnStandard: 15,
	ActionCompanionChatTurnPremium:  30,
	ActionKnowledgeGraphTraversal:   100,
	ActionBossChallengeAtomGen:      200,
	ActionFullExamPrepCoach:         500,
	// ADR-205 D6 — Growth-Edge premium analysis (mirrors chora_identity migration
	// 0028 platform default; tenant overrides resolved server-side on debit).
	ActionWeaknessAnalysis: 50,
	// CHO-2013 P1.B — Skill invoke codes (spec §7: mirror 0/5/10).
	ActionCompanionSkillProgressMirror: 0,
	ActionCompanionSkillRecapScribe:    5,
	ActionCompanionSkillExplainAnew:    10,
	// CHO-2014 P2 wave A — sight-skill invoke prices (spec §7).
	ActionCompanionSkillWeaknessSight: 15,
	ActionCompanionSkillMapSight:      0,
	// CHO-2016 wave B — answerable-pipe invoke prices (spec §7; mirrors the
	// chora_identity migration-0031 seed). quiz_me retrieve is free; the
	// pre-declared generate code (25) exists so a later wave can wire it without
	// a code change; socratic_drill is 15.
	ActionCompanionSkillQuizMe:        0,
	ActionCompanionSkillQuizMeGen:     25,
	ActionCompanionSkillSocraticDrill: 15,
	// kg_explore suggestion-inbox scout (spec §7): 20.
	ActionCompanionSkillKgExplore: 20,
	// P5 Far Sight (ADR-231 D6) — the Seeker grounded-egress meters, re-homed from
	// the fenced turn to the gateway grounded-search egress but keeping the per-skill
	// spec-§2.4 price. fact_check 40 (one grounded verify), web_research 80 (the
	// priciest Seeker — a depth-scaled grounded research pass).
	ActionCompanionSkillFactCheck:   40,
	ActionCompanionSkillWebResearch: 80,
	// CHO-2040 — ceremony edge-scout: PROVISIONAL consumption-side pre-flight
	// projection at the ai_assist medium tier (one extraction turn + source
	// reads). ⚠ The AUTHORITATIVE price is the chora_identity
	// mana_action_pricing row, which is an OWNER pricing decision and has NOT
	// been seeded — the gateway authoritative-resolves on debit; this entry
	// only backs the runner's read-only affordability pre-check (an
	// unregistered code would 500 the runner via LookupCost's fail-loud).
	ActionCompanionCeremonyEdgeScout: 25,
	// CHO-2040 R8-1 — owner-ruled FREE first run per goal (see the const doc
	// above): explicit 0, mirrored by the chora_identity migration-0032 seed
	// so the gateway meters the turn and debits 0.
	ActionCompanionCeremonyEdgeScoutFirst: 0,
}

// ChatTurnActionCodeForTier returns the canonical mana action_code for the
// per-tier Companion chat turn (ADR-154 D3). Unknown tiers default to basic
// (the lowest-cost tier — safest fallback in the absence of a known plan).
//
// Tier strings match the chora-identity ManaService convention:
//
//	"basic"    → ActionCompanionChatTurnBasic    (5 mana)
//	"standard" → ActionCompanionChatTurnStandard (15 mana)
//	"premium"  → ActionCompanionChatTurnPremium  (30 mana)
func ChatTurnActionCodeForTier(tier string) string {
	switch tier {
	case "premium":
		return ActionCompanionChatTurnPremium
	case "standard":
		return ActionCompanionChatTurnStandard
	default:
		return ActionCompanionChatTurnBasic
	}
}

// LookupCost returns the canonical mana cost for an action code, or
// ErrUnknownActionCode when the code is not registered. Unknown codes
// MUST fail loudly rather than default to 0 (silent bypass = abuse vector).
func LookupCost(actionCode string) (int64, error) {
	cost, ok := canonicalCostMap[actionCode]
	if !ok {
		return 0, ErrUnknownActionCode
	}
	return cost, nil
}

// IsFreeAction returns true when the action code maps to cost=0. Unknown
// codes return false (fail-safe; cannot be confirmed free).
func IsFreeAction(actionCode string) bool {
	cost, ok := canonicalCostMap[actionCode]
	if !ok {
		return false
	}
	return cost == 0
}
