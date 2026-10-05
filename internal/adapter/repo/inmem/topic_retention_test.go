// topic_retention_test.go — tests for the in-memory topic_retention repo
// (topic_retention.Repository port). Keyed by (tenant_id, gcid, topic_id);
// ctx is accepted but ignored by the in-memory adapter.
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

func TestTopicRetentionRepo_SaveAndGet(t *testing.T) {
	r := NewTopicRetentionRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s, err := topic_retention.New("t1", "g1", "agile", now, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "t1", "g1", "agile")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.TopicID != "agile" {
		t.Errorf("got = %v", got)
	}
}

// Per the port contract a missing score is (nil, nil) — NOT an error — so the
// caller seeds a fresh score on nil rather than swallowing a sentinel.
func TestTopicRetentionRepo_GetMissingReturnsNilNil(t *testing.T) {
	r := NewTopicRetentionRepo()
	got, err := r.Get(context.Background(), "t1", "g1", "missing")
	if err != nil {
		t.Errorf("err = %v; want nil (not-found is not an error)", err)
	}
	if got != nil {
		t.Errorf("got = %v; want nil on not-found", got)
	}
}

func TestTopicRetentionRepo_GetSoftDeletedReturnsNilNil(t *testing.T) {
	r := NewTopicRetentionRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s, _ := topic_retention.New("t1", "g1", "agile", now, 1.0)
	s.SoftDelete(now)
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "t1", "g1", "agile")
	if err != nil || got != nil {
		t.Errorf("got = %v, err = %v; want (nil, nil) for soft-deleted", got, err)
	}
}

func TestTopicRetentionRepo_ListByLearner(t *testing.T) {
	r := NewTopicRetentionRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	mk := func(tenant, gcid, topic string) *topic_retention.TopicScore {
		s, _ := topic_retention.New(tenant, gcid, topic, now, 1.0)
		return s
	}
	for _, s := range []*topic_retention.TopicScore{
		mk("t1", "g1", "agile"),
		mk("t1", "g1", "scrum"),
		mk("t1", "g2", "agile"),
		mk("t2", "g1", "agile"),
	} {
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := r.ListByLearner(ctx, "t1", "g1", 100)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d; want 2 (t1+g1)", len(got))
	}
}

func TestTopicRetentionRepo_ListByLearner_RespectsLimit(t *testing.T) {
	r := NewTopicRetentionRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	for _, topic := range []string{"agile", "scrum", "kanban", "estimation"} {
		s, _ := topic_retention.New("t1", "g1", topic, now, 1.0)
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := r.ListByLearner(ctx, "t1", "g1", 2)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d; want 2 (limit)", len(got))
	}
}

func TestTopicRetentionRepo_ListByLearner_ExcludesSoftDeleted(t *testing.T) {
	r := NewTopicRetentionRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	s, _ := topic_retention.New("t1", "g1", "agile", now, 1.0)
	s.SoftDelete(now)
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.ListByLearner(ctx, "t1", "g1", 100)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d; want 0", len(got))
	}
}
