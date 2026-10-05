// active_path_topics_subscriber_test.go — RED-phase tests for the
// `chora.consumption.learning_path.bootstrapped.v1` +
// `chora.consumption.learning_path.advanced.v1` → active_path_topics
// projection subscriber. Per S6.4 brief, this subscriber owns the WRITE
// path that A-Companion's daily-dose curiosity slot reads.
//
// Subscribers covered:
//
//	HandleBootstrap  — bootstraps the topic-set on path enrollment
//	                   (LearningPath.bootstrapped.v1). Topics derived
//	                   from the atom_index projection of every atom
//	                   in the path's atom_ids[].
//
//	HandleAdvance    — marks a topic as "explored" once the learner
//	                   passes its position. (S5.1: no-op once topics
//	                   are present; S6.4 will track the
//	                   `current_position` so explored topics can be
//	                   excluded from the curiosity pool — see AC §3
//	                   below).
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
)

// TestActivePathTopicsSubscriber_HandleBootstrap_PopulatesTopics — the
// learner enrolls in a course, the LearningPath.bootstrapped event
// fires, the subscriber loads the atom_index entries for every atom in
// the path, and unions their topic_tags into the projection.
func TestActivePathTopicsSubscriber_HandleBootstrap_PopulatesTopics(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	// Seed atoms with topics — atom-1 is "agile", atom-2 is "scrum".
	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	a2, _ := atomIndexHelperWithTopics("atom-2", "t1", "c1", []string{"scrum"})
	_ = atomIdx.Save(context.Background(), a1)
	_ = atomIdx.Save(context.Background(), a2)

	env := newTestEnvelope("evt-apt-bootstrap", "t1", "phyllis")
	payload := LearningPathBootstrappedPayload{
		PathID:      "path-1",
		CourseID:    "c1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		AtomIDs:     []string{"atom-1", "atom-2"},
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.HandleBootstrap(context.Background(), env, payload); err != nil {
		t.Fatalf("HandleBootstrap: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["agile"] {
		t.Errorf("expected 'agile' in topics: %v", got)
	}
	if !got["scrum"] {
		t.Errorf("expected 'scrum' in topics: %v", got)
	}
}

// TestActivePathTopicsSubscriber_HandleBootstrap_IdempotentOnReplay — a
// duplicated bootstrap event must not double-write or panic.
func TestActivePathTopicsSubscriber_HandleBootstrap_IdempotentOnReplay(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	env := newTestEnvelope("evt-apt-replay", "t1", "phyllis")
	payload := LearningPathBootstrappedPayload{
		PathID:      "path-1",
		CourseID:    "c1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		AtomIDs:     []string{"atom-1"},
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.HandleBootstrap(context.Background(), env, payload); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sub.HandleBootstrap(context.Background(), env, payload); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["agile"] {
		t.Errorf("expected agile after replay; got %v", got)
	}
}

// TestActivePathTopicsSubscriber_HandleBootstrap_RejectsBlankEnvelope —
// envelope mandatory-field validation contract.
func TestActivePathTopicsSubscriber_HandleBootstrap_RejectsBlankEnvelope(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)
	env := events.Envelope{}
	payload := LearningPathBootstrappedPayload{
		PathID:      "path-1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
	}
	if err := sub.HandleBootstrap(context.Background(), env, payload); err == nil {
		t.Error("expected error on blank envelope")
	}
}

// TestActivePathTopicsSubscriber_HandleBootstrap_TolerantOfMissingAtoms
// — out-of-order delivery: bootstrap arrives before atoms are
// projected. Subscriber must NOT error; the few topics it can resolve
// land in the projection, and the rest are discoverable later via
// HandleAdvance which re-queries the atom_index on each advance.
func TestActivePathTopicsSubscriber_HandleBootstrap_TolerantOfMissingAtoms(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	// Only seed atom-1; atom-2 hasn't projected yet.
	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	env := newTestEnvelope("evt-apt-partial", "t1", "phyllis")
	payload := LearningPathBootstrappedPayload{
		PathID:      "path-1",
		CourseID:    "c1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		AtomIDs:     []string{"atom-1", "atom-2"},
		OccurredAt:  time.Now().UTC(),
	}
	if err := sub.HandleBootstrap(context.Background(), env, payload); err != nil {
		t.Fatalf("partial: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["agile"] {
		t.Errorf("expected agile in topics: %v", got)
	}
	if got["scrum"] {
		t.Error("scrum should not be present (atom-2 missing)")
	}
}

// TestActivePathTopicsSubscriber_HandleAdvance_ResolvesNewTopics — when
// a learner advances past atom-2, and atom-2 has now projected with a
// new topic, subscriber unions the topic into the projection. This is
// how late-arriving atom_index updates eventually populate the topic
// set even if the bootstrap event was partial.
func TestActivePathTopicsSubscriber_HandleAdvance_ResolvesNewTopics(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	// Seed atom-2 only — bootstrap hadn't fired yet (or was partial).
	a2, _ := atomIndexHelperWithTopics("atom-2", "t1", "c1", []string{"kanban"})
	_ = atomIdx.Save(context.Background(), a2)

	env := newTestEnvelope("evt-apt-advance", "t1", "phyllis")
	payload := LearningPathAdvancedPayload{
		PathID:       "path-1",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		AtomID:       "atom-2",
		CurrentIndex: 1,
		TotalAtoms:   2,
		OccurredAt:   time.Now().UTC(),
	}
	if err := sub.HandleAdvance(context.Background(), env, payload); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["kanban"] {
		t.Errorf("expected kanban after advance; got %v", got)
	}
}

// TestActivePathTopicsSubscriber_HandleAdvance_IdempotentOnReplay — a
// duplicated advance event is a no-op (topic already in the union).
func TestActivePathTopicsSubscriber_HandleAdvance_IdempotentOnReplay(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	a, _ := atomIndexHelperWithTopics("atom-2", "t1", "c1", []string{"kanban"})
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-apt-advance-replay", "t1", "phyllis")
	payload := LearningPathAdvancedPayload{
		PathID:       "path-1",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		AtomID:       "atom-2",
		CurrentIndex: 1,
		OccurredAt:   time.Now().UTC(),
	}
	if err := sub.HandleAdvance(context.Background(), env, payload); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := sub.HandleAdvance(context.Background(), env, payload); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["kanban"] {
		t.Errorf("topics lost on replay: %v", got)
	}
}

// TestActivePathTopicsSubscriber_TenantIsolation — RLS leak prevention.
func TestActivePathTopicsSubscriber_TenantIsolation(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	a1, _ := atomIndexHelperWithTopics("atom-1", "tenant-a", "c1", []string{"agile"})
	a2, _ := atomIndexHelperWithTopics("atom-2", "tenant-b", "c1", []string{"kanban"})
	_ = atomIdx.Save(context.Background(), a1)
	_ = atomIdx.Save(context.Background(), a2)

	envA := newTestEnvelope("evt-apt-a", "tenant-a", "shared-gcid")
	if err := sub.HandleBootstrap(context.Background(), envA, LearningPathBootstrappedPayload{
		PathID:      "path-a",
		LearnerGCID: "shared-gcid",
		TenantID:    "tenant-a",
		AtomIDs:     []string{"atom-1"},
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	envB := newTestEnvelope("evt-apt-b", "tenant-b", "shared-gcid")
	if err := sub.HandleBootstrap(context.Background(), envB, LearningPathBootstrappedPayload{
		PathID:      "path-b",
		LearnerGCID: "shared-gcid",
		TenantID:    "tenant-b",
		AtomIDs:     []string{"atom-2"},
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	gotA := aptTopics(t, apt, "tenant-a", "shared-gcid")
	gotB := aptTopics(t, apt, "tenant-b", "shared-gcid")

	if !gotA["agile"] || gotA["kanban"] {
		t.Errorf("tenant-a topics leaked / wrong: %v", gotA)
	}
	if !gotB["kanban"] || gotB["agile"] {
		t.Errorf("tenant-b topics leaked / wrong: %v", gotB)
	}
}

// TestActivePathTopicsSubscriber_HandleAdvance_RejectsBlankEnvelope —
// envelope mandatory-field validation contract.
func TestActivePathTopicsSubscriber_HandleAdvance_RejectsBlankEnvelope(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)
	env := events.Envelope{}
	payload := LearningPathAdvancedPayload{
		PathID:      "path-1",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
	}
	if err := sub.HandleAdvance(context.Background(), env, payload); err == nil {
		t.Error("expected error on blank envelope")
	}
}
