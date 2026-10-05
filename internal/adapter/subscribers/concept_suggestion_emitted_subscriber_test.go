// concept_suggestion_emitted_subscriber_test.go — ADR-212 WS-4 inbound seam:
// the fog's proposed concepts/edges become pending Suggestions (idempotent,
// one-tx batch, fail-loud on malformed items).
package subscribers

import (
	"context"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

const (
	cseTenant = "01970000-0000-7000-8000-000000000001"
	cseGCID   = "01970000-0000-7000-9000-000000000001"
	cseSrc    = "01970000-0000-7000-c000-0000000000f2"
	cseTgt    = "01970000-0000-7000-c000-0000000000f3"
)

type cseStubRepo struct {
	batches       [][]*conceptgraph.Suggestion
	err           error
	bySourceEvent *conceptgraph.Suggestion // non-nil ⇒ event already ingested (cross-pod)
}

func (r *cseStubRepo) Create(context.Context, *conceptgraph.Suggestion) error { return nil }
func (r *cseStubRepo) CreateBatch(_ context.Context, ss []*conceptgraph.Suggestion) error {
	r.batches = append(r.batches, ss)
	return r.err
}
func (r *cseStubRepo) GetByID(context.Context, string, string, string) (*conceptgraph.Suggestion, error) {
	return nil, nil
}
func (r *cseStubRepo) GetBySourceEvent(context.Context, string, string, string) (*conceptgraph.Suggestion, error) {
	return r.bySourceEvent, nil
}
func (r *cseStubRepo) ListPending(context.Context, string, string) ([]*conceptgraph.Suggestion, error) {
	return nil, nil
}
func (r *cseStubRepo) ListPendingForFocal(context.Context, string, string, string) ([]*conceptgraph.Suggestion, error) {
	return nil, nil
}
func (r *cseStubRepo) Update(context.Context, *conceptgraph.Suggestion) error { return nil }

func csePayload() ConceptSuggestionEmittedPayload {
	return ConceptSuggestionEmittedPayload{
		TenantID: cseTenant, LearnerGCID: cseGCID, CompanionID: "fam-1", MapTheme: "agile",
		FocalConceptID: cseSrc, ModelUsed: "gemini-2.5-flash", RunID: "run-9",
		Concepts: []SuggestedConceptPayload{{Title: "Fibonacci sequence", Rationale: "relates to estimation"}},
		Edges:    []SuggestedEdgePayload{{SourceConceptID: cseSrc, TargetConceptID: cseTgt, EdgeClass: "hierarchy", Rationale: "parent"}},
	}
}

func TestConceptSuggestionEmitted_PersistsConceptAndEdge(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	if err := sub.Handle(context.Background(), lwEnv("evt-1"), csePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.batches) != 1 || len(repo.batches[0]) != 2 {
		t.Fatalf("want one batch of 2 suggestions; got %v", repo.batches)
	}
	got := repo.batches[0]
	var concept, edge *conceptgraph.Suggestion
	for _, s := range got {
		switch s.Kind {
		case conceptgraph.SuggestionKindConcept:
			concept = s
		case conceptgraph.SuggestionKindEdge:
			edge = s
		}
	}
	if concept == nil || edge == nil {
		t.Fatalf("want one concept + one edge suggestion; got %+v", got)
	}
	if concept.Title != "Fibonacci sequence" || concept.SourceEventID != "evt-1" || concept.ModelID != "gemini-2.5-flash" {
		t.Errorf("concept suggestion wrong: %+v", concept)
	}
	if concept.Status != conceptgraph.SuggestionStatusPending {
		t.Errorf("suggestion must be pending; got %q", concept.Status)
	}
	// The emitted event's focal concept must persist onto EACH ingested
	// suggestion so the learner's per-node inbox is focal-scoped (the bug fix).
	if concept.FocalConceptID != cseSrc {
		t.Errorf("concept focal_concept_id = %q; want %q (from the emitted event)", concept.FocalConceptID, cseSrc)
	}
	if edge.EdgeClass != conceptgraph.EdgeClassHierarchy || edge.SourceEventID != "evt-1" {
		t.Errorf("edge suggestion wrong: %+v", edge)
	}
	if edge.FocalConceptID != cseSrc {
		t.Errorf("edge focal_concept_id = %q; want %q (from the emitted event)", edge.FocalConceptID, cseSrc)
	}
}

func TestConceptSuggestionEmitted_WholeMapWhenNoFocal(t *testing.T) {
	// No focal on the emitted event ⇒ whole-map suggestions (empty focal), so
	// they surface on every node (back-compat with the pre-focal generate path).
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	p := csePayload()
	p.FocalConceptID = ""
	p.Edges = nil
	if err := sub.Handle(context.Background(), lwEnv("evt-nofocal"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.batches) != 1 || len(repo.batches[0]) != 1 {
		t.Fatalf("want one concept suggestion; got %v", repo.batches)
	}
	if got := repo.batches[0][0].FocalConceptID; got != "" {
		t.Errorf("focal_concept_id = %q; want empty (whole-map)", got)
	}
}

func TestConceptSuggestionEmitted_Idempotent(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	env := lwEnv("evt-dup")
	if err := sub.Handle(context.Background(), env, csePayload()); err != nil {
		t.Fatalf("Handle #1: %v", err)
	}
	if err := sub.Handle(context.Background(), env, csePayload()); err != nil {
		t.Fatalf("Handle #2: %v", err)
	}
	if len(repo.batches) != 1 {
		t.Fatalf("redelivery must be skipped; got %d batches", len(repo.batches))
	}
}

func TestConceptSuggestionEmitted_CrossPodIdempotentViaProbe(t *testing.T) {
	// A fresh subscriber (empty in-memory inbox — simulates a different pod) still
	// skips when the batch already landed (GetBySourceEvent finds it).
	repo := &cseStubRepo{bySourceEvent: &conceptgraph.Suggestion{SuggestionID: "already-there"}}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	if err := sub.Handle(context.Background(), lwEnv("evt-crosspod"), csePayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.batches) != 0 {
		t.Fatalf("already-ingested event must be skipped; got %v", repo.batches)
	}
}

func TestConceptSuggestionEmitted_EmptyBatchNoOp(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	p := csePayload()
	p.Concepts, p.Edges = nil, nil
	if err := sub.Handle(context.Background(), lwEnv("evt-empty"), p); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(repo.batches) != 0 {
		t.Fatalf("empty batch should not persist; got %v", repo.batches)
	}
}

func TestConceptSuggestionEmitted_MalformedConceptNACKs(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	p := csePayload()
	p.Concepts = []SuggestedConceptPayload{{Title: "   "}} // empty title → fail-loud
	if err := sub.Handle(context.Background(), lwEnv("evt-bad"), p); err == nil {
		t.Fatal("want error (NACK) for malformed concept")
	}
	if len(repo.batches) != 0 {
		t.Errorf("malformed batch must not persist: %v", repo.batches)
	}
}

func TestConceptSuggestionEmitted_MalformedEdgeClassNACKs(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	p := csePayload()
	p.Concepts = nil
	p.Edges = []SuggestedEdgePayload{{SourceConceptID: cseSrc, TargetConceptID: cseTgt, EdgeClass: "bogus"}}
	if err := sub.Handle(context.Background(), lwEnv("evt-bad2"), p); err == nil {
		t.Fatal("want error (NACK) for bad edge class")
	}
}

func TestConceptSuggestionEmitted_MissingTenantErrors(t *testing.T) {
	repo := &cseStubRepo{}
	sub := NewConceptSuggestionEmittedSubscriber(repo)
	p := csePayload()
	p.TenantID = ""
	if err := sub.Handle(context.Background(), lwEnv("evt-x"), p); err == nil {
		t.Fatal("want error for missing tenant")
	}
}
