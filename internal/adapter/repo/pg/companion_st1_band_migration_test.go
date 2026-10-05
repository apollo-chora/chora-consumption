// companion_st1_band_migration_test.go — F-I2 (CHO-2089, ADR-228 D3) migration
// content gate for 0086. The live-DB delta must re-seed EXACTLY what the
// seedspec fixtures say (the same single-source-of-truth discipline the 0061
// drift test enforces on the seed blocks): progress_mirror drops to the st1
// floor and each reordered species Path opens on progress_mirror. Reuses the
// readMigration helper from companion_capability_migration_test.go.
package pg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/seedspec"
)

// speciesReorderedByFI2 — the four species whose 0061-seeded Path had
// progress_mirror at a non-first (former st2-band) position and therefore need
// a live UPDATE. dragon already opened on progress_mirror in the 0061 seed, so
// its array is unchanged and 0086 leaves it alone.
var speciesReorderedByFI2 = []string{"owl", "fox", "penguin", "phoenix"}

func TestMigration0086_ReseedsExactlySeedspecStOneBand(t *testing.T) {
	up := readMigration(t, "0086_familiar_st1_band_progress_mirror.up.sql")

	// progress_mirror lowers to the st1 floor — and nothing else does.
	if !strings.Contains(up, "SET min_growth_stage = 1") || !strings.Contains(up, "'progress_mirror'") {
		t.Error("0086 up must lower progress_mirror min_growth_stage to 1")
	}
	if strings.Contains(up, "min_growth_stage = 0") {
		t.Error("0086 up must not touch any other Skill's floor (only progress_mirror → 1)")
	}

	// Each reordered species Path is re-seeded to EXACTLY the seedspec fixture
	// order — the migration can never disagree with the validated Go mirror.
	paths := seedspec.Paths()
	for _, species := range speciesReorderedByFI2 {
		entries, ok := paths[species]
		if !ok {
			t.Fatalf("seedspec has no path for %q", species)
		}
		if entries[0] != "progress_mirror" {
			t.Errorf("seedspec %q path opens on %q, want progress_mirror", species, entries[0])
		}
		// 0086 is FROZEN at the bytes the live lane applied (fog_scout in the
		// path arrays); 0110_companion_rename rewrites the live arrays to
		// kg_explore, so the seedspec order is compared through the pre-0110 map.
		wantJSON, _ := json.Marshal(seedspec.LegacyKeys(entries))
		if !strings.Contains(up, string(wantJSON)) {
			t.Errorf("0086 up does not re-seed %q to the seedspec order\n  want substring: %s", species, wantJSON)
		}
	}

	// dragon must NOT be re-seeded (already position-1 in the 0061 seed).
	if strings.Contains(up, "'dragon'") {
		t.Error("0086 up re-seeds dragon, but dragon already opened on progress_mirror — no delta needed")
	}
	// Transactional wrapper.
	if !strings.Contains(up, "BEGIN;") || !strings.Contains(up, "COMMIT;") {
		t.Error("0086 up must wrap its UPDATEs in a transaction")
	}
}

func TestMigration0086_DownRevertsToStTwoFloor(t *testing.T) {
	down := readMigration(t, "0086_familiar_st1_band_progress_mirror.down.sql")
	if !strings.Contains(down, "SET min_growth_stage = 2") || !strings.Contains(down, "'progress_mirror'") {
		t.Error("0086 down must restore progress_mirror min_growth_stage to 2")
	}
	// The down restores the pre-F-I2 order: for a reordered species, its ACTIVE
	// Path no longer opens on progress_mirror (progress_mirror sits at its
	// former st2-band position instead).
	for _, species := range speciesReorderedByFI2 {
		if !strings.Contains(down, "'"+species+"'") {
			t.Errorf("0086 down must re-seed the pre-F-I2 %q order", species)
		}
	}
}
