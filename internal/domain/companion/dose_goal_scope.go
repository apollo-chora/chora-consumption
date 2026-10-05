// dose_goal_scope.go - goal-scoped Daily Dose (WS-3).
//
// The ask: "in a goal, if the user selects daily dose, then the daily dose
// relating to the goal should appear first."
//
// The mechanism is a BIAS, never a filter. A GoalScopeInput partitions the dose
// universe into the goal's material and everything else; each biased slot then
// runs its EXISTING selector on the goal partition first and on the remainder
// second. Because every selector already honours its own slot cap and the global
// DoseTargetSize budget, running it twice cannot overfill, and a goal with thin
// material simply leaves the remainder phase to finish the dose. The 5-card
// budget therefore holds by construction, not by a special case.
//
// WHAT IS BIASED, AND WHY THE REST IS NOT:
//
//   - Ebbinghaus review - BIASED. Slot ELIGIBILITY (decay > DoseDecayThreshold)
//     is untouched, so a not-due atom is never promoted into the review slot and
//     a due atom is never dropped from the dose. Only the ORDER within the
//     already-due pool changes, which is exactly "reorder within what is
//     pedagogically due". Because decay is a float, exact ties essentially never
//     occur, so an affinity TIE-BREAK here would be a silent no-op; the bias has
//     to be the primary key within the due pool for the slot to respond at all.
//   - Curiosity, fresh top-up, final top-up - BIASED. Their order today is
//     atom_id ascending (or a seeded topic spread) and carries no pedagogical
//     signal, so goal relevance is a strict improvement.
//   - Weakness - NOT BIASED. ADR-224 spreads this slot ACROSS subjects
//     specifically to stop one subject monopolising it. A goal is roughly a
//     subject, so biasing the weakness slot toward one goal would silently
//     repeal that invariant. Growth-Edge shakiness stays in charge.
//   - Campaign - NOT BIASED. The ADR-227 D12 march is elected by a seeded daily
//     RNG that the ANSWER side re-runs to decide where to fold verified ladder
//     progress. Re-electing it from a UI-supplied goal would desynchronise the
//     two sides and silently discard that progress (see the report note).
//
// After composition a final stable pass orders the composed cards so the goal's
// material leads. That is presentation only: the dose CONTENTS are already
// decided by the slot rules above, so the forgetting curve still governs what is
// served today, not merely what is shown first.
//
// PURITY: stdlib-only, no clock, no I/O. The adapter resolves a goal into the
// carrier below (mirrors how GrowthEdgeInput and CampaignInput are assembled).
package companion

import "sort"

// Goal-affinity ranks. Higher leads.
const (
	// goalAffinityNone marks an atom that is not the goal's material.
	goalAffinityNone = 0
	// goalAffinityTopic marks a topic-slug match against one of the goal's
	// concept keys. A heuristic: the atom is about the right subject.
	goalAffinityTopic = 1
	// goalAffinityBound marks an atom explicitly bound to one of the goal's
	// concepts (concept_nodes.atom_refs). This is the goal's actual material and
	// so outranks a topic match.
	goalAffinityBound = 2
)

// GoalScopeInput is the composer's view of the goal the learner is browsing.
// The adapter resolves it from the Goal aggregate plus the goal's concept
// subtree; this carrier keeps the composer decoupled from those repositories
// (mirrors GrowthEdgeInput / CampaignInput).
//
// A nil *GoalScopeInput means "no goal was asked for" and every method below is
// nil-receiver safe, so the unscoped dose runs the identical code path with an
// empty goal partition.
type GoalScopeInput struct {
	// GoalID is the browsed goal, carried for traceability.
	GoalID string
	// AtomIDs are the atoms bound to the goal's concepts (the precise signal).
	AtomIDs []string
	// ConceptSlugs are the goal's concept keys/labels, matched against a seed's
	// topic slug (the broad signal). Normalised on use, so callers may pass raw
	// keys or labels.
	ConceptSlugs []string
}

// affinity ranks one atom's relevance to the goal. A nil scope ranks everything
// goalAffinityNone, which is what makes the unscoped path degenerate exactly.
func (g *GoalScopeInput) affinity(atomID, topic string) int {
	if g == nil {
		return goalAffinityNone
	}
	for _, id := range g.AtomIDs {
		if id != "" && id == atomID {
			return goalAffinityBound
		}
	}
	if slug := normalizeTopicSlug(topic); slug != "" {
		for _, cs := range g.ConceptSlugs {
			if normalizeTopicSlug(cs) == slug {
				return goalAffinityTopic
			}
		}
	}
	return goalAffinityNone
}

// partitionSeeds splits the universe into the goal's material and the remainder,
// preserving the caller's order within each part so every downstream selector
// keeps its own deterministic sort. A nil scope yields (nil, seeds) - the whole
// universe stays in the remainder and the goal phase is a no-op.
func (g *GoalScopeInput) partitionSeeds(seeds []AtomSeed) (goalMaterial, rest []AtomSeed) {
	if g == nil {
		return nil, seeds
	}
	goalMaterial = make([]AtomSeed, 0, len(seeds))
	rest = make([]AtomSeed, 0, len(seeds))
	for _, s := range seeds {
		if g.affinity(s.AtomID, s.Topic) > goalAffinityNone {
			goalMaterial = append(goalMaterial, s)
			continue
		}
		rest = append(rest, s)
	}
	return goalMaterial, rest
}

// orderEntries stable-sorts the composed dose so the goal's material leads,
// bound atoms ahead of topic matches. Stability preserves the cascade order
// (campaign, review, weakness, curiosity, fresh) within each affinity band, and
// a nil scope leaves the dose untouched.
//
// This reorders CARDS, not composition: which atoms are in today's dose was
// already settled by the slot rules, so nothing due is displaced out of the dose
// by being displayed second.
func (g *GoalScopeInput) orderEntries(entries []DailyDoseEntry) []DailyDoseEntry {
	if g == nil || len(entries) < 2 {
		return entries
	}
	ranks := make(map[string]int, len(entries))
	for _, e := range entries {
		ranks[e.AtomID] = g.affinity(e.AtomID, e.Topic)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return ranks[entries[i].AtomID] > ranks[entries[j].AtomID]
	})
	return entries
}

// withSeeds returns a shallow copy of the input whose universe is replaced. Used
// to run an existing slot selector over one partition without teaching that
// selector anything about goals.
func (in DailyDoseInput) withSeeds(seeds []AtomSeed) DailyDoseInput {
	in.Seeds = seeds
	return in
}

// DoseScope is the ADR-242 D2 disclosure: how the SERVED cards split across the
// goal's material, owner-ruled 2026-08-17 as a THREE-WAY split mirroring the two
// affinity ranks above plus the remainder. Counts are over served cards and sum
// to the card count. Nil when no goal was requested (D5: the unscoped response
// stays byte-identical). An off-goal focused drill's cards land in Broader,
// which is the D6 collision disclosure: the focused request wins the dose and
// the split says its cards were not the goal's material.
type DoseScope struct {
	GoalID   string `json:"goal_id"`
	FromGoal int    `json:"from_goal"` // bound: the atom is in a goal-subtree concept's atom_refs
	OnTopics int    `json:"on_topics"` // topic-slug match against the goal's concepts
	Broader  int    `json:"broader"`   // everything else (enrolled top-up, off-goal drills)
}

// scopeOf classifies the served entries into the D2 split. Computed by the
// composer, not the adapter, so the response can never disagree with the
// affinity the partition actually used. Nil receiver (no goal requested) yields
// nil; a resolved goal with no material yields an all-zeros-plus-Broader split,
// which D2 renders as "0 from this goal", the ADR's required honest statement.
func (g *GoalScopeInput) scopeOf(entries []DailyDoseEntry) *DoseScope {
	if g == nil {
		return nil
	}
	s := &DoseScope{GoalID: g.GoalID}
	for _, e := range entries {
		switch g.affinity(e.AtomID, e.Topic) {
		case goalAffinityBound:
			s.FromGoal++
		case goalAffinityTopic:
			s.OnTopics++
		default:
			s.Broader++
		}
	}
	return s
}
