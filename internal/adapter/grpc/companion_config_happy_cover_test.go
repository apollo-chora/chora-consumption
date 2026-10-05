// companion_config_happy_cover_test.go — happy-path + growth-axis-error
// coverage for ResolveCompanionConfig / ResolveCompanionInstanceConfig.
//
// The existing companion_config_server_test.go covers the nil-reader, arg-
// validation, NotFound and helper-mapping branches but left the full
// instance+growth → proto merge to the deploy e2e ("no in-mem growth repo
// exists"). That in-mem repo (internal/adapter/repo/inmem.GrowthRepo) DOES
// now exist, so these tests wire a REAL growth.Service as the GrowthStateReader
// and characterize the merge (base instance fields + ADR-149 growth axis) +
// the growth-axis-not-found error branch.
package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	handlerinmem "github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/consumption/v1"
)

// newConfigServer builds a CompanionGrowthServer whose growth axis is a real
// growth.Service over the in-mem repo, plus the supplied instance reader.
func newConfigServer(t *testing.T, reader CompanionInstanceReader) (*CompanionGrowthServer, *inmem.GrowthRepo) {
	t.Helper()
	repo := inmem.NewGrowthRepo()
	svc, err := growth.NewService(growth.ServiceConfig{
		Repo:   repo,
		Outbox: &fakeGrowthOutbox{},
		Dist:   stubBreedDist{},
		Clock:  func() time.Time { return fixedClock },
		NewID:  func() string { return "evt-fixed" },
	})
	require.NoError(t, err)
	s := NewCompanionGrowthServer(svc, WithCompanionInstanceReader(reader))
	return s, repo
}

func TestResolveCompanionConfig_HappyPath(t *testing.T) {
	aha := fixedClock.Add(24 * time.Hour)
	reader := fakeInstanceReader{inst: &companion.Instance{
		CompanionID:           "fam-1",
		TenantID:              "t",
		OwnerGCID:             "g",
		Name:                  "Spark",
		Specialization:        "math",
		EvolutionTier:         companion.TierAdept,
		SkillSlotsUnlocked:    2,
		MemoryContextCapacity: 4000,
		PersonaSummary:        "patient tutor",
		ConfiguredRules: map[string]string{
			"tone":            "socratic",
			"max_hint_count":  "2",
			"learner_persona": "cert-focused",
		},
		SkillGrants: []string{"calc", "graph"},
	}}
	s, repo := newConfigServer(t, reader)

	// Seed the growth axis: a real Stage-3 row with shiny + neighbors + aha.
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: 3, Species: "dragon", ShinyVariant: true, SpeciesRarity: "legendary",
		ResonantAtom:      "atom-9",
		AhaMomentConsumed: true, AhaMomentActiveUntil: &aha,
	})

	resp, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	require.NoError(t, err)
	cfg := resp.Config
	require.NotNil(t, cfg)

	// Base instance fields copied verbatim.
	assert.Equal(t, "fam-1", cfg.CompanionId)
	assert.Equal(t, "g", cfg.OwnerGcid)
	assert.Equal(t, "t", cfg.TenantId)
	assert.Equal(t, "Spark", cfg.Name)
	assert.Equal(t, "math", cfg.Specialization)
	// CHO-2012 (ADR-218 D2/D8): tier + slots DERIVE from the growth stage
	// (stage 3 → adept band, 4 slots); the instance columns no longer feed
	// the config. AllowedSkills = the EQUIPPED loadout — no loadout reader
	// is wired in this test, so the allowlist is empty (the legacy
	// SkillGrants slice is ignored).
	assert.Equal(t, "adept", cfg.EvolutionTier)
	assert.Equal(t, int32(4), cfg.SkillSlotsUnlocked)
	assert.Equal(t, int32(4000), cfg.MemoryContextCapacity)
	assert.Equal(t, "patient tutor", cfg.PersonaSummary)
	assert.Empty(t, cfg.AllowedSkills)
	// configured_rules merged with alias (max_hint_count → max_hints_before_reveal).
	require.NotNil(t, cfg.ConfiguredRules)
	assert.Equal(t, "socratic", cfg.ConfiguredRules.Tone)
	assert.Equal(t, int32(2), cfg.ConfiguredRules.MaxHintsBeforeReveal)
	// learner_persona honored from configured_rules.
	assert.Equal(t, "cert-focused", cfg.LearnerPersona)

	// ADR-149 growth axis (LOAD-BEARING GrowthStage per the docstring).
	assert.Equal(t, int32(3), cfg.GrowthStage)
	assert.Equal(t, "dragon", cfg.Species)
	assert.True(t, cfg.ShinyVariant)
	assert.Equal(t, "atom-9", cfg.ResonantAtomId)
	require.NotNil(t, cfg.AhaMomentActiveUntil)
	assert.Equal(t, aha.Unix(), cfg.AhaMomentActiveUntil.AsTime().Unix())
}

func TestResolveCompanionConfig_GrowthAxisNotFound(t *testing.T) {
	// Instance exists + owned by caller, but NO growth row seeded → the growth
	// reader returns ErrCompanionNotFound, which ResolveCompanionInstanceConfig
	// wraps and ResolveCompanionConfig maps to NotFound.
	reader := fakeInstanceReader{inst: &companion.Instance{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		Name: "Spark", Specialization: "math", EvolutionTier: companion.TierApprentice,
	}}
	s, _ := newConfigServer(t, reader) // growth repo left empty

	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	assert.Equal(t, codes.NotFound, codeOf(t, err))
}

func TestResolveCompanionConfig_InstanceReadError(t *testing.T) {
	// A non-sentinel reader error bubbles to Internal (the default branch).
	reader := fakeInstanceReader{err: assertAnErr{}}
	s, _ := newConfigServer(t, reader)
	_, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	assert.Equal(t, codes.Internal, codeOf(t, err))
}

type assertAnErr struct{}

func (assertAnErr) Error() string { return "boom: instance read failed" }

// TestResolveCompanionConfig_EquippedLoadoutAllowlist — CHO-2012 (ADR-218
// D3): with a loadout reader wired, AllowedSkills = EQUIPPED ACTIVE keys
// only (owned-but-unequipped and craft grants are excluded).
func TestResolveCompanionConfig_EquippedLoadoutAllowlist(t *testing.T) {
	aha := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	_ = aha
	reader := fakeInstanceReader{inst: &companion.Instance{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		Name: "Spark", Specialization: "math",
		EvolutionTier: companion.TierApprentice, SkillSlotsUnlocked: 1,
		MemoryContextCapacity: 1000,
		ConfiguredRules:       map[string]string{},
	}}
	s, repo := newConfigServer(t, reader)
	repo.SeedRow(&growth.CompanionGrowthRow{
		CompanionID: "fam-1", TenantID: "t", OwnerGCID: "g",
		GrowthStage: 2, Species: "owl",
	})

	catalog := handlerinmem.NewSkillCatalog()
	loadouts := handlerinmem.NewCompanionLoadoutRepo(catalog)
	_, err := loadouts.MintGrants(context.Background(), "t", "fam-1", []companion.GrantMint{
		{SkillKey: "explain_anew", Equipped: true, UnlockedVia: "species_path", UnlockedAtStage: 2},
		{SkillKey: "recap_scribe", Equipped: false, UnlockedVia: "species_path", UnlockedAtStage: 2},
		{SkillKey: "long_weaving", Equipped: true, UnlockedVia: "species_path", UnlockedAtStage: 5},
	})
	require.NoError(t, err)
	WithCompanionLoadoutReader(loadouts)(s)

	resp, err := s.ResolveCompanionConfig(context.Background(), &consumptionv1.ResolveCompanionConfigRequest{
		TenantId: "t", CompanionId: "fam-1", CallerGcid: "g",
	})
	require.NoError(t, err)
	cfg := resp.Config
	// Equipped active key only — unequipped recap_scribe and craft
	// long_weaving are excluded from the runtime allowlist.
	assert.Equal(t, []string{"explain_anew"}, cfg.AllowedSkills)
	// Stage-2 derivations: 3 slots, adept band.
	assert.Equal(t, int32(3), cfg.SkillSlotsUnlocked)
	assert.Equal(t, "adept", cfg.EvolutionTier)
}
