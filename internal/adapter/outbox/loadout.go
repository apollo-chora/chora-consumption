// loadout.go — outbox adapter satisfying the domain companion.LoadoutOutbox
// port (CHO-2012 loadout lifecycle events: skill_granted / loadout_changed).
// Mirrors growth.go's GrowthOutbox: domain envelope → canonical
// events.Envelope → durable outbox_events row for the dispatcher.
package outbox

import (
	"context"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// LoadoutOutbox wraps an outbox.Publisher to satisfy companion.LoadoutOutbox.
type LoadoutOutbox struct {
	publisher *Publisher
}

// NewLoadoutOutbox constructs the adapter. The caller drains the underlying
// Publisher via the outbox.Dispatcher goroutine (wired in cmd/server).
func NewLoadoutOutbox(p *Publisher) *LoadoutOutbox {
	return &LoadoutOutbox{publisher: p}
}

// PublishLoadoutEvent implements companion.LoadoutOutbox.
func (o *LoadoutOutbox) PublishLoadoutEvent(_ context.Context, topic string, payload map[string]any, env companion.LoadoutEnvelope) error {
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
var _ companion.LoadoutOutbox = (*LoadoutOutbox)(nil)
