package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

func TestGrowthOutbox_PublishesViaPublisher(t *testing.T) {
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{
		Store:         store,
		AggregateType: "companion",
	})
	g := outbox.NewGrowthOutbox(pub)

	env := growth.GrowthEnvelope{
		EventID:        "evt-1",
		IdempotencyKey: "k1",
		TenantID:       "t1",
		GCID:           "g1",
		OccurredAt:     time.Now(),
		PublishedAt:    time.Now(),
		Traceparent:    "00-aaa-bbb-00",
		SourceProject:  "chora-content",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	err := g.PublishGrowthEvent(context.Background(),
		growth.TopicCompanionExpAwarded,
		map[string]any{
			"companion_id": "fam-1",
			"owner_gcid":   "user-1",
			"exp_delta":    3,
		}, env)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("pending = %d, want 1", len(rows))
	}
	if rows[0].Topic != growth.TopicCompanionExpAwarded {
		t.Errorf("topic = %q", rows[0].Topic)
	}
	if rows[0].AggregateType != "companion" {
		t.Errorf("aggregate_type = %q, want companion", rows[0].AggregateType)
	}
	if rows[0].TenantID != "t1" {
		t.Errorf("tenant_id = %q", rows[0].TenantID)
	}
}

func TestGrowthOutbox_AllSevenTopicsAccepted(t *testing.T) {
	store := outbox.NewInMemoryStore()
	pub := outbox.NewPublisher(outbox.PublisherConfig{Store: store, AggregateType: "companion"})
	g := outbox.NewGrowthOutbox(pub)
	topics := []string{
		growth.TopicCompanionEggPurchased,
		growth.TopicCompanionBreedRevealed,
		growth.TopicCompanionHatched,
		growth.TopicCompanionExpAwarded,
		growth.TopicCompanionStageUp,
		"chora.consumption.companion.kg_neighbor_revealed.v1",
		growth.TopicCompanionSourceRevelation,
	}
	for i, tp := range topics {
		env := growth.GrowthEnvelope{
			EventID:        "evt-" + tp,
			IdempotencyKey: "k-" + tp + "-" + string(rune('A'+i)),
			TenantID:       "t1", GCID: "g1",
			OccurredAt: time.Now(), PublishedAt: time.Now(),
			Traceparent: "00-a-b-00", SourceProject: "chora-content",
			SourceService: "chora-consumption", SchemaVersion: 1,
		}
		err := g.PublishGrowthEvent(context.Background(), tp,
			map[string]any{"companion_id": "fam-1"}, env)
		if err != nil {
			t.Errorf("publish %q: %v", tp, err)
		}
	}
}
