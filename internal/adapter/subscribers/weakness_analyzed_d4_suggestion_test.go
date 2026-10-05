// weakness_analyzed_d4_suggestion_test.go — ADR-238 D4: an UNMATCHED weakness is
// surfaced, never swallowed.
//
// The edge itself was already parked goal-level, but D4's other half (offer to
// ADD that concept to the map) was never built. The consequence is not cosmetic:
// an unmatched edge paints on NO node (the read-side join is id-then-slug, and a
// label too distant to clear the cosine floor will not slug-match either), and
// WS-4 retired the only browsable growth-edge list, so the weakness surfaced
// NOWHERE. Until this lands, "never dropped" is true in storage and false in the UI.
package subscribers

import (
	"context"
	"errors"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

type stubSuggestionWriter struct {
	batches [][]*conceptgraph.Suggestion
	err     error
	// probeErr, when set, is returned by GetBySourceEvent so a probe-read failure
	// can be exercised independently of the write path.
	probeErr error
}

func (s *stubSuggestionWriter) CreateBatch(_ context.Context, ss []*conceptgraph.Suggestion) error {
	if s.err != nil {
		return s.err
	}
	s.batches = append(s.batches, ss)
	return nil
}

// GetBySourceEvent models the concept_suggestions source-event probe (migration
// 0060): it returns a previously written live suggestion for the event, letting a
// redelivery detect an already-ingested batch. The shared writer stands in for the
// durable DB across pods; the per-pod in-memory inbox does not.
func (s *stubSuggestionWriter) GetBySourceEvent(_ context.Context, tenantID, learnerGCID, sourceEventID string) (*conceptgraph.Suggestion, error) {
	if s.probeErr != nil {
		return nil, s.probeErr
	}
	for _, b := range s.batches {
		for _, sug := range b {
			if sug == nil {
				continue
			}
			if sug.SourceEventID == sourceEventID && sug.TenantID == tenantID &&
				sug.LearnerGCID == learnerGCID && sug.DeletedAt == nil {
				return sug, nil
			}
		}
	}
	return nil, nil
}

func (s *stubSuggestionWriter) all() []*conceptgraph.Suggestion {
	out := []*conceptgraph.Suggestion{}
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func d4Sub(repo *lwFakeRepo, w *stubSuggestionWriter) (*WeaknessAnalyzedSubscriber, *stubGoalConceptResolver) {
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0}, // cosine 1.0 with candidate A → MATCHED
		"matches nothing":   {0, 0, 1}, // orthogonal to both → UNMATCHED
	}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates(), rootConceptID: "root-concept"}
	lookup := &stubUploadGoalLookup{goalID: "goal-xyz"}
	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(resolver, lookup).
		WithConceptMatchMinCosine(0.55).
		WithConceptSuggestions(w)
	return sub, resolver
}

func TestWeaknessAnalyzed_D4_UnmatchedEdgeSuggestsAddingTheConcept(t *testing.T) {
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{}
	sub, _ := d4Sub(repo, w)

	if err := sub.Handle(context.Background(), lwEnv("evt-d4"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := w.all()
	if len(got) != 1 {
		t.Fatalf("got %d suggestions; want exactly 1 (only the UNMATCHED edge)", len(got))
	}
	s := got[0]
	if s.Kind != conceptgraph.SuggestionKindConcept {
		t.Errorf("Kind = %q; want concept", s.Kind)
	}
	if s.Status != conceptgraph.SuggestionStatusPending {
		t.Errorf("Status = %q; want pending", s.Status)
	}
	if s.Title != "matches nothing" {
		t.Errorf("Title = %q; want the unmatched edge's label", s.Title)
	}
	if s.Rationale == "" {
		t.Error("Rationale is empty; ADR-215 wants a learner-facing why")
	}
}

func TestWeaknessAnalyzed_D4_SuggestionIsAnchoredAtTheGoalRoot(t *testing.T) {
	// THE TRAP. concept_suggestion_handler's goal fence keeps only rows whose
	// focal is inside the goal subtree, and a whole-map (empty focal) row is in
	// NO subtree, so it is dropped on a rooted goal map. The FE always sends a
	// goalId. An empty focal here would therefore be written and then filtered
	// out on every read — invisible, which is the exact bug D4 exists to close.
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{}
	sub, _ := d4Sub(repo, w)

	if err := sub.Handle(context.Background(), lwEnv("evt-d4-anchor"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := w.all()
	if len(got) != 1 {
		t.Fatalf("got %d suggestions; want 1", len(got))
	}
	if got[0].FocalConceptID != "root-concept" {
		t.Fatalf("FocalConceptID = %q; want the goal ROOT — a whole-map row is filtered out on every goal-scoped read", got[0].FocalConceptID)
	}
}

func TestWeaknessAnalyzed_D4_NoSuggestionWhenEveryEdgeMatched(t *testing.T) {
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{}
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0},
		"matches nothing":   {1, 0, 0}, // now ALSO matches A
	}}
	resolver := &stubGoalConceptResolver{candidates: twoCandidates(), rootConceptID: "root-concept"}
	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(resolver, &stubUploadGoalLookup{goalID: "goal-xyz"}).
		WithConceptMatchMinCosine(0.55).
		WithConceptSuggestions(w)

	if err := sub.Handle(context.Background(), lwEnv("evt-d4-none"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(w.all()); n != 0 {
		t.Fatalf("got %d suggestions; want 0 when every edge landed on a node", n)
	}
}

func TestWeaknessAnalyzed_D4_WriteFailureIsFailLoud(t *testing.T) {
	// D4 says silent discard is forbidden. Swallowing this write would leave the
	// learner with no surface AND no signal that anything went wrong, which is
	// the failure mode itself. The upserts are idempotent, so a redelivery is
	// safe; NACK loudly rather than quietly losing the surfacing.
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{err: errors.New("boom")}
	sub, _ := d4Sub(repo, w)

	err := sub.Handle(context.Background(), lwEnv("evt-d4-fail"), lwResolvePayload())
	if err == nil {
		t.Fatal("Handle returned nil; want a loud error when the D4 suggestion write fails")
	}
}

func TestWeaknessAnalyzed_D4_NoWriterConfiguredStillPersistsEdges(t *testing.T) {
	// The writer is optional wiring; without it the ingest must still work
	// (edges persist goal-level) rather than nil-panic.
	repo := &lwFakeRepo{}
	emb := &stubEmbedderByText{byText: map[string][]float32{
		"matches A concept": {1, 0, 0},
		"matches nothing":   {0, 0, 1},
	}}
	sub := NewWeaknessAnalyzedSubscriber(repo, emb).
		WithGoalConceptResolver(&stubGoalConceptResolver{candidates: twoCandidates(), rootConceptID: "root-concept"}, &stubUploadGoalLookup{goalID: "goal-xyz"}).
		WithConceptMatchMinCosine(0.55)

	if err := sub.Handle(context.Background(), lwEnv("evt-d4-nowriter"), lwResolvePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.upserts) != 2 {
		t.Fatalf("got %d upserts; want 2", len(repo.upserts))
	}
}

func TestWeaknessAnalyzed_D4_RedeliveryToAFreshPodDoesNotDuplicate(t *testing.T) {
	// The subscriber's inbox is per-pod in-memory (weakness_wiring builds it with
	// NewWeaknessAnalyzedSubscriber, a memory store), so a Pub/Sub redelivery to a
	// FRESH pod, or after the local key's TTL, re-runs the whole handler. Edge
	// upserts are merge-idempotent, but a concept-suggestion insert is not (fresh
	// UUIDv7, PLAIN source_event index per migration 0060), so without the
	// documented GetBySourceEvent probe every redelivery adds a duplicate
	// "add this concept?" card to the learner's Suggestions tab.
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{}
	env := lwEnv("evt-d4-redeliver")

	sub1, _ := d4Sub(repo, w)
	if err := sub1.Handle(context.Background(), env, lwResolvePayload()); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Fresh pod: a new subscriber (empty inbox) sharing the SAME suggestion store.
	sub2, _ := d4Sub(repo, w)
	if err := sub2.Handle(context.Background(), env, lwResolvePayload()); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if n := len(w.all()); n != 1 {
		t.Fatalf("got %d D4 suggestions across a redelivery; want exactly 1 (idempotent on source event)", n)
	}
}

func TestWeaknessAnalyzed_D4_ProbeReadFailureIsFailLoud(t *testing.T) {
	// The idempotency probe read is load-bearing: if it errors (DB down) and the
	// write proceeded anyway, a redelivery could silently duplicate. A probe error
	// must NACK so the analysis is retried cleanly, never written blind.
	repo := &lwFakeRepo{}
	w := &stubSuggestionWriter{probeErr: errors.New("probe boom")}
	sub, _ := d4Sub(repo, w)

	err := sub.Handle(context.Background(), lwEnv("evt-d4-probe-fail"), lwResolvePayload())
	if err == nil {
		t.Fatal("Handle returned nil; want a loud error when the D4 idempotency probe read fails")
	}
	if len(w.all()) != 0 {
		t.Fatalf("got %d suggestions; want 0 (never write when the probe could not confirm)", len(w.all()))
	}
}
