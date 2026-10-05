// dose_goal_scope.go - WS-3 adapter lane: resolve the browsed goal
// (GET /companion/daily-dose?goal_id={uuid}) into the composer's GoalScopeInput,
// plus the gradable goal atoms that augment the dose universe.
//
// Two signals are handed to the composer, strongest first:
//
//  1. ATOM BINDINGS - the atoms bound to the concepts in the goal's map
//     (concept_nodes.atom_refs over the ADR-214 root subtree). This is the
//     goal's actual material.
//  2. CONCEPT SLUGS - the goal's ConceptSet plus each in-scope concept's
//     key/title, matched against a seed's topic slug. A broad fallback that
//     still finds relevant material when a concept has no atoms bound yet.
//
// SERVABILITY: a goal's atoms need not sit on an enrolled LearningPath (the same
// reasoning as the CHO-1895 focused-practice idiom - the goal's material IS the
// practice material), so resolved gradable atoms are returned as AtomSeeds for
// the caller to merge into the universe.
//
// ERROR POSTURE (ADR-242 D3, owner re-ruled 2026-08-17 over this lane's
// original fail-soft contract): goal resolution is a POSITIVE scope claim
// attached to a visible promise, so a REPO ERROR anywhere on the resolution
// path (goal read, concept-graph read, atom_index read) propagates and the
// handler 500s, exactly like doseAtomUniverse. Fail-soft here silently serves
// an unscoped dose under a goal-scoped heading, and with the D2 disclosure a
// failed read cannot render an honest scope line at all. Three cases stay
// QUIET, matching the choke point: an unknown or foreign goal id yields
// (nil, nil, nil) and the ordinary learner-wide dose with no scope block (a
// stale link is a caller bug, not a scope claim); atom_index.ErrNotFound on a
// referenced atom is an honest omission; and a RESOLVED goal with no material
// returns its scope carrier with empty signals so D2 reports "0 from this
// goal", never a synthetic tranche. A nil port also stays quiet: the feature
// is unwired, which is D5's degenerate, and it is logged at boot.
//
// PORT REUSE: KgExploreMap / KgExploreEdges are the Server's live ConceptNode +
// Edge list ports. Despite the kg_explore-era names they are the general
// concept-graph read seam - companion_ceremony_edge_scout_handler.go already
// walks the goal subtree through them, and reusing them keeps the compose side
// and every other goal-subtree consumer on ONE walk that cannot disagree.
package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

// doseGoalIDFromQuery reads the OPTIONAL goal scope off the dose request.
//
// `goal_id` is the house convention (it matches `growth_edge_id` on this very
// route and `goal_id` on the memory-read surface). `goalId` is accepted as well
// because the caller is owned by another lane: a spelling mismatch between the
// two would not fail loudly, it would ship a feature that silently never
// activates, which is far worse than tolerating both here.
func doseGoalIDFromQuery(r *http.Request) string {
	if v := strings.TrimSpace(r.URL.Query().Get("goal_id")); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get("goalId"))
}

// doseGoalScope resolves goalID into the composer's goal-scope carrier plus the
// gradable goal atoms to augment the dose universe. A repo error propagates
// (the handler 500s per D3); an unknown/foreign goal or an unwired port yields
// (nil, nil, nil); a resolved goal ALWAYS yields its carrier, even with no
// material, so D2 can disclose the zeros.
func (s *Server) doseGoalScope(ctx context.Context, tenantID, gcid, goalID string) (*companion.GoalScopeInput, []companion.AtomSeed, error) {
	if goalID == "" || s.Goals == nil {
		return nil, nil, nil
	}
	g, err := s.Goals.GetByID(ctx, tenantID, gcid, goalID)
	if err != nil {
		return nil, nil, fmt.Errorf("dose goal scope: load goal %s: %w", goalID, err)
	}
	if g == nil {
		return nil, nil, nil // unknown goal (stale link) ⇒ ordinary learner-wide dose
	}
	if g.LearnerGCID != gcid || g.TenantID != tenantID {
		return nil, nil, nil // never scope to another learner's goal
	}

	scope := &companion.GoalScopeInput{GoalID: g.GoalID}
	var extraSeeds []companion.AtomSeed
	slugs := make(map[string]bool, len(g.ConceptSet))
	addSlug := func(v string) {
		if v = strings.TrimSpace(v); v != "" {
			slugs[v] = true
		}
	}
	for _, ck := range g.ConceptSet {
		addSlug(ck)
	}

	// The goal's concepts: the ADR-214 subtree under its root concept.
	concepts, err := s.goalScopeConcepts(ctx, tenantID, gcid, g.RootConceptID)
	if err != nil {
		return nil, nil, fmt.Errorf("dose goal scope: goal %s concepts: %w", goalID, err)
	}
	for _, n := range concepts {
		addSlug(n.ConceptKey)
		addSlug(n.Title)
		for _, atomID := range n.AtomRefs {
			seed, ok, serr := s.resolveGoalAtomSeed(ctx, atomID)
			if serr != nil {
				return nil, nil, fmt.Errorf("dose goal scope: resolve atom %s: %w", atomID, serr)
			}
			if ok {
				scope.AtomIDs = append(scope.AtomIDs, seed.AtomID)
				extraSeeds = append(extraSeeds, seed)
			}
		}
	}

	for slug := range slugs {
		scope.ConceptSlugs = append(scope.ConceptSlugs, slug)
	}
	// Sorted so the carrier is deterministic for a given goal (map iteration is
	// not); affinity matching is order-independent, but a stable carrier keeps
	// the dose reproducible for replay + tests.
	sort.Strings(scope.ConceptSlugs)

	// A resolved goal with no signals still returns its carrier: the composed
	// dose degenerates (empty partition) while D2 honestly reports
	// "0 from this goal" instead of pretending no goal was asked for.
	return scope, extraSeeds, nil
}

// goalScopeConcepts returns the live ConceptNodes in the goal's map: the ADR-214
// downward-hierarchy subtree under the goal's root concept, walked through the
// SAME conceptgraph.SubtreeConceptIDs the ceremony + suggestion surfaces use, so
// "this goal's concepts" can never mean two different things. An unrooted goal
// or an unwired port yields (nil, nil) quietly (the caller then biases on
// ConceptSet slugs alone); a READ FAILURE propagates per ADR-242 D3.
func (s *Server) goalScopeConcepts(ctx context.Context, tenantID, gcid string, rootConceptID *string) ([]*conceptgraph.ConceptNode, error) {
	if rootConceptID == nil || strings.TrimSpace(*rootConceptID) == "" {
		return nil, nil
	}
	if s.KgExploreMap == nil || s.KgExploreEdges == nil {
		return nil, nil
	}
	nodes, nerr := s.KgExploreMap.ListByLearner(ctx, tenantID, gcid)
	if nerr != nil {
		return nil, fmt.Errorf("list concepts: %w", nerr)
	}
	edges, eerr := s.KgExploreEdges.ListByLearner(ctx, tenantID, gcid)
	if eerr != nil {
		return nil, fmt.Errorf("list edges: %w", eerr)
	}
	inGoal := conceptgraph.SubtreeConceptIDs(derefConcepts(nodes), derefEdges(edges), strings.TrimSpace(*rootConceptID))
	out := make([]*conceptgraph.ConceptNode, 0, len(inGoal))
	for _, n := range nodes {
		if n != nil && inGoal[n.ConceptID] {
			out = append(out, n)
		}
	}
	return out, nil
}

// resolveGoalAtomSeed resolves one bound atom to a servable seed: published,
// playable and answerable (the daily_dose_seeds.go universe rule), so a goal
// never leads the dose with a card the learner cannot render or complete.
// atom_index.ErrNotFound (an unprojected atom) and a non-servable atom are
// honest omissions; any OTHER read failure propagates per ADR-242 D3, exactly
// like doseAtomUniverse's per-atom posture.
func (s *Server) resolveGoalAtomSeed(ctx context.Context, atomID string) (companion.AtomSeed, bool, error) {
	if s.AtomIndex == nil || strings.TrimSpace(atomID) == "" {
		return companion.AtomSeed{}, false, nil
	}
	a, err := s.AtomIndex.Get(ctx, atomID)
	if errors.Is(err, atom_index.ErrNotFound) {
		return companion.AtomSeed{}, false, nil
	}
	if err != nil {
		return companion.AtomSeed{}, false, err
	}
	if a == nil || !a.Servable() {
		return companion.AtomSeed{}, false, nil
	}
	return companion.AtomSeed{AtomID: a.AtomID, Topic: a.PrimaryTopic(), Title: a.Title}, true, nil
}
