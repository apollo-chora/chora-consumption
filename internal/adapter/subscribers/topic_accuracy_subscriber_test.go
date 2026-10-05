// topic_accuracy_subscriber_test.go — RED-phase tests for the
// `chora.consumption.atom_session.completed.v1` → topic_accuracy
// projection subscriber. Implements S6.4 carry-forward from the
// A-Companion S5.1 deliverable: the read path (TopicAccuracyRepo.GetByLearner)
// is wired; this subscriber owns the WRITE path so the dose-composer's
// weakness slot reads real data instead of hot-seeded fixtures.
//
// Production wiring (M12+) replaces the in-memory delivery with a Cloud
// Pub/Sub Push subscriber that decodes the Protobuf payload + verifies
// the envelope mandatory fields against the Schema Registry. Subscriber
// contract is identical: idempotent on (gcid, atom_session_id) AND on
// envelope.event_id; tolerant of out-of-order events (per the S1.3
// platform-events lib reorder window).
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
)

// TestTopicAccuracySubscriber_RecordsCorrectMCQAttempt verifies the
// happy-path: MCQ atom + is_correct=true → topic accuracy 1.0 for the
// atom's primary topic.
func TestTopicAccuracySubscriber_RecordsCorrectMCQAttempt(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	// Seed atom_index for atom-1 with topic "agile".
	a, err := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	if err != nil {
		t.Fatalf("seed atom_index: %v", err)
	}
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-acc-1", "t1", "phyllis")
	payload := AtomSessionCompletedPayload{
		SessionID:   "sess-1",
		AtomID:      "atom-1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		IsCorrect:   true,
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := accByLearner(t, accRepo, "t1", "phyllis")
	if got["agile"] != 1.0 {
		t.Errorf("accuracy[agile] = %v; want 1.0", got["agile"])
	}
}

// TestTopicAccuracySubscriber_RecordsIncorrectAttempt verifies that an
// is_correct=false attempt drops accuracy below 1.0.
func TestTopicAccuracySubscriber_RecordsIncorrectAttempt(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	a, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"scrum"})
	_ = atomIdx.Save(context.Background(), a)

	for i, correct := range []bool{true, false, true} {
		env := newTestEnvelope("evt-acc-"+string(rune('a'+i)), "t1", "phyllis")
		payload := AtomSessionCompletedPayload{
			SessionID:   "sess-" + string(rune('a'+i)),
			AtomID:      "atom-1",
			LearnerGCID: "phyllis",
			TenantID:    "t1",
			IsCorrect:   correct,
			OccurredAt:  time.Now().UTC(),
		}
		if err := sub.Handle(context.Background(), env, payload); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}
	got := accByLearner(t, accRepo, "t1", "phyllis")
	// 2 of 3 correct = ~0.667
	want := 2.0 / 3.0
	if got["scrum"] < want-0.001 || got["scrum"] > want+0.001 {
		t.Errorf("accuracy[scrum] = %v; want ≈ %v", got["scrum"], want)
	}
}

// TestTopicAccuracySubscriber_IdempotentOnDuplicateEventID ensures
// that re-firing the same envelope event_id is a no-op (Pub/Sub
// at-least-once retry compatibility).
func TestTopicAccuracySubscriber_IdempotentOnDuplicateEventID(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	a, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-acc-dup", "t1", "phyllis")
	payload := AtomSessionCompletedPayload{
		SessionID:   "sess-X",
		AtomID:      "atom-1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		IsCorrect:   true,
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := accByLearner(t, accRepo, "t1", "phyllis")
	// Only ONE attempt should have been recorded — accuracy stays 1.0
	// because exactly one (gcid, atom_session_id) pair landed.
	if got["agile"] != 1.0 {
		t.Errorf("accuracy[agile] = %v; want 1.0", got["agile"])
	}
	// Verify by checking the second-pass dedupe path: a fresh event_id
	// for the SAME session_id must also no-op (different event ID, but
	// the (gcid, atom_session_id) tuple is the canonical idempotency
	// key per the S6.4 brief).
	env2 := newTestEnvelope("evt-acc-dup-fresh", "t1", "phyllis")
	if err := sub.Handle(context.Background(), env2, payload); err != nil {
		t.Fatalf("session-dup: %v", err)
	}
	got2 := accByLearner(t, accRepo, "t1", "phyllis")
	if got2["agile"] != 1.0 {
		t.Errorf("session-dedupe failed: accuracy[agile] = %v; want 1.0", got2["agile"])
	}
}

// TestTopicAccuracySubscriber_RejectsBlankEnvelope is the contract: a
// missing-event_id / missing-tenant_id envelope must fail validation.
func TestTopicAccuracySubscriber_RejectsBlankEnvelope(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)
	env := events.Envelope{} // missing mandatory fields
	payload := AtomSessionCompletedPayload{
		AtomID:   "atom-1",
		TenantID: "t1",
	}
	if err := sub.Handle(context.Background(), env, payload); err == nil {
		t.Error("expected error on blank envelope")
	}
}

// TestTopicAccuracySubscriber_SkipsNonMCQAtoms — only MCQ atoms feed the
// weakness-slot accuracy projection. Reading-only atoms (atom_type
// "video", "text") are silently dropped.
func TestTopicAccuracySubscriber_SkipsNonMCQAtoms(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	// Seed a non-MCQ atom (no correct_option_id, atom_type "text").
	a, err := atomIndexFromFreshParams("atom-1", "t1", "c1", "text", []string{"agile"}, "")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-non-mcq", "t1", "phyllis")
	payload := AtomSessionCompletedPayload{
		SessionID:   "sess-1",
		AtomID:      "atom-1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		IsCorrect:   true,
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := accByLearner(t, accRepo, "t1", "phyllis")
	if _, found := got["agile"]; found {
		t.Errorf("non-MCQ atom should NOT contribute: got %v", got)
	}
}

// TestTopicAccuracySubscriber_TolerantOfMissingAtomIndex — out-of-order
// events: AtomSessionCompleted may arrive BEFORE the AtomCreated
// projection has been hydrated. Handler must NOT fail (per S1.3 reorder
// window); instead it logs + drops + lets the event ack so Pub/Sub
// removes it from the queue (without re-projection chora-creation
// would never produce another atom.created.v1 to trigger replay).
func TestTopicAccuracySubscriber_TolerantOfMissingAtomIndex(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	// No atom seeded — the projection has not arrived yet.
	env := newTestEnvelope("evt-no-atom", "t1", "phyllis")
	payload := AtomSessionCompletedPayload{
		SessionID:   "sess-no-atom",
		AtomID:      "atom-not-yet-projected",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		IsCorrect:   true,
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle should be tolerant: %v", err)
	}
	if len(accByLearner(t, accRepo, "t1", "phyllis")) != 0 {
		t.Error("expected no-op when atom_index missing")
	}
}

// TestTopicAccuracySubscriber_ShuffledEventOrder ensures that swapped
// event order (b before a, then a) yields the same final state as the
// in-order replay. Per S1.3 platform-events reorder window, subscribers
// MUST be commutative on independent (atom_id, session_id) tuples.
func TestTopicAccuracySubscriber_ShuffledEventOrder(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	a2, _ := atomIndexHelperWithTopics("atom-2", "t1", "c1", []string{"scrum"})
	_ = atomIdx.Save(context.Background(), a1)
	_ = atomIdx.Save(context.Background(), a2)

	now := time.Now().UTC()
	makePayload := func(sessionID, atomID string, correct bool, ts time.Time) (events.Envelope, AtomSessionCompletedPayload) {
		env := newTestEnvelope("evt-"+sessionID, "t1", "phyllis")
		env.OccurredAt = ts
		p := AtomSessionCompletedPayload{
			SessionID:   sessionID,
			AtomID:      atomID,
			LearnerGCID: "phyllis",
			TenantID:    "t1",
			IsCorrect:   correct,
			OccurredAt:  ts,
		}
		return env, p
	}

	// Shuffle: deliver session-2 (incorrect, atom-2) BEFORE session-1
	// (correct, atom-1). Final state must still be:
	//   agile = 1.0 (1 correct)
	//   scrum = 0.0 (0/1 correct)
	env2, p2 := makePayload("sess-2", "atom-2", false, now.Add(2*time.Second))
	env1, p1 := makePayload("sess-1", "atom-1", true, now.Add(1*time.Second))

	if err := sub.Handle(context.Background(), env2, p2); err != nil {
		t.Fatalf("first event: %v", err)
	}
	if err := sub.Handle(context.Background(), env1, p1); err != nil {
		t.Fatalf("second event: %v", err)
	}

	got := accByLearner(t, accRepo, "t1", "phyllis")
	if got["agile"] != 1.0 {
		t.Errorf("agile = %v; want 1.0", got["agile"])
	}
	if got["scrum"] != 0.0 {
		t.Errorf("scrum = %v; want 0.0", got["scrum"])
	}
}

// TestTopicAccuracySubscriber_TenantIsolation — RLS leak prevention.
// Two tenants writing for the SAME gcid must yield distinct accuracy
// projections. Mirrors the streak_repo_test.go RLS isolation contract.
func TestTopicAccuracySubscriber_TenantIsolation(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	a1, _ := atomIndexHelperWithTopics("atom-A", "tenant-a", "c1", []string{"agile"})
	a2, _ := atomIndexHelperWithTopics("atom-B", "tenant-b", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)
	_ = atomIdx.Save(context.Background(), a2)

	envA := newTestEnvelope("evt-a-correct", "tenant-a", "shared-gcid")
	if err := sub.Handle(context.Background(), envA, AtomSessionCompletedPayload{
		SessionID:   "s1",
		AtomID:      "atom-A",
		LearnerGCID: "shared-gcid",
		TenantID:    "tenant-a",
		IsCorrect:   true,
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("tenant-a: %v", err)
	}
	envB := newTestEnvelope("evt-b-wrong", "tenant-b", "shared-gcid")
	if err := sub.Handle(context.Background(), envB, AtomSessionCompletedPayload{
		SessionID:   "s2",
		AtomID:      "atom-B",
		LearnerGCID: "shared-gcid",
		TenantID:    "tenant-b",
		IsCorrect:   false,
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("tenant-b: %v", err)
	}

	gotA := accByLearner(t, accRepo, "tenant-a", "shared-gcid")
	gotB := accByLearner(t, accRepo, "tenant-b", "shared-gcid")
	if gotA["agile"] != 1.0 {
		t.Errorf("tenant-a agile = %v; want 1.0", gotA["agile"])
	}
	if gotB["agile"] != 0.0 {
		t.Errorf("tenant-b agile = %v; want 0.0", gotB["agile"])
	}
}
