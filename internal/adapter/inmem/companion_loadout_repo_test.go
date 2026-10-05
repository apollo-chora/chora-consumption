// companion_loadout_repo_test.go — CHO-2012 / ADR-218: in-memory
// companion.CatalogReader + companion.SpeciesPathReader + companion.LoadoutRepository
// twins (dev/test reference-data mirror of migration 0061). RED-first.
package inmem

import (
	"context"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

// ---------- SkillCatalog ----------

func TestSkillCatalog_NewSeedsPlatformCatalogue(t *testing.T) {
	cat := NewSkillCatalog()
	got, err := cat.ListCatalogue(context.Background())
	if err != nil {
		t.Fatalf("ListCatalogue: %v", err)
	}
	if len(got) != len(seedspec.Catalogue()) {
		t.Fatalf("seeded catalogue size = %d, want %d", len(got), len(seedspec.Catalogue()))
	}
	for _, e := range seedspec.Catalogue() {
		if _, ok := got[e.SkillKey]; !ok {
			t.Errorf("seed row %q missing from catalogue", e.SkillKey)
		}
	}
	// ListCatalogue must be a defensive copy — mutating the result must not
	// leak into the store.
	delete(got, "quiz_me")
	if after, _ := cat.ListCatalogue(context.Background()); after["quiz_me"].SkillKey != "quiz_me" {
		t.Errorf("ListCatalogue leaked a copy: quiz_me dropped from store snapshot")
	}
}

func TestSkillCatalog_SetActive_TogglesReleaseGate(t *testing.T) {
	cat := NewSkillCatalogWith([]companion.CatalogEntry{
		{SkillKey: "quiz_me", SkillKind: companion.SkillKindActive, SlotCost: 1, Active: false},
	})
	cat.SetActive("quiz_me", true)
	got, _ := cat.ListCatalogue(context.Background())
	if !got["quiz_me"].Active {
		t.Errorf("SetActive(true): quiz_me still inactive")
	}
	// SetActive on an unknown key is a silent no-op (must not panic).
	cat.SetActive("never_seeded", true)
}

func TestSkillCatalog_SetActive_WithAndPreSeeding(t *testing.T) {
	// NewSkillCatalogWith consumes an explicit list; NewSkillCatalog delegates to it.
	explicit := NewSkillCatalogWith([]companion.CatalogEntry{{SkillKey: "k1"}})
	if l, _ := explicit.ListCatalogue(context.Background()); len(l) != 1 {
		t.Errorf("NewSkillCatalogWith size = %d, want 1", len(l))
	}
}

// ---------- SpeciesPathRepo ----------

func TestSpeciesPathRepo_ActivePathForSpecies(t *testing.T) {
	repo := NewSpeciesPathRepo()
	p, err := repo.ActivePathForSpecies(context.Background(), "owl")
	if err != nil {
		t.Fatalf("ActivePathForSpecies(owl): %v", err)
	}
	if p.Species != "owl" || !p.Active || len(p.Entries) == 0 {
		t.Fatalf("species path = %+v, want owl active with entries", p)
	}
	if !p.Active || p.Version != 1 {
		t.Errorf("path Active=%v Version=%d, want true/1", p.Active, p.Version)
	}
	// Entries must be defensively cloned — caller mutation must not leak.
	p.Entries[0] = "corrupted"
	again, _ := repo.ActivePathForSpecies(context.Background(), "owl")
	if again.Entries[0] == "corrupted" {
		t.Errorf("ActivePathForSpecies leaked a copy mutation")
	}
}

func TestSpeciesPathRepo_ActivePathForSpecies_UnknownFailsLoud(t *testing.T) {
	repo := NewSpeciesPathRepo()
	if _, err := repo.ActivePathForSpecies(context.Background(), "beast"); !errors.Is(err, companion.ErrSpeciesPathNotFound) {
		t.Errorf("unknown species err = %v, want ErrSpeciesPathNotFound", err)
	}
}

func TestSpeciesPathRepo_ActivePathForSpecies_InactiveFailsLoud(t *testing.T) {
	repo := NewSpeciesPathRepo()
	repo.paths["owl"].Active = false // same-package mutation to arm the inactive branch
	if _, err := repo.ActivePathForSpecies(context.Background(), "owl"); !errors.Is(err, companion.ErrSpeciesPathNotFound) {
		t.Errorf("inactive species err = %v, want ErrSpeciesPathNotFound", err)
	}
}

// ---------- CompanionLoadoutRepo ----------

func TestCompanionLoadoutRepo_MintListEquipLifecycle(t *testing.T) {
	cat := NewSkillCatalog()
	repo := NewCompanionLoadoutRepo(cat)
	ctx := context.Background()
	const tenant, companionID = "tenant-a", "fam-1"

	if want := loadoutKey(tenant, companionID); want != tenant+"|"+companionID {
		t.Fatalf("loadoutKey = %q", want)
	}

	inserted, err := repo.MintGrants(ctx, tenant, companionID, []companion.GrantMint{
		{SkillKey: "progress_mirror", Equipped: true, UnlockedVia: "species_path", UnlockedAtStage: 1},
		{SkillKey: "quiz_me", UnlockedVia: "species_path", UnlockedAtStage: 2},
	})
	if err != nil {
		t.Fatalf("MintGrants: %v", err)
	}
	if len(inserted) != 2 {
		t.Fatalf("inserted = %v, want 2 keys", inserted)
	}

	// Idempotent: re-minting an owned key inserts nothing.
	reMint, err := repo.MintGrants(ctx, tenant, companionID, []companion.GrantMint{{SkillKey: "quiz_me"}})
	if err != nil || len(reMint) != 0 {
		t.Fatalf("re-mint = %v (%v), want empty", reMint, err)
	}

	keys, err := repo.ListGrantedSkillKeys(ctx, tenant, companionID)
	if err != nil || len(keys) != 2 {
		t.Fatalf("ListGrantedSkillKeys = %v (%v), want 2", keys, err)
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		seen[k] = true
	}
	if !seen["progress_mirror"] || !seen["quiz_me"] {
		t.Errorf("granted keys = %v, want progress_mirror + quiz_me", keys)
	}

	grants, err := repo.ListGrants(ctx, tenant, companionID)
	if err != nil || len(grants) != 2 {
		t.Fatalf("ListGrants = %v (%v), want 2", grants, err)
	}
	var mirror *companion.SkillGrant
	for i := range grants {
		if grants[i].SkillKey == "progress_mirror" {
			mirror = &grants[i]
		}
	}
	if mirror == nil {
		t.Fatal("progress_mirror grant missing")
	}
	if mirror.SkillKind != companion.SkillKindActive || mirror.SlotCost != 1 || !mirror.Equipped || mirror.UnlockedAtStage != 1 {
		t.Errorf("enriched grant = %+v, want active/slot1/equipped/stage1", *mirror)
	}

	// SetEquipped over an owned grant persists the flag.
	if err := repo.SetEquipped(ctx, tenant, companionID, "quiz_me", true); err != nil {
		t.Fatalf("SetEquipped: %v", err)
	}
	grants, _ = repo.ListGrants(ctx, tenant, companionID)
	equipped := false
	for _, g := range grants {
		if g.SkillKey == "quiz_me" && g.Equipped {
			equipped = true
		}
	}
	if !equipped {
		t.Errorf("quiz_me not equipped after SetEquipped(true)")
	}
}

func TestCompanionLoadoutRepo_MintGrants_UnknownKeyFailsLoud(t *testing.T) {
	repo := NewCompanionLoadoutRepo(NewSkillCatalog())
	_, err := repo.MintGrants(context.Background(), "tenant-a", "fam-1", []companion.GrantMint{{SkillKey: "not_in_catalog"}})
	if !errors.Is(err, companion.ErrSkillNotInCatalog) {
		t.Errorf("MintGrants err = %v, want ErrSkillNotInCatalog", err)
	}
}

func TestCompanionLoadoutRepo_SetEquipped_UnownedFailsLoud(t *testing.T) {
	repo := NewCompanionLoadoutRepo(NewSkillCatalog())
	if err := repo.SetEquipped(context.Background(), "tenant-a", "fam-1", "quiz_me", true); !errors.Is(err, companion.ErrSkillNotOwned) {
		t.Errorf("SetEquipped unowned err = %v, want ErrSkillNotOwned", err)
	}
}

func TestCompanionLoadoutRepo_MintGrants_CatalogueErrorFailsLoud(t *testing.T) {
	repo := NewCompanionLoadoutRepo(errCatalog{err: errors.New("catalog down")})
	if _, err := repo.MintGrants(context.Background(), "tenant-a", "fam-1", []companion.GrantMint{{SkillKey: "quiz_me"}}); err == nil {
		t.Fatal("MintGrants with failing catalog must fail loud")
	}
}

func TestCompanionLoadoutRepo_TenantAndCompanionIsolation(t *testing.T) {
	repo := NewCompanionLoadoutRepo(NewSkillCatalog())
	ctx := context.Background()
	repo.MintGrants(ctx, "tenant-a", "fam-1", []companion.GrantMint{{SkillKey: "quiz_me"}})
	// Same companion id under a different tenant must not see tenant-a's grants.
	if keys, _ := repo.ListGrantedSkillKeys(ctx, "tenant-b", "fam-1"); len(keys) != 0 {
		t.Errorf("cross-tenant leak: tenant-b keys = %v, want none", keys)
	}
	// Same tenant, different companion id must not see fam-1's grants.
	if keys, _ := repo.ListGrantedSkillKeys(ctx, "tenant-a", "fam-2"); len(keys) != 0 {
		t.Errorf("cross-companion leak: fam-2 keys = %v, want none", keys)
	}
}

// errCatalog is a companion.CatalogReader that always fails — drives
// MintGrants' catalogue-read error arm.
type errCatalog struct{ err error }

func (e errCatalog) ListCatalogue(context.Context) (map[string]companion.CatalogEntry, error) {
	return nil, e.err
}
