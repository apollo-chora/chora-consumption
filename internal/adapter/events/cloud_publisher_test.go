package events_test

import (
	"context"
	"testing"
	"time"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

type stubRecorder struct {
	rows []*cgcoutbox.Row
}

func (s *stubRecorder) Record(_ context.Context, _ cgcoutbox.Tx, row *cgcoutbox.Row) error {
	s.rows = append(s.rows, row)
	return nil
}
func (s *stubRecorder) Claim(_ context.Context, _ int) ([]*cgcoutbox.Row, error) { return nil, nil }
func (s *stubRecorder) MarkPublished(_ context.Context, _ []string) error        { return nil }
func (s *stubRecorder) MarkFailed(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}

func TestCloudPublisher_Publish_WritesOutboxRow(t *testing.T) {
	rec := &stubRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{AggregateType: "atom_session"})

	now := time.Now().UTC()
	env := events.Envelope{
		EventID:        "01970000-0000-7000-8000-000000000001",
		IdempotencyKey: "01970000-0000-7000-8000-000000000001",
		TenantID:       "22222222-2222-7222-8222-222222222222",
		GCID:           "00000000-0000-7000-8000-000000001999",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	if err := p.Publish(events.TopicAtomSessionCompleted, env, map[string]any{
		"session_id":    "01970000-0000-7000-8000-00000000aaaa",
		"atom_id":       "00000000-0000-7000-8000-000000005e10",
		"source_action": "companion",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	row := rec.rows[0]
	if row.Topic != events.TopicAtomSessionCompleted {
		t.Errorf("topic: got %q want %q", row.Topic, events.TopicAtomSessionCompleted)
	}
	if row.AggregateType != "atom_session" {
		t.Errorf("aggregate_type: got %q want atom_session", row.AggregateType)
	}
	if row.Status != cgcoutbox.StatusPending {
		t.Errorf("status: got %q want pending", row.Status)
	}
	if err := cgcenvelope.Validate(row.Envelope); err != nil {
		t.Errorf("envelope validate: %v", err)
	}
}

func TestCloudPublisher_Publish_RejectsBadTopic(t *testing.T) {
	rec := &stubRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})

	now := time.Now().UTC()
	env := events.Envelope{
		EventID:        "01970000-0000-7000-8000-000000000001",
		IdempotencyKey: "01970000-0000-7000-8000-000000000001",
		TenantID:       "t",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
	if err := p.Publish("not.a.canonical.topic", env, nil); err == nil {
		t.Fatalf("expected topic validation error")
	}
}
