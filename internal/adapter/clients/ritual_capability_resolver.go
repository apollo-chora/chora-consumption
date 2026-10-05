// ritual_capability_resolver.go — CHO-2016 G5 item 2: the production
// RitualCapabilityResolver. Composes the companion's earned capability from the
// loadout (equipped-active + craft ownership), the ADR-149 growth stage, the
// platform catalogue, and the enabled-rituals count — into the
// companion.RitualCapabilityContext the publisher/runner gate on.
package clients

import (
	"context"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// CompanionStageReader yields a companion's current ADR-149 growth stage. The G5
// wiring injects the existing growth read (over srv.Growth.GetCompanionGrowth,
// which authorises on ownerGCID).
type CompanionStageReader interface {
	StageForCompanion(ctx context.Context, tenantID, companionID, ownerGCID string) (int, error)
}

// craft skill keys that expand the Grimoire designer grammar (spec §3).
const (
	craftLongWeaving  = "long_weaving"
	craftTwinRituals  = "twin_rituals"
	craftWeaveMastery = "weave_mastery"
)

// RitualCapabilityResolver implements companion.RitualCapabilityResolver.
type RitualCapabilityResolver struct {
	loadout companion.LoadoutRepository
	stages  CompanionStageReader
	catalog companion.CatalogReader
	rituals companion.RitualRepository
}

// NewRitualCapabilityResolver wires the resolver.
func NewRitualCapabilityResolver(
	loadout companion.LoadoutRepository,
	stages CompanionStageReader,
	catalog companion.CatalogReader,
	rituals companion.RitualRepository,
) *RitualCapabilityResolver {
	return &RitualCapabilityResolver{loadout: loadout, stages: stages, catalog: catalog, rituals: rituals}
}

// Compile-time check.
var _ companion.RitualCapabilityResolver = (*RitualCapabilityResolver)(nil)

// ResolveCapability reads the four inputs and assembles the context.
func (r *RitualCapabilityResolver) ResolveCapability(ctx context.Context, tenantID, companionID, ownerGCID, excludeRitualID string) (companion.RitualCapabilityContext, error) {
	if r == nil || r.loadout == nil || r.stages == nil || r.catalog == nil || r.rituals == nil {
		return companion.RitualCapabilityContext{}, fmt.Errorf("ritual_capability: nil dependency")
	}
	stage, err := r.stages.StageForCompanion(ctx, tenantID, companionID, ownerGCID)
	if err != nil {
		return companion.RitualCapabilityContext{}, fmt.Errorf("ritual_capability: stage: %w", err)
	}
	grants, err := r.loadout.ListGrants(ctx, tenantID, companionID)
	if err != nil {
		return companion.RitualCapabilityContext{}, fmt.Errorf("ritual_capability: grants: %w", err)
	}
	catalogue, err := r.catalog.ListCatalogue(ctx)
	if err != nil {
		return companion.RitualCapabilityContext{}, fmt.Errorf("ritual_capability: catalogue: %w", err)
	}
	count, err := r.rituals.CountEnabledByCompanion(ctx, tenantID, companionID, excludeRitualID)
	if err != nil {
		return companion.RitualCapabilityContext{}, fmt.Errorf("ritual_capability: enabled count: %w", err)
	}

	lo := companion.Loadout{Grants: grants}
	equipped := map[string]bool{}
	for _, k := range lo.EquippedActiveSkillKeys() {
		equipped[k] = true
	}
	owns := func(key string) bool {
		for _, g := range grants {
			if g.SkillKey == key {
				return true
			}
		}
		return false
	}

	return companion.RitualCapabilityContext{
		Stage:                stage,
		EquippedActiveSkills: equipped,
		HasLongWeaving:       owns(craftLongWeaving),
		HasTwinRituals:       owns(craftTwinRituals),
		HasWeaveMastery:      owns(craftWeaveMastery),
		Catalogue:            catalogue,
		OtherEnabledCount:    count,
	}, nil
}
