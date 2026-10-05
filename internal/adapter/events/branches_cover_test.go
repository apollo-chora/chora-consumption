// branches_cover_test.go — statement coverage for the events adapter branches
// not driven by the feature tests:
//
//   - ConceptEmitter / CampaignEmitter default `now` (nil) constructors,
//     nonNilStrings' nil-slice path, firstNonEmpty's all-empty return, and the
//     whole ClusterProjectionBindingSink adapter (0%).
//   - GoalKnowledgeRequestPayload (0%) including its slice bounds + default
//     source.
//   - LoadoutPublisherBridge (0% both methods).
//   - SuggestionRequestPayload's nil-weakness-list tail branches.
//
// reuses captureConceptPublisher from concept_emitter_test.go (same package).
package events

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TestConceptEmitter_NilNowDefaultsAndHelperNilPaths covers the convenient
// nil-`now` constructor, nonNilStrings' nil-slice normalisation and
// firstNonEmpty's all-empty return (all three drive the public publish path).
func TestConceptEmitter_NilNowDefaultsAndHelperNilPaths(t *testing.T) {
	pub := &captureConceptPublisher{}
	// nil now → defaults to time.Now().UTC().
	e := NewConceptEmitter(pub, nil)
	// nil Detached + nil Resulting (still a real change via attached), empty
	// traceparent and a bare ctx → nonNilStrings nil path + firstNonEmpty "".
	err := e.ConceptAtomsBound(context.Background(), ConceptAtomsBoundInput{
		TenantID:        "11111111-1111-7111-8111-111111111111",
		LearnerGCID:     "22222222-2222-7222-8222-222222222222",
		ConceptID:       "33333333-3333-7333-8333-333333333333",
		AttachedAtomIDs: []string{"a1"},
		OccurredAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ConceptAtomsBound: %v", err)
	}
	if pub.topic != TopicConceptAtomsBound {
		t.Fatalf("topic = %q, want %q", pub.topic, TopicConceptAtomsBound)
	}
	// NonNilStrings must have normalised the nil slices to non-nil empty twice.
	if det, _ := pub.payload["detached_atom_ids"].([]string); det == nil || len(det) != 0 {
		t.Errorf("detached_atom_ids = %#v, want an empty non-nil slice", pub.payload["detached_atom_ids"])
	}
	if res, _ := pub.payload["resulting_atom_refs"].([]string); res == nil || len(res) != 0 {
		t.Errorf("resulting_atom_refs = %#v, want an empty non-nil slice", pub.payload["resulting_atom_refs"])
	}
	if pub.env.PublishedAt.IsZero() {
		t.Error("PublishedAt was not stamped by the default now()")
	}
}

// TestClusterProjectionBindingSink covers the binding sink adapter: the happy
// translation, the nil-emitter no-op, and the nil-receiver no-op.
func TestClusterProjectionBindingSink(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)

	// Happy path: forwards to the wrapped emitter.
	pub := &captureConceptPublisher{}
	sink := NewClusterProjectionBindingSink(NewConceptEmitter(pub, func() time.Time { return now }))
	if err := sink.ConceptAtomsBound(ctx, "concept-1", "11111111-1111-7111-8111-111111111111", "22222222-2222-7222-8222-222222222222", "cluster_projection", []string{"a1", "a2"}, now); err != nil {
		t.Fatalf("ConceptAtomsBound: %v", err)
	}
	if pub.topic != TopicConceptAtomsBound {
		t.Fatalf("topic = %q, want %q", pub.topic, TopicConceptAtomsBound)
	}
	if pub.payload["change_source"] != ChangeSourceClusterProjection {
		t.Errorf("change_source = %v, want cluster_projection", pub.payload["change_source"])
	}

	// Nil emitter → silent no-op (the adapter is optional in a composition root).
	nilEmitter := NewClusterProjectionBindingSink(nil)
	if err := nilEmitter.ConceptAtomsBound(ctx, "c", "t", "g", "p", nil, now); err != nil {
		t.Errorf("nil-emitter err = %v, want nil no-op", err)
	}

	// Nil receiver → silent no-op.
	var nilSink *ClusterProjectionBindingSink
	if err := nilSink.ConceptAtomsBound(ctx, "c", "t", "g", "p", nil, now); err != nil {
		t.Errorf("nil-receiver err = %v, want nil no-op", err)
	}
}

// TestGoalKnowledgeRequestPayload drives the shape + bounds of the
// synthesis_requested payload (its only definition, per the src doc).
func TestGoalKnowledgeRequestPayload(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	// Exceed both caps to prove the builder bounds the slices here.
	shaky := make([]GoalKnowledgeShakyConcept, 0, 30)
	for i := 0; i < 30; i++ {
		shaky = append(shaky, GoalKnowledgeShakyConcept{ConceptKey: "k", ConceptLabel: "l", Strength: 0.5})
	}
	mems := make([]GoalKnowledgeMemory, 0, 30)
	for i := 0; i < 30; i++ {
		mems = append(mems, GoalKnowledgeMemory{MemoryID: "m", Content: "c", CreatedAt: now})
	}
	p := GoalKnowledgeRequestPayload(GoalKnowledgeRequestInput{
		TenantID: "t", LearnerGCID: "g", CompanionID: "f", CompanionName: "Eira",
		GoalID: "goal-1", GoalTitle: "Fractions", RootConceptID: "rc",
		ConceptsTotal: 10, ConceptsMastered: 3,
		ShakyConcepts: shaky, Memories: mems,
		ContentHash: "abc", PromptVersion: "v2",
		RequestedAt: now, RequestSource: "", // empty → default
	})
	if got := p["shaky_concepts"].([]map[string]any); len(got) != MaxShakyConceptsInGoalKnowledgeRequest {
		t.Errorf("shaky_concepts len = %d, want %d (bounded)", len(got), MaxShakyConceptsInGoalKnowledgeRequest)
	}
	if got := p["memories"].([]map[string]any); len(got) != MaxMemoriesInGoalKnowledgeRequest {
		t.Errorf("memories len = %d, want %d (bounded)", len(got), MaxMemoriesInGoalKnowledgeRequest)
	}
	if got := p["request_source"]; got != GoalKnowledgeSourceLazyRegen {
		t.Errorf("request_source = %v, want default %q", got, GoalKnowledgeSourceLazyRegen)
	}
	// familiar_id / familiar_name are PRE-RENAME WIRE KEYS on purpose: the
	// kennel's reflection fold lane decodes this body by name (ADR-254 D6).
	for _, k := range []string{"tenant_id", "learner_gcid", "familiar_id", "familiar_name", "goal_id",
		"goal_title", "root_concept_id", "concepts_total", "concepts_mastered",
		"content_hash", "prompt_version", "requested_at"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload missing %q", k)
		}
	}
}

// TestNewCampaignEmitter_NilNowDefaults covers the default-now constructor and
// one publish through the envelope builder with a nil now.
func TestNewCampaignEmitter_NilNowDefaults(t *testing.T) {
	pub := &captureConceptPublisher{}
	e := NewCampaignEmitter(pub, nil)
	err := e.FocusAssigned(context.Background(), CampaignFocusAssignedInput{
		TenantID: "t", LearnerGCID: "g", GoalID: "goal-1", ConceptID: "c1",
		ConceptKey: "ck", PreviousConceptID: "c0", CompanionID: "fam-1",
		AssignedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("FocusAssigned: %v", err)
	}
	if pub.topic != TopicCampaignFocusAssigned {
		t.Fatalf("topic = %q, want %q", pub.topic, TopicCampaignFocusAssigned)
	}
	if pub.env.PublishedAt.IsZero() {
		t.Error("CampaignEmitter default now() did not stamp PublishedAt")
	}
}

// TestLoadoutPublisherBridge covers the domain→events envelope bridge.
func TestLoadoutPublisherBridge(t *testing.T) {
	ctx := context.Background()
	pub := &captureConceptPublisher{}
	b := NewLoadoutPublisherBridge(pub)
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	err := b.PublishLoadoutEvent(ctx, TopicCompanionLoadoutChanged, map[string]any{
		"companion_id": "fam-1", "slot": "head",
	}, companion.LoadoutEnvelope{
		EventID: "evt-1", IdempotencyKey: "idem-1", TenantID: "t", GCID: "g",
		OccurredAt: now, PublishedAt: now, Traceparent: "tp", Tracestate: "ts",
		SourceProject: SourceProject, SourceService: SourceService, SchemaVersion: 1,
	})
	if err != nil {
		t.Fatalf("PublishLoadoutEvent: %v", err)
	}
	if pub.topic != TopicCompanionLoadoutChanged {
		t.Errorf("topic = %q, want %q", pub.topic, TopicCompanionLoadoutChanged)
	}
	if pub.env.EventID != "evt-1" || pub.env.IdempotencyKey != "idem-1" ||
		pub.env.TenantID != "t" || pub.env.GCID != "g" ||
		pub.env.SourceProject != SourceProject || pub.env.SourceService != SourceService ||
		pub.env.SchemaVersion != 1 {
		t.Errorf("env not translated: %+v", pub.env)
	}
	if _, ok := pub.payload["companion_id"]; !ok {
		t.Errorf("payload not carried through: %+v", pub.payload)
	}
}

// TestSuggestPayloadHelper_SmallHelpersLocks is a direct unit test of the two
// tiny unexported helpers whose all-empty / nil tails are hard to reach through
// the public emitter (TraceparentFromContext always returns a non-empty value,
// and the emitter guarantees at least one non-empty slice).
func Test_EmitterSmallHelpers(t *testing.T) {
	if got := firstNonEmpty("", "  ", ""); got != "" {
		t.Errorf("firstNonEmpty(all-empty) = %q, want \"\"", got)
	}
	if got := firstNonEmpty("", "real"); got != "real" {
		t.Errorf("firstNonEmpty(second) = %q, want real", got)
	}
	if got := nonNilStrings(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNilStrings(nil) = %#v, want a non-nil empty slice", got)
	}
}

// TestSuggestionRequestPayload_NilWeaknessLists covers the two nil-list
// branches inside SuggestionRequestPayload (a diagnosed weakness with nil
// Misconceptions / Evidence normalises to [] rather than null).
func TestSuggestionRequestPayload_NilWeaknessLists(t *testing.T) {
	p := SuggestionRequestPayload(SuggestionRequestInput{
		TenantID: "t", LearnerGCID: "g",
		Weakness: &WeaknessPayload{Descriptor: "d"}, // both lists nil
	})
	w, ok := p["weakness"].(map[string]any)
	if !ok {
		t.Fatalf("weakness = %#v, want an object", p["weakness"])
	}
	if misc, _ := w["misconceptions"].([]string); misc == nil || len(misc) != 0 {
		t.Errorf("weakness.misconceptions = %#v, want empty non-nil slice", w["misconceptions"])
	}
	if evi, _ := w["evidence"].([]string); evi == nil || len(evi) != 0 {
		t.Errorf("weakness.evidence = %#v, want empty non-nil slice", w["evidence"])
	}
}
