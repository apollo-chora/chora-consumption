// awakening_test.go — Phase 3 (CHO-2235, ADR-228 Amendment A1): the
// awakening reveal is class-flavoured per the companion art brief §2 —
// avian/mythic/reptile HATCH, mammal WAKE, machine POWER-ON. The species→
// class map lives in the growth domain (the roll-side species authority)
// and is exposed on the reveal response so the FE never hardcodes the
// taxonomy. The map must be TOTAL over every storable species — a species
// added without a class fails here, loudly, instead of silently rendering
// egg-crack framing for a mammal.
package growth_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestAwakeningClass_ArtBriefRoster(t *testing.T) {
	// docs/design/companion-art-brief.md §2 — the locked roster.
	want := map[string]string{
		"owl":     growth.AwakeningHatch, // avian
		"raven":   growth.AwakeningHatch, // avian
		"penguin": growth.AwakeningHatch, // avian
		"dragon":  growth.AwakeningHatch, // mythic
		"phoenix": growth.AwakeningHatch, // mythic
		"turtle":  growth.AwakeningHatch, // reptile
		"fox":     growth.AwakeningWake,  // mammal
		"cat":     growth.AwakeningWake,  // mammal
		"wolf":    growth.AwakeningWake,  // mammal
		"robot":   growth.AwakeningPowerOn,
	}
	for species, class := range want {
		if got := growth.AwakeningClass(species); got != class {
			t.Errorf("AwakeningClass(%q) = %q, want %q", species, got, class)
		}
	}
}

func TestAwakeningClass_TotalOverStorableSpecies(t *testing.T) {
	// Drift guard: every species the platform can roll (CanonicalSpecies)
	// or store (the 0046 CHECK list) must have an EXPLICIT class entry —
	// the default arm must never fire for a real species.
	storable := append([]string{}, growth.CanonicalSpecies...)
	storable = append(storable, // migration 0046 companion_instances_species_check
		"owl", "fox", "cat", "dragon", "phoenix", "turtle", "wolf", "raven", "penguin")
	for _, species := range storable {
		if _, ok := growth.AwakeningClassBySpecies[species]; !ok {
			t.Errorf("species %q has no explicit awakening class — add it to AwakeningClassBySpecies", species)
		}
	}
}

func TestAwakeningClass_UnknownDefaultsToHatch(t *testing.T) {
	// The documented universal fallback: an unknown or empty species reads
	// as the legacy hatch metaphor rather than crashing a reveal. The
	// totality test above guarantees this arm never fires for a species
	// the platform can actually persist.
	if got := growth.AwakeningClass("gryphon"); got != growth.AwakeningHatch {
		t.Errorf("AwakeningClass(unknown) = %q, want %q", got, growth.AwakeningHatch)
	}
	if got := growth.AwakeningClass(""); got != growth.AwakeningHatch {
		t.Errorf("AwakeningClass(\"\") = %q, want %q", got, growth.AwakeningHatch)
	}
}

func TestRevealBreed_ReturnsAwakeningClass(t *testing.T) {
	// A single-species distribution is the deterministic way to force a mammal
	// roll now that the owned-species set can only REMOVE candidates (owner
	// ruling 2026-08-07); the retired one-species override used to pin it.
	svc, repo, _ := newService(t, withThreshold(25), func(c *growth.ServiceConfig) {
		c.Dist = stubDist{dist: []growth.BreedWeight{{Species: "fox", Probability: 100, Rarity: "common"}}}
	})
	seedEgg(repo, 25)

	resp, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("RevealBreed: %v", err)
	}
	if resp.AwakeningClass != growth.AwakeningWake {
		t.Fatalf("a fox reveal must carry the WAKE class, got %q", resp.AwakeningClass)
	}

	// The idempotent replay carries the same class.
	replay, err := svc.RevealBreed(context.Background(), growth.RevealBreedInput{
		TenantID: "tenant-1", CompanionID: "fam-egg", OwnerGCID: "user-1",
		Traceparent: "00-aa-bb-00",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.AlreadyRevealed || replay.AwakeningClass != growth.AwakeningWake {
		t.Fatalf("replay must carry the persisted class: already=%v class=%q",
			replay.AlreadyRevealed, replay.AwakeningClass)
	}
}
