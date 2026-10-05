// ritual.go — outbox adapter satisfying the domain companion.RitualOutbox port
// (CHO-2016 Rituals v1 lifecycle events: ritual_published / ritual_run_completed).
// Mirrors loadout.go: domain envelope → canonical events.Envelope → durable
// outbox_events row for the dispatcher (atomic state-write + event-publish).
package outbox

import (
	"context"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// RitualOutbox wraps an outbox.Publisher to satisfy companion.RitualOutbox.
type RitualOutbox struct {
	publisher *Publisher
}

// NewRitualOutbox constructs the adapter. The caller drains the underlying
// Publisher via the outbox.Dispatcher goroutine (wired in cmd/server).
func NewRitualOutbox(p *Publisher) *RitualOutbox {
	return &RitualOutbox{publisher: p}
}

// PublishRitualEvent implements companion.RitualOutbox. The payload is the
// JSON-transitional shape (map[string]any) until the ritual.proto codegen pass;
// the topic + envelope are identical to the loadout family.
func (o *RitualOutbox) PublishRitualEvent(_ context.Context, topic string, payload map[string]any, env companion.LoadoutEnvelope) error {
	return o.publisher.Publish(topic, events.Envelope{
		EventID:        env.EventID,
		IdempotencyKey: env.IdempotencyKey,
		TenantID:       env.TenantID,
		GCID:           env.GCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    env.PublishedAt,
		Traceparent:    env.Traceparent,
		Tracestate:     env.Tracestate,
		SourceProject:  env.SourceProject,
		SourceService:  env.SourceService,
		SchemaVersion:  int32(env.SchemaVersion),
	}, payload)
}

// Compile-time port guarantee.
var _ companion.RitualOutbox = (*RitualOutbox)(nil)
