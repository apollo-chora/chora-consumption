// breed_cover_test.go — branch-completion coverage for the gacha roll.
// Targets the validation duplicate-species arm + the three RollBreedWithRand
// error/edge arms (reader error, short read, floating-point fall-back) that
// breed_test.go's deterministic readers never trip.
package growth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// ValidateBreedDistribution rejects a distribution that repeats a species even
// when every species is canonical and the sum is exactly 100.
func TestValidateBreedDistribution_DuplicateSpecies(t *testing.T) {
	dist := []growth.BreedWeight{
		{Species: "owl", Probability: 50, Rarity: "common"},
		{Species: "owl", Probability: 50, Rarity: "common"}, // duplicate
	}
	err := growth.ValidateBreedDistribution(dist)
	if !errors.Is(err, growth.ErrInvalidDistribution) {
		t.Fatalf("expected ErrInvalidDistribution, got %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got %v", err)
	}
}

// errReader returns a non-EOF error from Read. RollBreedWithRand must wrap it
// rather than proceed with an undefined roll.
type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

func TestRollBreedWithRand_ReaderError(t *testing.T) {
	dist := []growth.BreedWeight{
		{Species: "owl", Probability: 100, Rarity: "common"},
	}
	sentinel := errors.New("entropy source down")
	_, _, _, err := growth.RollBreedWithRand(dist, errReader{err: sentinel}.Read)
	if err == nil {
		t.Fatalf("expected error from failing reader")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error should wrap the reader error, got %v", err)
	}
}

// shortReader fills fewer than 8 bytes and returns nil error — the "insufficient
// random bytes" guard must fire (n < 8).
type shortReader struct{ n int }

func (s shortReader) Read(p []byte) (int, error) {
	limit := s.n
	if limit > len(p) {
		limit = len(p)
	}
	for i := 0; i < limit; i++ {
		p[i] = 0
	}
	return limit, nil
}

func TestRollBreedWithRand_InsufficientBytes(t *testing.T) {
	dist := []growth.BreedWeight{
		{Species: "owl", Probability: 100, Rarity: "common"},
	}
	_, _, _, err := growth.RollBreedWithRand(dist, shortReader{n: 4}.Read)
	if err == nil {
		t.Fatalf("expected error for short read")
	}
	if !strings.Contains(err.Error(), "insufficient random bytes") {
		t.Errorf("error should mention insufficient random bytes, got %v", err)
	}
}

// maxByteReader fills all 8 bytes with 0xFF → the largest possible roll, just
// under 100. With a distribution whose cumulative sum is itself just under the
// roll (but still within the ±0.01 tolerance), the cumulative bucket walk never
// satisfies roll < cum, exercising the floating-point fall-back-to-last arm.
type maxByteReader struct{}

func (maxByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xFF
	}
	return len(p), nil
}

func TestRollBreedWithRand_FloatingPointFallbackToLast(t *testing.T) {
	// Sum = 99.991 → diff from 100 is 0.009 < tolerance 0.01, so it validates.
	// 0xFF*8 yields roll ≈ 99.99999... which is >= 99.991, so the walk falls
	// through to the last (alphabetically-sorted) species.
	dist := []growth.BreedWeight{
		{Species: "fox", Probability: 49.991, Rarity: "common"},
		{Species: "owl", Probability: 50.0, Rarity: "common"},
	}
	species, rarity, prob, err := growth.RollBreedWithRand(dist, maxByteReader{}.Read)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sorted ascending by species: ["fox", "owl"]; the last is "owl".
	if species != "owl" {
		t.Errorf("fall-back species = %q, want owl (the last sorted bucket)", species)
	}
	if rarity != "common" {
		t.Errorf("rarity = %q, want common", rarity)
	}
	if prob != 50.0 {
		t.Errorf("prob = %g, want 50.0", prob)
	}
}
