// Package companion — Daily Dose composition logic.
//
// Per Comic Ch6 P14, the Phyllis MVP spec (docs/m13/phyllis-mvp-2026-05-08.md
// §5.4), and ux_engagement.md §2, the Daily Dose is a fixed 5-atom curated
// bundle picked by a deterministic SM-2 + Ebbinghaus rule with the
// canonical 40/30/30 split:
//
//	2 atoms — Ebbinghaus review (decay > 0.4)             (40%)
//	2 atoms — Weakness drill (topic accuracy < 0.7)       (30% upper)
//	1 atom  — Curiosity (topic adjacent to active path)   (30% lower)
//	(top-up) — Fresh atoms when other slots short        ⇒ 5-card budget
//
// Each atom carries a `dose_reason` tag so the UI can render a label:
//
//	ebbinghaus_review | weakness | curiosity | fresh
//
// Comic anchor (Ch6 P14 P4):
//
//	"Your Daily Dose is ready! 5 atoms... your memory's fading on a few
//	 topics — a quick 5-minute quiz will fix that!"
//
// Composition is deterministic in-memory and does NOT call an LLM.
// Production personalisation (LLM-personalised greeting + RAG memory
// lookup) is a separate adapter wired through the mana-metered Greeting
// port (see greeting.go).
//
// HISTORY:
//
//   - S4 shipped 2/1/2 (Ebbinghaus / fresh / curiosity) — net 40/20/40
//     with NO weakness slot. See dose_composition_breadcrumb_test.go for
//     the hand-off marker.
//   - S5.1 (this file) reconciled to 40/30/30 (Ebbinghaus / weakness /
//     curiosity) per the spec; fresh slot retained as a top-up.
package companion

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// DoseReason tags WHY an atom was picked into the Daily Dose. Surfaced to
// the UI for label rendering.
type DoseReason string

const (
	// DoseReasonEbbinghausReview = atom is due for review per SM-2 rule.
	DoseReasonEbbinghausReview DoseReason = "ebbinghaus_review"
	// DoseReasonFresh = atom learner has never seen.
	DoseReasonFresh DoseReason = "fresh"
	// DoseReasonCuriosity = related-topic exploration pick (S5.1 = active
	// LearningPath topic adjacency; S5.2+ = MapCluster KG traversal).
	DoseReasonCuriosity DoseReason = "curiosity"
	// DoseReasonWeakness = weakness drill pick — topic where MCQ accuracy
	// is below WeaknessThresholdMin (default 0.7). NEW in S5.1 per
	// Phyllis-MVP §5.4.
	DoseReasonWeakness DoseReason = "weakness"
	// DoseReasonCampaign = campaign march pick — the focused goal's focus
	// node served at the grader-decided rung (ADR-227 D12, CHO-2081).
	DoseReasonCampaign DoseReason = "campaign"
)

// AtomSeed is the in-memory seed atom record (MVP only — real Atom records
// live in chora_creation behind a Pub/Sub-projected cache).
//
// Topic is a coarse classification used by Daily Dose composition to pick
// "related-topic" curiosity + weakness atoms.
type AtomSeed struct {
	AtomID string
	Topic  string
	Title  string
}

// DailyDoseEntry is one card in the dose with its picked reason.
type DailyDoseEntry struct {
	AtomID     string     `json:"atom_id"`
	Topic      string     `json:"topic"`
	Title      string     `json:"title"`
	DoseReason DoseReason `json:"dose_reason"`
	// Campaign march metadata (ADR-227 D12) — stamped ONLY on campaign
	// picks so the answer path can route grading into the campaign Grader
	// (goal + concept + rung). omitempty keeps every non-campaign dose
	// byte-identical on the wire (degeneration guard).
	CampaignGoalID     string `json:"campaign_goal_id,omitempty"`
	CampaignConceptID  string `json:"campaign_concept_id,omitempty"`
	CampaignConceptKey string `json:"campaign_concept_key,omitempty"`
	CampaignRung       int    `json:"campaign_rung,omitempty"`
	CampaignRefresher  bool   `json:"campaign_refresher,omitempty"`
}

// DailyDose is the response payload for GET /companion/daily-dose. The
// Message string is the comic-anchored nudge text.
type DailyDose struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Message     string           `json:"message"`
	Entries     []DailyDoseEntry `json:"entries"`
	// Scope is the ADR-242 D2 served-card split. Nil when no goal was
	// requested (D5), so the unscoped payload is unchanged.
	Scope *DoseScope `json:"scope,omitempty"`
}

// DoseTargetSize is the canonical 5-atom budget per Comic Ch6 P14.
const DoseTargetSize = 5

// Slot caps per the canonical 40/30/30 split (S5.1).
//
// Sum of caps (2+2+2 = 6) intentionally exceeds DoseTargetSize so the
// composer can pick the best mix when not all eligibility pools are
// populated. Priority cascade Ebbinghaus → Weakness → Curiosity → Fresh
// trims the total back to DoseTargetSize.
//
// On a fully-active learner the natural net is 2 Ebb + 2 Weakness + 1
// Curiosity = 5 (= 40 / 40 / 20 by atom count, closest integer to the
// 40/30/30 spec target across a 5-card budget).
const (
	DoseEbbinghausCount = 2
	DoseWeaknessCount   = 2
	DoseCuriosityCount  = 2
	// DoseFreshCount is unused as a target — fresh entries are the
	// top-up pool that fills any leftover slots up to DoseTargetSize.
	DoseFreshCount = 0
)

// Campaign-day slot caps (ADR-227 D12, tunable). When a campaign march is
// present (ComposerV2 + DailyDoseInput.Campaign != nil) the default fill
// becomes campaign 2 · review 1 · weakness 1 · curiosity 1; the cascade +
// fresh/final top-ups still guarantee the 5-card budget. With no campaign
// the v2 caps above apply unchanged (byte-identical degeneration).
const (
	DoseCampaignCount          = 2
	CampaignDayEbbinghausCount = 1
	CampaignDayWeaknessCount   = 1
	CampaignDayCuriosityCount  = 1
)

// DoseDecayThreshold — atoms with decay > 0.4 are eligible for the
// Ebbinghaus review slot per the spec.
const DoseDecayThreshold = 0.4

// DefaultWeaknessThreshold matches ux_engagement.md §2 / VS05.WeaknessDrill
// default ("retention < 0.70 ± 0.10 warning band"). Atoms in topics with
// MCQ accuracy below this threshold are eligible for the weakness slot.
const DefaultWeaknessThreshold = 0.70

// CriticalWeaknessStrength (ADR-224) — an edge at or above this shakiness
// (Strength is shakiness in [0,1], 1 = weakest) is "critical". When urgency is
// CONCENTRATED in a single subject (exactly one subject has a critical edge) the
// v2 subject-spread cap is lifted so that one genuinely urgent subject can take
// BOTH weakness slots rather than being diluted by cross-subject balancing. When
// ≥2 subjects are critical the spread still wins (else a fresh multi-subject
// learner whose edges are all maximally shaky would monoculture). (The ADR's "very
// low strength" prose refers to the learner's mastery being low — HIGH shakiness.)
const CriticalWeaknessStrength = 0.9

// DoseMessageFor returns the nudge text for an n-entry dose. The copy
// reflects the ACTUAL composed count (no-debt directive 2026-06-10) — the
// legacy static "5 atoms" const (Comic Ch6 P14 P4) lied whenever the real
// enrolled-atom universe composed fewer. n<=0 is the honest empty-universe
// nudge (not enrolled / nothing projected yet).
func DoseMessageFor(n int) string {
	switch {
	case n <= 0:
		return "No atoms queued yet. Enrol in a course or check back once new atoms are published."
	case n == 1:
		return "Your Daily Dose is ready! 1 atom. Your memory's fading on a few topics."
	default:
		return fmt.Sprintf("Your Daily Dose is ready! %d atoms. Your memory's fading on a few topics.", n)
	}
}

// DailyDoseInput holds the inputs needed to compose a dose for one learner.
//   - Seeds                — the atom catalogue available (typically 20+ atoms across topics)
//   - States               — per-atom SM-2 state (key = atom_id; missing = never reviewed)
//   - SeenTopics           — set of topics the learner has interacted with
//   - ActivePathTopics     — set of topics covered by the learner's active LearningPath(s)
//     (drives curiosity-pick eligibility under the topic_tag adjacency
//     heuristic; full MapCluster traversal is S5.2 KG agent's scope)
//   - TopicAccuracy        — per-topic MCQ rolling-window accuracy in [0, 1]
//     (drives weakness-pick eligibility)
//   - WeaknessThresholdMin — accuracy threshold below which a topic is
//     "weak"; defaults to DefaultWeaknessThreshold (0.70) when zero.
//   - Now                  — injected clock for determinism in tests
type DailyDoseInput struct {
	Seeds                []AtomSeed
	States               map[string]SM2State
	SeenTopics           map[string]bool
	ActivePathTopics     map[string]bool
	TopicAccuracy        map[string]float64
	Now                  time.Time
	WeaknessThresholdMin float64
	// GrowthEdges — the learner's active Growth Edges (W6, Epic-1b). When
	// present they OWN the weakness slot: cached drill atoms (W4) are picked
	// first, then a topic/tag slug match within the seed universe; the raw
	// TopicAccuracy pool below is the legacy fallback for learners with no
	// edges yet. Callers pass them in any order — the composer sorts by
	// strength (shakiest first).
	GrowthEdges []GrowthEdgeInput
	// FocusEdgeID — Phase 2A focused practice session. When set, the dose is
	// scoped to drilling the ONE Growth Edge in GrowthEdges whose EdgeID
	// matches: its cached drill atoms (W4) present in the seed universe fill the
	// weakness slot (NO topic/tag slug fallback — the focused weakness slot
	// serves ONLY cached drill atoms so completing one recovers the edge via
	// RecoverByDrillAtomID), then fresh top-up fills the rest of the budget,
	// bypassing the 40/30/30 cascade. When it matches no edge the composer falls
	// through to a normal fresh dose (graceful fallback). Empty ⇒ the standard
	// Ebbinghaus → weakness → curiosity → fresh cascade.
	FocusEdgeID string
	// ComposerV2 gates the ADR-224 (CHO-2045) selection stage: subject/KG
	// allocation + seeded weighted sampling on the weakness + curiosity slots.
	// The zero value (false) runs the v1 path unchanged, so every existing dose
	// is byte-identical. Even with V2 on, a single-subject / single-topic learner
	// collapses to the v1 path (byte-identical degenerate case) — the balance only
	// *activates* as the learner's KG portfolio grows.
	ComposerV2 bool
	// RandSeed seeds the ADR-224 weighted sampler. The adapter sets it to
	// DoseSeed(date, gcid) so the dose is stable within a day (mana idempotency +
	// dose ID intact — both are (tenant,gcid,date)-derived, never content) yet
	// varies day-to-day and reaches the long tail. Unused when ComposerV2=false.
	RandSeed int64
	// Campaign — TODAY'S one campaign march (ADR-227 D12): the goal elected
	// by PickCampaignGoal + the serve decision from the campaign grader.
	// nil = no campaign day; the compose is byte-identical to the
	// pre-campaign v2 output (degeneration guard). v2-only (ignored when
	// ComposerV2 is false); focused practice (FocusEdgeID) wins over it.
	Campaign *CampaignInput
	// GoalScope: the goal the learner is browsing (WS-3, optional). When set,
	// the goal's material leads the biased slots and then the composed cards
	// (see dose_goal_scope.go for which slots are biased and why the weakness +
	// campaign slots deliberately are not). nil = no goal was asked for and the
	// dose composes exactly as it did before goal scoping existed.
	GoalScope *GoalScopeInput
}

// GrowthEdgeInput is the dose-composer's view of one Growth Edge. A plain
// carrier (mirrors how TopicAccuracy is passed as a map) so the companion
// aggregate stays decoupled from the learner_weakness repository types.
type GrowthEdgeInput struct {
	EdgeID       string
	ConceptKey   string
	ConceptLabel string
	// Category is the subject bucket (learner_weakness.category), e.g.
	// "Whole-Number Arithmetic", "Science". "" = subject-known/KG-unknown edges
	// (upload-born, not pinned to a map node) — the ADR-224 allocator buckets
	// these as a virtual "unmapped" KG so they are never dropped.
	Category           string
	Tags               []string
	Strength           float64
	CachedDrillAtomIDs []string
}

// ComposeDailyDose picks 5 atoms per the canonical 40/30/30 + fresh
// top-up rule. Picks are stable: the same input yields the same output
// (deterministic for tests + replay).
//
// Algorithm (priority cascade — each step honours its slot cap AND the
// global DoseTargetSize budget):
//
//  1. Ebbinghaus review: pick up to DoseEbbinghausCount atoms whose
//     EbbinghausDecay > DoseDecayThreshold. Sorted by decay descending
//     (most-forgotten first), atom_id ascending as tiebreaker.
//
//  2. Weakness drill: pick up to DoseWeaknessCount atoms in topics where
//     MCQ accuracy < WeaknessThresholdMin (default 0.70). Sorted by
//     accuracy ascending (weakest topic first), atom_id ascending as
//     tiebreaker. Atoms already picked in step 1 are skipped.
//
//  3. Curiosity: pick up to DoseCuriosityCount atoms from topics in
//     ActivePathTopics (S5.1 heuristic — replaces SeenTopics-only
//     adjacency from S4). Sorted by atom_id ascending. Atoms already
//     picked are skipped.
//
//  4. Fresh top-up: if total < DoseTargetSize, fill remaining slots
//     with atoms never reviewed (DoseReasonFresh). Atoms already picked
//     are skipped. Sorted by atom_id ascending.
//
//  5. Final top-up: if STILL short (e.g., empty curiosity pool + no
//     fresh atoms), fill from any remaining seeds tagged DoseReasonFresh
//     so the 5-card budget is always met when seeds are available.
//
// Spec invariant: total ≤ DoseTargetSize, NEVER more.
func ComposeDailyDose(in DailyDoseInput) DailyDose {
	picked := make(map[string]bool)
	out := DailyDose{
		GeneratedAt: in.Now,
		Entries:     make([]DailyDoseEntry, 0, DoseTargetSize),
	}

	// Phase 2A — focused practice session. When FocusEdgeID is set the weakness
	// slot drills that one Growth Edge from its cached drill atoms ONLY (no slug
	// fallback — see pickFocusedEdgeSlots); the fresh + final top-ups still
	// guarantee the 5-card budget. pickFocusedEdgeSlots is a no-op when
	// FocusEdgeID matches no edge OR no cached drill atom is servable, so an
	// unknown/stale id (or an edge with an empty/unservable cache) gracefully
	// degrades to a normal fresh dose. This bypasses the 40/30/30 cascade.
	// WS-3 goal scoping: split the universe into the browsed goal's material and
	// the remainder. Every biased slot below runs its EXISTING selector over
	// goalSeeds first and restSeeds second, so goal material leads each slot it
	// can fill while the remainder still finishes the 5-card budget (bias, never
	// filter). With no goal scope goalSeeds is empty and restSeeds is the whole
	// universe, so the goal phase is a no-op and the remainder phase IS the
	// pre-scope call, so the unscoped dose is unchanged by construction.
	goalSeeds, restSeeds := in.GoalScope.partitionSeeds(in.Seeds)
	goalIn, restIn := in.withSeeds(goalSeeds), in.withSeeds(restSeeds)

	if in.FocusEdgeID != "" {
		out.Entries = pickFocusedEdgeSlots(out.Entries, picked, in)
		out.Entries = pickFreshTopUp(out.Entries, picked, goalSeeds, in.States)
		out.Entries = pickFreshTopUp(out.Entries, picked, restSeeds, in.States)
		out.Entries = pickFinalTopUp(out.Entries, picked, goalSeeds, in.States)
		out.Entries = pickFinalTopUp(out.Entries, picked, restSeeds, in.States)
		out.Entries = in.GoalScope.orderEntries(out.Entries)
		out.Scope = in.GoalScope.scopeOf(out.Entries) // ADR-242 D2 disclosure
		out.Message = DoseMessageFor(len(out.Entries))
		return out
	}

	weaknessThreshold := in.WeaknessThresholdMin
	if weaknessThreshold <= 0 {
		weaknessThreshold = DefaultWeaknessThreshold
	}

	// 0) Campaign day (ADR-227 D12): the march leads the dose and the other
	// slot caps tighten to campaign 2 · review 1 · weakness 1 · curiosity 1
	// (cascade + top-ups unchanged). v2-only; a nil Campaign leaves the v2
	// caps untouched so the dose degenerates byte-identically.
	ebbCap, weakCap, curCap := DoseEbbinghausCount, DoseWeaknessCount, DoseCuriosityCount
	if in.ComposerV2 && in.Campaign != nil {
		ebbCap, weakCap, curCap =
			CampaignDayEbbinghausCount, CampaignDayWeaknessCount, CampaignDayCuriosityCount
		out.Entries = pickCampaignSlots(out.Entries, picked, in)
	}

	// 1) Ebbinghaus review picks. Goal material first, then the rest. The decay
	// > DoseDecayThreshold eligibility rule is applied unchanged inside EACH
	// phase, so goal scoping reorders the due pool and never enlarges it.
	out.Entries = pickEbbinghausSlots(out.Entries, picked, goalIn, ebbCap)
	out.Entries = pickEbbinghausSlots(out.Entries, picked, restIn, ebbCap)

	// 2) Weakness drill picks (NEW in S5.1). Deliberately NOT goal-partitioned:
	// ADR-224 spreads this slot across subjects to prevent monoculture and a goal
	// is roughly a subject (see dose_goal_scope.go).
	out.Entries = pickWeaknessSlots(out.Entries, picked, in, weaknessThreshold, weakCap)

	// 3) Curiosity picks (S5.1 = active LearningPath topic adjacency).
	out.Entries = pickCuriositySlots(out.Entries, picked, goalIn, curCap)
	out.Entries = pickCuriositySlots(out.Entries, picked, restIn, curCap)

	// 4) Fresh top-up — atoms never reviewed.
	out.Entries = pickFreshTopUp(out.Entries, picked, goalSeeds, in.States)
	out.Entries = pickFreshTopUp(out.Entries, picked, restSeeds, in.States)

	// 5) Final top-up — guarantee 5-card budget when seeds permit.
	out.Entries = pickFinalTopUp(out.Entries, picked, goalSeeds, in.States)
	out.Entries = pickFinalTopUp(out.Entries, picked, restSeeds, in.States)

	// 6) Goal-scoped presentation order: the goal's cards lead. No-op unscoped.
	out.Entries = in.GoalScope.orderEntries(out.Entries)
	out.Scope = in.GoalScope.scopeOf(out.Entries) // ADR-242 D2 disclosure

	// Message reflects the FINAL composed count (honest copy — the real
	// enrolled-atom universe may yield fewer than the 5-card budget).
	out.Message = DoseMessageFor(len(out.Entries))

	return out
}

// pickEbbinghausSlots fills up to `cap` review slots (DoseEbbinghausCount,
// or CampaignDayEbbinghausCount on a campaign day) from atoms whose decay
// exceeds DoseDecayThreshold. Stable sort by decay descending then atom_id
// ascending.
func pickEbbinghausSlots(
	entries []DailyDoseEntry,
	picked map[string]bool,
	in DailyDoseInput,
	cap int,
) []DailyDoseEntry {
	type decayPair struct {
		seed  AtomSeed
		decay float64
	}
	pool := make([]decayPair, 0, len(in.Seeds))
	for _, seed := range in.Seeds {
		state, ok := in.States[seed.AtomID]
		if !ok {
			continue // never reviewed → fresh pool, not review pool
		}
		decay := EbbinghausDecay(state, in.Now)
		if decay > DoseDecayThreshold {
			pool = append(pool, decayPair{seed: seed, decay: decay})
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].decay != pool[j].decay {
			return pool[i].decay > pool[j].decay
		}
		return pool[i].seed.AtomID < pool[j].seed.AtomID
	})
	for _, p := range pool {
		if len(entries) >= DoseTargetSize {
			break
		}
		if countByReason(entries, DoseReasonEbbinghausReview) >= cap {
			break
		}
		if picked[p.seed.AtomID] {
			continue
		}
		entries = append(entries, DailyDoseEntry{
			AtomID:     p.seed.AtomID,
			Topic:      p.seed.Topic,
			Title:      p.seed.Title,
			DoseReason: DoseReasonEbbinghausReview,
		})
		picked[p.seed.AtomID] = true
	}
	return entries
}

// pickWeaknessSlots fills up to DoseWeaknessCount weakness-drill slots.
//
// W6 (Epic-1b): when the learner has Growth Edges they own this slot —
// see pickGrowthEdgeSlots. Otherwise the legacy pool applies: atoms in
// topics where MCQ accuracy is below threshold, stable-sorted by accuracy
// ascending (weakest first), atom_id ascending.
func pickWeaknessSlots(
	entries []DailyDoseEntry,
	picked map[string]bool,
	in DailyDoseInput,
	threshold float64,
	cap int,
) []DailyDoseEntry {
	if len(in.GrowthEdges) > 0 {
		if in.ComposerV2 {
			return pickGrowthEdgeSlotsV2(entries, picked, in, cap)
		}
		return pickGrowthEdgeSlots(entries, picked, in, cap)
	}
	type weakPair struct {
		seed     AtomSeed
		accuracy float64
	}
	pool := make([]weakPair, 0, len(in.Seeds))
	for _, seed := range in.Seeds {
		acc, ok := in.TopicAccuracy[seed.Topic]
		if !ok {
			continue // no accuracy data → not eligible for weakness slot
		}
		if acc < threshold {
			pool = append(pool, weakPair{seed: seed, accuracy: acc})
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].accuracy != pool[j].accuracy {
			return pool[i].accuracy < pool[j].accuracy // weakest first
		}
		return pool[i].seed.AtomID < pool[j].seed.AtomID
	})
	for _, p := range pool {
		if len(entries) >= DoseTargetSize {
			break
		}
		if countByReason(entries, DoseReasonWeakness) >= cap {
			break
		}
		if picked[p.seed.AtomID] {
			continue
		}
		entries = append(entries, DailyDoseEntry{
			AtomID:     p.seed.AtomID,
			Topic:      p.seed.Topic,
			Title:      p.seed.Title,
			DoseReason: DoseReasonWeakness,
		})
		picked[p.seed.AtomID] = true
	}
	return entries
}

// pickGrowthEdgeSlots fills the weakness slot from the learner's Growth
// Edges, shakiest first (W6, Epic-1b). Per edge it picks ONE atom: the first
// cached drill atom (W4) present in the seed universe, else the first seed
// whose topic slug matches the edge's concept_key or tags. One pick per edge
// keeps the slot diverse across frontiers; the slot cap + global budget hold.
func pickGrowthEdgeSlots(
	entries []DailyDoseEntry,
	picked map[string]bool,
	in DailyDoseInput,
	cap int,
) []DailyDoseEntry {
	edges := append([]GrowthEdgeInput(nil), in.GrowthEdges...)
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Strength != edges[j].Strength {
			return edges[i].Strength > edges[j].Strength // shakiest first
		}
		return edges[i].ConceptKey < edges[j].ConceptKey
	})

	seedByID := seedIndex(in.Seeds)
	for _, edge := range edges {
		if len(entries) >= DoseTargetSize ||
			countByReason(entries, DoseReasonWeakness) >= cap {
			break
		}
		if seed, ok := resolveEdgeAtom(edge, in, picked, seedByID); ok {
			entries = appendWeakness(entries, picked, seed)
		}
	}
	return entries
}

// seedIndex maps atom_id → AtomSeed for O(1) cached-drill resolution.
func seedIndex(seeds []AtomSeed) map[string]AtomSeed {
	m := make(map[string]AtomSeed, len(seeds))
	for _, s := range seeds {
		m[s.AtomID] = s
	}
	return m
}

// appendWeakness appends a weakness-tagged entry and marks the atom picked.
func appendWeakness(entries []DailyDoseEntry, picked map[string]bool, seed AtomSeed) []DailyDoseEntry {
	picked[seed.AtomID] = true
	return append(entries, DailyDoseEntry{
		AtomID:     seed.AtomID,
		Topic:      seed.Topic,
		Title:      seed.Title,
		DoseReason: DoseReasonWeakness,
	})
}

// resolveEdgeAtom picks the ONE atom that drills a Growth Edge, in the v1 order:
// the first cached drill atom (W4) present + unpicked in the seed universe, else
// the first unpicked seed whose topic slug matches the edge's concept_key or a
// tag. Returns (seed, true) on a hit; (_, false) when nothing is servable. Shared
// by the v1 and v2 (ADR-224) weakness selectors so their per-edge picks are identical.
func resolveEdgeAtom(edge GrowthEdgeInput, in DailyDoseInput, picked map[string]bool, seedByID map[string]AtomSeed) (AtomSeed, bool) {
	// 1) Cached drill atom (W4) within the seed universe.
	for _, atomID := range edge.CachedDrillAtomIDs {
		seed, ok := seedByID[atomID]
		if !ok || picked[seed.AtomID] {
			continue
		}
		return seed, true
	}
	// 2) Topic/tag slug fallback — first unpicked seed whose topic slug matches
	// the edge's concept_key or one of its tags.
	slugs := make(map[string]bool, 1+len(edge.Tags))
	if k := normalizeTopicSlug(edge.ConceptKey); k != "" {
		slugs[k] = true
	}
	for _, tag := range edge.Tags {
		if k := normalizeTopicSlug(tag); k != "" {
			slugs[k] = true
		}
	}
	for _, seed := range in.Seeds {
		if picked[seed.AtomID] || !slugs[normalizeTopicSlug(seed.Topic)] {
			continue
		}
		return seed, true
	}
	return AtomSeed{}, false
}

// pickGrowthEdgeSlotsV2 is the ADR-224 (CHO-2045) weakness selector. It buckets
// the learner's Growth Edges by subject (Category; "" = a virtual "unmapped" KG so
// upload-born edges are never dropped) and fills the weakness slots with
// cross-subject balance + weighted sampling, seeded for day-stable determinism.
//
// It collapses to the v1 deterministic fill (BYTE-IDENTICAL) in two cases:
//   - a single subject bucket — the dose is focused by design, nothing to balance;
//   - urgency CONCENTRATED in one subject (exactly one subject has a critical edge,
//     Strength ≥ CriticalWeaknessStrength) — that one genuinely urgent subject may
//     take both slots so it is not diluted. When ≥2 subjects are critical the
//     spread wins instead (a fresh multi-subject learner still gets variety).
//
// Otherwise (≥2 subjects, none critical): weighted-sample a subject visiting order
// (weight = each subject's shakiest edge), then round-robin one sampled edge per
// subject — yielding ≤1 weakness slot per subject in the first DoseWeaknessCount
// picks, biased to shakier subjects, with the long tail still reachable. Fail-soft:
// an unservable edge simply falls through; the slot cap + 5-atom budget always hold.
func pickGrowthEdgeSlotsV2(entries []DailyDoseEntry, picked map[string]bool, in DailyDoseInput, cap int) []DailyDoseEntry {
	order := make([]string, 0, len(in.GrowthEdges))
	buckets := make(map[string][]GrowthEdgeInput, len(in.GrowthEdges))
	criticalSubjects := 0
	for _, e := range in.GrowthEdges {
		if _, ok := buckets[e.Category]; !ok {
			order = append(order, e.Category)
		}
		buckets[e.Category] = append(buckets[e.Category], e)
	}
	for _, cat := range order {
		for _, e := range buckets[cat] {
			if e.Strength >= CriticalWeaknessStrength {
				criticalSubjects++
				break
			}
		}
	}

	// Collapse to the v1 deterministic fill (byte-identical) in two cases:
	//   - a single subject bucket — the dose is focused by design; and
	//   - urgency CONCENTRATED in ONE subject (exactly one subject has a critical
	//     edge) — that subject may take both slots so a genuinely urgent weakness
	//     is not diluted. NB: when ≥2 subjects are critical (a fresh multi-subject
	//     learner whose edges are all maximally shaky — the exact monoculture case
	//     v2 exists to fix), the spread MUST win, so this does NOT fire.
	if len(order) <= 1 || criticalSubjects == 1 {
		return pickGrowthEdgeSlots(entries, picked, in, cap)
	}

	rng := rand.New(rand.NewSource(in.RandSeed))

	// Weighted-sample the subject visiting order (weight = subject's shakiest edge).
	subjOrder := weightedOrder(len(order), func(i int) float64 {
		m := 0.0
		for _, e := range buckets[order[i]] {
			if e.Strength > m {
				m = e.Strength
			}
		}
		return m
	}, rng)

	// Within each subject, weighted-sample the edge order (weight = strength).
	edgeOrder := make(map[string][]GrowthEdgeInput, len(order))
	maxDepth := 0
	for _, cat := range order {
		es := buckets[cat]
		idxs := weightedOrder(len(es), func(i int) float64 { return es[i].Strength }, rng)
		ordered := make([]GrowthEdgeInput, len(es))
		for i, ix := range idxs {
			ordered[i] = es[ix]
		}
		edgeOrder[cat] = ordered
		if len(es) > maxDepth {
			maxDepth = len(es)
		}
	}

	// Round-robin across subjects: rank-0 of each subject before any subject
	// doubles up — so distinct subjects fill the slots first (the spread).
	seedByID := seedIndex(in.Seeds)
	for depth := 0; depth < maxDepth; depth++ {
		for _, si := range subjOrder {
			if len(entries) >= DoseTargetSize ||
				countByReason(entries, DoseReasonWeakness) >= cap {
				return entries
			}
			es := edgeOrder[order[si]]
			if depth >= len(es) {
				continue
			}
			if seed, ok := resolveEdgeAtom(es[depth], in, picked, seedByID); ok {
				entries = appendWeakness(entries, picked, seed)
			}
		}
	}
	return entries
}

// weightedOrder returns indices [0,n) in a weighted-random order WITHOUT
// replacement (Efraimidis–Spirakis: key = u^(1/w), largest key first): an item
// with a larger weight is more likely to come earlier, but any item can appear
// anywhere (fat-tail exploration). Deterministic for a given rng. A non-positive
// weight is floored to a tiny epsilon so the item keeps a small chance rather than
// being dropped.
func weightedOrder(n int, weight func(i int) float64, rng *rand.Rand) []int {
	type keyed struct {
		idx int
		key float64
	}
	ks := make([]keyed, n)
	for i := 0; i < n; i++ {
		w := weight(i)
		if w <= 0 {
			w = 1e-9
		}
		u := rng.Float64()
		if u <= 0 {
			u = 1e-12
		}
		ks[i] = keyed{idx: i, key: math.Pow(u, 1.0/w)}
	}
	sort.SliceStable(ks, func(a, b int) bool {
		if ks[a].key != ks[b].key {
			return ks[a].key > ks[b].key
		}
		return ks[a].idx < ks[b].idx
	})
	out := make([]int, n)
	for i, k := range ks {
		out[i] = k.idx
	}
	return out
}

// DoseSeed derives the ADR-224 sampler seed from the dose day (YYYY-MM-DD) and the
// learner GCID via a non-cryptographic FNV-1a digest (mirrors composeDoseID's
// shortHash). Same (day, gcid) → same seed → same dose within a day; a new day →
// a new seed → a fresh sample. Deterministic and infra-free (hexagonal purity).
func DoseSeed(day, gcid string) int64 {
	const fnvOffset uint64 = 14695981039346656037
	const fnvPrime uint64 = 1099511628211
	h := fnvOffset
	for _, b := range []byte(day + "|" + gcid) {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return int64(h)
}

// pickFocusedEdgeSlots fills the dose's weakness slot by drilling a SINGLE Growth
// Edge — the focused-practice path (Phase 2A). It finds the edge in
// in.GrowthEdges whose EdgeID matches in.FocusEdgeID and serves up to
// DoseTargetSize of its cached drill atoms (W4) that are present in the seed
// universe, in edge order, all tagged DoseReasonWeakness.
//
// ALIGNMENT INVARIANT (CHO-1895): the focused weakness slot serves ONLY cached
// drill atoms — NEVER a broad topic/tag slug match. RecoverByDrillAtomID recovers
// the edge whose cached_drill_atom_ids contains the completed atom, so a
// topic-matched atom that is NOT cached can never recover the edge via the drill
// path (the upload→practice→grow loop would never close). When no cached drill
// atom is servable the slot fails soft — it adds nothing, and the caller's
// fresh/final top-up yields a normal dose (no misaligned drill is ever served).
// When no edge matches FocusEdgeID the entries are likewise returned unchanged
// (graceful fallback). Already-picked atoms are skipped; the DoseTargetSize budget
// always holds. Deterministic: cached atoms walk in edge order.
func pickFocusedEdgeSlots(
	entries []DailyDoseEntry,
	picked map[string]bool,
	in DailyDoseInput,
) []DailyDoseEntry {
	var focus GrowthEdgeInput
	found := false
	for _, edge := range in.GrowthEdges {
		if edge.EdgeID == in.FocusEdgeID {
			focus = edge
			found = true
			break
		}
	}
	if !found {
		return entries // graceful fallback — fresh/final top-up yields a normal dose
	}

	seedByID := make(map[string]AtomSeed, len(in.Seeds))
	for _, seed := range in.Seeds {
		seedByID[seed.AtomID] = seed
	}

	// Cached drill atoms (W4) within the seed universe, in edge order. This is
	// the ONLY source for the focused weakness slot — a slug/topic fallback is
	// deliberately absent so every focused weakness pick can recover its edge
	// via RecoverByDrillAtomID. The cached set is gradable-by-construction
	// (drillcache + the handler's resolve-and-filter both drop non-gradable
	// atoms), so a served drill is always answerable.
	for _, atomID := range focus.CachedDrillAtomIDs {
		if len(entries) >= DoseTargetSize {
			return entries
		}
		seed, ok := seedByID[atomID]
		if !ok || picked[seed.AtomID] {
			continue
		}
		entries = append(entries, DailyDoseEntry{
			AtomID:     seed.AtomID,
			Topic:      seed.Topic,
			Title:      seed.Title,
			DoseReason: DoseReasonWeakness,
		})
		picked[seed.AtomID] = true
	}
	return entries
}

// normalizeTopicSlug mirrors learner_weakness.NormalizeConceptKey (lower-case,
// non-alphanumeric runs → single hyphen) so dose topic matching agrees with
// how concept keys were minted, without coupling the aggregates.
func normalizeTopicSlug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// pickCuriositySlots fills up to DoseCuriosityCount curiosity slots with
// atoms in topics covered by the learner's active LearningPath. Stable
// sort by atom_id ascending. Falls back to SeenTopics adjacency when
// ActivePathTopics is empty (legacy compat with pre-S5.1 paths).
func pickCuriositySlots(
	entries []DailyDoseEntry,
	picked map[string]bool,
	in DailyDoseInput,
	cap int,
) []DailyDoseEntry {
	source := in.ActivePathTopics
	if len(source) == 0 {
		// Legacy path — pre-S5.1 callers passing only SeenTopics still
		// get a curiosity pool from the topics the learner has touched.
		source = in.SeenTopics
	}
	if len(source) == 0 {
		return entries
	}

	pool := make([]AtomSeed, 0, len(in.Seeds))
	for _, seed := range in.Seeds {
		if !source[seed.Topic] {
			continue
		}
		pool = append(pool, seed)
	}
	if in.ComposerV2 {
		return pickCuriositySpreadV2(entries, picked, in, pool, cap)
	}
	return fillCuriosityDeterministic(entries, picked, pool, cap)
}

// fillCuriosityDeterministic is the v1 curiosity fill: stable-sort the pool by
// atom_id and take up to DoseCuriosityCount unpicked atoms. Shared with the v2
// degenerate (single-topic) path so that path stays byte-identical.
func fillCuriosityDeterministic(entries []DailyDoseEntry, picked map[string]bool, pool []AtomSeed, cap int) []DailyDoseEntry {
	sorted := append([]AtomSeed(nil), pool...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].AtomID < sorted[j].AtomID
	})
	for _, seed := range sorted {
		if len(entries) >= DoseTargetSize {
			break
		}
		if countByReason(entries, DoseReasonCuriosity) >= cap {
			break
		}
		if picked[seed.AtomID] {
			continue
		}
		entries = appendCuriosity(entries, picked, seed)
	}
	return entries
}

// appendCuriosity appends a curiosity-tagged entry and marks the atom picked.
func appendCuriosity(entries []DailyDoseEntry, picked map[string]bool, seed AtomSeed) []DailyDoseEntry {
	picked[seed.AtomID] = true
	return append(entries, DailyDoseEntry{
		AtomID:     seed.AtomID,
		Topic:      seed.Topic,
		Title:      seed.Title,
		DoseReason: DoseReasonCuriosity,
	})
}

// pickCuriositySpreadV2 is the ADR-224 curiosity selector: bucket the curiosity
// pool by topic and spread the slots across topics (≤1 per topic in the first
// DoseCuriosityCount picks) so a multi-topic learner's active maps all stay warm.
// Active-path topics carry no per-topic priority signal today, so the topic
// visiting order is a seeded uniform shuffle; within a topic, atoms are taken in
// atom_id order. Degenerate (≤1 topic) → the v1 deterministic fill (byte-identical).
// It seeds its own RNG from in.RandSeed (independent of the weakness sampler; the
// two operate on disjoint pools, so reusing the day seed is deterministic + correct).
func pickCuriositySpreadV2(entries []DailyDoseEntry, picked map[string]bool, in DailyDoseInput, pool []AtomSeed, cap int) []DailyDoseEntry {
	order := make([]string, 0, len(pool))
	byTopic := make(map[string][]AtomSeed, len(pool))
	for _, s := range pool {
		if _, ok := byTopic[s.Topic]; !ok {
			order = append(order, s.Topic)
		}
		byTopic[s.Topic] = append(byTopic[s.Topic], s)
	}
	if len(order) <= 1 {
		return fillCuriosityDeterministic(entries, picked, pool, cap)
	}

	rng := rand.New(rand.NewSource(in.RandSeed))
	topicOrder := weightedOrder(len(order), func(int) float64 { return 1 }, rng)

	maxDepth := 0
	for _, ts := range order {
		sort.SliceStable(byTopic[ts], func(i, j int) bool {
			return byTopic[ts][i].AtomID < byTopic[ts][j].AtomID
		})
		if len(byTopic[ts]) > maxDepth {
			maxDepth = len(byTopic[ts])
		}
	}

	for depth := 0; depth < maxDepth; depth++ {
		for _, ti := range topicOrder {
			if len(entries) >= DoseTargetSize ||
				countByReason(entries, DoseReasonCuriosity) >= cap {
				return entries
			}
			ss := byTopic[order[ti]]
			if depth >= len(ss) {
				continue
			}
			if seed := ss[depth]; !picked[seed.AtomID] {
				entries = appendCuriosity(entries, picked, seed)
			}
		}
	}
	return entries
}

// pickFreshTopUp fills remaining slots up to DoseTargetSize with atoms
// the learner has never reviewed. Tagged DoseReasonFresh.
func pickFreshTopUp(
	entries []DailyDoseEntry,
	picked map[string]bool,
	seeds []AtomSeed,
	states map[string]SM2State,
) []DailyDoseEntry {
	if len(entries) >= DoseTargetSize {
		return entries
	}
	pool := make([]AtomSeed, 0, len(seeds))
	for _, seed := range seeds {
		if _, seen := states[seed.AtomID]; seen {
			continue
		}
		pool = append(pool, seed)
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return pool[i].AtomID < pool[j].AtomID
	})
	for _, seed := range pool {
		if len(entries) >= DoseTargetSize {
			break
		}
		if picked[seed.AtomID] {
			continue
		}
		entries = append(entries, DailyDoseEntry{
			AtomID:     seed.AtomID,
			Topic:      seed.Topic,
			Title:      seed.Title,
			DoseReason: DoseReasonFresh,
		})
		picked[seed.AtomID] = true
	}
	return entries
}

// pickFinalTopUp guarantees the 5-card budget when seeds permit by
// pulling from any remaining seeds (already-reviewed included). Tagged
// DoseReasonFresh — the surface treats this as "discover" content for
// the learner since their existing slots are saturated.
func pickFinalTopUp(
	entries []DailyDoseEntry,
	picked map[string]bool,
	seeds []AtomSeed,
	_ map[string]SM2State,
) []DailyDoseEntry {
	if len(entries) >= DoseTargetSize {
		return entries
	}
	all := make([]AtomSeed, len(seeds))
	copy(all, seeds)
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].AtomID < all[j].AtomID
	})
	for _, seed := range all {
		if len(entries) >= DoseTargetSize {
			break
		}
		if picked[seed.AtomID] {
			continue
		}
		entries = append(entries, DailyDoseEntry{
			AtomID:     seed.AtomID,
			Topic:      seed.Topic,
			Title:      seed.Title,
			DoseReason: DoseReasonFresh,
		})
		picked[seed.AtomID] = true
	}
	return entries
}

func countByReason(entries []DailyDoseEntry, r DoseReason) int {
	n := 0
	for _, e := range entries {
		if e.DoseReason == r {
			n++
		}
	}
	return n
}

// SeedTopicCount returns the number of distinct topics in a seed list.
// Convenience helper for tests/repos to verify the canonical 5-topic layout.
func SeedTopicCount(seeds []AtomSeed) int {
	topics := map[string]bool{}
	for _, s := range seeds {
		topics[strings.ToLower(s.Topic)] = true
	}
	return len(topics)
}

// ─── N-Companion dispatch (CHO-1577) ──────────────────────────────────────

// RosterCompanion is a lightweight projection of a Companion Instance used
// solely by the dispatch algorithm. It carries only the fields needed to
// score and rank candidates — avoids coupling daily_dose.go to the full
// Instance type.
//
// Populated by the HTTP handler from the Instance + GrowthSnapshot stored
// on the RosterEntry returned by InstanceRepository.ListRosterByOwner.
type RosterCompanion struct {
	CompanionID    string
	Name           string
	Specialization string
	GrowthStage    int // 0 = Egg (pre-hatch); ≥1 = hatched
	GrowthExp      int // cumulative EXP; tie-break criterion
}

// IsPreHatch reports whether the Companion has not yet hatched.
// Stage 0 (Egg) Companions MUST never be selected as a greeting candidate.
func (r RosterCompanion) IsPreHatch() bool {
	return r.GrowthStage == 0
}

// TieBreakReasonHighestExp is the canonical tie_break_reason string
// returned when the dispatch resolved a tie using growth_exp descending.
const TieBreakReasonHighestExp = "highest_growth_exp"

// SelectGreetingCompanion picks which Companion greets the learner at
// /a/daily-dose based on the topic distribution of the served DailyDose.
//
// Algorithm:
//
//  1. Filter pre-hatch Companions (GrowthStage == 0) from all candidates.
//  2. Count how many DailyDose entries share a topic with each hatched
//     Companion's Specialization.
//  3. The Companion with the highest match count wins. Ties break by
//     GrowthExp descending (highest EXP wins). Secondary stability
//     tie-break is CompanionID ascending to ensure determinism across
//     re-runs with identical state.
//  4. When no Companion has any topic match (score = 0 for all), step 3
//     still executes — effectively a pure-exp ranking among hatched
//     Companions, ensuring Ignis (9500 exp) wins over Aria (150) or Eira
//     (200) when no specialization overlap exists.
//  5. Returns ("", "") when the roster is nil/empty or every Companion is
//     pre-hatch (nobody can greet).
//
// Returns: (companion_id, tie_break_reason).
//   - tie_break_reason is empty when the winner was unambiguous (highest
//     match count AND no tie).
//   - tie_break_reason == TieBreakReasonHighestExp when a tie in match
//     count was resolved by GrowthExp.
//
// CRITICAL: this function is read-only over dose — it MUST NOT mutate
// dose.Entries. Callers may assert composition integrity after the call.
func SelectGreetingCompanion(roster []RosterCompanion, dose DailyDose) (companionID string, tieBreakReason string) {
	// Filter: only hatched candidates.
	candidates := make([]RosterCompanion, 0, len(roster))
	for _, f := range roster {
		if !f.IsPreHatch() {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		return "", ""
	}

	// Count how many dose entries' topics match each candidate's specialization.
	scores := make(map[string]int, len(candidates))
	for _, f := range candidates {
		spec := strings.ToLower(strings.TrimSpace(f.Specialization))
		for _, e := range dose.Entries {
			if strings.ToLower(strings.TrimSpace(e.Topic)) == spec {
				scores[f.CompanionID]++
			}
		}
	}

	// Find the highest score.
	maxScore := -1
	for _, f := range candidates {
		if s := scores[f.CompanionID]; s > maxScore {
			maxScore = s
		}
	}

	// Collect all candidates with the max score (tie candidates).
	tied := make([]RosterCompanion, 0, len(candidates))
	for _, f := range candidates {
		if scores[f.CompanionID] == maxScore {
			tied = append(tied, f)
		}
	}

	// Stable sort tied candidates by GrowthExp descending, then CompanionID
	// ascending for full determinism.
	sort.SliceStable(tied, func(i, j int) bool {
		if tied[i].GrowthExp != tied[j].GrowthExp {
			return tied[i].GrowthExp > tied[j].GrowthExp
		}
		return tied[i].CompanionID < tied[j].CompanionID
	})

	winner := tied[0]
	wasTie := len(tied) > 1

	reason := ""
	if wasTie {
		reason = TieBreakReasonHighestExp
	}
	return winner.CompanionID, reason
}

// DoseAtomBreakdown summarises the dose composition by reason bucket.
// Surfaced in the /companion/daily-dose HTTP response and the
// chora.consumption.daily_dose.served.v1 Pub/Sub event.
type DoseAtomBreakdown struct {
	Ebbinghaus int `json:"ebbinghaus"`
	Weakness   int `json:"weakness"`
	Curiosity  int `json:"curiosity"`
	Fresh      int `json:"fresh"`
	// Campaign counts campaign-march picks (ADR-227 D12). omitempty keeps
	// the pre-campaign wire shape when zero (degeneration guard).
	Campaign int `json:"campaign,omitempty"`
}

// BuildAtomBreakdown computes the DoseAtomBreakdown from the entries of a
// composed DailyDose. Pure read — does not mutate entries.
func BuildAtomBreakdown(entries []DailyDoseEntry) DoseAtomBreakdown {
	var b DoseAtomBreakdown
	for _, e := range entries {
		switch e.DoseReason {
		case DoseReasonEbbinghausReview:
			b.Ebbinghaus++
		case DoseReasonWeakness:
			b.Weakness++
		case DoseReasonCuriosity:
			b.Curiosity++
		case DoseReasonCampaign:
			b.Campaign++
		default:
			b.Fresh++
		}
	}
	return b
}

// GreetingFrom is the payload embedded in the HTTP response and Pub/Sub
// event to identify which Companion is greeting the learner for this dose.
type GreetingFrom struct {
	CompanionID    string `json:"companion_id"`
	Name           string `json:"name"`
	VoiceAccent    string `json:"voice_accent,omitempty"`
	TieBreakReason string `json:"tie_break_reason,omitempty"`
}
