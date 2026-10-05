// seedspec_test.go — the CI fail-loud Path validation gate (ADR-218 D3):
// every shipped species Path validates against the shipped catalogue under
// the default bands, and the fixture matches the spec pack's structural
// promises (counts, kinds, Launch 7, closed sinks, price keys).
package seedspec

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestCatalogueShape(t *testing.T) {
	entries := Catalogue()
	if len(entries) != CatalogueSize {
		t.Fatalf("catalogue has %d entries, want %d (24 active + 3 craft)", len(entries), CatalogueSize)
	}
	active, craft := 0, 0
	seen := map[string]bool{}
	validSinks := map[string]bool{
		companion.SinkChat: true, companion.SinkMemoryNote: true,
		companion.SinkSuggestionInbox: true, companion.SinkQuestionBank: true,
		companion.SinkNotification: true, companion.SinkCalendarArtifact: true,
	}
	for _, e := range entries {
		if seen[e.SkillKey] {
			t.Errorf("duplicate skill_key %q", e.SkillKey)
		}
		seen[e.SkillKey] = true
		if e.Active {
			t.Errorf("%s: P0 seeds are DARK — Active must be false until the P2 eval gate", e.SkillKey)
		}
		switch e.SkillKind {
		case companion.SkillKindActive:
			active++
			if !validSinks[e.OutputSink] {
				t.Errorf("%s: invalid sink %q (D9 closed list)", e.SkillKey, e.OutputSink)
			}
			if e.PriceKey == "" {
				t.Errorf("%s: active Skill needs a price_key (spec §7 action codes)", e.SkillKey)
			}
			if e.SlotCost < 1 || e.SlotCost > 2 {
				t.Errorf("%s: slot_cost %d out of the 1..2 range", e.SkillKey, e.SlotCost)
			}
		case companion.SkillKindCraft:
			craft++
			if e.SlotCost != 0 || e.OutputSink != "" || e.PriceKey != "" {
				t.Errorf("%s: craft Skills are slot-free, sink-free, price-free (got %d/%q/%q)",
					e.SkillKey, e.SlotCost, e.OutputSink, e.PriceKey)
			}
		default:
			t.Errorf("%s: unknown skill_kind %q", e.SkillKey, e.SkillKind)
		}
		// ADR-228 D3: progress_mirror ALONE drops to st1 (the EXP-gated hatch
		// mints it as the st1 band); every other Skill stays in 2..6, proving
		// "no other skill touched".
		lo := 2
		if e.SkillKey == "progress_mirror" {
			lo = 1
		}
		if e.MinGrowthStage < lo || e.MinGrowthStage > 6 {
			t.Errorf("%s: min_growth_stage %d outside %d..6", e.SkillKey, e.MinGrowthStage, lo)
		}
	}
	if active != 24 || craft != 3 {
		t.Errorf("catalogue split = %d active / %d craft, want 24/3", active, craft)
	}
	for _, key := range LaunchSeven {
		if !seen[key] {
			t.Errorf("Launch-7 Skill %q missing from the catalogue", key)
		}
	}
}

// TestProgressMirrorIsEveryHatchFirstSkill — ADR-228 D3 (F-I2): every species
// Path OPENS on progress_mirror, so the EXP-gated hatch (stage 0→1, st1 band
// K=1) mints the "know thyself" starter for every hatchling; and its floor is
// lowered 2→1 so ValidatePath admits it at position 1 (species differentiation
// still begins at st2 as before).
func TestProgressMirrorIsEveryHatchFirstSkill(t *testing.T) {
	if got := CatalogueByKey()["progress_mirror"].MinGrowthStage; got != 1 {
		t.Errorf("progress_mirror min_growth_stage = %d, want 1 (ADR-228 D3 st1 floor)", got)
	}
	for _, species := range companion.CanonicalSpecies {
		entries := Paths()[species]
		got := "<empty>"
		if len(entries) > 0 {
			got = entries[0]
		}
		if got != "progress_mirror" {
			t.Errorf("%s path opens on %q, want progress_mirror (the hatch-minted first Skill)", species, got)
		}
	}
}

// TestEverySpeciesPathValidates — the D3 gate: all 5 Paths cover the full
// catalogue exactly once and never front-run a floor under the default
// 1/3/4/6/7/6 bands (ADR-228 D3: st1 band K=1 + the rest unchanged).
func TestEverySpeciesPathValidates(t *testing.T) {
	catalogue := CatalogueByKey()
	paths := Paths()
	if len(paths) != len(companion.CanonicalSpecies) {
		t.Fatalf("paths for %d species, want %d", len(paths), len(companion.CanonicalSpecies))
	}
	for _, species := range companion.CanonicalSpecies {
		entries, ok := paths[species]
		if !ok {
			t.Errorf("species %q has no seeded path", species)
			continue
		}
		p := companion.SpeciesPath{Species: species, Version: 1, Entries: entries, Active: true}
		if err := companion.ValidatePath(p, catalogue, companion.DefaultStageUnlockCounts); err != nil {
			t.Errorf("species %q path invalid: %v", species, err)
		}
	}
}

// TestEveryRollableSpeciesHasPath is the guard that CHO-2032 needed and did
// not have: the ROLL authority (growth.CanonicalSpecies — what an egg hatch
// or sovereign acquire can actually produce) must be a subset of the seeded
// species Paths. The original coverage test above only checked
// companion.CanonicalSpecies (the Path side), which matched even while the
// roll side carried 4 extra breeds (cat/turtle/wolf/raven) with no Path — so
// hatching one produced a companion that stage-up-errored into an empty
// Grimoire. Ties the two authorities together at the seed layer.
func TestEveryRollableSpeciesHasPath(t *testing.T) {
	paths := Paths()
	for _, species := range growth.CanonicalSpecies {
		if _, ok := paths[species]; !ok {
			t.Errorf("rollable species %q (growth.CanonicalSpecies) has NO seeded species Path — "+
				"a hatched %s companion would stage-up-error into an empty Grimoire (CHO-2032)", species, species)
		}
	}
}

// TestBandsSumToCatalogue — the default bands must reach the full catalogue
// exactly at Matured (cumulative 27), keeping slots < unlocks from st2 on
// (ADR-218 D2 tuning invariant).
func TestBandsSumToCatalogue(t *testing.T) {
	if got := companion.CumulativeUnlocksThroughStage(6, companion.DefaultStageUnlockCounts); got != CatalogueSize {
		t.Fatalf("cumulative unlocks at Matured = %d, want %d", got, CatalogueSize)
	}
	// Slots stay strictly below cumulative unlocks from stage 2 on so the
	// equip choice remains meaningful (owner ruling GQ-5).
	for stage := 2; stage <= 6; stage++ {
		// growth.SlotsForStage is stage+1 — assert against the literal to
		// avoid a domain-package import cycle in this test's package.
		slots := stage + 1
		unlocks := companion.CumulativeUnlocksThroughStage(stage, companion.DefaultStageUnlockCounts)
		if slots >= unlocks {
			t.Errorf("stage %d: slots (%d) must stay below cumulative unlocks (%d)", stage, slots, unlocks)
		}
	}
}

// TestWeaveMasteryIsTheCapstone — every species ends on weave_mastery (the
// v2 canvas ships AS this Skill, R2-5) and every position-27 entry is
// st6-floored by construction.
func TestWeaveMasteryIsTheCapstone(t *testing.T) {
	for species, entries := range Paths() {
		if entries[len(entries)-1] != "weave_mastery" {
			t.Errorf("%s path ends on %q, want weave_mastery", species, entries[len(entries)-1])
		}
	}
}

// TestCatalogueSeedSQLShape — the generator emits one row per catalogue
// entry, dark (active FALSE), idempotent, with every skill key present.
func TestCatalogueSeedSQLShape(t *testing.T) {
	sql := CatalogueSeedSQL()
	if !strings.Contains(sql, "ON CONFLICT (skill_key) DO NOTHING") {
		t.Error("catalogue seed must be idempotent on skill_key")
	}
	if strings.Contains(sql, ", TRUE)") {
		t.Error("catalogue seed must be fully dark (no active=TRUE rows in P0)")
	}
	for _, e := range Catalogue() {
		if !strings.Contains(sql, "('"+e.SkillKey+"', ") {
			t.Errorf("seed SQL missing row for %q", e.SkillKey)
		}
		if skillDescriptions[e.SkillKey] == "" {
			t.Errorf("no description for %q (IMDA D2 disclosure line required)", e.SkillKey)
		}
	}
}

// TestSpeciesPathsSeedSQLShape — 5 species rows, one-active-per-species
// conflict target, JSON entries intact.
func TestSpeciesPathsSeedSQLShape(t *testing.T) {
	sql := SpeciesPathsSeedSQL()
	if !strings.Contains(sql, "ON CONFLICT (species) WHERE active DO NOTHING") {
		t.Error("species seed must target the one-active-per-species partial index")
	}
	for species := range Paths() {
		if !strings.Contains(sql, "('"+species+"', 1, ") {
			t.Errorf("seed SQL missing species row %q", species)
		}
	}
	if !strings.Contains(sql, `\"weave_mastery\"`) && !strings.Contains(sql, `"weave_mastery"`) {
		t.Error("entries JSON lost the capstone skill")
	}
}

// TestPathFloorValidationGate — the exported gate used by the migration
// content test + future authoring tooling.
func TestPathFloorValidationGate(t *testing.T) {
	if err := PathFloorValidation(); err != nil {
		t.Fatalf("PathFloorValidation: %v", err)
	}
}

// TestSQLQuoteEscapes — description literals with quotes must not break
// the emitted SQL.
func TestSQLQuoteEscapes(t *testing.T) {
	if got := sqlQuote("it's"); got != "'it''s'" {
		t.Errorf("sqlQuote = %q", got)
	}
	if got := sqlStringOrNull(""); got != "NULL" {
		t.Errorf("sqlStringOrNull(empty) = %q, want NULL", got)
	}
	if got := sqlStringOrNull("x"); got != "'x'" {
		t.Errorf("sqlStringOrNull(x) = %q", got)
	}
}
