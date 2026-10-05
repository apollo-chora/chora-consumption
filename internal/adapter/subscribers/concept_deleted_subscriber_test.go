// concept_deleted_subscriber_test.go — CHO-2324. The concept.deleted.v1 event
// MUST cascade the incident-edge cleanup, scoped + idempotent; a missing concept
// id is a producer bug → NACK.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

type fakeEdgeCascade struct {
	calls      int
	gotTenant  string
	gotLearner string
	gotConcept string
	err        error
}

func (f *fakeEdgeCascade) SoftDeleteByConcept(_ context.Context, tenantID, learnerGCID, conceptID string, _ time.Time) (int64, error) {
	f.calls++
	f.gotTenant, f.gotLearner, f.gotConcept = tenantID, learnerGCID, conceptID
	return 2, f.err
}

func cdEnvelope() events.Envelope {
	return events.Envelope{
		EventID:        "01990000-0000-7000-8000-0000000000e1",
		IdempotencyKey: "k",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		GCID:           "22222222-2222-7222-8222-222222222222",
		OccurredAt:     time.Now().UTC(),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

// The delete event cascades the incident-edge cleanup; scope falls back to the
// envelope tenant+gcid when the payload omits them.
func TestConceptDeletedSubscriber_CascadesScoped(t *testing.T) {
	edges := &fakeEdgeCascade{}
	s := NewConceptDeletedSubscriber(edges)
	err := s.Handle(cdEnvelope(), ConceptDeletedPayload{
		ConceptID: "33333333-3333-7333-8333-333333333333",
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if edges.calls != 1 {
		t.Fatalf("SoftDeleteByConcept called %d times; want 1", edges.calls)
	}
	if edges.gotConcept != "33333333-3333-7333-8333-333333333333" {
		t.Errorf("concept=%q", edges.gotConcept)
	}
	if edges.gotTenant == "" || edges.gotLearner == "" {
		t.Errorf("cascade not scoped: tenant=%q learner=%q", edges.gotTenant, edges.gotLearner)
	}
}

// Idempotent: a redelivery (same event_id) does not re-run the cascade.
func TestConceptDeletedSubscriber_IdempotentRedelivery(t *testing.T) {
	edges := &fakeEdgeCascade{}
	s := NewConceptDeletedSubscriber(edges)
	env := cdEnvelope()
	p := ConceptDeletedPayload{ConceptID: "c1", TenantID: "t1", LearnerGCID: "g1"}
	if err := s.Handle(env, p); err != nil {
		t.Fatalf("Handle #1: %v", err)
	}
	if err := s.Handle(env, p); err != nil {
		t.Fatalf("Handle #2: %v", err)
	}
	if edges.calls != 1 {
		t.Fatalf("cascade ran %d times on redelivery; want 1", edges.calls)
	}
}

// Fail-loud: a missing concept_id is a producer bug → NACK, no cascade.
func TestConceptDeletedSubscriber_RejectsMissingConcept(t *testing.T) {
	edges := &fakeEdgeCascade{}
	s := NewConceptDeletedSubscriber(edges)
	err := s.Handle(cdEnvelope(), ConceptDeletedPayload{TenantID: "t1", LearnerGCID: "g1"})
	if err == nil {
		t.Fatal("want an error for a missing concept_id")
	}
	if edges.calls != 0 {
		t.Fatalf("cascade ran despite the error; want 0")
	}
}
