// ritual_capability_resolver_test.go — CHO-2016 G5 item 2: the capability
// resolver reads loadout (equipped-active + craft) + growth stage + catalogue +
// enabled-count into a companion.RitualCapabilityContext. Fake-tested.
package clients

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

type fakeLoadoutRepo struct{ grants []companion.SkillGrant }

func (f *fakeLoadoutRepo) ListGrantedSkillKeys(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (f *fakeLoadoutRepo) MintGrants(_ context.Context, _, _ string, _ []companion.GrantMint) ([]string, error) {
	return nil, nil
}
func (f *fakeLoadoutRepo) ListGrants(_ context.Context, _, _ string) ([]companion.SkillGrant, error) {
	return f.grants, nil
}
func (f *fakeLoadoutRepo) SetEquipped(_ context.Context, _, _, _ string, _ bool) error { return nil }

type fakeStageReader struct{ stage int }

func (f *fakeStageReader) StageForCompanion(_ context.Context, _, _, _ string) (int, error) {
	return f.stage, nil
}

type fakeCatalogRepo struct {
	entries map[string]companion.CatalogEntry
}

func (f *fakeCatalogRepo) ListCatalogue(_ context.Context) (map[string]companion.CatalogEntry, error) {
	return f.entries, nil
}

type fakeCountRepo struct {
	companion.RitualRepository
	count      int
	gotExclude string
}

func (f *fakeCountRepo) CountEnabledByCompanion(_ context.Context, _, _, exclude string) (int, error) {
	f.gotExclude = exclude
	return f.count, nil
}

func TestCapabilityResolver_BuildsContext(t *testing.T) {
	loadout := &fakeLoadoutRepo{grants: []companion.SkillGrant{
		{SkillKey: "explain_anew", SkillKind: companion.SkillKindActive, Equipped: true},
		{SkillKey: "flashcard_forge", SkillKind: companion.SkillKindActive, Equipped: true},
		{SkillKey: "map_sight", SkillKind: companion.SkillKindActive, Equipped: false},  // owned, not equipped
		{SkillKey: "long_weaving", SkillKind: companion.SkillKindCraft, Equipped: true}, // craft → HasLongWeaving
	}}
	cat := &fakeCatalogRepo{entries: map[string]companion.CatalogEntry{
		"explain_anew": {SkillKey: "explain_anew", Active: true},
	}}
	count := &fakeCountRepo{count: 1}
	r := NewRitualCapabilityResolver(loadout, &fakeStageReader{stage: 4}, cat, count)

	caps, err := r.ResolveCapability(context.Background(), "t-1", "fam-1", "gcid-1", "rit-99")
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	if caps.Stage != 4 {
		t.Errorf("stage = %d, want 4", caps.Stage)
	}
	// EquippedActiveSkills = equipped ACTIVE only (map_sight not equipped, craft excluded).
	if !caps.EquippedActiveSkills["explain_anew"] || !caps.EquippedActiveSkills["flashcard_forge"] {
		t.Errorf("equipped-active missing: %+v", caps.EquippedActiveSkills)
	}
	if caps.EquippedActiveSkills["map_sight"] {
		t.Error("map_sight is owned-but-unequipped — must NOT be in the equipped-active set")
	}
	if caps.EquippedActiveSkills["long_weaving"] {
		t.Error("craft Skills must NOT be in the equipped-active set")
	}
	if !caps.HasLongWeaving {
		t.Error("owning long_weaving must set HasLongWeaving")
	}
	if caps.HasTwinRituals || caps.HasWeaveMastery {
		t.Error("twin_rituals/weave_mastery not owned → flags false")
	}
	if caps.Catalogue["explain_anew"].SkillKey != "explain_anew" {
		t.Error("catalogue not attached")
	}
	if caps.OtherEnabledCount != 1 {
		t.Errorf("OtherEnabledCount = %d, want 1", caps.OtherEnabledCount)
	}
	// The exclude id is forwarded so a re-enable of the ritual itself is not
	// counted against its own quota.
	if count.gotExclude != "rit-99" {
		t.Errorf("exclude not forwarded to count: %q", count.gotExclude)
	}
}
