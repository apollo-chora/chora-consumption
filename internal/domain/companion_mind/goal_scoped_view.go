// goal_scoped_view.go — the tier-1 DETERMINISTIC, goal-scoped block behind the
// Companion tab's per-goal reflection (CHO-2116 / CHO-2118).
//
// It answers "what does my Companion know about me ON THIS GOAL" using only
// facts already in chora_consumption — no LLM, no network, no I/O. It exists for
// two callers, and is composed ONCE so they can never drift apart:
//
//   - CHO-2116 serves it directly as the tier-1 block.
//   - CHO-2118 serves it as the fail-soft when the tier-2 LLM reflection is
//     absent, pending, or refused — and feeds the SAME assembled view to the
//     synthesiser as its input. Because both sides read one structure, the cached
//     ContentHash genuinely describes what the model saw.
//
// Scoping is the substance here. A weakness is only "shaky on this goal" when it
// lies inside the goal's concept set AND is still active; progress is DERIVED
// from the mastered set intersected with the goal, never read from
// goal.MasteredConceptCount (that field is the graduation subscriber's
// high-water mark, and its own doc comment warns it is not read-time progress —
// trusting it would silently misreport how far along the learner is).
//
// Honesty: an empty view reports itself empty (HasMemory/HasSignal false, non-nil
// empty collections) so the caller renders "no memory yet" rather than inventing
// a reflection — ADR-207 "Unknown is a state", ADR-215 D5 learner-depth
// projection (no vectors, no distances, no model ids, no prompt internals).
//
// Hexagonal: pure domain. No SQL, no SDK, no prompt strings.
package companionmind

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// WeaknessSignal is one learner weakness projected for goal scoping: the concept
// it attaches to, how shaky it is, and whether it is still live. Lifted from the
// learner_weakness aggregate so this read model does not depend on that entity.
type WeaknessSignal struct {
	ConceptKey   string
	ConceptLabel string  // Companion-facing natural phrase (already reframed, never raw)
	Strength     float64 // 0..1 shakiness — 1 = very weak
	Active       bool    // a grown weakness is no longer shaky
}

// ShakyConcept is a weakness that genuinely bears on THIS goal.
type ShakyConcept struct {
	ConceptKey   string
	ConceptLabel string
	Strength     float64
}

// GoalScopedInput is everything the tier-1 block is composed from. The caller
// (an adapter) resolves each part from its repository; this package only
// assembles.
type GoalScopedInput struct {
	GoalID        string
	GoalTitle     string
	CompanionID   string
	CompanionName string

	// GoalConceptKeys is the goal's concept set — the subtree under its root.
	GoalConceptKeys []string
	// MasteredConceptKeys is every concept the learner has mastered (platform-
	// wide); it is intersected with the goal here, so mastery earned elsewhere
	// cannot inflate this goal's progress.
	MasteredConceptKeys []string

	Weaknesses []WeaknessSignal
	Memories   []EpisodicMemory
}

// GoalScopedView is the assembled tier-1 block.
type GoalScopedView struct {
	GoalID        string
	GoalTitle     string
	CompanionID   string
	CompanionName string

	ConceptsTotal    int
	ConceptsMastered int

	// ShakyConcepts is goal-subtree ∩ active weaknesses, shakiest first.
	ShakyConcepts []ShakyConcept

	HasMemory bool
	Memories  []EpisodicMemory
}

// AssembleGoalScopedView composes the tier-1 block. Pure: same input, same
// output, no I/O.
//
// ⚠ IT NORMALISES THE CONCEPT KEY-SPACE ITSELF, on every side, and that is not
// defensive tidiness — it is the fix for a silent-failure class.
//
// `Goal.ConceptSet` is trimmed and deduped but NEVER slugified (its own doc says
// "normalised", which means something much weaker than it sounds). A learner
// weakness's ConceptKey IS a slug. Intersect the two raw and you get the EMPTY
// SET: no shaky concepts, no progress, HasSignal() false — so the learner reads
// as "no memory yet" forever, no synthesis is ever requested, and it looks
// exactly like an empty learner rather than a bug.
//
// Four adapters remembered to normalise before calling in. Nothing stopped a
// fifth from forgetting, and nothing would have told anyone. The slug rule is
// idempotent, so a caller that already normalised loses nothing — and one that
// did not is no longer silently wrong. Correct by construction beats correct by
// remembering.
func AssembleGoalScopedView(in GoalScopedInput) GoalScopedView {
	inGoal := make(map[string]bool, len(in.GoalConceptKeys))
	for _, k := range in.GoalConceptKeys {
		if k = lw.NormalizeConceptKey(k); k != "" {
			inGoal[k] = true
		}
	}

	// Progress: mastered ∩ goal. Mastery earned on another goal is real, but it is
	// not progress on THIS one.
	mastered := 0
	seenMastered := make(map[string]bool, len(in.MasteredConceptKeys))
	for _, k := range in.MasteredConceptKeys {
		k = lw.NormalizeConceptKey(k)
		if k == "" || seenMastered[k] || !inGoal[k] {
			continue
		}
		seenMastered[k] = true
		mastered++
	}

	// Shaky = inside the goal AND still active.
	shaky := make([]ShakyConcept, 0, len(in.Weaknesses))
	for _, w := range in.Weaknesses {
		key := lw.NormalizeConceptKey(w.ConceptKey)
		if key == "" || !w.Active || !inGoal[key] {
			continue
		}
		shaky = append(shaky, ShakyConcept{
			ConceptKey:   key,
			ConceptLabel: w.ConceptLabel,
			Strength:     w.Strength,
		})
	}
	// Shakiest first, then by key so the order is total and stable (an unstable
	// order would churn the ContentHash and regenerate the reflection forever).
	sort.SliceStable(shaky, func(i, j int) bool {
		if shaky[i].Strength != shaky[j].Strength {
			return shaky[i].Strength > shaky[j].Strength
		}
		return shaky[i].ConceptKey < shaky[j].ConceptKey
	})

	memories := in.Memories
	if memories == nil {
		memories = []EpisodicMemory{}
	}

	return GoalScopedView{
		GoalID:           in.GoalID,
		GoalTitle:        in.GoalTitle,
		CompanionID:      in.CompanionID,
		CompanionName:    in.CompanionName,
		ConceptsTotal:    len(inGoal),
		ConceptsMastered: mastered,
		ShakyConcepts:    shaky,
		HasMemory:        len(memories) > 0,
		Memories:         memories,
	}
}

// HasSignal reports whether there is anything worth reflecting on at all. With
// no memory and no shaky concept the Companion has nothing honest to say, so the
// caller shows the "no memory yet" empty state and does NOT spend an LLM call
// fabricating one.
func (v GoalScopedView) HasSignal() bool {
	return v.HasMemory || len(v.ShakyConcepts) > 0
}

// ContentHash fingerprints the INPUTS this view was assembled from. The cache row
// stores it so a regen can tell whether the facts actually moved.
//
// It must be stable across identical inputs (otherwise every read looks like
// drift and regenerates forever) and must move whenever any input moves
// (otherwise a stale reflection is served as current). Assembly has already put
// the shaky concepts in a total order, and memories are hashed by id, so
// incidental row ordering from the database cannot flip the result.
func (v GoalScopedView) ContentHash() string {
	h := sha256.New()

	fmt.Fprintf(h, "goal:%s\x1f", v.GoalID)
	fmt.Fprintf(h, "companion:%s\x1f", v.CompanionID)
	fmt.Fprintf(h, "progress:%d/%d\x1f", v.ConceptsMastered, v.ConceptsTotal)

	for _, s := range v.ShakyConcepts { // already shakiest-first, ties broken by key
		fmt.Fprintf(h, "shaky:%s=%.4f\x1f", s.ConceptKey, s.Strength)
	}

	ids := make([]string, 0, len(v.Memories))
	for _, m := range v.Memories {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids) // recall order is a query artefact, not an input change
	for _, id := range ids {
		fmt.Fprintf(h, "mem:%s\x1f", id)
	}

	return hex.EncodeToString(h.Sum(nil))
}
