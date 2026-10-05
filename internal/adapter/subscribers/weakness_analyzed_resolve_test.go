// weakness_analyzed_resolve_test.go — ADR-238 M-D2: the goal-scoped resolve leg
// of the weakness.analyzed ingest. Extends the W3 harness (lwFakeRepo / newLWSub /
// lwEnv / lwPayload) with a stub GoalConceptResolver + UploadGoalLookup and the
// per-text embedder (stubEmbedderByText) to prove:
//
//	(a) a matched edge stamps TargetConceptID AND aligns ConceptKey to the node,
//	(b) an unmatched edge is left goal-level (target "", key unchanged),
//	(c) resolution errors are NON-FATAL — the edges still persist.
package subscribers

import (
	"context"
	"errors"
	"testing"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- resolve-leg stubs ---

type stubGoalConceptResolver struct {
	candidates    []ConceptCandidate
	rootConceptID string
	err           error
	gotGoalID     string
	// gotEntryConceptID records ADR-238 D2's soft hint. Recorded DELIBERATELY:
	// a stub that captures only what it already knew about cannot observe a
	// parameter being dropped on the way in, which is how a plumbing gap
	// survives green tests on both sides of a seam.
	gotEntryConceptID string
	calls             int
}

func (s *stubGoalConceptResolver) Resolve(_ context.Context, _, _, goalID, entryConceptID string) (GoalScope, error) {
	s.calls++
	s.gotGoalID = goalID
	s.gotEntryConceptID = entryConceptID
	if s.err != nil {
		return GoalScope{}, s.err
	}
	return GoalScope{RootConceptID: s.rootConceptID, Candidates: s.candidates}, nil
}

type stubUploadGoalLookup struct {
	goalID         string
	entryConceptID string
	err            error
	calls          int
}

func (s *stubUploadGoalLookup) ScopeForUpload(_ context.Context, _, _ string) (wu.Scope, error) {
	s.calls++
	return wu.Scope{GoalID: s.goalID, EntryConceptID: s.entryConceptID}, s.err
}

// two edges: one that will match candidate A on cosine, one that matches nothing.
func lwResolvePayload() WeaknessAnalyzedPayload {
	p := lwPayload()
	p.Edges = []ExtractedGrowthEdgePayload{
		{ConceptLabel: "matches A concept", ConceptKey: "edge-a", Strength: 0.8},
		{ConceptLabel: "matches nothing", ConceptKey: "edge-none", Strength: 0.6},
	}
	return p
}

// candidates whose embeddings are canned so the payload edges resolve
// deterministically: A=[1,0,0], B=[0,1,0].
func twoCandidates() []ConceptCandidate {
	return []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "kA", Embedding: []float32{1, 0, 0}},
		{ConceptID: "cB", ConceptKey: "kB", Embedding: []float32{0, 1, 0}},
	}
}

func TestWeaknessAnalyzed_Resolve_StampsMatchedAndLeavesUnmatched(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0}, // cosine 1.0 with candidate A
		"matches nothing":   {0, 0, 1}, // orthogonal to both A and B → below floor
	}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates()}
	lookup := &stubUploadGoalLookup{goalID: "goal-xyz"}

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(resolver, lookup).
		WithConceptMatchMinCosine(0.55)

	if err := sub.Handle(context.Background(), lwEnv("evt-res"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("got %d upserts; want 2", len(repo.upserts))
	}
	// (a) matched edge → TargetConceptID stamped AND ConceptKey aligned to the node.
	if repo.upserts[0].TargetConceptID != "cA" {
		t.Errorf("edge0 TargetConceptID = %q; want cA", repo.upserts[0].TargetConceptID)
	}
	if repo.upserts[0].ConceptKey != "kA" {
		t.Errorf("edge0 ConceptKey = %q; want kA (aligned to matched node)", repo.upserts[0].ConceptKey)
	}
	// (b) unmatched edge → left goal-level: no target, original key preserved.
	if repo.upserts[1].TargetConceptID != "" {
		t.Errorf("edge1 TargetConceptID = %q; want empty (unmatched)", repo.upserts[1].TargetConceptID)
	}
	if repo.upserts[1].ConceptKey != "edge-none" {
		t.Errorf("edge1 ConceptKey = %q; want edge-none (unchanged)", repo.upserts[1].ConceptKey)
	}
	// resolver built ONCE, with the looked-up goal id.
	if resolver.calls != 1 || resolver.gotGoalID != "goal-xyz" {
		t.Errorf("resolver calls=%d goalID=%q; want 1 / goal-xyz", resolver.calls, resolver.gotGoalID)
	}
	if lookup.calls != 1 {
		t.Errorf("upload→goal lookup calls=%d; want 1", lookup.calls)
	}
}

func TestWeaknessAnalyzed_Resolve_ExactKeyStampsTarget(t *testing.T) {
	repo := &lwFakeRepo{}
	// edge embedding is orthogonal to the candidate — only the exact concept_key
	// can carry the match, proving matchConcept sees edge.ConceptKey.
	emb := &stubEmbedderByText{def: []float32{0, 1, 0}}
	resolver := &stubGoalConceptResolver{candidates: []ConceptCandidate{
		{ConceptID: "cA", ConceptKey: "on-map-key", Embedding: []float32{1, 0, 0}},
	}}
	lookup := &stubUploadGoalLookup{goalID: "goal-1"}

	p := lwPayload()
	p.Edges = []ExtractedGrowthEdgePayload{{ConceptLabel: "On Map Key", ConceptKey: "on-map-key", Strength: 0.7}}

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).WithGoalConceptResolver(resolver, lookup).WithConceptMatchMinCosine(0.99)
	if err := sub.Handle(context.Background(), lwEnv("evt-exact"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 1 || repo.upserts[0].TargetConceptID != "cA" || repo.upserts[0].ConceptKey != "on-map-key" {
		t.Errorf("exact-key resolve = %+v; want target cA / key on-map-key", repo.upserts)
	}
}

func TestWeaknessAnalyzed_Resolve_ResolverErrorIsNonFatal(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	resolver := &stubGoalConceptResolver{err: errors.New("resolver boom")}
	lookup := &stubUploadGoalLookup{goalID: "goal-xyz"}

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).WithGoalConceptResolver(resolver, lookup).WithConceptMatchMinCosine(0.55)
	// (c) resolution error must NOT NACK — edges still persist.
	if err := sub.Handle(context.Background(), lwEnv("evt-rerr"), lwResolvePayload()); err != nil {
		t.Fatalf("resolver error must be non-fatal, got NACK: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("edges must still upsert on resolver error; got %d", len(repo.upserts))
	}
	for i, u := range repo.upserts {
		if u.TargetConceptID != "" {
			t.Errorf("upsert[%d] TargetConceptID = %q; want empty (resolve failed)", i, u.TargetConceptID)
		}
	}
}

func TestWeaknessAnalyzed_Resolve_LookupErrorIsNonFatal(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates()}
	lookup := &stubUploadGoalLookup{err: errors.New("lookup boom")}

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).WithGoalConceptResolver(resolver, lookup).WithConceptMatchMinCosine(0.55)
	if err := sub.Handle(context.Background(), lwEnv("evt-lerr"), lwResolvePayload()); err != nil {
		t.Fatalf("lookup error must be non-fatal, got NACK: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("edges must still upsert on lookup error; got %d", len(repo.upserts))
	}
	// candidate build must be skipped when the goal can't be resolved.
	if resolver.calls != 0 {
		t.Errorf("resolver must not run after a lookup error; calls=%d", resolver.calls)
	}
	for i, u := range repo.upserts {
		if u.TargetConceptID != "" {
			t.Errorf("upsert[%d] TargetConceptID = %q; want empty", i, u.TargetConceptID)
		}
	}
}

func TestWeaknessAnalyzed_Resolve_BlankGoalIDSkipsResolver(t *testing.T) {
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{def: []float32{1, 0, 0}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates()}
	lookup := &stubUploadGoalLookup{goalID: ""} // non-goal upload

	sub := NewWeaknessAnalyzedSubscriber(repo, emb).WithGoalConceptResolver(resolver, lookup).WithConceptMatchMinCosine(0.55)
	if err := sub.Handle(context.Background(), lwEnv("evt-nogoal"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resolver.calls != 0 {
		t.Errorf("no goal_id must skip candidate build; resolver calls=%d", resolver.calls)
	}
	for i, u := range repo.upserts {
		if u.TargetConceptID != "" {
			t.Errorf("upsert[%d] TargetConceptID = %q; want empty (no goal scope)", i, u.TargetConceptID)
		}
	}
}

func TestWeaknessAnalyzed_Resolve_NoResolverLeavesTargetEmpty(t *testing.T) {
	// Back-compat: with no resolver wired the ingest is unchanged — target "",
	// concept_key straight from the analyser.
	repo := &lwFakeRepo{}
	emb := &lwFakeEmbedder{vec: []float32{0.1, 0.2}}
	if err := NewWeaknessAnalyzedSubscriber(repo, emb).Handle(context.Background(), lwEnv("evt-nores"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("got %d upserts; want 2", len(repo.upserts))
	}
	for i, u := range repo.upserts {
		if u.TargetConceptID != "" {
			t.Errorf("upsert[%d] TargetConceptID = %q; want empty (no resolver)", i, u.TargetConceptID)
		}
	}
	if repo.upserts[0].ConceptKey != "edge-a" {
		t.Errorf("edge0 ConceptKey = %q; want edge-a (unchanged)", repo.upserts[0].ConceptKey)
	}
}

// Compile-time: the pg upload repo's ScopeForUpload satisfies UploadScopeLookup
// and the stub too (documents the port the subscriber holds).
var _ UploadScopeLookup = (*stubUploadGoalLookup)(nil)
var _ lw.Embedder = (*stubEmbedderByText)(nil)
