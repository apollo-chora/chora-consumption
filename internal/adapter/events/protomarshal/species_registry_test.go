// Species-registry drift guard for the wire-marshal map (CHO-2037).
//
// companionSpeciesEnum is the producer-side name→varint table for every
// CompanionSpecies field this package encodes. Its silent drift from the
// canonical roster was a LIVE bug: penguin was rollable + storable everywhere
// else but could not marshal, deadlettering its lifecycle events (CHO-2032).
// This guard pins the map to chora-contracts/companion/species_registry.json —
// the full wire roster, heroes AND legacy breeds (legacy stays encodable for
// already-emitted event replay/decode).
//
// In-package: the map is deliberately unexported.
package protomarshal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
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
			if len(doc.Species) == 0 {
				t.Fatalf("species registry %s: empty species list", candidate)
			}
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
			t.Fatal("chora-contracts/companion/species_registry.json not found walking up from the test dir — the canonical species roster is missing (see docs/references/companion-species-onboarding.md)")
		}
		dir = parent
	}
}

func TestCompanionSpeciesEnum_MatchesRegistry(t *testing.T) {
	doc := loadSpeciesRegistry(t)

	want := map[string]int32{"": 0} // proto3 UNSPECIFIED
	for _, s := range doc.Species {
		want[s.Key] = s.WireValue
	}

	var missing, wrong, extra []string
	for k, v := range want {
		got, ok := companionSpeciesEnum[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if got != v {
			wrong = append(wrong, k)
		}
	}
	for k := range companionSpeciesEnum {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(wrong)
	sort.Strings(extra)

	if len(missing)+len(wrong)+len(extra) > 0 {
		t.Errorf("companionSpeciesEnum drifted from species registry: missing %v, wrong wire value %v, extra %v — a species the registry knows but this map does not CANNOT MARSHAL (the live penguin bug); follow docs/references/companion-species-onboarding.md", missing, wrong, extra)
	}
}
