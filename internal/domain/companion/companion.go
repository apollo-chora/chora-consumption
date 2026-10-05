// Package companion defines the Companion aggregate for the Content Consumption
// domain.
//
// CRITICAL DISTINCTION (per memory feedback_companion_vs_agent +
// chora-contracts/proto/events/consumption/companion.proto):
//
//	Companion is a DOMAIN ENTITY: an in-game RPG companion. Holds learner-
//	facing state (companion_id, name, level, xp, traits). Pure game mechanics.
//	NO LLM imports. NO prompt strings. NO model identifiers.
//
//	The AI agent that POWERS Companion responses (LangGraph orchestrator,
//	Model Broker calls, RAG retrieval, persona synthesis) lives in AI Kernel
//	and is a SEPARATE adapter concern. NEVER conflate them.
//
// XP-to-level curve — quadratic, not linear:
//
//	XP_required(level n) = n^2 * 100  (cumulative total)
//	level 1 → 0    XP
//	level 2 → 400  XP
//	level 3 → 900  XP
//	level 4 → 1600 XP
//	...
//
// Rationale: linear curves either trivialise late progression (e.g. 100/level
// makes level 50 reachable in a single session) or punish early players
// (e.g. cubic 100*n^3). A gentle quadratic curve gives smooth, increasing
// difficulty and matches the engagement loop primitive in CLAUDE.md
// (Curiosity-Reward-Social with the Ebbinghaus Forgetting Curve as base).
package companion

import (
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Companion is the legacy 1:1 "Companion" DOMAIN ENTITY (see package docs). One
// per learner per tenant in the original rules (1:1 with owner_gcid).
//
// Deprecated (ADR-212 D5, WS-3): this is the in-memory "Companion". Its only
// live store is the per-pod `inmem.CompanionRepo` (auto-bonded on first GET), so
// it is NON-DURABLE (lost on pod restart) and UNREAD by Growth-Edge / the
// roster panel — the durability bug D5 fixes. It is RETIRED as a source of
// truth in favour of the persisted multi-Companion roster (`Instance` ->
// companion_instances, ADR-116/147), which is canonical. A map's designated
// Companion is now the map's Goal itself (Goal.AttachedCompanionID, ADR-214 D1 —
// WS-A3 consolidation), resolved durably from Postgres; the earlier theme-keyed
// `CompanionMapBinding` is retired. New code MUST NOT auto-bond `Companion`;
// acquire a Companion for a map via POST /v1/me/companions/acquire. (The legacy
// /api/companions XP handlers still touch this entity; rewiring that live path to
// the persisted Instance is a later WS.)
//
// Phyllis MVP extensions (Comic Ch4 P3 + Ch6 P14): adds Archetype + Stats +
// UnlockedTraits to support the Summoning ceremony and Daily Dose nudge
// composition. Existing Traits + level/xp curve preserved unchanged.
type Companion struct {
	CompanionID    string
	TenantID       string
	OwnerGCID      string
	Name           string
	Level          int
	XP             int
	Traits         []string
	Archetype      Archetype // optional — empty pre-summon (auto-bond legacy)
	Stats          Stats     // 5-dimension vector (default 50 each)
	UnlockedTraits []string  // earned via level-up; distinct from Traits
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// New constructs a fresh Companion at level 1 with 0 XP and no traits.
func New(tenantID, ownerGCID, name string) (*Companion, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("companion: tenant_id required")
	}
	if strings.TrimSpace(ownerGCID) == "" {
		return nil, errors.New("companion: gcid required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("companion: name required")
	}

	now := time.Now().UTC()
	return &Companion{
		CompanionID:    domain.NewUUIDv7(),
		TenantID:       tenantID,
		OwnerGCID:      ownerGCID,
		Name:           name,
		Level:          1,
		XP:             0,
		Traits:         []string{},
		Stats:          DefaultStats(),
		UnlockedTraits: []string{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// LevelUpTraitForLevel returns the canonical trait unlocked at a given level.
// Comic Ch6 P14 anchor: "leveled up to 5! New trait: Pattern Recognition".
//
// Maps the 50-level curve to a trait roster — game-mechanic only, no LLM.
// Levels not in the table grant empty trait (no unlock at that level).
func LevelUpTraitForLevel(level int) string {
	traits := map[int]string{
		2:  "Quick Learner",
		3:  "Steady Focus",
		4:  "Curiosity Spark",
		5:  "Pattern Recognition",
		7:  "Deep Recall",
		10: "Mentor Voice",
		15: "Insight Synthesis",
		20: "Mastery Touch",
		25: "Wisdom Echo",
		30: "Legend Mark",
	}
	return traits[level]
}

// AwardXPWithTrait is like AwardXP but, on level-up, additionally records
// the canonical trait for the new level into UnlockedTraits and returns it.
// Returns the unlocked trait string (empty if no level-up occurred or no
// trait is mapped to that level).
//
// Used by /atoms/{id}/feedback to surface the Comic Ch6 P14 nudge text.
func (f *Companion) AwardXPWithTrait(delta int) (leveledUp bool, trait string) {
	leveledUp = f.AwardXP(delta)
	if !leveledUp {
		return false, ""
	}
	trait = LevelUpTraitForLevel(f.Level)
	if trait != "" {
		f.UnlockedTraits = appendUniqueTrait(f.UnlockedTraits, trait)
		f.UpdatedAt = time.Now().UTC()
	}
	return true, trait
}

func appendUniqueTrait(existing []string, t string) []string {
	for _, e := range existing {
		if e == t {
			return existing
		}
	}
	return append(existing, t)
}

// XPRequiredFor returns the cumulative XP required to BE at level n.
// Quadratic curve: f(n) = n^2 * 100.
//
// level 1 → 0     (already at level 1 from creation)
// level 2 → 400
// level 3 → 900
// level 4 → 1600
//
// Note: level 1 is free (the "starting point"), so f(1)=0 is intentional.
// For levels >= 2, f(n) = n^2 * 100.
func XPRequiredFor(level int) int {
	if level <= 1 {
		return 0
	}
	return level * level * 100
}

// AwardXP adds delta XP and re-derives level. Returns true if the level
// changed. Negative or zero delta is a no-op.
//
// Re-deriving level from total XP (rather than incrementally checking only
// the next threshold) makes a single large award correctly cross multiple
// thresholds — e.g., 1000 XP at level 1 jumps to level 3 in one call.
func (f *Companion) AwardXP(delta int) bool {
	if delta <= 0 {
		return false
	}
	previousLevel := f.Level
	f.XP += delta
	f.Level = levelForXP(f.XP)
	f.UpdatedAt = time.Now().UTC()
	return f.Level != previousLevel
}

// levelForXP returns the highest level n such that XPRequiredFor(n) <= xp.
// Iterative; bounded by reasonable game-mechanic ceiling (level 1000 ~ 100M
// XP — practically unreachable, so the loop is fast in all real cases).
func levelForXP(xp int) int {
	level := 1
	for {
		next := level + 1
		if XPRequiredFor(next) > xp {
			return level
		}
		level = next
		// Safety bound: never iterate past 1000 levels in case of overflow.
		if level >= 1000 {
			return level
		}
	}
}

// AddTrait appends a trait if not already present (case-sensitive dedup).
func (f *Companion) AddTrait(t string) {
	t = strings.TrimSpace(t)
	if t == "" {
		return
	}
	for _, existing := range f.Traits {
		if existing == t {
			return
		}
	}
	f.Traits = append(f.Traits, t)
	f.UpdatedAt = time.Now().UTC()
}
