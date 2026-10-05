package companionmind

import (
	"testing"
	"time"
)

func TestAssembleGoalScopedView_ShakyConceptsAreGoalSubtreeIntersectActive(t *testing.T) {
	// The whole point of the goal scoping: a weakness only counts as "shaky on
	// THIS goal" when it sits inside the goal's concept set AND is still active.
	in := GoalScopedInput{
		GoalID:              "goal-1",
		GoalTitle:           "Fractions",
		CompanionID:         "fam-1",
		CompanionName:       "Ember",
		GoalConceptKeys:     []string{"fractions", "number-sense", "decimals"},
		MasteredConceptKeys: []string{"decimals"},
		Weaknesses: []WeaknessSignal{
			{ConceptKey: "fractions", ConceptLabel: "comparing fractions", Strength: 0.8, Active: true},
			{ConceptKey: "decimals", ConceptLabel: "decimals", Strength: 0.9, Active: false}, // grown → not shaky
			{ConceptKey: "trigonometry", ConceptLabel: "trig", Strength: 0.7, Active: true},  // outside the goal → not this goal's problem
		},
	}

	v := AssembleGoalScopedView(in)

	if len(v.ShakyConcepts) != 1 {
		t.Fatalf("ShakyConcepts = %d (%+v), want exactly 1 (fractions)", len(v.ShakyConcepts), v.ShakyConcepts)
	}
	if v.ShakyConcepts[0].ConceptKey != "fractions" {
		t.Errorf("shaky concept = %q, want %q", v.ShakyConcepts[0].ConceptKey, "fractions")
	}
}

func TestAssembleGoalScopedView_ShakiestConceptFirst(t *testing.T) {
	// The reflection leads with what the learner is struggling with most, so the
	// block must hand the caller a deterministic shakiest-first order.
	in := GoalScopedInput{
		GoalID: "goal-1", CompanionID: "fam-1",
		GoalConceptKeys: []string{"a", "b", "c"},
		Weaknesses: []WeaknessSignal{
			{ConceptKey: "a", ConceptLabel: "A", Strength: 0.3, Active: true},
			{ConceptKey: "b", ConceptLabel: "B", Strength: 0.9, Active: true},
			{ConceptKey: "c", ConceptLabel: "C", Strength: 0.6, Active: true},
		},
	}

	v := AssembleGoalScopedView(in)

	got := []string{v.ShakyConcepts[0].ConceptKey, v.ShakyConcepts[1].ConceptKey, v.ShakyConcepts[2].ConceptKey}
	want := []string{"b", "c", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shaky order = %v, want shakiest-first %v", got, want)
		}
	}
}

func TestAssembleGoalScopedView_DerivesProgressNeverTrustsHighWaterMark(t *testing.T) {
	// goal.MasteredConceptCount is the graduation subscriber's HIGH-WATER MARK,
	// not read-time progress (its own doc comment says so). The block must derive
	// progress from the mastered set, or a stale high-water mark would silently
	// misreport how far along the learner is.
	in := GoalScopedInput{
		GoalID: "goal-1", CompanionID: "fam-1",
		GoalConceptKeys:     []string{"a", "b", "c", "d"},
		MasteredConceptKeys: []string{"a", "b"},
	}

	v := AssembleGoalScopedView(in)

	if v.ConceptsTotal != 4 {
		t.Errorf("ConceptsTotal = %d, want 4", v.ConceptsTotal)
	}
	if v.ConceptsMastered != 2 {
		t.Errorf("ConceptsMastered = %d, want 2 (derived from the mastered set)", v.ConceptsMastered)
	}
}

func TestAssembleGoalScopedView_MasteredOutsideGoalDoesNotInflateProgress(t *testing.T) {
	// A concept the learner mastered elsewhere must not count toward THIS goal.
	in := GoalScopedInput{
		GoalID: "goal-1", CompanionID: "fam-1",
		GoalConceptKeys:     []string{"a", "b"},
		MasteredConceptKeys: []string{"a", "zzz-unrelated"},
	}

	v := AssembleGoalScopedView(in)

	if v.ConceptsMastered != 1 {
		t.Errorf("ConceptsMastered = %d, want 1 — an unrelated mastered concept inflated goal progress", v.ConceptsMastered)
	}
}

func TestAssembleGoalScopedView_HonestEmptyStates(t *testing.T) {
	// ADR-207 "Unknown is a state": an empty reflection must say so honestly and
	// never fabricate. Collections come back non-nil for a stable wire shape.
	v := AssembleGoalScopedView(GoalScopedInput{GoalID: "goal-1", CompanionID: "fam-1"})

	if v.HasMemory {
		t.Error("HasMemory must be false with no recalls")
	}
	if v.Memories == nil || len(v.Memories) != 0 {
		t.Errorf("Memories = %v, want a non-nil empty slice", v.Memories)
	}
	if v.ShakyConcepts == nil || len(v.ShakyConcepts) != 0 {
		t.Errorf("ShakyConcepts = %v, want a non-nil empty slice", v.ShakyConcepts)
	}
	if v.HasSignal() {
		t.Error("HasSignal must be false when there is no memory AND no shaky concept — nothing to reflect on")
	}
}

func TestAssembleGoalScopedView_HasSignalWhenThereIsSomethingToSay(t *testing.T) {
	withMemory := AssembleGoalScopedView(GoalScopedInput{
		GoalID: "g", CompanionID: "f",
		Memories: []EpisodicMemory{{ID: "m1", Content: "we talked about fractions", CreatedAt: time.Now()}},
	})
	if !withMemory.HasMemory || !withMemory.HasSignal() {
		t.Error("a view with recalls must report HasMemory and HasSignal")
	}

	withWeakness := AssembleGoalScopedView(GoalScopedInput{
		GoalID: "g", CompanionID: "f",
		GoalConceptKeys: []string{"a"},
		Weaknesses:      []WeaknessSignal{{ConceptKey: "a", ConceptLabel: "A", Strength: 0.5, Active: true}},
	})
	if !withWeakness.HasSignal() {
		t.Error("a view with a shaky concept must report HasSignal even with no memory")
	}
}

func TestGoalScopedView_ContentHashIsStableAndInputSensitive(t *testing.T) {
	// The cache row stores this hash so a regen can tell whether the INPUTS
	// actually moved. It must be stable across identical inputs (no spurious
	// regens) and must change when any input changes (no stale reflection served
	// as current).
	base := GoalScopedInput{
		GoalID: "g", CompanionID: "f",
		GoalConceptKeys:     []string{"a", "b"},
		MasteredConceptKeys: []string{"a"},
		Weaknesses:          []WeaknessSignal{{ConceptKey: "b", ConceptLabel: "B", Strength: 0.7, Active: true}},
		Memories:            []EpisodicMemory{{ID: "m1", Content: "hello", CreatedAt: time.Unix(0, 0)}},
	}

	h1 := AssembleGoalScopedView(base).ContentHash()
	h2 := AssembleGoalScopedView(base).ContentHash()
	if h1 == "" {
		t.Fatal("ContentHash must not be empty")
	}
	if h1 != h2 {
		t.Errorf("ContentHash is unstable across identical inputs: %q vs %q — this would regenerate forever", h1, h2)
	}

	moved := base
	moved.Weaknesses = []WeaknessSignal{{ConceptKey: "b", ConceptLabel: "B", Strength: 0.9, Active: true}} // shakier
	if AssembleGoalScopedView(moved).ContentHash() == h1 {
		t.Error("ContentHash did not change when a weakness strength moved — input drift would go undetected")
	}

	newMemory := base
	newMemory.Memories = append(append([]EpisodicMemory{}, base.Memories...),
		EpisodicMemory{ID: "m2", Content: "world", CreatedAt: time.Unix(1, 0)})
	if AssembleGoalScopedView(newMemory).ContentHash() == h1 {
		t.Error("ContentHash did not change when a new memory landed — input drift would go undetected")
	}
}

func TestGoalScopedView_ContentHashIgnoresIncidentalOrdering(t *testing.T) {
	// The same facts arriving in a different row order are the SAME inputs. If the
	// hash flipped on ordering, every read would look like drift and regenerate.
	a := GoalScopedInput{
		GoalID: "g", CompanionID: "f",
		GoalConceptKeys: []string{"a", "b"},
		Weaknesses: []WeaknessSignal{
			{ConceptKey: "a", ConceptLabel: "A", Strength: 0.5, Active: true},
			{ConceptKey: "b", ConceptLabel: "B", Strength: 0.5, Active: true},
		},
	}
	b := GoalScopedInput{
		GoalID: "g", CompanionID: "f",
		GoalConceptKeys: []string{"b", "a"},
		Weaknesses: []WeaknessSignal{
			{ConceptKey: "b", ConceptLabel: "B", Strength: 0.5, Active: true},
			{ConceptKey: "a", ConceptLabel: "A", Strength: 0.5, Active: true},
		},
	}

	if AssembleGoalScopedView(a).ContentHash() != AssembleGoalScopedView(b).ContentHash() {
		t.Error("ContentHash flipped on incidental input ordering — every read would look like drift")
	}
}

// ── The concept KEY-SPACE guard, at the DOMAIN boundary (CHO-2160 audit R6a) ──
//
// ⚠ THE SILENT-FAILURE CLASS. `Goal.ConceptSet` is trimmed and deduped but NEVER
// slugified — its own doc calls that "normalised", which means something much
// weaker than it sounds. A learner weakness's `ConceptKey` IS a slug. Intersect
// the two raw and the result is EMPTY: no shaky concepts, no progress,
// HasSignal() false — so the learner reads as "no memory yet" FOREVER, no
// synthesis is ever requested, and it looks exactly like an empty learner.
//
// Four adapters currently remember to normalise before calling in here. Nothing
// stopped a fifth from forgetting, and the failure is invisible. So the domain
// normalises BOTH sides itself: the slug rule is idempotent, so a caller that
// already normalised loses nothing, and a caller that did not is no longer
// silently wrong. Correct by construction beats correct by remembering.

func TestAssembleGoalScopedView_NormalisesTheGoalKeySpaceItself(t *testing.T) {
	view := AssembleGoalScopedView(GoalScopedInput{
		GoalID:      "g-1",
		CompanionID: "f-1",
		// RAW, exactly as Goal.ConceptSet holds them — human-cased, spaced.
		GoalConceptKeys: []string{"Place Value", "Long Division"},
		// SLUGS, exactly as learner_weakness holds them.
		MasteredConceptKeys: []string{"place-value"},
		Weaknesses: []WeaknessSignal{
			{ConceptKey: "long-division", ConceptLabel: "long division", Strength: 0.8, Active: true},
		},
	})

	if len(view.ShakyConcepts) != 1 {
		t.Fatalf("shaky = %d, want 1 — raw goal keys intersected to NOTHING against slugged weakness keys; "+
			"this is the silent 'empty learner' bug", len(view.ShakyConcepts))
	}
	if view.ConceptsMastered != 1 {
		t.Errorf("mastered = %d, want 1 — the same key-space mismatch silently zeroes progress", view.ConceptsMastered)
	}
	if view.ConceptsTotal != 2 {
		t.Errorf("total = %d, want 2", view.ConceptsTotal)
	}
	if !view.HasSignal() {
		t.Error("HasSignal false — no synthesis would EVER be requested for this learner")
	}
}

func TestAssembleGoalScopedView_NormalisationIsIdempotent(t *testing.T) {
	// The four adapters that already normalise must lose nothing. Slugged input
	// and raw input must produce the IDENTICAL view — including the ContentHash,
	// which gates every cached reflection (a hash that moved with the caller's
	// normalisation habit would refuse every synthesis forever).
	in := func(keys []string) GoalScopedInput {
		return GoalScopedInput{
			GoalID:              "g-1",
			CompanionID:         "f-1",
			GoalConceptKeys:     keys,
			MasteredConceptKeys: []string{"place-value"},
			Weaknesses: []WeaknessSignal{
				{ConceptKey: "long-division", ConceptLabel: "long division", Strength: 0.8, Active: true},
			},
		}
	}
	raw := AssembleGoalScopedView(in([]string{"Place Value", "Long Division"}))
	slug := AssembleGoalScopedView(in([]string{"place-value", "long-division"}))

	if raw.ContentHash() != slug.ContentHash() {
		t.Fatalf("ContentHash differs by the CALLER's normalisation habit (%s vs %s) — "+
			"the drift guard would refuse every synthesis", raw.ContentHash(), slug.ContentHash())
	}
	if raw.ConceptsMastered != slug.ConceptsMastered || len(raw.ShakyConcepts) != len(slug.ShakyConcepts) {
		t.Error("raw and slugged input produced different views")
	}
}
