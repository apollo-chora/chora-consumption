// service_reveal_test.go — Phase 2 (CHO-2229, ADR-228 amendment): the breed
// roll is split OUT of HatchEgg into RevealBreed so the reveal precedes
// naming. RevealBreed is stirring-gated, rolls exactly once, persists the
// roll (species/shiny/rarity/probability + revealed_at) and publishes
// breed_revealed at REVEAL time; a re-POST returns the persisted roll and
// must never re-roll. HatchEgg becomes commit-only: it requires a revealed
// pod, reads the persisted roll, and publishes hatched + stage_up only.
package growth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func revealEgg(svc *growth.Service) (*growth.RevealBreedResponse, error) {
	return svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Traceparent: "00-aa-bb-00",
	})
}

func TestRevealBreed_RejectsWhenNotStirring(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 24) // one short of the threshold

	if _, err := revealEgg(svc); !errors.Is(err, growth.ErrNotStirring) {
		t.Fatalf("expected ErrNotStirring for a pre-threshold pod, got %v", err)
	}
	if row := repo.rows["fam-egg"]; row.RevealedAt != nil || row.Species != "" {
		t.Fatalf("a refused reveal must persist nothing, got species=%q revealed_at=%v", row.Species, row.RevealedAt)
	}
	if got := ox.topics(); len(got) != 0 {
		t.Fatalf("a refused reveal must publish nothing, got %v", got)
	}
}

func TestRevealBreed_RollsPersistsAndPublishes(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 25)

	resp, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("RevealBreed on a stirring pod: %v", err)
	}
	if resp.Species == "" || resp.Rarity == "" {
		t.Fatalf("reveal must return the roll, got species=%q rarity=%q", resp.Species, resp.Rarity)
	}
	if resp.AlreadyRevealed {
		t.Fatalf("first reveal must not report AlreadyRevealed")
	}
	if resp.RevealedAt.IsZero() {
		t.Fatalf("reveal must stamp RevealedAt")
	}

	row := repo.rows["fam-egg"]
	if row.RevealedAt == nil {
		t.Fatalf("reveal must persist revealed_at")
	}
	if row.Species != resp.Species || row.SpeciesRarity != resp.Rarity {
		t.Fatalf("persisted roll %q/%q must match the response %q/%q",
			row.Species, row.SpeciesRarity, resp.Species, resp.Rarity)
	}
	if row.GrowthStage != 0 || row.HatchedAt != nil {
		t.Fatalf("reveal must NOT hatch: stage=%d hatched=%v", row.GrowthStage, row.HatchedAt)
	}

	topics := ox.topics()
	if len(topics) != 1 || topics[0] != growth.TopicCompanionBreedRevealed {
		t.Fatalf("reveal must publish exactly breed_revealed, got %v", topics)
	}
	call := ox.calls[0]
	if call.Env.IdempotencyKey != "breed_revealed:fam-egg" {
		t.Fatalf("envelope idempotency key = %q", call.Env.IdempotencyKey)
	}
	if call.Payload["species"] != resp.Species {
		t.Fatalf("payload species = %v, want %q", call.Payload["species"], resp.Species)
	}
	if _, ok := call.Payload["revealed_at"]; !ok {
		t.Fatalf("payload must carry revealed_at")
	}
}

func TestRevealBreed_IdempotentRePostNeverRerolls(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 25)

	first, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("first reveal: %v", err)
	}
	second, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("idempotent re-POST must succeed, got %v", err)
	}
	if !second.AlreadyRevealed {
		t.Fatalf("re-POST must report AlreadyRevealed")
	}
	if second.Species != first.Species || second.ShinyVariant != first.ShinyVariant ||
		second.Rarity != first.Rarity || second.RolledProbability != first.RolledProbability {
		t.Fatalf("re-POST must return the persisted roll unchanged: first=%+v second=%+v", first, second)
	}
	if !second.RevealedAt.Equal(first.RevealedAt) {
		t.Fatalf("re-POST must not re-stamp revealed_at: %v vs %v", first.RevealedAt, second.RevealedAt)
	}
	if topics := ox.topics(); len(topics) != 1 {
		t.Fatalf("re-POST must not publish a second breed_revealed, got %v", topics)
	}
}

// Owner ruling 2026-08-07: a ceremony must NOT hand back a companion type the
// learner already owns while an unseen species remains. This replaces
// TestRevealBreed_AppliesOneSpeciesPerUserOverride, which asserted the exact
// opposite (the 2026-07-08 one-species directive forced the repeat).
//
// The stub distribution is owl 50 / dragon 50, so owning owl leaves dragon as
// the only survivor: an owl here would be a repeat with 50% probability, which
// a single-run test could not distinguish from luck.
func TestRevealBreed_NeverRepeatsAnOwnedSpecies(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 25)
	repo.owns("owl")

	resp, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("RevealBreed: %v", err)
	}
	if resp.Species == "owl" {
		t.Fatalf("reveal returned the already-owned species owl; an unseen species (dragon) was available")
	}
	if resp.Species != "dragon" {
		t.Fatalf("species = %q, want dragon (the sole unseen survivor)", resp.Species)
	}
}

// The recorded probability MUST be the EFFECTIVE weight actually rolled
// against, not the declared weight of a discarded roll. companion_growth_events
// feeds the IMDA D2 (ADR-149) chi-square fairness audit: a species paired with
// an unrelated probability skews it. Owning owl renormalises dragon 50 -> 100.
func TestRevealBreed_RecordsTheEffectiveRolledProbability(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 25)
	repo.owns("owl")

	resp, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("RevealBreed: %v", err)
	}
	if resp.RolledProbability != 100 {
		t.Fatalf("RolledProbability = %g, want 100 (dragon renormalised after owl was excluded); "+
			"the declared 50 would mean the audit reads a weight the roll never used", resp.RolledProbability)
	}
	if row := repo.rows["fam-egg"]; row.Species != resp.Species || row.RolledProb != resp.RolledProbability {
		t.Fatalf("persisted (%q,%g) must pair the rolled species with ITS OWN weight, response was (%q,%g)",
			row.Species, row.RolledProb, resp.Species, resp.RolledProbability)
	}
	if p := ox.calls[0].Payload["rolled_probability"]; p != resp.RolledProbability {
		t.Fatalf("breed_revealed payload rolled_probability = %v, want %g", p, resp.RolledProbability)
	}
}

// WRAP: once every offered species is owned the declared distribution stands so
// the hatch is never blocked, and shiny_variant (rosterCount > 0) begins firing.
func TestRevealBreed_WrapsWhenEveryOfferedSpeciesIsOwned(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 25)
	repo.owns("owl", "dragon")
	repo.speciesRosterCount = 1 // the wrapped roster already holds one of these

	resp, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("a wrapped roster must still hatch, got %v", err)
	}
	if resp.Species != "owl" && resp.Species != "dragon" {
		t.Fatalf("wrap must roll the declared distribution, got %q", resp.Species)
	}
	if !resp.ShinyVariant {
		t.Fatalf("shiny_variant must fire once the roster wraps (rosterCount > 0), got false")
	}
}

func TestRevealBreed_RejectsAlreadyHatched(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 1, GrowthExp: 30, Species: "owl", HatchedAt: &now,
	}
	if _, err := revealEgg(svc); !errors.Is(err, growth.ErrAlreadyHatched) {
		t.Fatalf("expected ErrAlreadyHatched for a stage-1 companion, got %v", err)
	}
}

func TestRevealBreed_RequiresTraceparent(t *testing.T) {
	svc, repo, _ := newService(t, withThreshold(25))
	seedEgg(repo, 25)
	_, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
	})
	if !errors.Is(err, growth.ErrInvalidArguments) {
		t.Fatalf("expected ErrInvalidArguments for a missing traceparent, got %v", err)
	}
}

func TestHatchEgg_RejectsUnrevealedStirringEgg(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 25) // stirring but NOT revealed

	if err := hatchEgg(svc); !errors.Is(err, growth.ErrNotRevealed) {
		t.Fatalf("commit-only hatch must reject an unrevealed pod with ErrNotRevealed, got %v", err)
	}
	if got := ox.topics(); len(got) != 0 {
		t.Fatalf("a refused hatch must publish nothing, got %v", got)
	}
}

func TestHatchEgg_CommitsThePersistedRoll(t *testing.T) {
	svc, repo, ox := newService(t, withThreshold(25))
	seedEgg(repo, 25)

	revealed, err := revealEgg(svc)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if err := hatchEgg(svc); err != nil {
		t.Fatalf("hatch after reveal: %v", err)
	}

	row := repo.rows["fam-egg"]
	if row.GrowthStage != 1 || row.HatchedAt == nil {
		t.Fatalf("hatch must commit stage 0→1, got stage=%d hatched=%v", row.GrowthStage, row.HatchedAt)
	}
	if row.Species != revealed.Species {
		t.Fatalf("hatch must commit the REVEALED species %q, got %q", revealed.Species, row.Species)
	}

	topics := ox.topics()
	breedRevealedCount := 0
	for _, tp := range topics {
		if tp == growth.TopicCompanionBreedRevealed {
			breedRevealedCount++
		}
	}
	if breedRevealedCount != 1 {
		t.Fatalf("breed_revealed must publish exactly once (at reveal), got %d in %v", breedRevealedCount, topics)
	}
	// The commit publishes hatched + stage_up (+ the slot announcement) —
	// never a second breed_revealed.
	wantAfterReveal := []string{
		growth.TopicCompanionHatched,
		growth.TopicCompanionStageUp,
		growth.TopicCompanionSkillSlotUnlocked,
	}
	after := topics[1:]
	if len(after) != len(wantAfterReveal) {
		t.Fatalf("hatch publishes = %v, want %v", after, wantAfterReveal)
	}
	for i, tp := range wantAfterReveal {
		if after[i] != tp {
			t.Fatalf("hatch publish[%d] = %q, want %q (all: %v)", i, after[i], tp, topics)
		}
	}
}

func TestHatchEgg_NeverConsultsTheDistribution(t *testing.T) {
	// A revealed pod hatches even when the SKU distribution is broken —
	// proof the commit lane reads the persisted roll and never re-rolls.
	distErr := errors.New("distribution must not be consulted at commit time")
	svc, repo, _ := newService(t, withThreshold(25), func(c *growth.ServiceConfig) {
		c.Dist = stubDist{err: distErr}
	})
	revealedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	repo.rows["fam-egg"] = &growth.CompanionGrowthRow{
		CompanionID: "fam-egg", TenantID: "tenant-1", OwnerGCID: "user-1",
		GrowthStage: 0, GrowthExp: 30, EggSku: "egg.standard.v1",
		Species: "owl", SpeciesRarity: "common", RolledProb: 50,
		RevealedAt: &revealedAt,
	}

	if err := hatchEgg(svc); err != nil {
		t.Fatalf("commit-only hatch must not touch the distribution, got %v", err)
	}
	if row := repo.rows["fam-egg"]; row.Species != "owl" {
		t.Fatalf("hatch must keep the persisted species owl, got %q", row.Species)
	}
}
