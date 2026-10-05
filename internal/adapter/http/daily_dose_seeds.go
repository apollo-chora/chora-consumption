// daily_dose_seeds.go — L4 Fix-1 (CHO-1702): source the daily-dose atom
// UNIVERSE from the learner's REAL enrolled-course LearningPaths instead of
// the synthetic 20-atom NewAtomCatalogue. This is a handler-level INPUT swap:
// the 40/30/30 + pickWeaknessSlots + SM-2 composer is untouched — only
// DailyDoseInput.Seeds (and the /atoms/{id}/feedback seed resolution) change
// source. Both reads are local chora_consumption projections (learning_paths
// + atom_index) — no cross-DB access.
//
// Fail-loud contract (no-debt directive 2026-06-10):
//   - a repo ERROR (pg outage, RLS-context miss) surfaces as an error → the
//     handler 500s. Silently serving synthetic atoms on error masks failure.
//   - atom_index.ErrNotFound is NOT an error: an enrolled-but-unprojected
//     atom is honestly OMITTED; no paths / no resolvable atoms ⇒ EMPTY
//     universe ("no atoms queued"), NOT synthetic.
//   - nil repos (wiring-level absence — tests / dev without pg) ⇒ synthetic
//     catalogue. Production always wires both (cmd/server logs loudly when a
//     pool is missing).
package http

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// doseAtomUniverse returns the learner's dose universe: every atom on the
// learner's non-deleted LearningPaths, resolved to metadata via the
// atom_index projection, deduplicated across paths.
//
// ctx must carry the RLS tenant/gcid (repoCtx) — the pg adapters'
// rls.ApplySession reads tracing.{TenantID,GCID}FromContext.
func (s *Server) doseAtomUniverse(ctx context.Context, tenantID, gcid string) ([]companion.AtomSeed, error) {
	if s.LearningPaths == nil || s.AtomIndex == nil {
		return s.syntheticSeeds(), nil
	}
	paths, err := s.LearningPaths.ListByLearner(ctx, tenantID, gcid, 0)
	if err != nil {
		return nil, fmt.Errorf("dose universe: list learner paths: %w", err)
	}
	seen := make(map[string]bool)
	seeds := make([]companion.AtomSeed, 0)
	for _, p := range paths {
		if p == nil {
			continue
		}
		for _, atomID := range p.AtomIDs {
			if atomID == "" || seen[atomID] {
				continue
			}
			seen[atomID] = true
			a, gerr := s.AtomIndex.Get(ctx, atomID)
			if errors.Is(gerr, atom_index.ErrNotFound) || (gerr == nil && a == nil) {
				continue // unprojected — honest omission, not an error
			}
			if gerr != nil {
				return nil, fmt.Errorf("dose universe: resolve atom %s: %w", atomID, gerr)
			}
			// CHO-1968: serve only PUBLISHED + answerable atoms. A draft/archived
			// or question-less atom is silently skipped (the learner is never
			// shown an unplayable or un-gradable dead-end). A real Get error above
			// still surfaces (fail-loud), only servability is a quiet skip.
			// Servable() is the one definition of that bar (ADR-243 D4), shared
			// with the goal tranche and the concept-attachment candidate source.
			if !a.Servable() {
				continue
			}
			seeds = append(seeds, companion.AtomSeed{
				AtomID: a.AtomID,
				Topic:  a.PrimaryTopic(),
				Title:  a.Title,
			})
		}
	}
	return seeds, nil
}

// resolveAtomSeed resolves one atom for /atoms/{id}/feedback: atom_index
// first (real published atoms), synthetic catalogue as fallback. Shipping
// this together with the universe swap is load-bearing — without it a real
// atom_id 404s on feedback, the SM-2 state is never written, and the
// Ebbinghaus review slot never fills. A non-NotFound storage error surfaces
// (fail loud) — it must not masquerade as a 404.
func (s *Server) resolveAtomSeed(ctx context.Context, atomID string) (companion.AtomSeed, bool, error) {
	if s.AtomIndex != nil {
		a, err := s.AtomIndex.Get(ctx, atomID)
		switch {
		case err == nil && a != nil:
			return companion.AtomSeed{
				AtomID: a.AtomID,
				Topic:  a.PrimaryTopic(),
				Title:  a.Title,
			}, true, nil
		case err != nil && !errors.Is(err, atom_index.ErrNotFound):
			return companion.AtomSeed{}, false, fmt.Errorf("resolve atom %s: %w", atomID, err)
		}
	}
	if s.Atoms == nil {
		return companion.AtomSeed{}, false, nil
	}
	seed, ok := s.Atoms.Lookup(atomID)
	return seed, ok, nil
}

// syntheticSeeds is the legacy catalogue fallback (test/dev wiring absence).
func (s *Server) syntheticSeeds() []companion.AtomSeed {
	if s.Atoms == nil {
		return nil
	}
	return s.Atoms.Seeds()
}
