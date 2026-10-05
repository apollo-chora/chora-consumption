// slots.go — ADR-218 D2 + D8: capability slots and the evolution-tier
// presentation band both derive from the ADR-149 growth stage.
//
// D2: slots = +1 per stage — 1 at Egg (stage 0) → 7 at Matured (stage 6).
// The defaults below are the editor-tunable baseline (ADR-201); supersedes
// the per-user-XP tier table (apprentice 1 / adept 3 / master 5 / sage 8)
// from multi-companion-per-user-2026-05-11.md.
//
// D8: evolution_tier survives ONLY as L6's derived, never-shown band —
// {stages 0-1 apprentice · 2-3 adept · 4-5 master · 6 sage}. The per-user
// XP feeder (`chora.sharing.xp.credited.v1`) never existed; the tier is a
// pure function of growth_stage.
package growth

// SlotsForStage returns the capability-slot allowance at a growth stage:
// stage + 1, clamped to [1, 7]. The slot cap binds the EQUIPPED set only
// (ADR-218 D3 — unlocked Skills are owned forever, equip/unequip is free).
func SlotsForStage(stage int) int {
	return clampStage(stage) + 1
}

// TierForStage returns the derived evolution-tier band for a growth stage
// (L6 one-visible-ladder: the learner sees stages; the band drives copy /
// grouping only and is never a gate).
func TierForStage(stage int) string {
	switch clampStage(stage) {
	case 0, 1:
		return "apprentice"
	case 2, 3:
		return "adept"
	case 4, 5:
		return "master"
	default:
		return "sage"
	}
}
