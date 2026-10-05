// loadout_bridge.go — adapts any events.Publisher (in-memory dev/tests,
// outbox-backed production) to the domain companion.LoadoutOutbox port
// (CHO-2012 loadout lifecycle events: skill_granted / loadout_changed).
//
// Mirrors outbox/growth.go's GrowthOutbox translation: the domain owns its
// envelope contract; this bridge maps it onto the canonical events.Envelope
// with every mandatory field carried through.
package events

import (
	"context"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// TopicCompanionLoadoutChanged — NEW with CHO-2012 (ADR-218 D3): emitted on
// every equip/unequip so downstream projections (P1 growth UX, O+ audit)
// track the loadout without polling. The skill_granted topic constant
// already exists (TopicCompanionSkillGranted).
const TopicCompanionLoadoutChanged = "chora.consumption.companion.loadout_changed.v1"

// LoadoutPublisherBridge satisfies companion.LoadoutOutbox over a Publisher.
type LoadoutPublisherBridge struct {
	publisher Publisher
}

// NewLoadoutPublisherBridge constructs the bridge.
func NewLoadoutPublisherBridge(p Publisher) *LoadoutPublisherBridge {
	return &LoadoutPublisherBridge{publisher: p}
}

// PublishLoadoutEvent implements companion.LoadoutOutbox.
func (b *LoadoutPublisherBridge) PublishLoadoutEvent(_ context.Context, topic string, payload map[string]any, env companion.LoadoutEnvelope) error {
	return b.publisher.Publish(topic, Envelope{
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
var _ companion.LoadoutOutbox = (*LoadoutPublisherBridge)(nil)
