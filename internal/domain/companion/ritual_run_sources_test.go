package companion

// ritual_run_sources_test.go: the learner story never shows a raw atom id as a
// source (UX Track U, D2 / N9 slice a).
//
// Why this arrives with the citations and not before. Nothing populated
// StepStamp.Citations until the grounding wire-up, so LearnerRunStory copying
// them straight into Sources was harmless. The moment real citations land it
// stops being harmless: the agent's cite_atom refs carry an atom_id and a
// revision_id and NO title, so the honest citation to record is the id, and
// copying it through would print a bare UUID to the learner.
//
// ADR-215 D5 keeps model internals out of the learner projection, and Track U
// R25 is making the whole surface speak learner language. A UUID is neither a
// source the learner can act on nor something they can look up. The O+ record
// keeps every id; the learner sees a source only when it has a NAME.

import "testing"

func storyFor(citations []string) RitualRunStory {
	run := &RitualRun{
		RitualName: "Study session",
		Status:     RunStatusCompleted,
		Stamps:     []StepStamp{{StepIndex: 0, SkillKey: "explain_anew", Citations: citations}},
	}
	return LearnerRunStory(run, map[string]CatalogEntry{
		"explain_anew": {SkillKey: "explain_anew", Name: "Explain It Differently"},
	})
}

func TestLearnerRunStory_NamedSourceIsShown(t *testing.T) {
	story := storyFor([]string{"Fractions basics"})
	if len(story.Steps) != 1 {
		t.Fatalf("steps = %d; want 1", len(story.Steps))
	}
	if got := story.Steps[0].Sources; len(got) != 1 || got[0] != "Fractions basics" {
		t.Errorf("Sources = %v; want the named source", got)
	}
}

func TestLearnerRunStory_RawAtomIDIsNotShownAsASource(t *testing.T) {
	story := storyFor([]string{"01920000-0000-7000-8000-000000000001"})
	if len(story.Steps) != 1 {
		t.Fatalf("steps = %d; want 1", len(story.Steps))
	}
	if got := story.Steps[0].Sources; len(got) != 0 {
		t.Errorf("Sources = %v; want none: a bare atom id is not a source a learner can act on", got)
	}
}

// A mixed stamp keeps the nameable source and drops the id. Dropping BOTH
// because one was an id would hide a real source; showing both would print a
// UUID beside it.
func TestLearnerRunStory_MixedCitationsKeepOnlyTheNamedOnes(t *testing.T) {
	story := storyFor([]string{"01920000-0000-7000-8000-000000000001", "Fractions basics"})
	if got := story.Steps[0].Sources; len(got) != 1 || got[0] != "Fractions basics" {
		t.Errorf("Sources = %v; want only the named source", got)
	}
}

// The O plus record is NOT filtered. Provenance keeps every id, because the
// filter is about what the LEARNER is shown, not about what happened.
func TestLearnerRunStory_StampKeepsEveryCitation(t *testing.T) {
	ids := []string{"01920000-0000-7000-8000-000000000001", "Fractions basics"}
	run := &RitualRun{
		RitualName: "Study session", Status: RunStatusCompleted,
		Stamps: []StepStamp{{StepIndex: 0, SkillKey: "explain_anew", Citations: ids}},
	}
	_ = LearnerRunStory(run, nil)
	if len(run.Stamps[0].Citations) != 2 {
		t.Errorf("the stamp lost citations to the learner filter: %v", run.Stamps[0].Citations)
	}
}

// D2 tail (a): a grounded step shows its sources BY NAME.
//
// The bare-id filter was the right first move (a UUID is not a source a
// learner can act on) but it left a grounded step showing nothing at all. The
// adapter now resolves atom ids to titles through the SAME reader D3 uses, and
// the projection renders the name.
func storyWithTitles(citations []string, titles map[string]string) RitualRunStory {
	run := &RitualRun{
		RitualName: "Study session",
		Status:     RunStatusCompleted,
		Stamps:     []StepStamp{{StepIndex: 0, SkillKey: "explain_anew", Citations: citations}},
	}
	return LearnerRunStoryWithTitles(run, map[string]CatalogEntry{
		"explain_anew": {SkillKey: "explain_anew", Name: "Explain It Differently"},
	}, titles)
}

func TestLearnerRunStory_ResolvedAtomIDRendersItsTitle(t *testing.T) {
	const id = "01920000-0000-7000-8000-000000000001"
	story := storyWithTitles([]string{id}, map[string]string{id: "Dividing Fractions"})
	if got := story.Steps[0].Sources; len(got) != 1 || got[0] != "Dividing Fractions" {
		t.Errorf("Sources = %v; want the resolved title", got)
	}
}

// An id the projection cannot name is OMITTED, never shown raw. The learner
// gains nothing from a UUID and the step is honest either way: it says which
// skill ran, it just cannot name that one source.
func TestLearnerRunStory_UnresolvableAtomIDIsOmittedNotShown(t *testing.T) {
	const id = "01920000-0000-7000-8000-000000000002"
	story := storyWithTitles([]string{id}, map[string]string{})
	if got := story.Steps[0].Sources; len(got) != 0 {
		t.Errorf("Sources = %v; want none, and never the raw id", got)
	}
}

// A mixed step keeps every source it can name and drops only the one it cannot.
func TestLearnerRunStory_ResolvesWhatItCanAndDropsTheRest(t *testing.T) {
	const known, unknown = "01920000-0000-7000-8000-000000000001", "01920000-0000-7000-8000-000000000002"
	story := storyWithTitles([]string{unknown, known, "Fractions basics"},
		map[string]string{known: "Dividing Fractions"})
	got := story.Steps[0].Sources
	if len(got) != 2 || got[0] != "Dividing Fractions" || got[1] != "Fractions basics" {
		t.Errorf("Sources = %v; want the resolved title then the already-named source", got)
	}
}

// A resolved title that is itself blank is not a name. Rendering it would put an
// empty bullet on the page, which reads as a source with no title rather than
// as no source.
func TestLearnerRunStory_BlankResolvedTitleIsOmitted(t *testing.T) {
	const id = "01920000-0000-7000-8000-000000000003"
	story := storyWithTitles([]string{id}, map[string]string{id: "   "})
	if got := story.Steps[0].Sources; len(got) != 0 {
		t.Errorf("Sources = %v; want none for a blank title", got)
	}
}

// The stamp is untouched by the resolve. Provenance keeps the id whether or not
// anything could name it.
func TestLearnerRunStory_ResolveDoesNotRewriteTheStamp(t *testing.T) {
	const id = "01920000-0000-7000-8000-000000000001"
	run := &RitualRun{
		RitualName: "Study session", Status: RunStatusCompleted,
		Stamps: []StepStamp{{StepIndex: 0, SkillKey: "explain_anew", Citations: []string{id}}},
	}
	_ = LearnerRunStoryWithTitles(run, nil, map[string]string{id: "Dividing Fractions"})
	if len(run.Stamps[0].Citations) != 1 || run.Stamps[0].Citations[0] != id {
		t.Errorf("the stamp was rewritten by the learner resolve: %v", run.Stamps[0].Citations)
	}
}
