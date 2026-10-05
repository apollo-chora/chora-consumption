// workflow_topics.go - ADR-254 D4 caller-facing workflow lanes consumption
// publishes to and consumes from. JSON-wire, schemaless on the bus (topics.yaml
// `schema: null`), protos authored in chora-contracts as the struct source:
//
//	proto/events/consumption/companion_turn.proto        (CompanionTurnRequested / CompanionTurnCompleted)
//	proto/events/consumption/dose_recommendation.proto   (DoseRecommendationRequested / DoseRecommendationCompleted)
//
// Transport on both sides is PULL: the kennel pulls
// chora-ai-kernel-orchestrator.consumption-{companion-turn,dose-recommendation}-requested
// and consumption pulls the *-completed subscriptions below. Envelope fields
// ride in the Pub/Sub attributes AND the body; routing is on event_topic.
package events

const (
	// TopicCompanionTurnRequested carries every bus-dispatched Companion turn
	// (typed chat, skill invoke, ceremony edge scout, ritual step, dose
	// greeting). Payload keys = the CompanionTurnRequested proto field names
	// (the kennel passes the body VERBATIM to the chat binary: familiar_id /
	// familiar_config / ... are the PRE-RENAME wire names inside that lane,
	// ADR-254 D6 payload addendum; a later rename is a coordinated cut).
	TopicCompanionTurnRequested = "chora.consumption.companion_turn.requested.v1"
	// TopicCompanionTurnCompleted is the kennel's result per turn_id.
	TopicCompanionTurnCompleted = "chora.consumption.companion_turn.completed.v1"

	// TopicDoseRecommendationRequested asks the recommender for the AI picks
	// of one daily dose (dose_request_id is the business key).
	TopicDoseRecommendationRequested = "chora.consumption.dose_recommendation.requested.v1"
	// TopicDoseRecommendationCompleted is the kennel's result per dose_request_id.
	TopicDoseRecommendationCompleted = "chora.consumption.dose_recommendation.completed.v1"

	// Pull subscription ids consumption reads (provisioned by the coordinator's
	// companion_topic_estate.tf at G0; env-overridable at boot).
	SubscriptionCompanionTurnCompleted      = "chora-consumption.companion-turn-completed"
	SubscriptionDoseRecommendationCompleted = "chora-consumption.dose-recommendation-completed"
)
