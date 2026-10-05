// speciespath.go — ADR-218 D3: the species Path is platform-authored
// reference data giving each species a fixed unlock ORDER over the same
// full catalogue (no exclusives, no discounts — species stays temperament/
// art/voice). At each stage-up the next K entries unlock; K per stage is
// editor-tunable (ADR-201) with the spec-pack defaults below.
//
// Path safety invariant (D3, fail-loud): a Path may NEVER front-run a
// Skill's min_growth_stage floor — ValidatePath rejects at seed/publish
// time with named errors, and the CI seed test (seedspec package) keeps the
// shipped Paths honest.
package companion

import (
	"context"
	"errors"
	"fmt"
)

// CanonicalSpecies — the 5 hero species (growth/breed.go is the roll-side
// authority; this list mirrors it for Path validation).
var CanonicalSpecies = []string{"owl", "fox", "dragon", "phoenix", "penguin"}

// DefaultStageUnlockCounts is the editor-tunable default K-per-stage-up
// vector, indexed by growth stage: 1/3/4/6/7/6 at stages 1/2/3/4/5/6
// (cumulative 1·4·8·14·21·27 — the full 27-Skill catalogue by Matured).
// ADR-228 D3 (the incubation arc) carved a st1 band of K=1 out of the former
// st2 K=4 (now split 1+3): the st1 band is the one Skill the EXP-gated hatch
// (stage 0→1) mints auto-equipped (progress_mirror, the "know thyself"
// starter). Every st2-on cumulative total is UNCHANGED, so no Skill except
// that first one moves band. spec pack §4 + ADR-228 D3.
var DefaultStageUnlockCounts = [7]int{0, 1, 3, 4, 6, 7, 6}

// Named fail-loud validation errors (D3).
var (
	ErrPathUnknownSpecies  = errors.New("companion.speciespath: unknown species")
	ErrPathUnknownSkill    = errors.New("companion.speciespath: path entry not in catalogue")
	ErrPathDuplicateSkill  = errors.New("companion.speciespath: duplicate path entry")
	ErrPathMissingSkill    = errors.New("companion.speciespath: catalogue skill missing from path")
	ErrPathFloorViolation  = errors.New("companion.speciespath: path position front-runs min_growth_stage floor")
	ErrSpeciesPathNotFound = errors.New("companion.speciespath: no active path for species")
)

// SpeciesPath is one versioned per-species unlock order (the Go projection
// of the species_paths table).
type SpeciesPath struct {
	PathID  string
	Species string
	Version int
	// Entries is the full ordered skill_key sequence (position 1 = index 0).
	Entries []string
	Active  bool
}

// SpeciesPathReader loads the ACTIVE path for a species. Implemented by the
// pg adapter over species_paths.
type SpeciesPathReader interface {
	// ActivePathForSpecies returns ErrSpeciesPathNotFound when the species
	// has no active path (a seed bug once P0 has shipped — callers fail
	// loud, never skip silently).
	ActivePathForSpecies(ctx context.Context, species string) (*SpeciesPath, error)
}

// UnlockStageForPosition returns the growth stage at which the 1-indexed
// Path position unlocks under the given band vector. Positions beyond the
// cumulative band total clamp to stage 6 (they would only exist if the
// catalogue outgrew the bands — ValidatePath rejects that separately).
func UnlockStageForPosition(pos int, bands [7]int) int {
	if pos < 1 {
		return 0
	}
	cum := 0
	for stage := 0; stage <= 6; stage++ {
		cum += bands[stage]
		if pos <= cum {
			return stage
		}
	}
	return 6
}

// CumulativeUnlocksThroughStage returns how many Path entries are unlocked
// once the companion has REACHED the given stage (cumulative band sum).
func CumulativeUnlocksThroughStage(stage int, bands [7]int) int {
	if stage < 0 {
		return 0
	}
	if stage > 6 {
		stage = 6
	}
	cum := 0
	for s := 0; s <= stage; s++ {
		cum += bands[s]
	}
	return cum
}

// NextUnlockPreview is one upcoming Path entry surfaced to the stage-up
// ceremony / named-tease (CHO-2030, R3-9): the Skill is NAMED, its
// catalogue-activation state rides along so dormants render as "stirring,
// not yet awake" — never hidden, never fake-usable.
type NextUnlockPreview struct {
	SkillKey        string
	SkillKind       SkillKind
	UnlocksAtStage  int
	CatalogueActive bool
}

// NextUnlocksForStage returns the Path entries that unlock at exactly
// stage+1 under the band vector (the R5-1 option-A preview — the Paths
// stay the single source of truth; no FE fork of the band data). Empty at
// the top stage; zero bands fall back to DefaultStageUnlockCounts; a Path
// entry missing from the catalogue is seed corruption and fails loud
// (ErrPathUnknownSkill), mirroring the unlocker.
func NextUnlocksForStage(path *SpeciesPath, catalogue map[string]CatalogEntry, stage int, bands [7]int) ([]NextUnlockPreview, error) {
	if path == nil {
		return nil, fmt.Errorf("%w: nil path", ErrSpeciesPathNotFound)
	}
	if bands == ([7]int{}) {
		bands = DefaultStageUnlockCounts
	}
	if stage >= 6 {
		return []NextUnlockPreview{}, nil
	}
	from := CumulativeUnlocksThroughStage(stage, bands)
	to := CumulativeUnlocksThroughStage(stage+1, bands)
	if from > len(path.Entries) {
		from = len(path.Entries)
	}
	if to > len(path.Entries) {
		to = len(path.Entries)
	}
	out := make([]NextUnlockPreview, 0, to-from)
	for i := from; i < to; i++ {
		key := path.Entries[i]
		entry, ok := catalogue[key]
		if !ok {
			return nil, fmt.Errorf("%w: %q at position %d of the %s path",
				ErrPathUnknownSkill, key, i+1, path.Species)
		}
		out = append(out, NextUnlockPreview{
			SkillKey:        key,
			SkillKind:       entry.SkillKind,
			UnlocksAtStage:  UnlockStageForPosition(i+1, bands),
			CatalogueActive: entry.Active,
		})
	}
	return out, nil
}

// ValidatePath enforces the D3 authoring invariants against a catalogue
// snapshot: canonical species; every catalogue key exactly once (full
// catalogue by Matured, GQ-6-f1); no unknown keys; no band position
// front-running a Skill's min_growth_stage floor. First violation wins;
// errors carry the offending key/position.
func ValidatePath(p SpeciesPath, catalogue map[string]CatalogEntry, bands [7]int) error {
	speciesOK := false
	for _, s := range CanonicalSpecies {
		if p.Species == s {
			speciesOK = true
			break
		}
	}
	if !speciesOK {
		return fmt.Errorf("%w: %q", ErrPathUnknownSpecies, p.Species)
	}

	seen := make(map[string]bool, len(p.Entries))
	for i, key := range p.Entries {
		pos := i + 1
		entry, ok := catalogue[key]
		if !ok {
			return fmt.Errorf("%w: %q at position %d", ErrPathUnknownSkill, key, pos)
		}
		if seen[key] {
			return fmt.Errorf("%w: %q at position %d", ErrPathDuplicateSkill, key, pos)
		}
		seen[key] = true
		if unlockStage := UnlockStageForPosition(pos, bands); unlockStage < entry.MinGrowthStage {
			return fmt.Errorf("%w: %q at position %d unlocks at stage %d but requires stage %d",
				ErrPathFloorViolation, key, pos, unlockStage, entry.MinGrowthStage)
		}
	}
	for key := range catalogue {
		if !seen[key] {
			return fmt.Errorf("%w: %q absent from the %s path", ErrPathMissingSkill, key, p.Species)
		}
	}
	return nil
}
