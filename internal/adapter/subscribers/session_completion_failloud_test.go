// session_completion_failloud_test.go — CHO-1949 fail-loud retention contract.
//
// Before the port refactor the handler swallowed retention errors:
//
//	if err != nil || score == nil { score = New(...) }
//
// which conflated a REAL pg/RLS failure with a genuine not-found and silently
// created a fresh score over a transient error. These tests pin the corrected
// contract against an injectable topic_retention.Repository double:
//
//   - a real Get error PROPAGATES (no write attempted);
//   - a (nil, nil) Get (not found) seeds + Saves a fresh score (TopicScored);
//   - a Save error PROPAGATES.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// fakeRetentionRepo is a topic_retention.Repository test double with injectable
// Get/Save errors that records the score handed to Save.
type fakeRetentionRepo struct {
	getErr    error
	saveErr   error
	getScore  *topic_retention.TopicScore
	saved     *topic_retention.TopicScore
	saveCalls int
}

func (f *fakeRetentionRepo) Save(_ context.Context, s *topic_retention.TopicScore) error {
	f.saveCalls++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = s
	return nil
}

func (f *fakeRetentionRepo) Get(_ context.Context, _, _, _ string) (*topic_retention.TopicScore, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getScore, nil // (nil, nil) when getScore is nil = not found
}

func (f *fakeRetentionRepo) ListByLearner(_ context.Context, _, _ string, _ int) ([]*topic_retention.TopicScore, error) {
	return nil, nil
}

func TestSessionCompletion_RetentionGetErrorPropagates(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	seedAtomCreatedWithTopics(t, atomIdx, "a1", "t1", "c1", []string{"agile"})
	fake := &fakeRetentionRepo{getErr: errInjected}
	h := NewSessionCompletionHandler(inmem.NewLearningPathRepo(), atomIdx, fake, events.NewInMemoryPublisher())

	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want the retention Get error propagated (fail-loud)", err)
	}
	if fake.saveCalls != 0 {
		t.Errorf("Save called %d times; want 0 (Get failed → no write)", fake.saveCalls)
	}
}

func TestSessionCompletion_RetentionNotFoundCreatesAndSaves(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	seedAtomCreatedWithTopics(t, atomIdx, "a1", "t1", "c1", []string{"agile"})
	fake := &fakeRetentionRepo{} // getScore nil → (nil, nil) not found
	h := NewSessionCompletionHandler(inmem.NewLearningPathRepo(), atomIdx, fake, events.NewInMemoryPublisher())

	res, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.TopicScored {
		t.Error("TopicScored = false; want true (fresh score created on not-found)")
	}
	if fake.saveCalls != 1 || fake.saved == nil {
		t.Fatalf("Save calls = %d, saved = %v; want exactly 1 save of a fresh score", fake.saveCalls, fake.saved)
	}
	if fake.saved.TopicID != "agile" {
		t.Errorf("saved.TopicID = %q; want agile", fake.saved.TopicID)
	}
	if fake.saved.ReviewCount != 1 {
		t.Errorf("saved.ReviewCount = %d; want 1 (one Review applied)", fake.saved.ReviewCount)
	}
}

func TestSessionCompletion_RetentionSaveErrorPropagates(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	seedAtomCreatedWithTopics(t, atomIdx, "a1", "t1", "c1", []string{"agile"})
	fake := &fakeRetentionRepo{saveErr: errInjected}
	h := NewSessionCompletionHandler(inmem.NewLearningPathRepo(), atomIdx, fake, events.NewInMemoryPublisher())

	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want the retention Save error propagated (fail-loud)", err)
	}
}
