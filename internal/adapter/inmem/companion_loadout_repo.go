// companion_loadout_repo.go — in-memory companion.LoadoutRepository +
// companion.CatalogReader + companion.SpeciesPathReader (CHO-2012, ADR-218).
//
// Dev/test twin of the pg adapters over companion_skill_catalog /
// species_paths / companion_skill_grants. The catalogue + paths default to
// the seedspec fixtures — the SAME data migration 0061 seeds — so the
// in-memory dev server exercises real reference data, not stubs.
package inmem

import (
	"context"
	"fmt"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

// SkillCatalog is the in-memory platform catalogue.
type SkillCatalog struct {
	mu      sync.RWMutex
	entries map[string]companion.CatalogEntry
}

// NewSkillCatalog seeds from seedspec.Catalogue() (the 0061 seed mirror).
func NewSkillCatalog() *SkillCatalog {
	return NewSkillCatalogWith(seedspec.Catalogue())
}

// NewSkillCatalogWith seeds from an explicit entry list (tests).
func NewSkillCatalogWith(entries []companion.CatalogEntry) *SkillCatalog {
	m := make(map[string]companion.CatalogEntry, len(entries))
	for _, e := range entries {
		m[e.SkillKey] = e
	}
	return &SkillCatalog{entries: m}
}

// ListCatalogue implements companion.CatalogReader.
func (c *SkillCatalog) ListCatalogue(_ context.Context) (map[string]companion.CatalogEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]companion.CatalogEntry, len(c.entries))
	for k, v := range c.entries {
		out[k] = v
	}
	return out, nil
}

// SetActive flips a row's release gate (test helper for equip paths).
func (c *SkillCatalog) SetActive(skillKey string, active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[skillKey]; ok {
		e.Active = active
		c.entries[skillKey] = e
	}
}

// SpeciesPathRepo is the in-memory species_paths twin.
type SpeciesPathRepo struct {
	mu    sync.RWMutex
	paths map[string]*companion.SpeciesPath
}

// NewSpeciesPathRepo seeds the 5 hero Paths from seedspec.Paths().
func NewSpeciesPathRepo() *SpeciesPathRepo {
	out := &SpeciesPathRepo{paths: map[string]*companion.SpeciesPath{}}
	for species, entries := range seedspec.Paths() {
		out.paths[species] = &companion.SpeciesPath{
			Species: species, Version: 1, Entries: entries, Active: true,
		}
	}
	return out
}

// ActivePathForSpecies implements companion.SpeciesPathReader.
func (r *SpeciesPathRepo) ActivePathForSpecies(_ context.Context, species string) (*companion.SpeciesPath, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.paths[species]
	if !ok || !p.Active {
		return nil, fmt.Errorf("%w: %q", companion.ErrSpeciesPathNotFound, species)
	}
	clone := *p
	clone.Entries = append([]string(nil), p.Entries...)
	return &clone, nil
}

// CompanionLoadoutRepo is the in-memory companion_skill_grants twin.
type CompanionLoadoutRepo struct {
	mu      sync.RWMutex
	catalog companion.CatalogReader
	// grants[tenant|companion][skillKey]
	grants map[string]map[string]companion.SkillGrant
}

// NewCompanionLoadoutRepo wires the grants store to a catalogue (for
// kind/slot-cost enrichment — mirrors the pg JOIN).
func NewCompanionLoadoutRepo(catalog companion.CatalogReader) *CompanionLoadoutRepo {
	return &CompanionLoadoutRepo{catalog: catalog, grants: map[string]map[string]companion.SkillGrant{}}
}

func loadoutKey(tenantID, companionID string) string { return tenantID + "|" + companionID }

// ListGrantedSkillKeys implements companion.GrantWriter.
func (r *CompanionLoadoutRepo) ListGrantedSkillKeys(_ context.Context, tenantID, companionID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var keys []string
	for k := range r.grants[loadoutKey(tenantID, companionID)] {
		keys = append(keys, k)
	}
	return keys, nil
}

// MintGrants implements companion.GrantWriter (idempotent insert; returns
// the keys actually inserted). Unknown catalogue keys fail loud — the pg
// twin's INSERT..SELECT joins the catalogue and inserts nothing for them,
// which the unlocker treats as corruption anyway.
func (r *CompanionLoadoutRepo) MintGrants(ctx context.Context, tenantID, companionID string, mints []companion.GrantMint) ([]string, error) {
	catalogue, err := r.catalog.ListCatalogue(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := loadoutKey(tenantID, companionID)
	if r.grants[key] == nil {
		r.grants[key] = map[string]companion.SkillGrant{}
	}
	var inserted []string
	for _, m := range mints {
		entry, ok := catalogue[m.SkillKey]
		if !ok {
			return inserted, fmt.Errorf("%w: %q", companion.ErrSkillNotInCatalog, m.SkillKey)
		}
		if _, exists := r.grants[key][m.SkillKey]; exists {
			continue
		}
		r.grants[key][m.SkillKey] = companion.SkillGrant{
			SkillKey:        m.SkillKey,
			SkillKind:       entry.SkillKind,
			SlotCost:        entry.SlotCost,
			Equipped:        m.Equipped,
			UnlockedVia:     m.UnlockedVia,
			UnlockedAtStage: m.UnlockedAtStage,
		}
		inserted = append(inserted, m.SkillKey)
	}
	return inserted, nil
}

// ListGrants implements companion.LoadoutRepository.
func (r *CompanionLoadoutRepo) ListGrants(_ context.Context, tenantID, companionID string) ([]companion.SkillGrant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []companion.SkillGrant
	for _, g := range r.grants[loadoutKey(tenantID, companionID)] {
		out = append(out, g)
	}
	return out, nil
}

// SetEquipped implements companion.LoadoutRepository.
func (r *CompanionLoadoutRepo) SetEquipped(_ context.Context, tenantID, companionID, skillKey string, equipped bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := loadoutKey(tenantID, companionID)
	g, ok := r.grants[key][skillKey]
	if !ok {
		return fmt.Errorf("%w: %q", companion.ErrSkillNotOwned, skillKey)
	}
	g.Equipped = equipped
	r.grants[key][skillKey] = g
	return nil
}

// Compile-time port guarantees.
var (
	_ companion.CatalogReader     = (*SkillCatalog)(nil)
	_ companion.SpeciesPathReader = (*SpeciesPathRepo)(nil)
	_ companion.LoadoutRepository = (*CompanionLoadoutRepo)(nil)
)
