package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// TestGrowthRepo_GetGrowthRow_NotFoundAndTenantIsolation covers the two
// failure branches of GetGrowthRow: an absent companion, and a companion
// that exists under a DIFFERENT tenant (the in-memory analogue of the
// per-tenant RLS predicate — a cross-tenant read must NOT leak the row).
func TestGrowthRepo_GetGrowthRow_NotFoundAndTenantIsolation(t *testing.T) {
	repo := inmem.NewGrowthRepo()
	ctx := context.Background()

	// Absent companion → ErrCompanionNotFound.
	if _, err := repo.GetGrowthRow(ctx, "tenant-1", "missing"); err != growth.ErrCompanionNotFound {
		t.Errorf("absent err = %v, want ErrCompanionNotFound", err)
	}

	// Seed a row owned by tenant-1.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1",
		TenantID:    "tenant-1",
		OwnerGCID:   "user-1",
	})

	// Same tenant → found.
	if _, err := repo.GetGrowthRow(ctx, "tenant-1", "fam-1"); err != nil {
		t.Fatalf("same-tenant GetGrowthRow: %v", err)
	}

	// Cross-tenant read of an existing companion → ErrCompanionNotFound
	// (tenant mismatch must be indistinguishable from absence).
	if _, err := repo.GetGrowthRow(ctx, "tenant-2", "fam-1"); err != growth.ErrCompanionNotFound {
		t.Errorf("cross-tenant err = %v, want ErrCompanionNotFound", err)
	}
}
