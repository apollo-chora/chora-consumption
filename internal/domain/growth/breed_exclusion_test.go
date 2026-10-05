// breed_exclusion_test.go: owned-species exclusion for the hatch roll.
//
// OWNER RULING 2026-08-07: companions must not repeat while the learner has an
// unseen species left. This INVERTS the "one species per user" directive of
// 2026-07-08 (growth.ErrSpeciesLocked), under which the first Companion set the
// species and every later one INHERITED it: the roster's variety was the thing
// being suppressed.
//
// Wrap rule (owner pick): once every species in the SKU's distribution is owned,
// fall back to the full declared distribution rather than blocking the hatch -
// so an egg is never unusable, and shiny_variant (rosterCount > 0) starts firing
// exactly when the roster wraps.
//
// DERIVE, NEVER HARDCODE: the candidate set comes from the SKU's
// breed_distribution, not from CanonicalSpecies, so a newly authored species
// joins the pool the moment it is added to a SKU (owner: "I'll author more
// species as platform matures").
package growth

import (
	"math"
	"testing"
)

func distOf(pairs ...any) []BreedWeight {
	out := make([]BreedWeight, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, BreedWeight{Species: pairs[i].(string), Probability: pairs[i+1].(float64)})
	}
	return out
}

func weightOf(dist []BreedWeight, species string) (float64, bool) {
	for _, w := range dist {
		if w.Species == species {
			return w.Probability, true
		}
	}
	return 0, false
}

// The live case: Dale owns fox + owl + penguin; only dragon (12) and phoenix (8)
// remain, so they must renormalise to 60/40 while keeping their 12:8 ratio.
func TestExcludeOwnedSpeciesRenormalisesRemainder(t *testing.T) {
	dist := distOf("fox", 27.0, "owl", 28.0, "dragon", 12.0, "penguin", 25.0, "phoenix", 8.0)
	owned := map[string]bool{"fox": true, "owl": true, "penguin": true}

	got := ExcludeOwnedSpecies(dist, owned)

	if len(got) != 2 {
		t.Fatalf("want 2 remaining species, got %+v", got)
	}
	if err := ValidateBreedDistribution(got); err != nil {
		t.Fatalf("result must remain a valid distribution: %v", err)
	}
	d, ok := weightOf(got, "dragon")
	if !ok {
		t.Fatal("dragon must survive")
	}
	p, ok := weightOf(got, "phoenix")
	if !ok {
		t.Fatal("phoenix must survive")
	}
	if math.Abs(d-60.0) > 0.01 {
		t.Errorf("dragon = %g, want 60.0 (12 of 20 renormalised)", d)
	}
	if math.Abs(p-40.0) > 0.01 {
		t.Errorf("phoenix = %g, want 40.0 (8 of 20 renormalised)", p)
	}
}

// An owned species must be gone entirely: not merely down-weighted.
func TestExcludeOwnedSpeciesDropsOwnedOutright(t *testing.T) {
	dist := distOf("fox", 50.0, "dragon", 50.0)
	got := ExcludeOwnedSpecies(dist, map[string]bool{"fox": true})
	if _, present := weightOf(got, "fox"); present {
		t.Fatalf("owned species must be absent, got %+v", got)
	}
	if w, _ := weightOf(got, "dragon"); math.Abs(w-100.0) > 0.01 {
		t.Errorf("sole survivor must renormalise to 100, got %g", w)
	}
}

// Wrap: every species owned ⇒ fall back to the declared distribution so the
// hatch is never blocked (owner ruling), and shiny can finally fire.
func TestExcludeOwnedSpeciesWrapsWhenAllOwned(t *testing.T) {
	dist := distOf("fox", 60.0, "dragon", 40.0)
	got := ExcludeOwnedSpecies(dist, map[string]bool{"fox": true, "dragon": true})
	if len(got) != 2 {
		t.Fatalf("wrap must return the full distribution, got %+v", got)
	}
	if err := ValidateBreedDistribution(got); err != nil {
		t.Fatalf("wrap result invalid: %v", err)
	}
	if w, _ := weightOf(got, "fox"); math.Abs(w-60.0) > 0.01 {
		t.Errorf("wrap must preserve declared weights, fox = %g want 60", w)
	}
}

func TestExcludeOwnedSpeciesNoOwnedIsPassthrough(t *testing.T) {
	dist := distOf("fox", 60.0, "dragon", 40.0)
	got := ExcludeOwnedSpecies(dist, nil)
	if len(got) != 2 {
		t.Fatalf("nil owned-set must pass through, got %+v", got)
	}
	if err := ValidateBreedDistribution(got); err != nil {
		t.Fatalf("passthrough invalid: %v", err)
	}
}

// Owning something the SKU never offers must not disturb the distribution.
func TestExcludeOwnedSpeciesIgnoresSpeciesNotInDistribution(t *testing.T) {
	dist := distOf("fox", 60.0, "dragon", 40.0)
	got := ExcludeOwnedSpecies(dist, map[string]bool{"penguin": true})
	if len(got) != 2 {
		t.Fatalf("unrelated owned species must not change the pool, got %+v", got)
	}
	if w, _ := weightOf(got, "fox"); math.Abs(w-60.0) > 0.01 {
		t.Errorf("fox = %g, want unchanged 60", w)
	}
}

// DERIVE-NOT-HARDCODE guarantee: a species the platform authors LATER must be
// rollable purely by appearing in the SKU distribution. If this ever fails, the
// exclusion has been keyed on a hardcoded roster and new species are invisible.
func TestExcludeOwnedSpeciesAdmitsNewlyAuthoredSpecies(t *testing.T) {
	dist := distOf("fox", 50.0, "griffin", 50.0) // griffin authored after launch
	got := ExcludeOwnedSpecies(dist, map[string]bool{"fox": true})
	if len(got) != 1 || got[0].Species != "griffin" {
		t.Fatalf("a newly authored species must survive exclusion, got %+v", got)
	}
	if math.Abs(got[0].Probability-100.0) > 0.01 {
		t.Errorf("griffin = %g, want 100", got[0].Probability)
	}
}

// The roll must actually honour the exclusion end to end.
func TestRollBreedNeverReturnsAnOwnedSpecies(t *testing.T) {
	dist := distOf("fox", 90.0, "dragon", 10.0)
	pool := ExcludeOwnedSpecies(dist, map[string]bool{"fox": true})
	for i := 0; i < 64; i++ {
		species, _, prob, err := RollBreed(pool)
		if err != nil {
			t.Fatalf("roll %d: %v", i, err)
		}
		if species == "fox" {
			t.Fatalf("roll %d returned the owned species fox", i)
		}
		if math.Abs(prob-100.0) > 0.01 {
			t.Errorf("roll %d recorded probability %g; must be the EFFECTIVE weight (100), not the declared 10", i, prob)
		}
	}
}
