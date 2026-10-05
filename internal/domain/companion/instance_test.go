// instance_test.go — TDD spec for the multi-Companion 1:N aggregate.
//
// Companion to instance.go. Tests written FIRST per .claude/rules/development-execution.md
// strict TDD discipline (RED → GREEN → REFACTOR).
//
// Scope (per docs/architecture/multi-companion-per-user-2026-05-11.md +
// .claude/skills/domain-content-consumption/SKILL.md §Multi-Companion):
//
//   - Instance constructor invariants (owner_gcid + tenant_id + name + specialization)
//   - Default tier (apprentice) ⇒ skill_slots_unlocked=1, memory_context_capacity=1000
//   - Tier progression (apprentice → adept → master → sage) gated on user XP
//   - Skill grant cap = skill_slots_unlocked; grant rejects beyond cap
//   - Specialization change re-tags but preserves identity (id unchanged)
//   - Soft delete (DeletedAt) — never hard delete (per ddd-enforcement.md)
//   - configured_rules JSONB carries tone / hint_policy / difficulty_cap
//
// Roster-level multi-Companion cap (max_companions_per_user) is enforced at
// the repository / service layer, not at the entity constructor — tested
// separately in the inmem repo test file.
package companion

import (
	"strings"
	"testing"
	"time"
)

func TestNewInstance_validates_required_fields(t *testing.T) {
	cases := []struct {
		name           string
		tenantID       string
		ownerGCID      string
		companionName  string
		specialization string
		wantErrSubstr  string
	}{
		{"missing tenant_id", "", "g1", "Newton", "math", "tenant_id"},
		{"missing owner_gcid", "t1", "", "Newton", "math", "owner_gcid"},
		{"missing name", "t1", "g1", "", "math", "name"},
		{"missing specialization", "t1", "g1", "Newton", "", "specialization"},
		{"whitespace-only name", "t1", "g1", "   ", "math", "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewInstance(tc.tenantID, tc.ownerGCID, tc.companionName, tc.specialization)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSubstr)
			}
		})
	}
}

func TestNewInstance_default_tier_apprentice(t *testing.T) {
	inst, err := NewInstance("t1", "g1", "Newton", "math")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.EvolutionTier != TierApprentice {
		t.Fatalf("expected default tier %q, got %q", TierApprentice, inst.EvolutionTier)
	}
	if inst.SkillSlotsUnlocked != 1 {
		t.Fatalf("apprentice default skill_slots=1, got %d", inst.SkillSlotsUnlocked)
	}
	if inst.MemoryContextCapacity != 1000 {
		t.Fatalf("apprentice default memory_context_capacity=1000, got %d", inst.MemoryContextCapacity)
	}
	if inst.CompanionID == "" {
		t.Fatal("CompanionID must be assigned (UUIDv7)")
	}
	if inst.CreatedAt.IsZero() || inst.UpdatedAt.IsZero() {
		t.Fatal("CreatedAt + UpdatedAt must be set")
	}
	if inst.DeletedAt != nil {
		t.Fatal("DeletedAt must be nil on new instance")
	}
	if inst.ConfiguredRules == nil {
		t.Fatal("ConfiguredRules must be non-nil (empty map ok)")
	}
}

// CHO-2012 (ADR-218 D2/D3/D8): the per-user-XP tier promotion
// (PromoteByUserXP over the 1/3/5/8 DefaultProgressionTiers table) and the
// grant-time slot cap (Instance.GrantSkill) are RETIRED. Slots derive from
// the companion's growth stage (+1/stage — growth.SlotsForStage; the award
// tx maintains the column) and the slot cap binds only the EQUIPPED set —
// see loadout_test.go for the Loadout Equip/Unequip contract and
// path_unlocker_test.go for species-Path grant minting.

func TestGrantSkillRetired_LoadoutOwnsGrants(t *testing.T) {
	// The aggregate keeps the owned-keys projection but exposes no capped
	// grant mutation any more; loadout mutations go through GrantWriter /
	// Loadout. This test pins the projection default.
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	if len(inst.SkillGrants) != 0 {
		t.Fatalf("fresh instance must own no skills, got %v", inst.SkillGrants)
	}
}

func TestChangeSpecialization_preserves_identity(t *testing.T) {
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	originalID := inst.CompanionID
	originalCreate := inst.CreatedAt
	time.Sleep(1 * time.Millisecond) // ensure UpdatedAt bumps

	old, err := inst.ChangeSpecialization("coding")
	if err != nil {
		t.Fatalf("specialization change: %v", err)
	}
	if old != "math" {
		t.Fatalf("expected old=math, got %s", old)
	}
	if inst.Specialization != "coding" {
		t.Fatalf("expected new=coding, got %s", inst.Specialization)
	}
	if inst.CompanionID != originalID {
		t.Fatal("CompanionID must NOT change on specialization swap")
	}
	if !inst.CreatedAt.Equal(originalCreate) {
		t.Fatal("CreatedAt must NOT change on specialization swap")
	}
	if !inst.UpdatedAt.After(originalCreate) {
		t.Fatal("UpdatedAt must advance on specialization swap")
	}
}

func TestChangeSpecialization_rejects_empty(t *testing.T) {
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	if _, err := inst.ChangeSpecialization("   "); err == nil {
		t.Fatal("empty specialization must reject")
	}
}

func TestSoftDelete_marks_deleted_at(t *testing.T) {
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	if inst.IsDeleted() {
		t.Fatal("fresh instance must not report deleted")
	}
	inst.SoftDelete()
	if !inst.IsDeleted() {
		t.Fatal("soft-deleted instance must report deleted")
	}
	if inst.DeletedAt == nil {
		t.Fatal("DeletedAt must be set after SoftDelete")
	}
	// Idempotent — re-deleting must not change the timestamp.
	stamp := *inst.DeletedAt
	inst.SoftDelete()
	if !inst.DeletedAt.Equal(stamp) {
		t.Fatal("SoftDelete must be idempotent (timestamp preserved)")
	}
}

func TestApplyConfiguredRule_stores_rule(t *testing.T) {
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	inst.SetRule("tone", "socratic")
	inst.SetRule("difficulty_cap", "intermediate")
	inst.SetRule("language", "en")

	if inst.ConfiguredRules["tone"] != "socratic" {
		t.Fatalf("tone rule lost; got %v", inst.ConfiguredRules["tone"])
	}
	if inst.ConfiguredRules["difficulty_cap"] != "intermediate" {
		t.Fatal("difficulty_cap rule lost")
	}
	if inst.ConfiguredRules["language"] != "en" {
		t.Fatal("language rule lost")
	}
}

func TestMemoryBankAppName_format(t *testing.T) {
	inst, _ := NewInstance("t1", "g1", "Newton", "math")
	want := "companion:" + inst.CompanionID
	if got := inst.MemoryBankAppName(); got != want {
		t.Fatalf("expected app_name=%q, got %q", want, got)
	}
}
