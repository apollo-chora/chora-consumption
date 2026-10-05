// concept_emitter_test.go - ADR-244 D4 (CHO-2303). RED-first: the binding must
// become a domain event with an owner instead of a silent handler side effect.
package events

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConceptEmitter_AtomsBound_TopicEnvelopeAndDelta(t *testing.T) {
	pub := &captureConceptPublisher{}
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	e := NewConceptEmitter(pub, func() time.Time { return now })

	err := e.ConceptAtomsBound(context.Background(), ConceptAtomsBoundInput{
		TenantID:          "11111111-1111-7111-8111-111111111111",
		LearnerGCID:       "22222222-2222-7222-8222-222222222222",
		ConceptID:         "33333333-3333-7333-8333-333333333333",
		AttachedAtomIDs:   []string{"a1"},
		DetachedAtomIDs:   []string{"d1"},
		ResultingAtomRefs: []string{"a1", "keep"},
		ChangeSource:      ChangeSourceManualAttach,
		Provenance:        "learner_authored",
		OccurredAt:        now,
	})
	if err != nil {
		t.Fatalf("ConceptAtomsBound: %v", err)
	}
	if pub.topic != TopicConceptAtomsBound {
		t.Fatalf("topic = %q, want %q", pub.topic, TopicConceptAtomsBound)
	}
	if TopicConceptAtomsBound != "chora.consumption.concept.atoms_bound.v1" {
		t.Fatalf("topic const drifted from the proto contract: %q", TopicConceptAtomsBound)
	}
	// Envelope: the mandatory fields must be populated, not silently defaulted.
	for _, f := range []struct {
		name string
		val  string
	}{
		{"event_id", pub.env.EventID},
		{"idempotency_key", pub.env.IdempotencyKey},
		{"tenant_id", pub.env.TenantID},
		{"gcid", pub.env.GCID},
		{"traceparent", pub.env.Traceparent},
		{"source_service", pub.env.SourceService},
	} {
		if strings.TrimSpace(f.val) == "" {
			t.Errorf("envelope %s is empty", f.name)
		}
	}
	// Delta AND resulting state both ride the one event (D4).
	for _, k := range []string{"concept_id", "attached_atom_ids", "detached_atom_ids",
		"resulting_atom_refs", "change_source", "provenance", "occurred_at"} {
		if _, ok := pub.payload[k]; !ok {
			t.Errorf("payload missing %q", k)
		}
	}
}

// A no-op mutation must not emit: AttachAtom/DetachAtom are idempotent and only
// touch() on a real change, so an event always means a real transition.
func TestConceptEmitter_AtomsBound_NoOpDoesNotEmit(t *testing.T) {
	pub := &captureConceptPublisher{}
	e := NewConceptEmitter(pub, nil)
	err := e.ConceptAtomsBound(context.Background(), ConceptAtomsBoundInput{
		TenantID: "t", LearnerGCID: "g", ConceptID: "c",
		ResultingAtomRefs: []string{"a1"},
		ChangeSource:      ChangeSourceManualAttach,
		OccurredAt:        time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("ConceptAtomsBound: %v", err)
	}
	if pub.calls != 0 {
		t.Fatalf("a no-op change published %d event(s); want 0", pub.calls)
	}
}

// Fail-loud: an unscoped event is a producer bug, never a silent publish.
func TestConceptEmitter_AtomsBound_RejectsUnscoped(t *testing.T) {
	pub := &captureConceptPublisher{}
	e := NewConceptEmitter(pub, nil)
	err := e.ConceptAtomsBound(context.Background(), ConceptAtomsBoundInput{
		ConceptID: "c", AttachedAtomIDs: []string{"a1"}, OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("want an error for a missing tenant/gcid, got nil")
	}
	if pub.calls != 0 {
		t.Fatalf("published %d event(s) despite the error; want 0", pub.calls)
	}
}

// CHO-2324: a concept soft-delete becomes a domain event so the edge-cleanup
// subscriber can cascade (edges are a separate aggregate, cleaned via the event).
func TestConceptEmitter_Deleted_TopicEnvelopeAndPayload(t *testing.T) {
	pub := &captureConceptPublisher{}
	now := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	e := NewConceptEmitter(pub, func() time.Time { return now })

	err := e.ConceptDeleted(context.Background(), ConceptDeletedInput{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		LearnerGCID: "22222222-2222-7222-8222-222222222222",
		ConceptID:   "33333333-3333-7333-8333-333333333333",
		OccurredAt:  now,
	})
	if err != nil {
		t.Fatalf("ConceptDeleted: %v", err)
	}
	if pub.topic != TopicConceptDeleted {
		t.Fatalf("topic = %q, want %q", pub.topic, TopicConceptDeleted)
	}
	if TopicConceptDeleted != "chora.consumption.concept.deleted.v1" {
		t.Fatalf("topic const drifted from the proto contract: %q", TopicConceptDeleted)
	}
	for _, f := range []struct{ name, val string }{
		{"event_id", pub.env.EventID},
		{"idempotency_key", pub.env.IdempotencyKey},
		{"tenant_id", pub.env.TenantID},
		{"gcid", pub.env.GCID},
		{"traceparent", pub.env.Traceparent},
		{"source_service", pub.env.SourceService},
	} {
		if strings.TrimSpace(f.val) == "" {
			t.Errorf("envelope %s is empty", f.name)
		}
	}
	for _, k := range []string{"concept_id", "tenant_id", "learner_gcid", "occurred_at"} {
		if _, ok := pub.payload[k]; !ok {
			t.Errorf("payload missing %q", k)
		}
	}
}

// Fail-loud: an unscoped delete event is a producer bug, never a silent publish.
func TestConceptEmitter_Deleted_RejectsUnscoped(t *testing.T) {
	pub := &captureConceptPublisher{}
	e := NewConceptEmitter(pub, nil)
	err := e.ConceptDeleted(context.Background(), ConceptDeletedInput{
		ConceptID: "c", OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("want an error for a missing tenant/gcid, got nil")
	}
	if pub.calls != 0 {
		t.Fatalf("published %d event(s) despite the error; want 0", pub.calls)
	}
}

type captureConceptPublisher struct {
	calls   int
	topic   string
	env     Envelope
	payload map[string]any
}

func (c *captureConceptPublisher) Publish(topic string, env Envelope, payload map[string]any) error {
	c.calls++
	c.topic, c.env, c.payload = topic, env, payload
	return nil
}
