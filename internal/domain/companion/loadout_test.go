// loadout_test.go — ADR-218 D3 loadout semantics: unlocked = owned forever;
// the slot cap binds ONLY the equipped set (sum of slot costs); equip/unequip
// is free; craft Skills are slot-free and always in effect. RED-first for
// CHO-2012 P0.
package companion

import (
	"errors"
	"testing"
)

func testLoadout() *Loadout {
	return &Loadout{Grants: []SkillGrant{
		{SkillKey: "explain_anew", SkillKind: SkillKindActive, SlotCost: 1, Equipped: false, UnlockedVia: "species_path", UnlockedAtStage: 2},
		{SkillKey: "recap_scribe", SkillKind: SkillKindActive, SlotCost: 1, Equipped: false, UnlockedVia: "species_path", UnlockedAtStage: 2},
		{SkillKey: "path_weaver", SkillKind: SkillKindActive, SlotCost: 2, Equipped: false, UnlockedVia: "species_path", UnlockedAtStage: 4},
		{SkillKey: "long_weaving", SkillKind: SkillKindCraft, SlotCost: 0, Equipped: true, UnlockedVia: "species_path", UnlockedAtStage: 5},
	}}
}

func TestLoadoutEquipHappyPath(t *testing.T) {
	l := testLoadout()
	if err := l.Equip("explain_anew", 3); err != nil {
		t.Fatalf("Equip: %v", err)
	}
	if !l.IsEquipped("explain_anew") {
		t.Error("explain_anew not equipped after Equip")
	}
	// Idempotent re-equip is a no-op, not an error.
	if err := l.Equip("explain_anew", 3); err != nil {
		t.Errorf("re-Equip should no-op, got %v", err)
	}
}

func TestLoadoutEquipUnowned(t *testing.T) {
	l := testLoadout()
	if err := l.Equip("atom_forge", 7); !errors.Is(err, ErrSkillNotOwned) {
		t.Errorf("equipping an unowned skill = %v, want ErrSkillNotOwned", err)
	}
}

// TestLoadoutSlotCostCap — the cap counts SLOT COSTS, not skills: a
// 2-slot Skill (path_weaver) fills two slots.
func TestLoadoutSlotCostCap(t *testing.T) {
	l := testLoadout()
	if err := l.Equip("path_weaver", 3); err != nil {
		t.Fatalf("Equip path_weaver: %v", err)
	}
	if err := l.Equip("explain_anew", 3); err != nil {
		t.Fatalf("Equip explain_anew: %v", err)
	}
	// 2 + 1 = 3 slots used; a third active Skill must not fit at cap 3.
	if err := l.Equip("recap_scribe", 3); !errors.Is(err, ErrSkillSlotsFull) {
		t.Errorf("over-cap equip = %v, want ErrSkillSlotsFull", err)
	}
	if got := l.EquippedSlotCost(); got != 3 {
		t.Errorf("EquippedSlotCost = %d, want 3", got)
	}
	// Free swap: unequip the 2-slot skill, then the third fits.
	if err := l.Unequip("path_weaver"); err != nil {
		t.Fatalf("Unequip: %v", err)
	}
	if err := l.Equip("recap_scribe", 3); err != nil {
		t.Errorf("equip after free swap = %v, want nil", err)
	}
}

// TestLoadoutCraftSemantics — craft Skills are slot-free passives: always
// in effect (equipped), never count toward the cap, cannot be unequipped.
func TestLoadoutCraftSemantics(t *testing.T) {
	l := testLoadout()
	if !l.IsEquipped("long_weaving") {
		t.Error("craft grant must be in effect (equipped) from mint")
	}
	if err := l.Equip("long_weaving", 1); err != nil {
		t.Errorf("craft Equip should no-op, got %v", err)
	}
	if got := l.EquippedSlotCost(); got != 0 {
		t.Errorf("craft skills must not consume slots, EquippedSlotCost = %d", got)
	}
	if err := l.Unequip("long_weaving"); !errors.Is(err, ErrCraftSkillAlwaysOn) {
		t.Errorf("craft Unequip = %v, want ErrCraftSkillAlwaysOn", err)
	}
}

func TestLoadoutUnequipSemantics(t *testing.T) {
	l := testLoadout()
	if err := l.Unequip("atom_forge"); !errors.Is(err, ErrSkillNotOwned) {
		t.Errorf("unequip unowned = %v, want ErrSkillNotOwned", err)
	}
	// Owned but not equipped → idempotent no-op.
	if err := l.Unequip("explain_anew"); err != nil {
		t.Errorf("unequip not-equipped = %v, want nil (idempotent)", err)
	}
}

// TestLoadoutEquippedActiveSkillKeys — the runtime allowlist slice: ACTIVE
// equipped keys only (craft Skills bind no tools; they expand the designer
// grammar instead).
func TestLoadoutEquippedActiveSkillKeys(t *testing.T) {
	l := testLoadout()
	if err := l.Equip("explain_anew", 3); err != nil {
		t.Fatalf("Equip: %v", err)
	}
	keys := l.EquippedActiveSkillKeys()
	if len(keys) != 1 || keys[0] != "explain_anew" {
		t.Errorf("EquippedActiveSkillKeys = %v, want [explain_anew] (craft excluded)", keys)
	}
}
