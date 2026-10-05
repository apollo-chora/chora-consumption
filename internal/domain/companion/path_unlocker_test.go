// path_unlocker_test.go — ADR-218 D3: at each stage transition the next K
// species-Path entries become OWNED grants (idempotent, cumulative through
// the current stage), craft Skills arrive already in effect, and each real
// mint emits skill_granted. RED-first for CHO-2012 P0.
package companion

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeCatalogReader struct {
	entries map[string]CatalogEntry
	err     error
}

func (f *fakeCatalogReader) ListCatalogue(_ context.Context) (map[string]CatalogEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

type fakePathReader struct {
	paths map[string]*SpeciesPath
	err   error
}

func (f *fakePathReader) ActivePathForSpecies(_ context.Context, species string) (*SpeciesPath, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.paths[species]
	if !ok {
		return nil, ErrSpeciesPathNotFound
	}
	return p, nil
}

type fakeGrantWriter struct {
	granted map[string]bool // pre-existing grants by skill key
	minted  []GrantMint
	err     error
}

func (f *fakeGrantWriter) ListGrantedSkillKeys(_ context.Context, _, _ string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]string, 0, len(f.granted))
	for k := range f.granted {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeGrantWriter) MintGrants(_ context.Context, _, _ string, mints []GrantMint) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	var inserted []string
	for _, m := range mints {
		if f.granted[m.SkillKey] {
			continue // idempotent skip
		}
		f.granted[m.SkillKey] = true
		f.minted = append(f.minted, m)
		inserted = append(inserted, m.SkillKey)
	}
	return inserted, nil
}

type fakeLoadoutOutbox struct {
	calls []loadoutOutboxCall
	err   error
}

type loadoutOutboxCall struct {
	Topic   string
	Payload map[string]any
	Env     LoadoutEnvelope
}

func (f *fakeLoadoutOutbox) PublishLoadoutEvent(_ context.Context, topic string, payload map[string]any, env LoadoutEnvelope) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, loadoutOutboxCall{Topic: topic, Payload: payload, Env: env})
	return nil
}

func unlockerFixture(t *testing.T) (*PathUnlocker, *fakeGrantWriter, *fakeLoadoutOutbox) {
	t.Helper()
	catalogue := map[string]CatalogEntry{
		"explain_anew":    {SkillKey: "explain_anew", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
		"recap_scribe":    {SkillKey: "recap_scribe", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
		"quiz_me":         {SkillKey: "quiz_me", SkillKind: SkillKindActive, MinGrowthStage: 3, SlotCost: 1},
		"long_weaving":    {SkillKey: "long_weaving", SkillKind: SkillKindCraft, MinGrowthStage: 5, SlotCost: 0},
		"web_research":    {SkillKey: "web_research", SkillKind: SkillKindActive, MinGrowthStage: 5, SlotCost: 2},
		"progress_mirror": {SkillKey: "progress_mirror", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
	}
	// 6-entry path: 2 at st2, 1 at st3, 0 at st4, 2 at st5, 1 at st6.
	bands := [7]int{0, 0, 2, 1, 0, 2, 1}
	path := &SpeciesPath{Species: "owl", Version: 1, Active: true,
		Entries: []string{"explain_anew", "recap_scribe", "quiz_me", "long_weaving", "web_research", "progress_mirror"}}
	grants := &fakeGrantWriter{granted: map[string]bool{}}
	outbox := &fakeLoadoutOutbox{}
	u, err := NewPathUnlocker(PathUnlockerConfig{
		Catalog: &fakeCatalogReader{entries: catalogue},
		Paths:   &fakePathReader{paths: map[string]*SpeciesPath{"owl": path}},
		Grants:  grants,
		Outbox:  outbox,
		Bands:   bands,
		Clock:   func() time.Time { return time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC) },
		NewID:   func() string { return "evt-fixed" },
	})
	if err != nil {
		t.Fatalf("NewPathUnlocker: %v", err)
	}
	return u, grants, outbox
}

func unlockInput(stage int) UnlockInput {
	return UnlockInput{
		TenantID: "tenant-1", CompanionID: "fam-1", OwnerGCID: "user-1",
		Species: "owl", Stage: stage, Traceparent: "00-trace-01",
	}
}

// TestUnlockThroughStageMintsBandedEntries — stage 3 unlocks positions 1-3
// (2 at st2 + 1 at st3) with unlocked_via=species_path and the banded
// unlocked_at_stage; one skill_granted per mint.
func TestUnlockThroughStageMintsBandedEntries(t *testing.T) {
	u, grants, outbox := unlockerFixture(t)
	if err := u.UnlockThroughStage(context.Background(), unlockInput(3)); err != nil {
		t.Fatalf("UnlockThroughStage: %v", err)
	}
	if len(grants.minted) != 3 {
		t.Fatalf("minted %d grants, want 3 (cumulative through st3)", len(grants.minted))
	}
	byKey := map[string]GrantMint{}
	for _, m := range grants.minted {
		byKey[m.SkillKey] = m
	}
	if m := byKey["explain_anew"]; m.UnlockedAtStage != 2 || m.UnlockedVia != "species_path" || m.Equipped {
		t.Errorf("explain_anew mint = %+v, want st2/species_path/unequipped", m)
	}
	if m := byKey["quiz_me"]; m.UnlockedAtStage != 3 {
		t.Errorf("quiz_me mint stage = %d, want 3", m.UnlockedAtStage)
	}
	if len(outbox.calls) != 3 {
		t.Fatalf("outbox calls = %d, want 3 skill_granted", len(outbox.calls))
	}
	for _, c := range outbox.calls {
		if c.Topic != TopicCompanionSkillGranted {
			t.Errorf("topic = %q, want %q", c.Topic, TopicCompanionSkillGranted)
		}
		if c.Env.TenantID != "tenant-1" || c.Env.Traceparent != "00-trace-01" {
			t.Errorf("envelope not propagated: %+v", c.Env)
		}
	}
}

// TestUnlockThroughStageIdempotent — a second run (redelivery replay) mints
// nothing and emits nothing.
func TestUnlockThroughStageIdempotent(t *testing.T) {
	u, grants, outbox := unlockerFixture(t)
	if err := u.UnlockThroughStage(context.Background(), unlockInput(3)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	mintedBefore, eventsBefore := len(grants.minted), len(outbox.calls)
	if err := u.UnlockThroughStage(context.Background(), unlockInput(3)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(grants.minted) != mintedBefore || len(outbox.calls) != eventsBefore {
		t.Errorf("replay minted %d→%d grants / %d→%d events; want no change",
			mintedBefore, len(grants.minted), eventsBefore, len(outbox.calls))
	}
}

// TestUnlockThroughStageCraftArrivesEquipped — craft Skills mint with
// Equipped=true (owned = in effect, slot-free).
func TestUnlockThroughStageCraftArrivesEquipped(t *testing.T) {
	u, grants, _ := unlockerFixture(t)
	if err := u.UnlockThroughStage(context.Background(), unlockInput(5)); err != nil {
		t.Fatalf("UnlockThroughStage: %v", err)
	}
	var craft *GrantMint
	for i := range grants.minted {
		if grants.minted[i].SkillKey == "long_weaving" {
			craft = &grants.minted[i]
		}
	}
	if craft == nil {
		t.Fatal("long_weaving (craft, position 4, st5 band) not minted at stage 5")
	}
	if !craft.Equipped {
		t.Error("craft mint must arrive Equipped=true")
	}
}

// TestUnlockThroughStageStageOneNoOp — with a st1=0 band vector, stage 1 mints
// nothing (and stage 0 never reaches the unlocker). Contrast with the
// incubation bands below where the st1 band mints the hatch's first Skill.
func TestUnlockThroughStageStageOneNoOp(t *testing.T) {
	u, grants, outbox := unlockerFixture(t)
	if err := u.UnlockThroughStage(context.Background(), unlockInput(1)); err != nil {
		t.Fatalf("UnlockThroughStage: %v", err)
	}
	if len(grants.minted) != 0 || len(outbox.calls) != 0 {
		t.Errorf("stage 1 minted %d / emitted %d, want 0/0", len(grants.minted), len(outbox.calls))
	}
}

// TestUnlockThroughStageHatchMintsStOneBandAutoEquipped — ADR-228 D3 (F-I2):
// under the incubation bands (st1 band K=1) the hatch (stage 0→1) mints the
// FIRST Path entry AUTO-EQUIPPED — st1 carries 2 slots, so the "know thyself"
// starter arrives usable, not a tease. A later stage-up still mints active
// Skills UNEQUIPPED (the equip choice is the standing agency).
func TestUnlockThroughStageHatchMintsStOneBandAutoEquipped(t *testing.T) {
	catalogue := map[string]CatalogEntry{
		"progress_mirror": {SkillKey: "progress_mirror", SkillKind: SkillKindActive, MinGrowthStage: 1, SlotCost: 1},
		"explain_anew":    {SkillKey: "explain_anew", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
		"recap_scribe":    {SkillKey: "recap_scribe", SkillKind: SkillKindActive, MinGrowthStage: 2, SlotCost: 1},
	}
	// Incubation shape: st1 band K=1, st2 band K=2.
	bands := [7]int{0, 1, 2, 0, 0, 0, 0}
	path := &SpeciesPath{Species: "owl", Version: 1, Active: true,
		Entries: []string{"progress_mirror", "explain_anew", "recap_scribe"}}
	grants := &fakeGrantWriter{granted: map[string]bool{}}
	outbox := &fakeLoadoutOutbox{}
	u, err := NewPathUnlocker(PathUnlockerConfig{
		Catalog: &fakeCatalogReader{entries: catalogue},
		Paths:   &fakePathReader{paths: map[string]*SpeciesPath{"owl": path}},
		Grants:  grants,
		Outbox:  outbox,
		Bands:   bands,
		Clock:   func() time.Time { return time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC) },
		NewID:   func() string { return "evt-fixed" },
	})
	if err != nil {
		t.Fatalf("NewPathUnlocker: %v", err)
	}

	// Hatch → unlock through stage 1: exactly the st1 band, auto-equipped.
	if err := u.UnlockThroughStage(context.Background(), unlockInput(1)); err != nil {
		t.Fatalf("UnlockThroughStage(1): %v", err)
	}
	if len(grants.minted) != 1 {
		t.Fatalf("hatch minted %d grants, want 1 (the st1 band)", len(grants.minted))
	}
	if m := grants.minted[0]; m.SkillKey != "progress_mirror" || m.UnlockedAtStage != 1 || !m.Equipped {
		t.Errorf("hatch mint = %+v, want progress_mirror@st1 auto-equipped", m)
	}

	// Awakening → stage 2 mints the st2 band UNEQUIPPED (standing equip choice).
	if err := u.UnlockThroughStage(context.Background(), unlockInput(2)); err != nil {
		t.Fatalf("UnlockThroughStage(2): %v", err)
	}
	byKey := map[string]GrantMint{}
	for _, m := range grants.minted {
		byKey[m.SkillKey] = m
	}
	if m, ok := byKey["explain_anew"]; !ok || m.UnlockedAtStage != 2 || m.Equipped {
		t.Errorf("explain_anew mint = %+v (ok=%v), want st2 unequipped", m, ok)
	}
}

// TestUnlockThroughStageMissingPathFailsLoud — a hatched species without a
// seeded active Path is a seed bug: loud error, never a silent skip.
func TestUnlockThroughStageMissingPathFailsLoud(t *testing.T) {
	u, _, _ := unlockerFixture(t)
	in := unlockInput(3)
	in.Species = "dragon"
	if err := u.UnlockThroughStage(context.Background(), in); !errors.Is(err, ErrSpeciesPathNotFound) {
		t.Fatalf("missing path = %v, want ErrSpeciesPathNotFound", err)
	}
}

// TestUnlockThroughStagePartialMintReplayHeals — grants already minted are
// skipped; only the missing tail mints (crash-heal semantics).
func TestUnlockThroughStagePartialMintReplayHeals(t *testing.T) {
	u, grants, outbox := unlockerFixture(t)
	grants.granted["explain_anew"] = true // pre-existing from a crashed run
	if err := u.UnlockThroughStage(context.Background(), unlockInput(2)); err != nil {
		t.Fatalf("UnlockThroughStage: %v", err)
	}
	if len(grants.minted) != 1 || grants.minted[0].SkillKey != "recap_scribe" {
		t.Fatalf("minted = %+v, want exactly the missing recap_scribe", grants.minted)
	}
	if len(outbox.calls) != 1 {
		t.Errorf("events = %d, want 1 (only the real mint announces)", len(outbox.calls))
	}
}
