package companion

import (
	"strings"
	"testing"
)

const (
	tTenant = "01970000-0000-7000-8000-000000000001"
	tGCID   = "01970000-0000-7000-9000-000000000001"
)

// TestSummon_HappyPath verifies the canonical 6-archetype + 3-stat-allocation
// summon ceremony (Comic Ch4 P3). Resulting Companion has level 1, archetype
// locked, stats = baseline + allocation, UnlockedTraits empty.
func TestSummon_HappyPath(t *testing.T) {
	cases := []struct {
		name      string
		archetype Archetype
	}{
		{"ScholarOwl", ArchetypeScholarOwl},
		{"ExplorerFox", ArchetypeExplorerFox},
		{"GuardianBear", ArchetypeGuardianBear},
		{"TricksterCat", ArchetypeTricksterCat},
		{"SageTurtle", ArchetypeSageTurtle},
		{"SparkPhoenix", ArchetypeSparkPhoenix},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			alloc := StatAllocation{Curiosity: 1, Focus: 1, Recall: 1}
			f, err := Summon(tTenant, tGCID, "Eira", c.archetype, alloc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Archetype != c.archetype {
				t.Errorf("Archetype = %q, want %q", f.Archetype, c.archetype)
			}
			if f.Level != 1 {
				t.Errorf("Level = %d, want 1", f.Level)
			}
			if f.Stats.Curiosity != 51 {
				t.Errorf("Stats.Curiosity = %d, want 51 (50 base + 1 alloc)", f.Stats.Curiosity)
			}
			if f.Stats.Focus != 51 {
				t.Errorf("Stats.Focus = %d, want 51", f.Stats.Focus)
			}
			if f.Stats.Recall != 51 {
				t.Errorf("Stats.Recall = %d, want 51", f.Stats.Recall)
			}
			if f.Stats.Social != 50 {
				t.Errorf("Stats.Social = %d, want 50 (no alloc)", f.Stats.Social)
			}
			if f.Stats.Perseverance != 50 {
				t.Errorf("Stats.Perseverance = %d, want 50 (no alloc)", f.Stats.Perseverance)
			}
			if f.UnlockedTraits == nil {
				t.Error("UnlockedTraits = nil, want []string{}")
			}
		})
	}
}

// TestSummon_InvalidArchetype rejects archetypes not in the canonical roster.
func TestSummon_InvalidArchetype(t *testing.T) {
	alloc := StatAllocation{Curiosity: 3}
	_, err := Summon(tTenant, tGCID, "Eira", Archetype("EvilDragon"), alloc)
	if err == nil {
		t.Error("expected error for invalid archetype")
	}
}

// TestSummon_AllocationMustSumToThree rejects bad point totals (must be exactly 3).
func TestSummon_AllocationMustSumToThree(t *testing.T) {
	cases := []struct {
		name  string
		alloc StatAllocation
	}{
		{"sum=0", StatAllocation{}},
		{"sum=2", StatAllocation{Curiosity: 1, Focus: 1}},
		{"sum=4", StatAllocation{Curiosity: 2, Focus: 1, Recall: 1}},
		{"sum=10", StatAllocation{Curiosity: 10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Summon(tTenant, tGCID, "Eira", ArchetypeScholarOwl, c.alloc)
			if err == nil {
				t.Errorf("expected error for sum=%d", c.alloc.Sum())
			}
			if !strings.Contains(err.Error(), "sum to exactly 3") {
				t.Errorf("error = %v, want 'sum to exactly 3'", err)
			}
		})
	}
}

// TestSummon_AllocationRejectsNegative rejects negative point allocations.
func TestSummon_AllocationRejectsNegative(t *testing.T) {
	alloc := StatAllocation{Curiosity: -1, Focus: 4}
	_, err := Summon(tTenant, tGCID, "Eira", ArchetypeScholarOwl, alloc)
	if err == nil {
		t.Error("expected error for negative components")
	}
}

// TestSummon_RequiresName ensures non-empty name (Comic Ch4 P3 ceremony invariant).
func TestSummon_RequiresName(t *testing.T) {
	alloc := StatAllocation{Curiosity: 3}
	_, err := Summon(tTenant, tGCID, "", ArchetypeScholarOwl, alloc)
	if err == nil {
		t.Error("expected error when name empty")
	}
}

// TestSummon_RequiresTenant + TestSummon_RequiresGCID ensure multi-tenant
// invariant (per ddd-enforcement #2).
func TestSummon_RequiresTenant(t *testing.T) {
	alloc := StatAllocation{Curiosity: 3}
	_, err := Summon("", tGCID, "Eira", ArchetypeScholarOwl, alloc)
	if err == nil {
		t.Error("expected error for empty tenant_id")
	}
}

func TestSummon_RequiresGCID(t *testing.T) {
	alloc := StatAllocation{Curiosity: 3}
	_, err := Summon(tTenant, "", "Eira", ArchetypeScholarOwl, alloc)
	if err == nil {
		t.Error("expected error for empty gcid")
	}
}

// TestIsValidArchetype exhaustively verifies the canonical roster is exactly 6.
func TestIsValidArchetype(t *testing.T) {
	if len(allArchetypes) != 6 {
		t.Errorf("expected exactly 6 archetypes (Comic Ch4 P3 carousel), got %d", len(allArchetypes))
	}
	for _, a := range allArchetypes {
		if !IsValidArchetype(a) {
			t.Errorf("canonical archetype %q rejected", a)
		}
	}
	if IsValidArchetype(Archetype("UnknownBeast")) {
		t.Error("non-canonical archetype accepted")
	}
}

// TestStatAllocation_Apply_ClampsAtMax ensures stats are clamped to 100 max.
func TestStatAllocation_Apply_ClampsAtMax(t *testing.T) {
	base := Stats{Curiosity: 99, Focus: 50, Recall: 50, Social: 50, Perseverance: 50}
	alloc := StatAllocation{Curiosity: 3}
	got := alloc.Apply(base)
	if got.Curiosity != 100 {
		t.Errorf("Curiosity = %d, want 100 (clamped)", got.Curiosity)
	}
}

// TestLevelUpTraitForLevel verifies the comic-anchored trait map.
// Comic Ch6 P14: "leveled up to 5! New trait: Pattern Recognition".
func TestLevelUpTraitForLevel(t *testing.T) {
	if LevelUpTraitForLevel(5) != "Pattern Recognition" {
		t.Errorf("level 5 trait = %q, want 'Pattern Recognition'", LevelUpTraitForLevel(5))
	}
	if LevelUpTraitForLevel(2) != "Quick Learner" {
		t.Errorf("level 2 trait = %q, want 'Quick Learner'", LevelUpTraitForLevel(2))
	}
	if LevelUpTraitForLevel(99) != "" {
		t.Errorf("level 99 trait = %q, want '' (not in map)", LevelUpTraitForLevel(99))
	}
}

// TestAwardXPWithTrait_LevelUpEmitsTrait verifies trait unlock on level-up.
func TestAwardXPWithTrait_LevelUpEmitsTrait(t *testing.T) {
	f, _ := New(tTenant, tGCID, "Eira")
	leveled, trait := f.AwardXPWithTrait(400)
	if !leveled {
		t.Fatal("expected level-up at 400 XP")
	}
	if trait != "Quick Learner" {
		t.Errorf("trait = %q, want 'Quick Learner' (level 2)", trait)
	}
	if len(f.UnlockedTraits) != 1 || f.UnlockedTraits[0] != "Quick Learner" {
		t.Errorf("UnlockedTraits = %v, want ['Quick Learner']", f.UnlockedTraits)
	}
}

// TestAwardXPWithTrait_NoLevelNoTrait — no level-up emits no trait.
func TestAwardXPWithTrait_NoLevelNoTrait(t *testing.T) {
	f, _ := New(tTenant, tGCID, "Eira")
	leveled, trait := f.AwardXPWithTrait(100)
	if leveled {
		t.Error("expected no level-up at 100 XP")
	}
	if trait != "" {
		t.Errorf("trait = %q, want '' on no-level-up", trait)
	}
}

// TestAwardXPWithTrait_DedupOnRepeatedLevelUp prevents double-record of
// the same level's trait if AwardXPWithTrait is called twice and the level
// happens to land on the same number (defensive — not realistic).
func TestAwardXPWithTrait_DedupOnRepeatedLevelUp(t *testing.T) {
	f, _ := New(tTenant, tGCID, "Eira")
	f.AwardXPWithTrait(400) // level 1 → 2 ; trait Quick Learner
	// Simulate trait re-record (defensive): manual dup append shouldn't grow.
	f.UnlockedTraits = appendUniqueTrait(f.UnlockedTraits, "Quick Learner")
	if len(f.UnlockedTraits) != 1 {
		t.Errorf("UnlockedTraits len = %d, want 1 (dedup)", len(f.UnlockedTraits))
	}
}
