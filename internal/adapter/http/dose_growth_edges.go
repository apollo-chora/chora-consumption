// dose_growth_edges.go — W6 (Epic-1b): adapt the learner's Growth Edges onto
// the daily-dose composer's GrowthEdgeInput carrier. Fail-soft: nil repo or a
// read error yields nil, leaving the legacy topic_accuracy weakness slot in
// charge (the dose NEVER breaks on overlay enrichment).
package http

import (
	"context"
	"errors"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// doseGrowthEdgePageSize bounds the per-dose edge fetch; the composer drills
// at most DoseWeaknessCount edges, so a strength-desc top-10 is plenty.
const doseGrowthEdgePageSize = 10

func (s *Server) doseGrowthEdges(ctx context.Context, tenantID, gcid string) []companion.GrowthEdgeInput {
	if s.LearnerWeakness == nil {
		return nil
	}
	res, err := s.LearnerWeakness.List(ctx, lw.ListQuery{
		TenantID:    tenantID,
		LearnerGCID: gcid,
		Sort:        lw.SortStrengthDesc,
		PageSize:    doseGrowthEdgePageSize,
	})
	if err != nil {
		return nil
	}
	// ADR-224 #3 — drop weakness edges whose concept belongs to a map the learner
	// excluded from their dose. Fail-soft: excluded is nil on any error/absence.
	excluded := s.excludedDoseConceptKeys(ctx, tenantID, gcid)
	out := make([]companion.GrowthEdgeInput, 0, len(res.Items))
	for _, e := range res.Items {
		if excluded != nil && excluded[lw.NormalizeConceptKey(e.ConceptKey)] {
			continue
		}
		out = append(out, companion.GrowthEdgeInput{
			EdgeID:             e.ID,
			ConceptKey:         e.ConceptKey,
			ConceptLabel:       e.ConceptLabel,
			Category:           e.Category, // ADR-224: subject bucket for v2 allocation
			Tags:               e.Tags,
			Strength:           e.Strength,
			CachedDrillAtomIDs: e.CachedDrillAtomIDs,
		})
	}
	return out
}

// excludedDoseConceptKeys resolves the learner's per-KG dose EXCLUSIONS
// (ADR-224 #3) to the set of NORMALISED concept_keys to drop from the weakness
// pool: each excluded map_id → that Goal's ConceptSet. Exclusion wins — a concept
// in an excluded map is dropped even if it also sits in an included map. Fail-soft
// throughout: a nil pref/goal port, a read error, or no exclusions returns nil
// (no filtering — the dose is never broken by preference resolution). The ctx
// carries the RLS session (repoCtx) so both reads are tenant+gcid-scoped.
func (s *Server) excludedDoseConceptKeys(ctx context.Context, tenantID, gcid string) map[string]bool {
	if s.DoseKGPrefs == nil || s.Goals == nil {
		return nil
	}
	excludedMaps, err := s.DoseKGPrefs.ExcludedMapIDs(ctx, tenantID, gcid)
	if err != nil || len(excludedMaps) == 0 {
		return nil
	}
	keys := make(map[string]bool)
	for mapID := range excludedMaps {
		g, gerr := s.Goals.GetByID(ctx, tenantID, gcid, mapID)
		if gerr != nil || g == nil {
			continue // fail-soft per map — a stale/removed map just doesn't filter
		}
		for _, ck := range g.ConceptSet {
			if k := lw.NormalizeConceptKey(ck); k != "" {
				keys[k] = true
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// doseFocusedGrowthEdge adapts the ONE Growth Edge a focused-practice dose drills
// (Phase 2A: GET /companion/daily-dose?growth_edge_id={id}) onto the composer's
// single-element GrowthEdgeInput carrier, AND resolves its cached drill atoms so
// they can be served:
//
//   - GRADABILITY (CHO-1895): each cached drill atom is resolved via the
//     atom_index projection and kept ONLY when gradable+answerable (IsMCQ AND
//     topic-tagged). A non-gradable cached atom (e.g. cached before the drillcache
//     gradability invariant) is dropped so the focused weakness slot is never a
//     dead-end drill that the player can't render / the projector can't grade.
//   - SERVABILITY: the resolved gradable atoms are ALSO returned as AtomSeeds so
//     the caller can augment the dose universe with them — a focused-practice
//     drill is the practice material and need NOT be on an enrolled LearningPath.
//     Without this, semantic-nearest cached atoms (which are routinely unenrolled)
//     could never be served, so completing one could never recover its edge.
//
// Fail-soft: a nil repo, a read error, or a missing edge yields (nil, nil); a
// per-atom resolve error drops that candidate (the dose NEVER breaks on overlay
// enrichment — consistent with this file's contract). The handler still sets
// FocusEdgeID, so the composer gracefully degrades to a normal fresh dose. Tenant
// scoping rides the ctx (RLS); the tenantID parameter mirrors doseGrowthEdges.
func (s *Server) doseFocusedGrowthEdge(ctx context.Context, tenantID, gcid, edgeID string) ([]companion.GrowthEdgeInput, []companion.AtomSeed) {
	_ = tenantID
	if s.LearnerWeakness == nil {
		return nil, nil
	}
	e, err := s.LearnerWeakness.Get(ctx, gcid, edgeID)
	if err != nil || e == nil {
		return nil, nil
	}

	gradableIDs := make([]string, 0, len(e.CachedDrillAtomIDs))
	seeds := make([]companion.AtomSeed, 0, len(e.CachedDrillAtomIDs))
	if s.AtomIndex != nil {
		for _, atomID := range e.CachedDrillAtomIDs {
			a, gerr := s.AtomIndex.Get(ctx, atomID)
			if errors.Is(gerr, atom_index.ErrNotFound) || (gerr == nil && a == nil) {
				continue // unprojected — can't render/grade; drop
			}
			if gerr != nil {
				continue // overlay enrichment fail-soft: drop this candidate, never 500 the dose
			}
			if !a.IsMCQ() || a.PrimaryTopic() == "" {
				continue // non-gradable — never a focused drill (dead end)
			}
			gradableIDs = append(gradableIDs, a.AtomID)
			seeds = append(seeds, companion.AtomSeed{
				AtomID: a.AtomID,
				Topic:  a.PrimaryTopic(),
				Title:  a.Title,
			})
		}
	}

	return []companion.GrowthEdgeInput{{
		EdgeID:             e.ID,
		ConceptKey:         e.ConceptKey,
		ConceptLabel:       e.ConceptLabel,
		Tags:               e.Tags,
		Strength:           e.Strength,
		CachedDrillAtomIDs: gradableIDs,
	}}, seeds
}

// mergeAtomSeeds appends extra seeds not already present (by AtomID) to base,
// preserving base order then extra order. Used to augment the focused dose's
// enrolled universe with the focused edge's (gradable) cached drill atoms so
// they are servable even when unenrolled.
func mergeAtomSeeds(base, extra []companion.AtomSeed) []companion.AtomSeed {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(extra))
	for _, s := range base {
		seen[s.AtomID] = true
	}
	out := base
	for _, s := range extra {
		if s.AtomID == "" || seen[s.AtomID] {
			continue
		}
		seen[s.AtomID] = true
		out = append(out, s)
	}
	return out
}
