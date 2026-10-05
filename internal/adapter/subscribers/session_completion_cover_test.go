// session_completion_cover_test.go — white-box coverage tests for the
// SessionCompletionHandler error + validation branches in
// session_completion.go that the happy-path tests in session_completion_test.go
// do not reach:
//
//   - input validation (tenant / learner / atom required)
//   - OccurredAt zero-value fallback
//   - ListByLearnerWithAtom error propagation
//   - LearningPath.Save error propagation
//   - publishAdvanced / publishCompleted error propagation
//   - terminal IMDA Publish error propagation
//
// Plus the CourseContentComposedSubscriber bad-envelope branch.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// failPublisher errors on the Nth Publish call (1-indexed). failOn=0 never
// errors. Records the topics it saw so tests can assert which publish failed.
type failPublisher struct {
	failOn int
	n      int
	topics []string
}

func (p *failPublisher) Publish(topic string, _ events.Envelope, _ map[string]any) error {
	p.n++
	p.topics = append(p.topics, topic)
	if p.failOn != 0 && p.n == p.failOn {
		return errInjected
	}
	return nil
}

// failingAtomPathRepo wraps the inmem LearningPathRepo to inject errors on
// ListByLearnerWithAtom and Save while keeping the rest functional.
type sessPathRepo struct {
	*inmem.LearningPathRepo
	listWithAtomErr error
	saveErr         error
}

func (r *sessPathRepo) ListByLearnerWithAtom(ctx context.Context, tenantID, gcid, atomID string) ([]*learning_path.LearningPath, error) {
	if r.listWithAtomErr != nil {
		return nil, r.listWithAtomErr
	}
	return r.LearningPathRepo.ListByLearnerWithAtom(ctx, tenantID, gcid, atomID)
}

func (r *sessPathRepo) Save(ctx context.Context, p *learning_path.LearningPath) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	return r.LearningPathRepo.Save(ctx, p)
}

func newSessHandler(t *testing.T, paths learning_path.Repo, pub events.Publisher) (*SessionCompletionHandler, *inmem.AtomIndexRepo) {
	t.Helper()
	atomIdx := inmem.NewAtomIndexRepo()
	retention := inmem.NewTopicRetentionRepo()
	return NewSessionCompletionHandler(paths, atomIdx, retention, pub), atomIdx
}

// ----------------- input validation -----------------

func TestSessionCompletion_InputValidation(t *testing.T) {
	cases := []struct {
		name string
		in   SessionCompletedInput
	}{
		{"missing_tenant", SessionCompletedInput{LearnerGCID: "g", AtomID: "a"}},
		{"missing_learner", SessionCompletedInput{TenantID: "t", AtomID: "a"}},
		{"missing_atom", SessionCompletedInput{TenantID: "t", LearnerGCID: "g"}},
		{"blank_tenant_whitespace", SessionCompletedInput{TenantID: "  ", LearnerGCID: "g", AtomID: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newSessHandler(t, inmem.NewLearningPathRepo(), events.NewInMemoryPublisher())
			if _, err := h.Handle(context.Background(), tc.in); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestSessionCompletion_OccurredAtZeroFallsBackToNow(t *testing.T) {
	pub := events.NewInMemoryPublisher()
	h, _ := newSessHandler(t, inmem.NewLearningPathRepo(), pub)
	// No path, no atom — but OccurredAt zero must not break; handler stamps
	// time.Now() and still emits the IMDA evidence event.
	res, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", SessionID: "s1",
		Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		// OccurredAt intentionally zero
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.IMDAEvidenceID == "" {
		t.Error("expected an IMDA evidence id to be emitted")
	}
	if len(pub.Events()) == 0 {
		t.Error("expected the terminal IMDA evidence event to be published")
	}
}

// ----------------- repo / publish error propagation -----------------

func TestSessionCompletion_ListByLearnerWithAtomError(t *testing.T) {
	paths := &sessPathRepo{LearningPathRepo: inmem.NewLearningPathRepo(), listWithAtomErr: errInjected}
	h, _ := newSessHandler(t, paths, events.NewInMemoryPublisher())
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want injected list error", err)
	}
}

func TestSessionCompletion_PathSaveError(t *testing.T) {
	base := inmem.NewLearningPathRepo()
	now := time.Now().UTC()
	path, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: "t1", LearnerGCID: "g1", CourseID: "c1",
		AtomIDs: []string{"a1", "a2"}, Now: now,
	})
	_ = base.Save(context.Background(), path)
	paths := &sessPathRepo{LearningPathRepo: base, saveErr: errInjected}
	h, _ := newSessHandler(t, paths, events.NewInMemoryPublisher())
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: now,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want injected save error", err)
	}
}

func TestSessionCompletion_PublishAdvancedError(t *testing.T) {
	base := inmem.NewLearningPathRepo()
	now := time.Now().UTC()
	// 2-atom path so advancing a1 does NOT complete (only advance event fires).
	path, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: "t1", LearnerGCID: "g1", CourseID: "c1",
		AtomIDs: []string{"a1", "a2"}, Now: now,
	})
	_ = base.Save(context.Background(), path)
	pub := &failPublisher{failOn: 1} // first publish (advanced) fails
	h, _ := newSessHandler(t, base, pub)
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: now,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want injected publishAdvanced error", err)
	}
	if len(pub.topics) == 0 || pub.topics[0] != events.TopicLearningPathAdvanced {
		t.Errorf("first publish topic = %v; want advanced", pub.topics)
	}
}

func TestSessionCompletion_PublishCompletedError(t *testing.T) {
	base := inmem.NewLearningPathRepo()
	now := time.Now().UTC()
	// Single-atom path: advancing a1 advances AND completes → two publishes;
	// fail the SECOND (completed).
	path, _ := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: "t1", LearnerGCID: "g1", CourseID: "c1",
		AtomIDs: []string{"a1"}, Now: now,
	})
	_ = base.Save(context.Background(), path)
	pub := &failPublisher{failOn: 2}
	h, _ := newSessHandler(t, base, pub)
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: true,
		SessionID: "s1", OccurredAt: now,
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want injected publishCompleted error", err)
	}
	if len(pub.topics) < 2 || pub.topics[1] != events.TopicLearningPathCompleted {
		t.Errorf("second publish topic = %v; want completed", pub.topics)
	}
}

func TestSessionCompletion_IMDAEvidencePublishError(t *testing.T) {
	// No path advances (IsCorrect=false) and no atom_index entry, so the ONLY
	// publish is the terminal IMDA evidence — fail it.
	pub := &failPublisher{failOn: 1}
	h, _ := newSessHandler(t, inmem.NewLearningPathRepo(), pub)
	_, err := h.Handle(context.Background(), SessionCompletedInput{
		TenantID: "t1", LearnerGCID: "g1", AtomID: "a1", IsCorrect: false,
		SessionID: "s1", OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, errInjected) {
		t.Errorf("err = %v; want injected IMDA publish error", err)
	}
	if len(pub.topics) != 1 || pub.topics[0] != events.TopicAtomSessionCompletedV1 {
		t.Errorf("publish topics = %v; want only the IMDA evidence topic", pub.topics)
	}
}

// ----------------- CourseContentComposed bad envelope -----------------

func TestCourseContentComposed_RejectsBlankEnvelope(t *testing.T) {
	sub := NewCourseContentComposedSubscriber(newFakeProjRepo(), nil)
	if err := sub.Handle(events.Envelope{}, CourseContentComposedPayload{
		CourseID: "c1",
	}); err == nil {
		t.Error("expected error on blank envelope")
	}
}
