// session_completion_test.go — RED phase tests for the in-process
// SessionCompletionHandler that fans out an atom_session.completed.v1
// event into LearningPath.Advance + TopicRetention.Review side effects.
//
// In MVP this is co-located in chora-consumption (same DB). M12+ will
// promote this into a proper Pub/Sub subscriber (still inside
// chora-consumption — same domain, no cross-DB).
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

func TestSessionCompletionHandler_AdvancesLearningPath(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	pathRepo := inmem.NewLearningPathRepo()
	retention := inmem.NewTopicRetentionRepo()
	publisher := events.NewInMemoryPublisher()

	// Seed atom_index — path will hold these in sorted order.
	for _, id := range []string{"a1", "a2"} {
		seedAtomCreated(t, atomIdx, id, "t1", "c1")
	}
	now := time.Now().UTC()
	path, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		CourseID:    "c1",
		AtomIDs:     []string{"a1", "a2"},
		Now:         now,
	})
	pathRepo.Save(context.Background(), path)

	h := NewSessionCompletionHandler(pathRepo, atomIdx, retention, publisher)
	got, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "a1",
		IsCorrect:   true,
		SessionID:   "sess-1",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  now,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PathsAdvanced != 1 {
		t.Errorf("PathsAdvanced = %d; want 1", got.PathsAdvanced)
	}
	updated, _ := pathRepo.Get(context.Background(), path.PathID)
	if updated.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d; want 1", updated.CurrentIndex)
	}
}

func TestSessionCompletionHandler_PublishesAdvancedEvent(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	pathRepo := inmem.NewLearningPathRepo()
	retention := inmem.NewTopicRetentionRepo()
	publisher := events.NewInMemoryPublisher()

	seedAtomCreated(t, atomIdx, "a1", "t1", "c1")
	now := time.Now().UTC()
	path, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		CourseID:    "c1",
		AtomIDs:     []string{"a1"},
		Now:         now,
	})
	pathRepo.Save(context.Background(), path)

	h := NewSessionCompletionHandler(pathRepo, atomIdx, retention, publisher)
	if _, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "a1",
		IsCorrect:   true,
		SessionID:   "sess-1",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	advancedSeen := false
	completedSeen := false
	for _, ev := range publisher.Events() {
		if ev.Topic == events.TopicLearningPathAdvanced {
			advancedSeen = true
		}
		if ev.Topic == events.TopicLearningPathCompleted {
			completedSeen = true
		}
	}
	if !advancedSeen {
		t.Error("expected learning_path.advanced.v1 event")
	}
	if !completedSeen {
		t.Error("expected learning_path.completed.v1 (single-atom path)")
	}
}

func TestSessionCompletionHandler_UpdatesTopicRetention(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	pathRepo := inmem.NewLearningPathRepo()
	retention := inmem.NewTopicRetentionRepo()
	publisher := events.NewInMemoryPublisher()

	seedAtomCreatedWithTopics(t, atomIdx, "a1", "t1", "c1", []string{"agile"})

	now := time.Now().UTC()
	h := NewSessionCompletionHandler(pathRepo, atomIdx, retention, publisher)
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "a1",
		IsCorrect:   true,
		SessionID:   "sess-1",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should have created a TopicScore for ("t1", "phyllis", "agile").
	score, err := retention.Get(context.Background(), "t1", "phyllis", "agile")
	if err != nil {
		t.Fatalf("retention.Get: %v", err)
	}
	if score == nil {
		t.Fatal("retention.Get: score is nil; want a created score")
	}
	if score.ReviewCount != 1 {
		t.Errorf("ReviewCount = %d; want 1", score.ReviewCount)
	}
}

func TestSessionCompletionHandler_NoOpWhenAtomMissing(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	pathRepo := inmem.NewLearningPathRepo()
	retention := inmem.NewTopicRetentionRepo()
	publisher := events.NewInMemoryPublisher()

	h := NewSessionCompletionHandler(pathRepo, atomIdx, retention, publisher)
	got, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "missing",
		IsCorrect:   true,
		SessionID:   "sess-1",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.PathsAdvanced != 0 {
		t.Errorf("PathsAdvanced = %d; want 0 (no path)", got.PathsAdvanced)
	}
	if got.TopicScored {
		t.Errorf("TopicScored = true; want false (no atom_index entry)")
	}
}

func TestSessionCompletionHandler_RetentionShrinksOnIncorrect(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	pathRepo := inmem.NewLearningPathRepo()
	retention := inmem.NewTopicRetentionRepo()
	publisher := events.NewInMemoryPublisher()

	seedAtomCreatedWithTopics(t, atomIdx, "a1", "t1", "c1", []string{"agile"})
	h := NewSessionCompletionHandler(pathRepo, atomIdx, retention, publisher)
	now := time.Now().UTC()
	if _, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "a1",
		IsCorrect:   true,
		SessionID:   "sess-1",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  now,
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := retention.Get(context.Background(), "t1", "phyllis", "agile")
	startStrength := first.Strength

	// Now an incorrect — strength should shrink.
	if _, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID:    "t1",
		LearnerGCID: "phyllis",
		AtomID:      "a1",
		IsCorrect:   false,
		SessionID:   "sess-2",
		Traceparent: "00-aaaa-bbbb-00",
		OccurredAt:  now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := retention.Get(context.Background(), "t1", "phyllis", "agile")
	if after.Strength >= startStrength {
		t.Errorf("Strength after incorrect = %v; want < %v", after.Strength, startStrength)
	}
}

func seedAtomCreatedWithTopics(t *testing.T, repo *inmem.AtomIndexRepo, atomID, tenantID, courseID string, topics []string) {
	t.Helper()
	a, err := atomIndexHelperWithTopics(atomID, tenantID, courseID, topics)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = repo.Save(context.Background(), a)
}
