package companion

import "testing"

// TestNewInstance_EggDefaults — CHO-2012 (ADR-218 D2/D8): a fresh Instance
// starts at the stage-0 allowances (1 slot, apprentice band, 1000 memory
// events). The per-user-XP promotion path (PromoteByUserXP + the
// DefaultProgressionTiers 1/3/5/8 table) is RETIRED — slots re-key to the
// growth stage and the award tx maintains the column.
func TestNewInstance_EggDefaults(t *testing.T) {
	inst, err := NewInstance("t1", "g1", "Newton", "math")
	if err != nil {
		t.Fatalf("NewInstance: %v", err)
	}
	if inst.SkillSlotsUnlocked != 1 {
		t.Fatalf("SkillSlotsUnlocked = %d, want 1 (stage-0 egg allowance)", inst.SkillSlotsUnlocked)
	}
	if inst.EvolutionTier != TierApprentice {
		t.Fatalf("EvolutionTier = %s, want apprentice (derived band for stage 0)", inst.EvolutionTier)
	}
	if inst.MemoryContextCapacity != 1_000 {
		t.Fatalf("MemoryContextCapacity = %d, want 1000", inst.MemoryContextCapacity)
	}
}

// TestSetRule_EdgeBranches covers SetRule's three guard arms not exercised by
// the happy-path test: blank key (ignored), nil-map lazy init, and the
// same-value dedupe no-op (which must not bump UpdatedAt).
func TestSetRule_EdgeBranches(t *testing.T) {
	t.Run("blank key ignored", func(t *testing.T) {
		inst, _ := NewInstance("t1", "g1", "Newton", "math")
		inst.SetRule("   ", "value")
		if len(inst.ConfiguredRules) != 0 {
			t.Fatalf("blank key stored: %v", inst.ConfiguredRules)
		}
	})

	t.Run("nil map lazily initialised", func(t *testing.T) {
		inst, _ := NewInstance("t1", "g1", "Newton", "math")
		inst.ConfiguredRules = nil // simulate a zero-value / legacy-loaded entity
		inst.SetRule("tone", "socratic")
		if inst.ConfiguredRules["tone"] != "socratic" {
			t.Fatalf("rule lost after nil-map init: %v", inst.ConfiguredRules)
		}
	})

	t.Run("same value is a no-op", func(t *testing.T) {
		inst, _ := NewInstance("t1", "g1", "Newton", "math")
		inst.SetRule("tone", "socratic")
		stamp := inst.UpdatedAt
		inst.SetRule("tone", "socratic") // identical re-write
		if !inst.UpdatedAt.Equal(stamp) {
			t.Fatal("identical re-write must not bump UpdatedAt")
		}
	})

	t.Run("trims key and value", func(t *testing.T) {
		inst, _ := NewInstance("t1", "g1", "Newton", "math")
		inst.SetRule("  tone  ", "  socratic  ")
		if inst.ConfiguredRules["tone"] != "socratic" {
			t.Fatalf("ConfiguredRules = %v, want trimmed tone=socratic", inst.ConfiguredRules)
		}
	})
}
