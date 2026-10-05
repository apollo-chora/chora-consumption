// weakness_analyzed_hint_test.go - ADR-238 D2, the SEAM test.
//
// The matcher can be perfect and the resolver can mark Preferred correctly, and
// the bias will still never fire if the entry-concept hint is dropped somewhere
// between the upload row and the resolver call. That is not hypothetical: the
// goal_id half of this same feature shipped green on both sides while the
// gateway silently dropped the parameter in between, because no stub in the
// package recorded it. These tests assert the value ARRIVES, not merely that
// each end handles it correctly in isolation.
package subscribers

import (
	"context"
	"testing"
)

func TestWeaknessAnalyzed_Hint_ReachesTheResolver(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0},
		"matches nothing":   {0, 0, 1},
	}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates()}
	lookup := &stubUploadGoalLookup{goalID: "goal-xyz", entryConceptID: "concept-entry-7"}

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(resolver, lookup).
		WithConceptMatchMinCosine(0.55).
		WithConceptMatchTieBand(0.03)

	if err := sub.Handle(context.Background(), lwEnv("evt-hint"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d; want exactly 1 (built once per upload)", resolver.calls)
	}
	if resolver.gotEntryConceptID != "concept-entry-7" {
		t.Fatalf("resolver received entry concept %q; want concept-entry-7 - the D2 hint was dropped between the upload row and the resolver", resolver.gotEntryConceptID)
	}
}

func TestWeaknessAnalyzed_Hint_GoalLevelUploadSendsNoHint(t *testing.T) {
	// A goal-level "Diagnose my map" upload stores no entry concept, so the
	// resolver must be asked for an unbiased map. ADR-238 section 5 is explicit
	// that a whole-map diagnosis biased toward an arbitrary node would be a
	// subtler version of the very defect the ADR closes.
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0},
		"matches nothing":   {0, 0, 1},
	}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates()}
	lookup := &stubUploadGoalLookup{goalID: "goal-xyz"} // no entry concept

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(resolver, lookup).
		WithConceptMatchMinCosine(0.55).
		WithConceptMatchTieBand(0.03)

	if err := sub.Handle(context.Background(), lwEnv("evt-nohint"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resolver.gotEntryConceptID != "" {
		t.Fatalf("resolver received entry concept %q; want \"\" for a goal-level upload", resolver.gotEntryConceptID)
	}
}

func TestWeaknessAnalyzed_Hint_TieBandDefaultsOffWhenUnset(t *testing.T) {
	// The tie band is opt-in at the composition root. An un-wired subscriber must
	// behave exactly as it did pre-D2 (pure argmax), so forgetting to wire the
	// knob degrades to the old behaviour rather than to an arbitrary bias.
	sub := NewWeaknessAnalyzedSubscriber(&lwFakeRepo{}, &stubEmbedderByText{})
	if sub.tieBand != 0 {
		t.Fatalf("default tieBand = %v; want 0 (bias off unless explicitly wired)", sub.tieBand)
	}
}
