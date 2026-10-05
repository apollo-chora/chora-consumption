// loadout.go — ADR-218 D3 loadout semantics (GQ-7: agency lives in the
// loadout). A grant row = a Skill OWNED FOREVER; the growth-stage slot cap
// (growth.SlotsForStage) binds only the sum of EQUIPPED slot costs, and
// equip/unequip is free at any time. Craft Skills (R2-5) are slot-free
// passives: owned = in effect, never unequippable, so learner artifacts
// (long Rituals, extra quotas, the v2 canvas) never break on a swap.
//
// Supersedes the grant-time slot cap in Instance.GrantSkill (the pre-P0
// model where a grant occupied a slot); the companion_skill_grants bridge
// carries the equipped/unlocked_via/unlocked_at_stage columns since
// consumption migration 0061.
package companion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Loadout-specific sentinel errors. ErrSkillSlotsFull (instance.go) is
// reused so the existing 409 SKILL_SLOTS_FULL edge mapping keeps working —
// it now fires at EQUIP time, not grant time.
var (
	ErrSkillNotOwned      = errors.New("companion.loadout: skill not owned (not yet unlocked on this companion)")
	ErrCraftSkillAlwaysOn = errors.New("companion.loadout: craft skills are always in effect and cannot be unequipped")
	ErrSkillNotInCatalog  = errors.New("companion.loadout: skill_key not in the platform catalogue")
)

// LoadoutRepository persists the companion_skill_grants bridge with its
// CHO-2012 loadout columns (equipped / unlocked_via / unlocked_at_stage).
// Implementations: pg (production, catalogue-JOINed) and inmem (dev/tests).
type LoadoutRepository interface {
	GrantWriter
	// ListGrants returns the companion's owned Skills enriched with their
	// catalogue kind + slot cost (empty slice when none).
	ListGrants(ctx context.Context, tenantID, companionID string) ([]SkillGrant, error)
	// SetEquipped persists one grant's equipped flag. The DOMAIN validates
	// (Loadout.Equip/Unequip) before calling — this is a dumb writer that
	// returns ErrSkillNotOwned when no grant row exists.
	SetEquipped(ctx context.Context, tenantID, companionID, skillKey string, equipped bool) error
}

// SkillGrant is one owned Skill on a companion (the Go projection of a
// companion_skill_grants row joined to its catalogue entry).
type SkillGrant struct {
	SkillKey        string
	SkillKind       SkillKind
	SlotCost        int
	Equipped        bool
	UnlockedVia     string // species_path | admin_grant | aha_preview
	UnlockedAtStage int
}

// Loadout is the equip/unequip aggregate slice over a companion's grants.
type Loadout struct {
	Grants []SkillGrant
}

func (l *Loadout) find(skillKey string) *SkillGrant {
	for i := range l.Grants {
		if l.Grants[i].SkillKey == skillKey {
			return &l.Grants[i]
		}
	}
	return nil
}

// IsEquipped reports whether the skill is currently in effect.
func (l *Loadout) IsEquipped(skillKey string) bool {
	g := l.find(skillKey)
	return g != nil && g.Equipped
}

// EquippedSlotCost sums the slot costs of equipped ACTIVE Skills (craft
// Skills are slot-free by construction).
func (l *Loadout) EquippedSlotCost() int {
	total := 0
	for _, g := range l.Grants {
		if g.Equipped && g.SkillKind == SkillKindActive {
			total += g.SlotCost
		}
	}
	return total
}

// EquippedActiveSkillKeys returns the sorted ACTIVE equipped skill keys —
// the runtime AllowedSkills slice (craft Skills bind no tools).
func (l *Loadout) EquippedActiveSkillKeys() []string {
	var keys []string
	for _, g := range l.Grants {
		if g.Equipped && g.SkillKind == SkillKindActive {
			keys = append(keys, g.SkillKey)
		}
	}
	sort.Strings(keys)
	return keys
}

// Equip marks an owned Skill as equipped, enforcing the slot-cost cap on
// the equipped set only. Idempotent for already-equipped Skills and a
// no-op for craft Skills (always in effect).
func (l *Loadout) Equip(skillKey string, slotCap int) error {
	skillKey = strings.TrimSpace(skillKey)
	g := l.find(skillKey)
	if g == nil {
		return fmt.Errorf("%w: %q", ErrSkillNotOwned, skillKey)
	}
	if g.Equipped || g.SkillKind == SkillKindCraft {
		return nil
	}
	if l.EquippedSlotCost()+g.SlotCost > slotCap {
		return fmt.Errorf("%w: equipping %q needs %d slot(s), %d of %d in use",
			ErrSkillSlotsFull, skillKey, g.SlotCost, l.EquippedSlotCost(), slotCap)
	}
	g.Equipped = true
	return nil
}

// Unequip frees an owned Skill's slots. Idempotent for owned-but-unequipped
// Skills; craft Skills refuse (owned = in effect).
func (l *Loadout) Unequip(skillKey string) error {
	skillKey = strings.TrimSpace(skillKey)
	g := l.find(skillKey)
	if g == nil {
		return fmt.Errorf("%w: %q", ErrSkillNotOwned, skillKey)
	}
	if g.SkillKind == SkillKindCraft {
		return fmt.Errorf("%w: %q", ErrCraftSkillAlwaysOn, skillKey)
	}
	g.Equipped = false
	return nil
}
