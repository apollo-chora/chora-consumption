// breed.go — gacha-roll mechanics for HatchEgg per ADR-149 §"Breed lootbox +
// transparency". Uses crypto/rand for the production roll; an injected
// reader function lets tests assert deterministic outcomes.
package growth

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

// CanonicalSpecies is the roll-side authority for the 5 hero species
// (ADR-218 §5.2). Species carries temperament/art/voice only (no mechanical
// link, GQ-8); each hero has art (chora-web/public/assets/companions/<species>),
// a species Path (species_paths, migration 0061), and is storable/encodable
// (companion_instances.species CHECK per migration 0046 + CompanionSpecies proto
// enum). companion.CanonicalSpecies mirrors this list; growth_test's
// TestCanonicalSpeciesReconciled fails loud if the two ever drift again.
//
// CHO-2032: the legacy egg-gacha taxonomy carried 8 breeds here
// (cat/turtle/wolf/raven) that the Grimoire/Path/art layer never supported —
// hatching one produced a companion with an empty Grimoire and stand-in art.
// Those breeds are retired from the roll authority; the proto enum keeps them
// as reserved values for backward-compatible decoding of already-emitted
// events.
var CanonicalSpecies = []string{"owl", "fox", "dragon", "phoenix", "penguin"}

var canonicalSpecies = func() map[string]bool {
	m := make(map[string]bool, len(CanonicalSpecies))
	for _, s := range CanonicalSpecies {
		m[s] = true
	}
	return m
}()

// IsValidSpecies returns true if s is a canonical CompanionSpecies value.
func IsValidSpecies(s string) bool { return canonicalSpecies[s] }

// BreedWeight is one row of the egg SKU's breed_distribution table.
// Probability is on a 0..100 scale and the sum across a distribution MUST
// equal 100 ± distributionTolerance.
type BreedWeight struct {
	Species     string
	Probability float64 // 0..100
	Rarity      string  // common | uncommon | rare | legendary
}

// distributionTolerance is the allowed slack on the sum-to-100 invariant.
// Per Iter G.2 instructions: 100 ± 0.01.
const distributionTolerance = 0.01

// ErrInvalidDistribution is returned by ValidateBreedDistribution + RollBreed
// when the supplied weights are invalid.
var ErrInvalidDistribution = errors.New("growth: invalid breed_distribution")

// ValidateBreedDistribution checks the invariants:
//   - non-empty
//   - all species canonical
//   - all probabilities ≥ 0
//   - sum ≈ 100 within distributionTolerance
func ValidateBreedDistribution(dist []BreedWeight) error {
	if len(dist) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidDistribution)
	}
	var sum float64
	seen := make(map[string]bool, len(dist))
	for _, w := range dist {
		if !canonicalSpecies[w.Species] {
			return fmt.Errorf("%w: unknown species %q", ErrInvalidDistribution, w.Species)
		}
		if seen[w.Species] {
			return fmt.Errorf("%w: duplicate species %q", ErrInvalidDistribution, w.Species)
		}
		seen[w.Species] = true
		if w.Probability < 0 {
			return fmt.Errorf("%w: negative probability for %q", ErrInvalidDistribution, w.Species)
		}
		sum += w.Probability
	}
	diff := sum - 100
	if diff < 0 {
		diff = -diff
	}
	if diff >= distributionTolerance {
		return fmt.Errorf("%w: sum=%g (want 100 ± %g)", ErrInvalidDistribution, sum, distributionTolerance)
	}
	return nil
}

// ExcludeOwnedSpecies removes species the learner already owns from an egg
// SKU's distribution and renormalises the survivors back to 100, preserving
// their relative ratios (owner ruling 2026-08-07: a new ceremony should not
// hand back a companion type the learner already has).
//
// This INVERTS the 2026-07-08 "one species per user" directive, under which
// every Companion after the first INHERITED the committed species. The retired
// override (and its ErrSpeciesLocked / OwnerCommittedSpecies machinery) is gone;
// the replacement signal is ErrSpeciesAlreadyOwned + Repository.OwnerSpeciesSet.
//
// WRAP: when every offered species is owned the declared distribution is returned
// UNCHANGED: a hatch is never blocked, and shiny_variant (rosterCount > 0)
// begins firing exactly when the roster wraps.
//
// DERIVED, NOT HARDCODED: the candidate set is the SKU's own distribution rather
// than CanonicalSpecies, so a species authored later joins the roll the moment it
// appears in a SKU. Callers MUST pass the result to RollBreed so the recorded
// rolled_probability is the EFFECTIVE weight actually rolled against: the same
// function backs the learner-facing odds table, keeping declared == observed for
// the IMDA D2 fairness audit.
//
// Pure: no I/O, no clock, no randomness.
func ExcludeOwnedSpecies(dist []BreedWeight, owned map[string]bool) []BreedWeight {
	kept := make([]BreedWeight, 0, len(dist))
	var total float64
	for _, w := range dist {
		if owned[w.Species] {
			continue
		}
		kept = append(kept, w)
		total += w.Probability
	}
	// Wrap (everything owned) or a degenerate all-zero remainder: fall back to
	// the declared distribution rather than returning something unrollable.
	if len(kept) == 0 || total <= 0 {
		out := make([]BreedWeight, len(dist))
		copy(out, dist)
		return out
	}
	scale := 100.0 / total
	for i := range kept {
		kept[i].Probability *= scale
	}
	// Absorb float drift into the largest survivor so the sum-to-100 invariant
	// holds exactly rather than failing ValidateBreedDistribution by a rounding
	// hair (the roll and the published odds both depend on it).
	var sum float64
	largest := 0
	for i, w := range kept {
		sum += w.Probability
		if w.Probability > kept[largest].Probability {
			largest = i
		}
	}
	kept[largest].Probability += 100.0 - sum
	return kept
}

// RollBreed draws a breed from the supplied distribution using crypto/rand.
// Returns (species, rarity, probability, error). The returned probability is
// the SKU-defined weight for the rolled species, persisted in
// companion_growth_events for IMDA D2 transparency audits.
func RollBreed(dist []BreedWeight) (string, string, float64, error) {
	return RollBreedWithRand(dist, rand.Read)
}

// RollBreedWithRand is the seam under test. Reader supplies the random
// bytes used to derive the roll. Production passes crypto/rand.Read; tests
// pass a deterministic byte sequence.
func RollBreedWithRand(dist []BreedWeight, reader func(p []byte) (int, error)) (string, string, float64, error) {
	if err := ValidateBreedDistribution(dist); err != nil {
		return "", "", 0, err
	}
	// Stable order — sort by Species ascending so test determinism + audit
	// reproducibility hold.
	sorted := make([]BreedWeight, len(dist))
	copy(sorted, dist)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Species < sorted[j].Species
	})

	// Draw 8 bytes → uint64, map to [0, 100).
	var buf [8]byte
	n, err := reader(buf[:])
	if err != nil && err != io.EOF {
		return "", "", 0, fmt.Errorf("growth: read random: %w", err)
	}
	if n < 8 {
		return "", "", 0, fmt.Errorf("growth: insufficient random bytes (got %d)", n)
	}
	raw := binary.BigEndian.Uint64(buf[:])
	// Convert to a [0, 100) float by taking the top 53 bits to avoid loss.
	roll := float64(raw>>11) / float64(uint64(1)<<53) * 100.0

	// Cumulative bucket walk.
	var cum float64
	for _, w := range sorted {
		cum += w.Probability
		if roll < cum {
			return w.Species, w.Rarity, w.Probability, nil
		}
	}
	// Floating-point edge case — fall back to last species.
	last := sorted[len(sorted)-1]
	return last.Species, last.Rarity, last.Probability, nil
}

// BreedDistributionProvider is the port the gRPC server depends on to fetch
// an egg SKU's distribution. Production wiring (Iter G.6) reads from a
// chora_consumption-side projection of chora_tenancy.companion_egg_catalog.
// For Iter G.2 we ship an in-memory fixture implementation.
type BreedDistributionProvider interface {
	// Lookup returns the distribution for an egg SKU. Returns
	// ErrSkuNotFound when the SKU is unknown to the provider.
	Lookup(tenantID, eggSku string) ([]BreedWeight, error)
}

// ErrSkuNotFound is returned by BreedDistributionProvider when the requested
// SKU is unknown.
var ErrSkuNotFound = errors.New("growth: egg SKU not found")
