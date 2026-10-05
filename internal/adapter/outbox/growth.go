// growth.go — outbox adapter that satisfies the domain
// growth.OutboxPort. Bridges growth.GrowthEnvelope (domain-defined) to the
// canonical outbox.Publisher (which accepts events.Envelope).
//
// Per hexagonal SKILL: the domain owns the GrowthEnvelope contract; this
// adapter translates it into the existing events package's mandatory-field
// envelope and writes a row to outbox_events for the dispatcher to drain.
package outbox

import (
	"context"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// GrowthOutbox wraps an outbox.Publisher to satisfy growth.OutboxPort.
type GrowthOutbox struct {
	publisher *Publisher
}

// NewGrowthOutbox constructs the adapter. Caller is responsible for
// dispatching the underlying outbox.Publisher rows to Pub/Sub via the
// outbox.Dispatcher goroutine (wired in cmd/server).
func NewGrowthOutbox(p *Publisher) *GrowthOutbox {
	return &GrowthOutbox{publisher: p}
}

// PublishGrowthEvent translates the domain envelope to an events.Envelope
// and forwards to outbox.Publisher.Publish.
//
// Implements growth.OutboxPort.
func (g *GrowthOutbox) PublishGrowthEvent(_ context.Context, topic string, payload map[string]any, env growth.GrowthEnvelope) error {
	return g.publisher.Publish(topic, events.Envelope{
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
		SchemaVersion:  env.SchemaVersion,
	}, payload)
}

// Compile-time guarantee that *GrowthOutbox satisfies growth.OutboxPort.
var _ growth.OutboxPort = (*GrowthOutbox)(nil)
