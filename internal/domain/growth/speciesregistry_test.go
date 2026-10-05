// Species-registry drift guards (CHO-2037).
//
// chora-contracts/companion/species_registry.json is the single canonical
// roster for Companion species. Every consumption surface that carries its own
// copy of the roster is asserted against the registry here, so onboarding
// species N+1 is a fail-loud checklist instead of a scavenger hunt
// (docs/references/companion-species-onboarding.md).
//
// In-package (not growth_test) so the deliberately-unexported
// sovereign-acquire pool (sovereignPool) is guarded too.
package growth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

type registrySpecies struct {
	Key       string `json:"key"`
	WireValue int32  `json:"wire_value"`
	Status    string `json:"status"`
}

type speciesRegistryDoc struct {
	SchemaVersion int               `json:"schema_version"`
	Species       []registrySpecies `json:"species"`
}

// loadSpeciesRegistry walks up from this file's directory to the monorepo
// root and loads the canonical roster. It fails loud — never skips — when the
// registry is missing or malformed: a missing source of truth must never pass
// as green.
func loadSpeciesRegistry(t *testing.T) speciesRegistryDoc {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("species registry: runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		candidate := filepath.Join(dir, "chora-contracts", "companion", "species_registry.json")
		if raw, err := os.ReadFile(candidate); err == nil {
			var doc speciesRegistryDoc
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("species registry %s: malformed JSON: %v", candidate, err)
			}
			validateSpeciesRegistry(t, doc)
			return doc
		}
		// Stop AT the monorepo root (the directory holding go.work). Without
		// this the walk keeps climbing past a git worktree into whatever
		// checkout happens to contain it, reads THAT tree's registry and
		// reports green: exactly how the missed half of the ADR-254 D9 rename
		// hid here for a whole session, because the parent checkout still had
		// the pre-rename directory.
		if _, gerr := os.Stat(filepath.Join(dir, "go.work")); gerr == nil {
			t.Fatalf("%s not found at the monorepo root %s: the canonical species roster is missing (see docs/references/companion-species-onboarding.md)", candidate, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("chora-contracts/companion/species_registry.json not found walking up from the test dir: the canonical species roster is missing (see docs/references/companion-species-onboarding.md)")
		}
		dir = parent
	}
}

func validateSpeciesRegistry(t *testing.T, doc speciesRegistryDoc) {
	t.Helper()
	if doc.SchemaVersion != 1 {
		t.Fatalf("species registry: schema_version = %d, want 1", doc.SchemaVersion)
	}
	if len(doc.Species) == 0 {
		t.Fatal("species registry: empty species list")
	}
	keys := map[string]bool{}
	wires := map[int32]bool{}
	for _, s := range doc.Species {
		if s.Key == "" {
			t.Fatal("species registry: entry with empty key")
		}
		if keys[s.Key] {
			t.Fatalf("species registry: duplicate key %q", s.Key)
		}
		keys[s.Key] = true
		if s.WireValue <= 0 {
			t.Fatalf("species registry: %q wire_value %d must be positive (0 is UNSPECIFIED)", s.Key, s.WireValue)
		}
		if wires[s.WireValue] {
			t.Fatalf("species registry: duplicate wire_value %d (%q) — wire values are never reused", s.WireValue, s.Key)
		}
		wires[s.WireValue] = true
		if s.Status != "hero" && s.Status != "legacy" {
			t.Fatalf("species registry: %q has unknown status %q (want hero|legacy)", s.Key, s.Status)
		}
	}
}

func registryHeroSet(doc speciesRegistryDoc) map[string]bool {
	out := map[string]bool{}
	for _, s := range doc.Species {
		if s.Status == "hero" {
			out[s.Key] = true
		}
	}
	return out
}

func registryAllSet(doc speciesRegistryDoc) map[string]bool {
	out := map[string]bool{}
	for _, s := range doc.Species {
		out[s.Key] = true
	}
	return out
}

func speciesSetFromSlice(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		out[s] = true
	}
	return out
}

// diffSpeciesSets reports what got is missing vs want and what it carries
// beyond want, sorted for stable failure messages.
func diffSpeciesSets(got, want map[string]bool) (missing, extra []string) {
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func assertSpeciesSetEquals(t *testing.T, surface string, got, want map[string]bool) {
	t.Helper()
	missing, extra := diffSpeciesSets(got, want)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s drifted from species registry: missing %v, extra %v — follow docs/references/familiar-species-onboarding.md", surface, missing, extra)
	}
}

func TestSpeciesRegistry_RollAuthorityMatchesHeroes(t *testing.T) {
	heroes := registryHeroSet(loadSpeciesRegistry(t))
	assertSpeciesSetEquals(t, "growth.CanonicalSpecies (egg-gacha roll authority)",
		speciesSetFromSlice(CanonicalSpecies), heroes)
}

func TestSpeciesRegistry_PathCanonMatchesHeroes(t *testing.T) {
	heroes := registryHeroSet(loadSpeciesRegistry(t))
	assertSpeciesSetEquals(t, "familiar.CanonicalSpecies (species-Path canon)",
		speciesSetFromSlice(companion.CanonicalSpecies), heroes)
}

// The sovereign acquire lane used to carry its OWN hardcoded hero set
// (heroSpeciesSet), a second roster to keep in step by hand. It now derives its
// candidate pool from CanonicalSpecies (the roll authority), so this guard
// asserts the derivation stays TOTAL rather than policing a duplicate list:
// onboarding a species must reach the sovereign lane without touching it.
func TestSpeciesRegistry_SovereignAcquirePoolMatchesHeroes(t *testing.T) {
	heroes := registryHeroSet(loadSpeciesRegistry(t))
	got := map[string]bool{}
	for _, w := range sovereignPool() {
		got[w.Species] = true
	}
	assertSpeciesSetEquals(t, "growth.sovereignPool() (sovereign born/hatched acquire)", got, heroes)
}

func TestSpeciesRegistry_SeedspecPathsMatchHeroes(t *testing.T) {
	heroes := registryHeroSet(loadSpeciesRegistry(t))
	got := map[string]bool{}
	for sp := range seedspec.Paths() {
		got[sp] = true
	}
	assertSpeciesSetEquals(t, "seedspec.Paths() (species-Path seed source of truth)", got, heroes)
}

// consumptionSpeciesMigrations pins the LATEST migration file that defines
// each species-bearing SQL constraint in chora_consumption. Onboarding a new
// species requires a NEW migration (ALTER the CHECK / reseed) AND bumping the
// matching entry here — a stale entry fails the guard because the old file's
// roster no longer matches the registry.
var consumptionSpeciesMigrations = struct {
	instancesCheck string // companion_instances species CHECK + hero DEFAULT
	speciesPaths   string // species_paths table CHECK
}{
	instancesCheck: "0046_familiar_species_hero_default_backfill.up.sql",
	speciesPaths:   "0061_familiar_capability_catalogue_v2.up.sql",
}

func readConsumptionMigration(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("species migration manifest points at %s but it cannot be read (%v) — update consumptionSpeciesMigrations to the latest species-bearing migration", name, err)
	}
	return string(raw)
}

var quotedSpeciesRe = regexp.MustCompile(`'([a-z_]+)'`)

// extractQuotedSpecies pulls the quoted lowercase identifiers out of the SQL
// fragment between marker and the next terminator, failing loud when the
// marker is absent (a rewritten migration must bump the manifest).
func extractQuotedSpecies(t *testing.T, sql, file, marker, terminator string) map[string]bool {
	t.Helper()
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("%s: marker %q not found — species SQL moved; update consumptionSpeciesMigrations + this guard", file, marker)
	}
	rest := sql[start+len(marker):]
	end := strings.Index(rest, terminator)
	if end < 0 {
		t.Fatalf("%s: no terminator %q after marker %q", file, terminator, marker)
	}
	out := map[string]bool{}
	for _, m := range quotedSpeciesRe.FindAllStringSubmatch(rest[:end], -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s: no quoted species between %q and %q", file, marker, terminator)
	}
	return out
}

func TestSpeciesRegistry_MigrationConstraintsMatchRegistry(t *testing.T) {
	doc := loadSpeciesRegistry(t)
	heroes := registryHeroSet(doc)
	all := registryAllSet(doc)

	// companion_instances CHECK deliberately admits the FULL wire roster
	// (legacy rows stay readable); only the DEFAULT rolls heroes.
	inst := readConsumptionMigration(t, consumptionSpeciesMigrations.instancesCheck)
	assertSpeciesSetEquals(t,
		consumptionSpeciesMigrations.instancesCheck+" familiar_instances species CHECK",
		extractQuotedSpecies(t, inst, consumptionSpeciesMigrations.instancesCheck,
			"ADD CONSTRAINT familiar_instances_species_check", ";"),
		all)
	assertSpeciesSetEquals(t,
		consumptionSpeciesMigrations.instancesCheck+" familiar_instances species DEFAULT",
		extractQuotedSpecies(t, inst, consumptionSpeciesMigrations.instancesCheck,
			"ALTER COLUMN species SET DEFAULT", ";"),
		heroes)

	// species_paths CHECK admits heroes only — every rollable species must
	// carry a Path or stage-up mints nothing (the CHO-2032 failure mode).
	paths := readConsumptionMigration(t, consumptionSpeciesMigrations.speciesPaths)
	assertSpeciesSetEquals(t,
		consumptionSpeciesMigrations.speciesPaths+" species_paths species CHECK",
		extractQuotedSpecies(t, paths, consumptionSpeciesMigrations.speciesPaths,
			"CREATE TABLE IF NOT EXISTS species_paths", ");"),
		heroes)
}

// TestSpeciesRegistry_DiffDetectsDrift is the mutant-fixture proof that the
// comparator the guards above share actually has teeth in both directions.
func TestSpeciesRegistry_DiffDetectsDrift(t *testing.T) {
	heroes := registryHeroSet(loadSpeciesRegistry(t))
	var anyHero string
	for k := range heroes {
		anyHero = k
		break
	}
	mutant := map[string]bool{"chimera": true}
	for k := range heroes {
		if k != anyHero {
			mutant[k] = true
		}
	}
	missing, extra := diffSpeciesSets(mutant, heroes)
	if len(missing) != 1 || missing[0] != anyHero {
		t.Errorf("comparator failed to flag dropped species %q: missing=%v", anyHero, missing)
	}
	if len(extra) != 1 || extra[0] != "chimera" {
		t.Errorf("comparator failed to flag unknown species: extra=%v", extra)
	}
}
