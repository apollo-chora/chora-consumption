package http

// companion_roster_next_unlocks.go: the next-stage Path preview for every
// roster row (UX Track U, C1a).
//
// The preview already existed on the direct growth read (CHO-2030 R3-9). C3's
// home renders a NAMED stage-up tease per roster row and will not fall back to
// one growth call per companion, which is exactly the N+1 that debt #44 and the
// inline growth_state exist to have removed.
//
// Two rules shape everything here.
//
// The roster is the PAGE and a preview is a NICETY. Every failure below omits
// the previews and serves the roster, mirroring learnerCatalogue in the ritual
// handlers. The direct growth read 500s on these same errors, and that is right
// there: a profile whose whole subject is one companion's growth should not
// render half of it. A roster that loses its teases has still done its job.
//
// One catalogue read and one path read per SPECIES per request, never per row.
// A roster of eight companions of one species makes one of each.

import (
	"context"
	"errors"
	"log"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// rosterNextUnlocks resolves the previews for every row, keyed by companion id.
//
// A nil map is a legitimate answer and the only one that can be given honestly
// when the seams are unwired: the caller then omits the field per row, and the
// client sees an absent preview rather than an empty list that would read as
// "nothing unlocks next".
func (s *Server) rosterNextUnlocks(ctx context.Context, entries []*companion.RosterEntry) map[string][]nextUnlockResp {
	if s.SpeciesPaths == nil || s.SkillCatalog == nil || len(entries) == 0 {
		return nil
	}
	// One catalogue read for the whole request. If it fails, no row gets a
	// preview: a per-row retry would multiply one outage by the roster size.
	catalogue, err := s.SkillCatalog.ListCatalogue(ctx)
	if err != nil {
		log.Printf("consumption: roster next-unlocks omitted, catalogue unreadable (roster still served): %v", err)
		return nil
	}

	out := make(map[string][]nextUnlockResp, len(entries))
	paths := map[string]*companion.SpeciesPath{} // species → path, resolved once
	for _, entry := range entries {
		if entry == nil || entry.Instance == nil || entry.Growth == nil {
			continue
		}
		species := entry.Growth.CurrentBreed
		if species == "" {
			continue // pre-hatch: no species, so no path and nothing to tease
		}
		path, resolved := paths[species]
		if !resolved {
			path = s.activePathForSpecies(ctx, species)
			paths[species] = path
		}
		if path == nil {
			continue
		}
		previews, err := companion.NextUnlocksForStage(path, catalogue,
			entry.Growth.Stage, companion.DefaultStageUnlockCounts)
		if err != nil {
			log.Printf("consumption: roster next-unlocks omitted for companion %s (roster still served): %v",
				entry.Instance.CompanionID, err)
			continue
		}
		rows := make([]nextUnlockResp, 0, len(previews))
		for _, p := range previews {
			rows = append(rows, nextUnlockResp{
				SkillKey:       p.SkillKey,
				SkillKind:      string(p.SkillKind),
				UnlocksAtStage: p.UnlocksAtStage,
				// Carried, never defaulted. Without it a tease promises a
				// Skill the learner cannot use the moment they earn it.
				CatalogueActive: p.CatalogueActive,
			})
		}
		if len(rows) > 0 {
			out[entry.Instance.CompanionID] = rows
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// activePathForSpecies reads one species' active path, answering nil for both
// "no active path" and "could not read".
//
// The two are logged differently and treated the same, deliberately. A legacy
// species with no path is an expected data state; an unreadable store is not.
// Neither is worth costing the learner their roster, and the caller cannot act
// on the difference: both mean this row gets no tease.
func (s *Server) activePathForSpecies(ctx context.Context, species string) *companion.SpeciesPath {
	path, err := s.SpeciesPaths.ActivePathForSpecies(ctx, species)
	switch {
	case errors.Is(err, companion.ErrSpeciesPathNotFound):
		log.Printf("consumption: roster next-unlocks omitted, no active path for species %q", species)
		return nil
	case err != nil:
		log.Printf("consumption: roster next-unlocks omitted for species %q, path unreadable "+
			"(roster still served): %v", species, err)
		return nil
	}
	return path
}
