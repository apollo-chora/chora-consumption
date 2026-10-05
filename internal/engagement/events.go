package engagement

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the engagement service.
// Mirrors the chora-core DomainEvent structure.
type DomainEvent struct {
	EventID       uuid.UUID              `json:"event_id"`
	EventType     string                 `json:"event_type"`
	Timestamp     time.Time              `json:"timestamp"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          *uuid.UUID             `json:"gcid,omitempty"`
	AggregateID   uuid.UUID              `json:"aggregate_id"`
	AggregateType string                 `json:"aggregate_type"`
	Payload       map[string]interface{} `json:"payload"`
	CorrelationID *uuid.UUID             `json:"correlation_id,omitempty"`
	CausationID   *uuid.UUID             `json:"causation_id,omitempty"`
}

// TopicEngagementEvents is the Cloud Pub/Sub topic for all engagement domain events.
const TopicEngagementEvents = "chora.engagement.events"

// Engagement domain event type constants.
const (
	EventAtomCompleted          = "engagement.atom.completed"
	EventStreakUpdated          = "engagement.streak.updated"
	EventStreakMilestoneReached = "engagement.streak.milestone_reached"
	EventXPAwarded              = "engagement.xp.awarded"
	EventLevelUp                = "engagement.level.up"
	EventGoalAccepted           = "engagement.goal.accepted"
	EventGoalCompleted          = "engagement.goal.completed"
	EventGoalExpired            = "engagement.goal.expired"
	EventFogNodeRevealed        = "fog_of_war.node_revealed"
	EventFogNodeConquered       = "fog_of_war.node_conquered"
	EventStarAccountLevelUp     = "engagement.star_account.level_up"
)

// Aggregate type constants.
const (
	AggregateStarAccount = "StarAccount"
)

// TopicChoraverseEvents is the Cloud Pub/Sub topic for Choraverse fog-of-war events.
const TopicChoraverseEvents = "chora.choraverse.events"

// NewDomainEvent creates a new DomainEvent with a generated UUIDv7 event ID
// and the current timestamp.
func NewDomainEvent(
	eventType string,
	tenantID uuid.UUID,
	gcid *uuid.UUID,
	aggregateID uuid.UUID,
	aggregateType string,
	payload map[string]interface{},
) DomainEvent {
	return DomainEvent{
		EventID:       uuid.Must(uuid.NewV7()),
		EventType:     eventType,
		Timestamp:     time.Now().UTC(),
		TenantID:      tenantID,
		GCID:          gcid,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       payload,
	}
}
