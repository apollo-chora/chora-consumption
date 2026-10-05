// companion_roster_test.go — TDD spec for the enriched
// `CompanionInstanceRepo.ListRosterByOwner` per debt #44 / A24 / B5 (2026-
// 05-16).
//
// Asserts:
//
//  1. RLS pair (SET LOCAL chora.tenant_id + chora.user_gcid) lands BEFORE
//     any user query.
//  2. The single SELECT projects all 0006 base columns + 0032 growth-axis
//     columns from companion_instances (no JOIN needed — additive ALTER
//     TABLE per migration 0032 placed all columns on the same table).
//  3. Each RosterEntry carries a populated Instance + GrowthSnapshot +
//     CosmeticSnapshot.
//  4. Stage-0 (Egg) rows surface the pre-hatch NULLs gracefully (empty
//     species + nil HatchedAt + zero rarity).
//  5. Empty result is NOT an error.
//  6. Bare context (no tenant_id) returns rls.ErrNoTenantContext.
//
// Closes the demo-blocking N+1 pattern that FE's
// `CompanionGrowthService.listMyCompanions` was hitting (per chora-web
// commit 4cff383e + handoff round-§B5).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
)

// canonicalEiraRosterValues mirrors the 0036_phyllis_eira_seed.up.sql
// payload at the listRosterByOwnerSQL projection shape (base 13 columns
// from 0006 + growth-axis columns from 0032).
//
// Column order MUST mirror listCompanionRosterByOwnerSQL exactly:
//
//	companion_id, tenant_id, owner_gcid, name, specialization,
//	evolution_tier, skill_slots_unlocked, memory_context_capacity,
//	persona_summary, configured_rules (JSONB),
//	created_at, updated_at, deleted_at,
//	-- 0032 growth axis (additive on the same table)
//	growth_stage, species, shiny_variant, species_rarity,
//	growth_exp, resonant_atom_id, hatched_at,
//	aha_moment_consumed, aha_moment_active_until,
//	effective_llm_tier_cached, last_stage_up_at
func canonicalEiraRosterValues() []any {
	created := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	hatched := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	stageUp := hatched
	rules, _ := json.Marshal(map[string]any{
		"max_hint_count":      3,
		"max_session_minutes": 25,
	})
	species := "dragon"
	rarity := "common"
	llmTier := "flash-lite"
	atomID := "00000000-0000-7000-8000-00000000a0a2"
	return []any{
		// Base 0006 columns
		"00000000-0000-7000-8000-00000000e1a0", // companion_id
		"11111111-1111-7111-8111-111111111111", // tenant_id
		"00000000-0000-7000-8000-000000001999", // owner_gcid
		"Eira",                                 // name
		"cspo",                                 // specialization
		"apprentice",                           // evolution_tier
		int(1),                                 // skill_slots_unlocked
		int(1000),                              // memory_context_capacity
		"Dragon Companion bonded to Phyllis at the Fledgling stage. Specialised on Certified Scrum Product Owner content. Curious, encouraging, cite-first.", // persona_summary
		rules,             // configured_rules (JSONB)
		created,           // created_at
		created,           // updated_at
		(*time.Time)(nil), // deleted_at
		// 0032 growth axis
		int(2),            // growth_stage = Fledgling
		&species,          // species = dragon
		false,             // shiny_variant
		&rarity,           // species_rarity = common
		int(200),          // growth_exp
		&atomID,           // resonant_atom_id
		&hatched,          // hatched_at
		false,             // aha_moment_consumed
		(*time.Time)(nil), // aha_moment_active_until
		&llmTier,          // effective_llm_tier_cached
		&stageUp,          // last_stage_up_at
	}
}

// eggStageRosterValues mirrors a Stage-0 (Egg) row where breed + atom +
// hatched_at are all NULL. Establishes that the pre-hatch projection
// degrades cleanly (empty species, zero rarity, nil HatchedAt).
func eggStageRosterValues() []any {
	created := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)
	rules, _ := json.Marshal(map[string]any{})
	return []any{
		"00000000-0000-7000-8000-00000000beef", // companion_id
		"11111111-1111-7111-8111-111111111111", // tenant_id
		"00000000-0000-7000-8000-000000001999", // owner_gcid
		"Egg",                                  // name
		"general",                              // specialization
		"apprentice",                           // evolution_tier
		int(1),                                 // skill_slots_unlocked
		int(1000),                              // memory_context_capacity
		"",                                     // persona_summary
		rules,                                  // configured_rules (JSONB)
		created,                                // created_at
		created,                                // updated_at
		(*time.Time)(nil),                      // deleted_at
		// 0032 growth axis — all NULLs / zeros for an unhatched egg
		int(0),            // growth_stage
		(*string)(nil),    // species (NULL)
		false,             // shiny_variant
		(*string)(nil),    // species_rarity (NULL)
		int(0),            // growth_exp
		(*string)(nil),    // resonant_atom_id (NULL)
		(*time.Time)(nil), // hatched_at (NULL)
		false,             // aha_moment_consumed
		(*time.Time)(nil), // aha_moment_active_until (NULL)
		(*string)(nil),    // effective_llm_tier_cached (NULL)
		(*time.Time)(nil), // last_stage_up_at (NULL)
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_ReturnsEnrichedEntries(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{
			values: [][]any{canonicalEiraRosterValues()},
		},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	out, err := repo.ListRosterByOwner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d; want 1", len(out))
	}
	entry := out[0]
	if entry == nil || entry.Instance == nil {
		t.Fatal("entry or entry.Instance is nil")
	}
	if entry.Instance.CompanionID != "00000000-0000-7000-8000-00000000e1a0" {
		t.Errorf("Instance.CompanionID = %q; want canonical Eira", entry.Instance.CompanionID)
	}
	if entry.Instance.Name != "Eira" {
		t.Errorf("Instance.Name = %q; want Eira", entry.Instance.Name)
	}
	if entry.Instance.Specialization != "cspo" {
		t.Errorf("Instance.Specialization = %q; want cspo", entry.Instance.Specialization)
	}
	if entry.Growth == nil {
		t.Fatal("Growth nil — projection dropped")
	}
	if entry.Growth.Stage != 2 {
		t.Errorf("Growth.Stage = %d; want 2", entry.Growth.Stage)
	}
	if entry.Growth.StageName != "fledgling" {
		t.Errorf("Growth.StageName = %q; want fledgling", entry.Growth.StageName)
	}
	if entry.Growth.Exp != 200 {
		t.Errorf("Growth.Exp = %d; want 200", entry.Growth.Exp)
	}
	if entry.Growth.ExpToNextStage != 200 {
		t.Errorf("Growth.ExpToNextStage = %d; want 200 (Stage 3 threshold)", entry.Growth.ExpToNextStage)
	}
	if entry.Growth.CurrentBreed != "dragon" {
		t.Errorf("Growth.CurrentBreed = %q; want dragon", entry.Growth.CurrentBreed)
	}
	if entry.Growth.EffectiveLLMTier != "flash-lite" {
		t.Errorf("Growth.EffectiveLLMTier = %q; want flash-lite", entry.Growth.EffectiveLLMTier)
	}
	if entry.Growth.ResonantAtomID != "00000000-0000-7000-8000-00000000a0a2" {
		t.Errorf("Growth.ResonantAtomID = %q", entry.Growth.ResonantAtomID)
	}
	if entry.Growth.BreedRevealedAt == nil {
		t.Error("Growth.BreedRevealedAt nil; want hatched_at populated")
	}
	if entry.Cosmetic == nil {
		t.Fatal("Cosmetic nil — projection dropped")
	}
	if entry.Cosmetic.Shiny {
		t.Error("Cosmetic.Shiny = true; want false")
	}
	if entry.Cosmetic.Rarity != "common" {
		t.Errorf("Cosmetic.Rarity = %q; want common", entry.Cosmetic.Rarity)
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_EggStage_HandlesNullsGracefully(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{
			values: [][]any{eggStageRosterValues()},
		},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)

	out, err := repo.ListRosterByOwner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d; want 1", len(out))
	}
	entry := out[0]
	if entry.Growth == nil {
		t.Fatal("Growth should be populated even at Stage 0")
	}
	if entry.Growth.Stage != 0 {
		t.Errorf("Growth.Stage = %d; want 0 (Egg)", entry.Growth.Stage)
	}
	if entry.Growth.StageName != "egg" {
		t.Errorf("Growth.StageName = %q; want egg", entry.Growth.StageName)
	}
	if entry.Growth.CurrentBreed != "" {
		t.Errorf("Growth.CurrentBreed = %q; want empty (pre-hatch)", entry.Growth.CurrentBreed)
	}
	if entry.Growth.BreedRevealedAt != nil {
		t.Errorf("Growth.BreedRevealedAt = %v; want nil (pre-hatch)", entry.Growth.BreedRevealedAt)
	}
	if entry.Growth.EffectiveLLMTier != "" {
		t.Errorf("Growth.EffectiveLLMTier = %q; want empty (pre-hatch)", entry.Growth.EffectiveLLMTier)
	}
	if entry.Cosmetic == nil {
		t.Fatal("Cosmetic should be populated even at Stage 0")
	}
	if entry.Cosmetic.Shiny {
		t.Error("Cosmetic.Shiny = true; want false (pre-hatch)")
	}
	if entry.Cosmetic.Rarity != "" {
		t.Errorf("Cosmetic.Rarity = %q; want empty (pre-hatch)", entry.Cosmetic.Rarity)
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_AppliesRLS(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{values: nil},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	if _, err := repo.ListRosterByOwner(withCtx(), pgTenantID, pgUserGCID); err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(stub.execCalls) < 2 {
		t.Fatalf("expected ≥ 2 SET LOCAL calls, got %d", len(stub.execCalls))
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", stub.execCalls[1])
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_EmptyResult_IsNotAnError(t *testing.T) {
	stub := &instanceStubQuerier{
		nextRows: &instanceStubRows{values: nil},
	}
	tx := &instanceStubTxRunner{q: stub}
	repo := NewCompanionInstanceRepo(tx)
	out, err := repo.ListRosterByOwner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListRosterByOwner: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len(out) = %d; want 0 for empty result", len(out))
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_FailsLoud_OnMissingTenantContext(t *testing.T) {
	tx := &instanceStubTxRunner{}
	repo := NewCompanionInstanceRepo(tx)
	_, err := repo.ListRosterByOwner(context.Background(), pgTenantID, pgUserGCID)
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestPGCompanionInstanceRepo_ListRosterByOwner_SQLProjectsGrowthAxisColumns(t *testing.T) {
	// Self-check on the SQL constant: it MUST reference both 0006 base
	// columns (e.g., 'specialization') and 0032 growth-axis columns
	// (e.g., 'growth_stage') from companion_instances. Stops the SQL from
	// silently regressing to the bare 13-column projection.
	if !contains(listCompanionRosterByOwnerSQL, "companion_instances") {
		t.Error("listCompanionRosterByOwnerSQL missing companion_instances table reference")
	}
	for _, col := range []string{
		"specialization", "configured_rules", "deleted_at",
		"growth_stage", "species", "shiny_variant", "species_rarity",
		"growth_exp", "resonant_atom_id", "hatched_at",
		"effective_llm_tier_cached",
	} {
		if !contains(listCompanionRosterByOwnerSQL, col) {
			t.Errorf("listCompanionRosterByOwnerSQL missing column %q", col)
		}
	}
	if !contains(listCompanionRosterByOwnerSQL, "WHERE") {
		t.Error("listCompanionRosterByOwnerSQL missing WHERE clause")
	}
	if !contains(listCompanionRosterByOwnerSQL, "deleted_at IS NULL") {
		t.Error("listCompanionRosterByOwnerSQL missing soft-delete filter")
	}
}
