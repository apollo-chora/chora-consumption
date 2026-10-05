// wire_topic_retention_test.go — CHO-1949 durability-wiring contract.
//
// WireTopicRetentionPg must (a) swap the Retention field to the supplied
// topic_retention.Repository and (b) rebuild SessionCompletion so its
// retention writes go THROUGH the swapped repo (mirrors WireLearningPathPg /
// WireAtomIndexPg). Proves the boot-time pg swap actually reaches the
// completion side-effect that persists the Ebbinghaus score.
package http

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// spyRetentionRepo is a topic_retention.Repository double recording writes.
type spyRetentionRepo struct {
	saved []*topic_retention.TopicScore
}

func (s *spyRetentionRepo) Save(_ context.Context, sc *topic_retention.TopicScore) error {
	s.saved = append(s.saved, sc)
	return nil
}
func (s *spyRetentionRepo) Get(_ context.Context, _, _, _ string) (*topic_retention.TopicScore, error) {
	return nil, nil // always not-found → completion seeds a fresh score
}
func (s *spyRetentionRepo) ListByLearner(_ context.Context, _, _ string, _ int) ([]*topic_retention.TopicScore, error) {
	return s.saved, nil
}

func TestWireTopicRetentionPg_SwapsRepoAndRebuildsSessionCompletion(t *testing.T) {
	ext := NewExtServer(nil)
	spy := &spyRetentionRepo{}
	ext.WireTopicRetentionPg(spy)

	if ext.Retention != topic_retention.Repository(spy) {
		t.Fatal("Retention field was not swapped to the supplied repo")
	}

	// Seed a topic-tagged MCQ atom so SessionCompletion updates retention.
	atomID := "01970000-0000-7000-a000-000000000099"
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:          atomID,
		TenantID:        meTenantID,
		AtomType:        "mcq",
		TopicTags:       []string{"agile"},
		CorrectOptionID: "opt-a",
		AnswerCount:     4,
		PublishedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("atom_index.New: %v", err)
	}
	if err := ext.AtomIndex.Save(context.Background(), a); err != nil {
		t.Fatalf("AtomIndex.Save: %v", err)
	}

	res, err := ext.SessionCompletion.Handle(context.Background(), subscribers.SessionCompletedInput{
		TenantID: meTenantID, LearnerGCID: meGCID, AtomID: atomID, IsCorrect: true,
		SessionID: "s1", Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SessionCompletion.Handle: %v", err)
	}
	if !res.TopicScored {
		t.Error("TopicScored = false; want true")
	}
	if len(spy.saved) != 1 {
		t.Fatalf("spy.saved = %d; want 1 write through the swapped repo", len(spy.saved))
	}
	if spy.saved[0].TopicID != "agile" {
		t.Errorf("saved topic = %q; want agile", spy.saved[0].TopicID)
	}
}

func TestWireTopicRetentionPg_NilRepoIsNoOp(t *testing.T) {
	ext := NewExtServer(nil)
	before := ext.Retention
	ext.WireTopicRetentionPg(nil)
	if ext.Retention != before {
		t.Error("WireTopicRetentionPg(nil) must be a no-op (retain in-memory default)")
	}
}
