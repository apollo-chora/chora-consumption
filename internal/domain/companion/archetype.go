// Package companion — archetype + stats extension for the Phyllis MVP.
//
// Adds the 6-archetype roster + 5-dimension Stats vector + summon ceremony
// per Comic Ch4 P3 / Ch6 P14. Extends the base Companion entity (level / xp /
// traits) with Archetype + Stats + UnlockedTraits while preserving existing
// XP-curve game mechanics.
//
// Source-of-truth:
//   - docs/m13/phyllis-mvp-2026-05-08.md §5.4 (Companion daily-dose endpoint)
//   - docs/design/ux_companion_chat.md §Summoning + §StatAllocation
//   - CLAUDE.md §1: Companion = DOMAIN ENTITY, NOT an AI agent
//
// Six canonical archetypes (Comic Ch4 P3 carousel):
//
//	ScholarOwl      — focus + recall biased
//	ExplorerFox     — curiosity biased
//	GuardianBear    — perseverance biased
//	TricksterCat    — social biased
//	SageTurtle      — recall + perseverance biased
//	SparkPhoenix    — curiosity + social biased (rare)
//
// Five canonical Stats (1..100 each):
//
//	Curiosity       — exploration appetite (KnowledgeGraph traversal weight)
//	Focus           — concentration depth (active-session attention budget)
//	Recall          — memory retention (Ebbinghaus decay multiplier)
//	Social          — willingness to interact (peer A2A discovery weight)
//	Perseverance    — grit on hard atoms (retry preference)
//
// Summon ceremony invariant: learner allocates EXACTLY 3 stat points across
// the 5 dimensions in the initial summon (stat values start at archetype
// baseline 50 and may go up to 53 for the dimension allocated).
package companion

import (
	"errors"
	"strings"
)

// Archetype is the immutable archetype chosen at summon time.
type Archetype string

const (
	ArchetypeScholarOwl   Archetype = "ScholarOwl"
	ArchetypeExplorerFox  Archetype = "ExplorerFox"
	ArchetypeGuardianBear Archetype = "GuardianBear"
	ArchetypeTricksterCat Archetype = "TricksterCat"
	ArchetypeSageTurtle   Archetype = "SageTurtle"
	ArchetypeSparkPhoenix Archetype = "SparkPhoenix"
)

// allArchetypes is the canonical roster in carousel order.
var allArchetypes = []Archetype{
	ArchetypeScholarOwl,
	ArchetypeExplorerFox,
	ArchetypeGuardianBear,
	ArchetypeTricksterCat,
	ArchetypeSageTurtle,
	ArchetypeSparkPhoenix,
}

// IsValidArchetype returns true if a is one of the canonical 6 archetypes.
func IsValidArchetype(a Archetype) bool {
	for _, candidate := range allArchetypes {
		if a == candidate {
			return true
		}
	}
	return false
}

// Stats is the 5-dimension personality / capability vector. Each value is
// clamped to [1, 100] inclusive at allocation time. Defaults to 50 each.
type Stats struct {
	Curiosity    int
	Focus        int
	Recall       int
	Social       int
	Perseverance int
}

// DefaultStats returns the archetype-baseline stats (50 each before
// summon-ceremony allocation).
func DefaultStats() Stats {
	return Stats{
		Curiosity:    50,
		Focus:        50,
		Recall:       50,
		Social:       50,
		Perseverance: 50,
	}
}

// StatAllocation specifies how the 3 summon points are distributed across
// the 5 dimensions. Sum must equal 3 exactly; each component must be >= 0.
type StatAllocation struct {
	Curiosity    int
	Focus        int
	Recall       int
	Social       int
	Perseverance int
}

// Sum returns the total points allocated.
func (a StatAllocation) Sum() int {
	return a.Curiosity + a.Focus + a.Recall + a.Social + a.Perseverance
}

// Validate ensures the allocation has total = 3 and no negative components.
// Returns nil if valid.
func (a StatAllocation) Validate() error {
	if a.Curiosity < 0 || a.Focus < 0 || a.Recall < 0 ||
		a.Social < 0 || a.Perseverance < 0 {
		return errors.New("companion: stat allocation cannot have negative components")
	}
	if a.Sum() != 3 {
		return errors.New("companion: stat allocation must sum to exactly 3")
	}
	return nil
}

// Apply returns the resulting Stats after applying the allocation to baseline
// DefaultStats. Clamps each component to [1, 100].
func (a StatAllocation) Apply(base Stats) Stats {
	return Stats{
		Curiosity:    clamp(base.Curiosity+a.Curiosity, 1, 100),
		Focus:        clamp(base.Focus+a.Focus, 1, 100),
		Recall:       clamp(base.Recall+a.Recall, 1, 100),
		Social:       clamp(base.Social+a.Social, 1, 100),
		Perseverance: clamp(base.Perseverance+a.Perseverance, 1, 100),
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Summon constructs a freshly-summoned Companion with archetype + name +
// allocated stats. Returns ErrInvalidArchetype if archetype is not in the
// canonical roster, ErrInvalidAllocation if the stat allocation is invalid,
// or any required-field error.
//
// Comic Ch4 P3 anchor: ceremony locks archetype permanently (visual
// customisation remains unlimited via skins).
func Summon(tenantID, ownerGCID, name string, archetype Archetype, alloc StatAllocation) (*Companion, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("companion: tenant_id required")
	}
	if strings.TrimSpace(ownerGCID) == "" {
		return nil, errors.New("companion: gcid required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("companion: name required")
	}
	if !IsValidArchetype(archetype) {
		return nil, errors.New("companion: invalid archetype")
	}
	if err := alloc.Validate(); err != nil {
		return nil, err
	}

	f, err := New(tenantID, ownerGCID, name)
	if err != nil {
		return nil, err
	}
	f.Archetype = archetype
	f.Stats = alloc.Apply(DefaultStats())
	f.UnlockedTraits = []string{}
	return f, nil
}
