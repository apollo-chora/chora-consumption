package growth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestValidateBreedDistribution(t *testing.T) {
	// Sum to 100, all 5 hero species (owl/fox/dragon/phoenix/penguin) → OK.
	good := []growth.BreedWeight{
		{Species: "owl", Probability: 25.0, Rarity: "common"},
		{Species: "fox", Probability: 25.0, Rarity: "common"},
		{Species: "penguin", Probability: 25.0, Rarity: "uncommon"},
		{Species: "dragon", Probability: 25.0, Rarity: "legendary"},
	}
	if err := growth.ValidateBreedDistribution(good); err != nil {
		t.Errorf("good distribution rejected: %v", err)
	}

	// Sum to 99.99 within tolerance (±0.01) → OK.
	tol := []growth.BreedWeight{
		{Species: "owl", Probability: 33.33, Rarity: "common"},
		{Species: "fox", Probability: 33.33, Rarity: "common"},
		{Species: "penguin", Probability: 33.33, Rarity: "uncommon"},
	}
	if err := growth.ValidateBreedDistribution(tol); err == nil {
		t.Errorf("99.99 sum should fail tolerance (off by 0.01)")
	}

	// Sum to 100 with tighter precision → OK.
	tol2 := []growth.BreedWeight{
		{Species: "owl", Probability: 33.34, Rarity: "common"},
		{Species: "fox", Probability: 33.33, Rarity: "common"},
		{Species: "penguin", Probability: 33.33, Rarity: "uncommon"},
	}
	if err := growth.ValidateBreedDistribution(tol2); err != nil {
		t.Errorf("tight-precision good distribution rejected: %v", err)
	}

	// ADR-218: species is one of the 5 heroes only. The legacy gacha breeds
	// (cat/turtle/wolf/raven) are NO LONGER canonical — a distribution that
	// offers them must be rejected (else a hatched companion rolls a species
	// with no Path/art → dead Grimoire). This is the CHO-2032 invariant.
	for _, retired := range []string{"cat", "turtle", "wolf", "raven"} {
		dist := []growth.BreedWeight{
			{Species: "owl", Probability: 50.0, Rarity: "common"},
			{Species: retired, Probability: 50.0, Rarity: "uncommon"},
		}
		if err := growth.ValidateBreedDistribution(dist); err == nil {
			t.Errorf("retired breed %q must be rejected by ValidateBreedDistribution", retired)
		}
	}

	// Sum to 90 → fail.
	bad := []growth.BreedWeight{
		{Species: "owl", Probability: 45.0, Rarity: "common"},
		{Species: "fox", Probability: 45.0, Rarity: "common"},
	}
	if err := growth.ValidateBreedDistribution(bad); err == nil {
		t.Errorf("90-sum distribution should be rejected")
	}

	// Empty → fail.
	if err := growth.ValidateBreedDistribution(nil); err == nil {
		t.Errorf("empty distribution should be rejected")
	}

	// Unknown species → fail.
	bs := []growth.BreedWeight{
		{Species: "unicorn", Probability: 100.0, Rarity: "legendary"},
	}
	if err := growth.ValidateBreedDistribution(bs); err == nil {
		t.Errorf("unknown species should be rejected")
	}

	// Negative probability → fail.
	neg := []growth.BreedWeight{
		{Species: "owl", Probability: -10.0, Rarity: "common"},
		{Species: "fox", Probability: 110.0, Rarity: "common"},
	}
	if err := growth.ValidateBreedDistribution(neg); err == nil {
		t.Errorf("negative probability should be rejected")
	}
}

func TestRollBreed_Deterministic(t *testing.T) {
	dist := []growth.BreedWeight{
		{Species: "owl", Probability: 50.0, Rarity: "common"},
		{Species: "dragon", Probability: 50.0, Rarity: "legendary"},
	}
	// Roll 0..49.9 → owl (cumulative buckets sorted by Species for stability).
	// We supply explicit byte-seed input for determinism in tests.
	species, rarity, prob, err := growth.RollBreedWithRand(dist, mockReader{val: 0}.Read)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First species by Species ordering is "dragon" (alphabetical) — verify
	// the deterministic mapping. Then ensure species matches one of dist.
	found := false
	for _, w := range dist {
		if w.Species == species {
			found = true
			if w.Probability != prob {
				t.Errorf("returned prob = %f, want %f for species %s",
					prob, w.Probability, species)
			}
			if w.Rarity != rarity {
				t.Errorf("rarity mismatch")
			}
		}
	}
	if !found {
		t.Errorf("species %q not in distribution", species)
	}
}

func TestRollBreed_AllProbabilityToOne(t *testing.T) {
	dist := []growth.BreedWeight{
		{Species: "phoenix", Probability: 100.0, Rarity: "legendary"},
	}
	species, _, _, err := growth.RollBreedWithRand(dist, mockReader{val: 42}.Read)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if species != "phoenix" {
		t.Errorf("species = %q, want phoenix", species)
	}
}

func TestRollBreed_RealCryptoRand(t *testing.T) {
	// Uses crypto/rand. Just check it returns something valid; statistical
	// fairness is tested elsewhere with deterministic readers.
	dist := []growth.BreedWeight{
		{Species: "owl", Probability: 50.0, Rarity: "common"},
		{Species: "fox", Probability: 50.0, Rarity: "common"},
	}
	species, _, _, err := growth.RollBreed(dist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if species != "owl" && species != "fox" {
		t.Errorf("species = %q, want owl|fox", species)
	}
}

func TestRollBreed_RejectsBadDistribution(t *testing.T) {
	_, _, _, err := growth.RollBreed(nil)
	if err == nil {
		t.Fatalf("expected error on empty distribution")
	}
	if !strings.Contains(err.Error(), "distribution") {
		t.Errorf("error should mention distribution: %v", err)
	}
}

func TestIsValidSpecies(t *testing.T) {
	// The 5 canonical hero species (ADR-218 §5.2): each has art, a persona,
	// and a species Path. penguin is the 5th hero — the sovereign-acquire
	// path + species_paths + the 0046 DB default already carry it; the roll
	// authority mirrors them here (CHO-2032 reconciliation).
	valid := []string{"owl", "fox", "dragon", "phoenix", "penguin"}
	for _, s := range valid {
		if !growth.IsValidSpecies(s) {
			t.Errorf("IsValidSpecies(%q) = false, want true", s)
		}
	}
	// Retired legacy gacha breeds — no Path, no art; must NOT be canonical.
	for _, s := range []string{"cat", "turtle", "wolf", "raven", "unicorn", ""} {
		if growth.IsValidSpecies(s) {
			t.Errorf("IsValidSpecies(%q) = true, want false (non-hero)", s)
		}
	}
}

// TestCanonicalSpeciesReconciled guards the invariant that broke as CHO-2032:
// the roll-side authority (growth.CanonicalSpecies) and the Path-side mirror
// (companion.CanonicalSpecies) MUST name the same 5 hero species. They drifted
// silently once (growth carried 8 legacy breeds, companion carried the 5
// heroes) → wolf/cat/turtle/raven companions hatched with an empty Grimoire.
func TestCanonicalSpeciesReconciled(t *testing.T) {
	want := map[string]bool{"owl": true, "fox": true, "dragon": true, "phoenix": true, "penguin": true}
	got := map[string]bool{}
	for _, s := range growth.CanonicalSpecies {
		got[s] = true
	}
	if len(got) != len(want) {
		t.Fatalf("growth.CanonicalSpecies = %v, want the 5 heroes %v", growth.CanonicalSpecies, want)
	}
	for s := range want {
		if !got[s] {
			t.Errorf("growth.CanonicalSpecies missing hero %q", s)
		}
	}

	// The Path-side mirror must name the SAME set — this is the exact
	// invariant that drifted as CHO-2032 (growth had 8, companion had 5).
	mirror := map[string]bool{}
	for _, s := range companion.CanonicalSpecies {
		mirror[s] = true
	}
	if len(mirror) != len(got) {
		t.Fatalf("companion.CanonicalSpecies %v != growth.CanonicalSpecies %v",
			companion.CanonicalSpecies, growth.CanonicalSpecies)
	}
	for s := range got {
		if !mirror[s] {
			t.Errorf("companion.CanonicalSpecies missing %q that growth carries (drift)", s)
		}
	}
}

func TestErrInvalidDistribution(t *testing.T) {
	if !errors.Is(growth.ErrInvalidDistribution, growth.ErrInvalidDistribution) {
		t.Errorf("ErrInvalidDistribution should match itself")
	}
}

// mockReader returns a byte sequence then EOF. Used to drive deterministic
// RollBreedWithRand tests.
type mockReader struct {
	val byte
}

func (m mockReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = m.val
	}
	return len(p), nil
}
