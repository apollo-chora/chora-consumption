package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestGrowthRepo_ProvisionEgg_Idempotent(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	in := growth.ProvisionEggInput{
		TenantID:      "tenant-1",
		OwnerGCID:     "user-1",
		EggSku:        "egg.standard.v1",
		EggPurchaseID: "p1",
		EggSource:     "purchase",
		Now:           now,
		SoftExpiryAt:  now.AddDate(0, 0, 30),
		HardExpiryAt:  now.AddDate(0, 0, 60),
	}
	row, err := repo.ProvisionEgg(context.Background(), in)
	if err != nil {
		t.Fatalf("ProvisionEgg: %v", err)
	}
	if row.GrowthStage != 0 {
		t.Errorf("GrowthStage = %d, want 0", row.GrowthStage)
	}
	// Idempotent second call yields the same companion_id.
	row2, err := repo.ProvisionEgg(context.Background(), in)
	if err != nil {
		t.Fatalf("ProvisionEgg dup: %v", err)
	}
	if row2.CompanionID != row.CompanionID {
		t.Errorf("not idempotent: %s vs %s", row2.CompanionID, row.CompanionID)
	}
}

func TestGrowthRepo_AwardExpTx_CapAndStageUp(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	now := time.Now()
	// Seed a Stage-1 row at 48 EXP — one more atom_session should hit Stage 2 (50).
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 48,
	})
	out, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "k1",
		Now: now, ManaTier: "standard",
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if !out.TriggeredStageUp {
		t.Errorf("expected TriggeredStageUp")
	}
	if out.Row.GrowthStage != 2 {
		t.Errorf("Stage = %d", out.Row.GrowthStage)
	}
	if out.ClampedDelta != 3 {
		t.Errorf("Clamped = %d", out.ClampedDelta)
	}
	if out.EffectiveLLMTier == "" {
		t.Errorf("EffectiveLLMTier empty")
	}
}

// F-I1.1 (CHO-2088, ADR-228): awarding EXP to a Stage-0 egg via the award
// transaction accrues growth_exp but leaves the row at Stage 0 with
// hatched_at NULL — proving the StateAfterAward clamp propagates through the
// real award seam (this is the latent bug the slice closes: pre-fix, the egg
// silently jumped to Stage 1 UNHATCHED on its first award).
func TestGrowthRepo_AwardExpTx_Stage0EggAccruesButStaysUnhatched(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	now := time.Now()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 0, EggSku: "egg.standard.v1",
	})
	out, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Source: "campaign_node_won", RequestedDelta: 25, IdempotencyKey: "k1",
		Now: now, ManaTier: "standard",
	})
	if err != nil {
		t.Fatalf("AwardExpTx: %v", err)
	}
	if out.TriggeredStageUp {
		t.Errorf("egg award TriggeredStageUp = true, want false (no stage advance)")
	}
	if out.NewStage != 0 || out.Row.GrowthStage != 0 {
		t.Errorf("egg award NewStage=%d Row.GrowthStage=%d, want both 0", out.NewStage, out.Row.GrowthStage)
	}
	if out.Row.GrowthExp != 25 {
		t.Errorf("egg award GrowthExp = %d, want 25 (exp accrues)", out.Row.GrowthExp)
	}
	if out.Row.HatchedAt != nil {
		t.Errorf("egg award HatchedAt = %v, want nil (still pre-hatch)", out.Row.HatchedAt)
	}
}

func TestGrowthRepo_AwardExpTx_DuplicateIdempotencyKey(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	})
	_, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "same",
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	out, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Source: "atom_session", RequestedDelta: 3, IdempotencyKey: "same",
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	if !out.Duplicate {
		t.Errorf("expected Duplicate=true")
	}
	if out.ClampedDelta != 0 {
		t.Errorf("dup ClampedDelta = %d, want 0", out.ClampedDelta)
	}
}

func TestGrowthRepo_CommitHatch(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, EggSku: "egg.standard.v1",
	})
	now := time.Now()

	// CHO-2229 commit-only: an unrevealed pod cannot commit.
	_, err := repo.CommitHatch(context.Background(), growth.HatchTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "atom-1", Now: now,
	})
	if err != growth.ErrNotRevealed {
		t.Fatalf("expected ErrNotRevealed for an unrevealed pod, got %v", err)
	}

	// Reveal persists the roll write-once.
	revealed, err := repo.CommitReveal(context.Background(), growth.RevealTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Species: "dragon", ShinyVariant: false, SpeciesRarity: "legendary",
		RolledProbability: 25.0, Now: now,
	})
	if err != nil {
		t.Fatalf("CommitReveal: %v", err)
	}
	if revealed.Duplicate || revealed.Row.RevealedAt == nil || revealed.Row.Species != "dragon" {
		t.Fatalf("CommitReveal must persist the roll, got %+v", revealed)
	}
	// A second reveal returns the ORIGINAL roll with Duplicate=true.
	replay, err := repo.CommitReveal(context.Background(), growth.RevealTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Species: "owl", ShinyVariant: true, SpeciesRarity: "common",
		RolledProbability: 50.0, Now: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("CommitReveal replay: %v", err)
	}
	if !replay.Duplicate || replay.Row.Species != "dragon" {
		t.Fatalf("replay must keep the original roll, got %+v", replay)
	}

	row, err := repo.CommitHatch(context.Background(), growth.HatchTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "atom-1", Now: now,
	})
	if err != nil {
		t.Fatalf("CommitHatch: %v", err)
	}
	if row.GrowthStage != 1 {
		t.Errorf("Stage = %d, want 1", row.GrowthStage)
	}
	if row.HatchedAt == nil {
		t.Errorf("HatchedAt should be set")
	}
	if row.Species != "dragon" {
		t.Errorf("hatch must keep the persisted roll, got %q", row.Species)
	}
	// Already hatched rejects.
	_, err = repo.CommitHatch(context.Background(), growth.HatchTxInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		DisplayName: "Eira", Tone: "socratic", LearnerPersona: "curious-explorer",
		ResonantAtomID: "atom-1", Now: now,
	})
	if err != growth.ErrAlreadyHatched {
		t.Errorf("expected ErrAlreadyHatched, got %v", err)
	}
}

func TestGrowthRepo_MarkAhaMoment(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 3,
	})
	expires := time.Now().Add(24 * time.Hour)
	row, err := repo.MarkAhaMoment(context.Background(), growth.AhaMomentInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		PreviewLLMTier: "pro", WindowExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("MarkAhaMoment: %v", err)
	}
	if !row.AhaMomentConsumed {
		t.Errorf("AhaMomentConsumed should be true")
	}
	// Already-consumed rejects.
	_, err = repo.MarkAhaMoment(context.Background(), growth.AhaMomentInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		PreviewLLMTier: "pro", WindowExpiresAt: expires,
	})
	if err != growth.ErrAhaMomentConsumed {
		t.Errorf("expected ErrAhaMomentConsumed, got %v", err)
	}
}

func TestGrowthRepo_CountCompanionsOfSpecies(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 2, Species: "dragon",
	})
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-2", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, Species: "dragon",
	})
	n, err := repo.CountCompanionsOfSpecies(context.Background(), "tenant-1", "user-1", "dragon", "fam-3")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	n, _ = repo.CountCompanionsOfSpecies(context.Background(), "tenant-1", "user-1", "dragon", "fam-1")
	if n != 1 {
		t.Errorf("excluded count = %d, want 1", n)
	}
}

// OwnerSpeciesSet mirrors ownerSpeciesSetSQL: DISTINCT non-empty species of the
// caller's companions, minus the excluded one. A blank species (an unrevealed
// pod) must contribute nothing, or an unopened pod would silently exclude a
// breed the learner has never seen from their own odds.
func TestGrowthRepo_OwnerSpeciesSet(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 2, Species: "dragon",
	})
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-2", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, Species: "dragon", // duplicate collapses
	})
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-3", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, Species: "owl",
	})
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-pod", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, Species: "", // unrevealed pod
	})
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-other", TenantID: "tenant-1", OwnerGCID: "user-2",
		GrowthStage: 1, Species: "phoenix", // another learner
	})

	owned, err := repo.OwnerSpeciesSet(context.Background(), "tenant-1", "user-1", "fam-9")
	if err != nil {
		t.Fatalf("OwnerSpeciesSet: %v", err)
	}
	if len(owned) != 2 || !owned["dragon"] || !owned["owl"] {
		t.Fatalf("owned = %v, want exactly {dragon, owl}", owned)
	}

	excluded, err := repo.OwnerSpeciesSet(context.Background(), "tenant-1", "user-1", "fam-3")
	if err != nil {
		t.Fatalf("OwnerSpeciesSet excluded: %v", err)
	}
	if len(excluded) != 1 || !excluded["dragon"] {
		t.Fatalf("excluding fam-3 = %v, want only {dragon}", excluded)
	}
}

func TestGrowthRepo_ListGrowthEvents(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 0,
	})
	for i := 0; i < 3; i++ {
		_, err := repo.AwardExpTx(context.Background(), growth.AwardExpTxInput{
			TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
			Source: "atom_session", RequestedDelta: 3,
			IdempotencyKey: "k" + string(rune('1'+i)), Now: time.Now(),
		})
		if err != nil {
			t.Fatalf("Award: %v", err)
		}
	}
	out, err := repo.ListGrowthEvents(context.Background(), growth.ListGrowthEventsInput{
		TenantID: "tenant-1", CompanionID: "fam-1", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out.Events) != 3 {
		t.Errorf("events = %d", len(out.Events))
	}
}

func TestGrowthRepo_GetGrowthRow_NotFound(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	_, err := repo.GetGrowthRow(context.Background(), "tenant-1", "missing")
	if err != growth.ErrCompanionNotFound {
		t.Errorf("expected ErrCompanionNotFound, got %v", err)
	}
}
