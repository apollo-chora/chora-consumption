// Package events provides a channel-based, in-memory event publisher for the
// Phyllis MVP. Real Pub/Sub wiring lands in M12 — see pub-sub-topology skill.
//
// Per Comic Ch6 P14 + Ch7 P16 the chora-consumption service must emit two
// new event types on the Companion / Daily Dose flow:
//
//	chora.consumption.atom_session.completed.v1
//	chora.consumption.daily_dose.served.v1
//
// Event envelopes follow chora-contracts/proto/events/envelope.proto with all
// mandatory fields populated. This adapter materialises the envelope as Go
// struct + JSON-encodable map (real Protobuf binary lands in M11.4 / M12).
//
// Hexagonal note: this is an ADAPTER — domain code never imports this
// package. The HTTP handler injects a Publisher interface; tests inject
// the in-memory implementation; production injects the Pub/Sub adapter.
package events

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain"
)

// Topic names emitted by chora-consumption.
//
// Phyllis-MVP topics (Companion / Daily Dose):
//
//	TopicAtomSessionCompleted   chora.consumption.atom_session.completed.v1
//	TopicDailyDoseServed        chora.consumption.daily_dose.served.v1
//
// EXT-scope topics (LearningPath + state-machine AtomSession + KG):
//
//	TopicLearningPathEnrollmentCreated  chora.consumption.learning_path.enrollment.created.v1
//	TopicLearningPathBootstrapped       chora.consumption.learning_path.bootstrapped.v1
//	TopicLearningPathAdvanced           chora.consumption.learning_path.advanced.v1
//	TopicLearningPathCompleted          chora.consumption.learning_path.completed.v1
//	TopicAtomSessionStarted             chora.consumption.atom_session.started.v1
//	TopicAtomSessionCompletedV1         chora.consumption.atom_session.completed.v1
//	TopicAtomSessionAbandoned           chora.consumption.atom_session.abandoned.v1
//
// TopicAtomSessionCompletedV1 aliases TopicAtomSessionCompleted — EXT and
// Companion paths converge on the same Pub/Sub topic name. Subscribers
// disambiguate via the payload field `source_action` ("companion" | "ext").
const (
	TopicAtomSessionCompleted          = "chora.consumption.atom_session.completed.v1"
	TopicAtomSessionCompletedV1        = "chora.consumption.atom_session.completed.v1"
	TopicDailyDoseServed               = "chora.consumption.daily_dose.served.v1"
	TopicLearningPathEnrollmentCreated = "chora.consumption.learning_path.enrollment.created.v1"
	TopicLearningPathBootstrapped      = "chora.consumption.learning_path.bootstrapped.v1"
	TopicLearningPathAdvanced          = "chora.consumption.learning_path.advanced.v1"
	TopicLearningPathCompleted         = "chora.consumption.learning_path.completed.v1"
	TopicAtomSessionStarted            = "chora.consumption.atom_session.started.v1"
	TopicAtomSessionAbandoned          = "chora.consumption.atom_session.abandoned.v1"

	// S5.2 — Per-User Knowledge Graph (ADR-143).
	TopicKGMapClusterCreated     = "chora.consumption.kg_map_cluster.created.v1"
	TopicKGMapClusterArchived    = "chora.consumption.kg_map_cluster.archived.v1"
	TopicKGMapClusterMerged      = "chora.consumption.kg_map_cluster.merged.v1"
	TopicKGExplorationCreated    = "chora.consumption.kg_exploration.created.v1"
	TopicKGExplorationFocalChng  = "chora.consumption.kg_exploration.focal_changed.v1"
	TopicKGExplorationArchived   = "chora.consumption.kg_exploration.archived.v1"
	TopicKGHexagonFogGenerated   = "chora.consumption.kg_hexagon_fog.generated.v1"
	TopicKGHexagonFogInvalidated = "chora.consumption.kg_hexagon_fog.invalidated.v1"
	TopicKGJunctionDetected      = "chora.consumption.kg_junction.detected.v1"
	TopicKGJunctionAccepted      = "chora.consumption.kg_junction.accepted.v1"
	TopicKGJunctionRejected      = "chora.consumption.kg_junction.rejected.v1"

	// M12.2.B Companion slice — multi-Companion (1:N) per ADR-116 amendment +
	// ADR-147 §7 + docs/architecture/multi-companion-per-user-2026-05-11.md
	// §2.8. Migrated from chora-companion's `chora.companion.*` topics; the
	// canonical home is now `chora.consumption.companion.*` since the
	// Companion service-side aggregate lives in chora-consumption per the
	// 2026-05-12 consolidation decision.
	//
	// Topic-name migration (chora-companion → chora-consumption):
	//
	//	chora.companion.created.v1                  → chora.consumption.companion.created.v1
	//	chora.companion.skill_slot_unlocked.v1      → chora.consumption.companion.skill_slot_unlocked.v1
	//	chora.companion.skill_granted.v1            → chora.consumption.companion.skill_granted.v1
	//	chora.companion.memory_eviction.v1          → chora.consumption.companion.memory_eviction.v1
	//	chora.companion.specialization_changed.v1   → chora.consumption.companion.specialization_changed.v1
	TopicCompanionCreated               = "chora.consumption.companion.created.v1"
	TopicCompanionSkillSlotUnlocked     = "chora.consumption.companion.skill_slot_unlocked.v1"
	TopicCompanionSkillGranted          = "chora.consumption.companion.skill_granted.v1"
	TopicCompanionMemoryEviction        = "chora.consumption.companion.memory_eviction.v1"
	TopicCompanionSpecializationChanged = "chora.consumption.companion.specialization_changed.v1"
	// TopicCompanionRetired — a learner retired (soft-released) a roster
	// companion, freeing a cap-3 slot (CHO-2033). Audit + future consumers;
	// pseudonymise-not-delete (the row persists with deleted_at set).
	TopicCompanionRetired = "chora.consumption.companion.retired.v1"

	// ADR-154 — Companion conversational chat surface. Emitted at the end of
	// every successful chat turn; failed turns flow as SSE `event: error`
	// frames and do NOT publish this event.
	TopicCompanionChatTurnCompleted = "chora.consumption.companion.chat_turn_completed.v1"

	// ADR-196 — Growth-Edge "grown" reward spine. Emitted once per real
	// active→grown transition on a recover path (the canonical Reward trigger of
	// the Curiosity-Reward loop). No live consumer until B3 (chora-sharing
	// XP/Coins + Companion reaction attach as independent subscribers).
	TopicWeaknessGrown = "chora.consumption.weakness.grown.v1"

	// §3 Goal graduation (CHO-1962, ADR-204 §9). Emitted by the goal-graduation
	// subscriber as Growth Edges grow: graduated.v1 when an active Goal's whole
	// concept_set is mastered (active→achieved), progress_updated.v1 when the
	// mastered share moves but stays below 100%. Loosely-coupled consumers
	// (sharing reward / Companion reaction / observability) attach independently;
	// the A+ FE celebrates by reading status='achieved' directly.
	TopicGoalGraduated       = "chora.consumption.goal.graduated.v1"
	TopicGoalProgressUpdated = "chora.consumption.goal.progress_updated.v1"

	// ADR-227 Companion Campaign verified stream (WS-C1, CHO-2080). Every event
	// carries goal_id (Verification addendum #6 — chora-identity routes XP via
	// ResolveForGoal). Emitters: campaign ladder/seal/focus services + the
	// WS-C4 reveal trigger. Consumers (identity XP, consumption reveal,
	// observability IMDA D1/D2) attach in later workstreams.
	TopicCampaignFocusAssigned = "chora.consumption.campaign.focus_assigned.v1"
	TopicCampaignRungCleared   = "chora.consumption.campaign.rung_cleared.v1"
	// ADR-244 D4: every atom_refs mutation, from every path (CHO-2303).
	TopicConceptAtomsBound = "chora.consumption.concept.atoms_bound.v1"
	// CHO-2324: a learner soft-deletes a concept; the durable trigger for the
	// edge-cleanup cascade (edges are a separate aggregate, cleaned via the event).
	TopicConceptDeleted       = "chora.consumption.concept.deleted.v1"
	TopicCampaignNodeWon      = "chora.consumption.campaign.node_won.v1"
	TopicCampaignNodeRevealed = "chora.consumption.campaign.node_revealed.v1"
	TopicCampaignGoalSealed   = "chora.consumption.campaign.goal_sealed.v1"

	// Inbound topics (consumed FROM other domains for KG fog-cache invalidation).
	// Schema Registry note: topic names are v1; schema versions are v2
	// (PROTOCOL_BUFFER). Topic semver and schema semver evolve independently.
	//
	// TopicAtomPublished + TopicAtomRevisionUpdated: EXISTS in chora-489812.
	// TopicUserRetentionShifted: NOT YET PROVISIONED — tracked under CHO-1452.
	TopicAtomPublished        = "chora.creation.atom.published.v1"
	TopicAtomRevisionUpdated  = "chora.creation.atom.updated.v1"
	TopicUserRetentionShifted = "chora.consumption.user_retention.shifted.v1"

	// WS1 preferences leg (ADR-200 / CHO-2049) — emitted by the dose_pref store
	// on PUT /v1/me/dose-preferences; consumed by the LearnerProfile projection.
	// NOT YET PROVISIONED in Pub/Sub — the topic/schema Terraform + binary
	// encoder/decoder (codegen) are the integration step; code emits the
	// transitional JSON shape via the outbox JSON fallback until then.
	TopicPreferencesUpdated = "chora.consumption.preferences.updated.v1"
)

// SourceProject + SourceService for envelope provenance.
const (
	SourceProject = "chora-content"
	SourceService = "chora-consumption"
)

// Envelope mirrors the mandatory fields of chora.common.v1.EventEnvelope.
//
// JSON tags are present so the in-memory adapter can also be used to feed
// log sinks; real Protobuf serialization lands in M11.4 / M12.
type Envelope struct {
	EventID        string    `json:"event_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	TenantID       string    `json:"tenant_id"`
	GCID           string    `json:"gcid"`
	OccurredAt     time.Time `json:"occurred_at"`
	PublishedAt    time.Time `json:"published_at"`
	Traceparent    string    `json:"traceparent"`
	Tracestate     string    `json:"tracestate"`
	SourceProject  string    `json:"source_project"`
	SourceService  string    `json:"source_service"`
	SchemaVersion  int32     `json:"schema_version"`
}

// Event is a published envelope + topic + arbitrary payload (kept loosely
// typed for the MVP — domain-specific structs land with Protobuf codegen).
type Event struct {
	Topic    string
	Envelope Envelope
	Payload  map[string]any
}

// Publisher is the port the HTTP handler depends on.
//
// Implementations: InMemoryPublisher (this file) for MVP + tests; future
// PubSubPublisher (M12) writes Protobuf binary to Cloud Pub/Sub.
type Publisher interface {
	Publish(topic string, env Envelope, payload map[string]any) error
}

// InMemoryPublisher buffers published events into an internal slice + a
// channel for fan-out to test observers. Thread-safe.
type InMemoryPublisher struct {
	mu     sync.Mutex
	events []Event
	ch     chan Event
}

// NewInMemoryPublisher constructs a publisher with a buffered fan-out channel.
// Buffer is sized large enough for a typical demo run (rare overflow ok).
func NewInMemoryPublisher() *InMemoryPublisher {
	return &InMemoryPublisher{
		events: make([]Event, 0, 64),
		ch:     make(chan Event, 256),
	}
}

// Publish records an event into the in-memory buffer. Validates that the
// envelope satisfies all mandatory fields per the project_chora_data_plane
// memory + ddd-enforcement.md contract.
func (p *InMemoryPublisher) Publish(topic string, env Envelope, payload map[string]any) error {
	if err := validateEnvelope(topic, env); err != nil {
		return err
	}
	ev := Event{Topic: topic, Envelope: env, Payload: payload}
	p.mu.Lock()
	p.events = append(p.events, ev)
	p.mu.Unlock()
	select {
	case p.ch <- ev:
	default:
		// Channel full — drop the fan-out copy but keep the buffered slice.
	}
	return nil
}

// Events returns a defensive copy of all events published so far. Tests use
// this to assert envelope shape and topic flow.
func (p *InMemoryPublisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Event, len(p.events))
	copy(out, p.events)
	return out
}

// Channel returns the fan-out channel for stream observers.
func (p *InMemoryPublisher) Channel() <-chan Event { return p.ch }

func validateEnvelope(topic string, env Envelope) error {
	if strings.TrimSpace(topic) == "" {
		return errors.New("events: topic required")
	}
	if env.EventID == "" {
		return errors.New("events: envelope.event_id required")
	}
	if env.IdempotencyKey == "" {
		return errors.New("events: envelope.idempotency_key required")
	}
	if env.TenantID == "" {
		return errors.New("events: envelope.tenant_id required")
	}
	// gcid MAY be empty for system-emitted events per envelope.proto field 4
	// docstring; not enforced here.
	if env.OccurredAt.IsZero() {
		return errors.New("events: envelope.occurred_at required")
	}
	if env.PublishedAt.IsZero() {
		return errors.New("events: envelope.published_at required")
	}
	if env.SourceProject == "" {
		return errors.New("events: envelope.source_project required")
	}
	if env.SourceService == "" {
		return errors.New("events: envelope.source_service required")
	}
	if env.SchemaVersion < 1 {
		return errors.New("events: envelope.schema_version >= 1 required")
	}
	// traceparent + tracestate ARE mandatory fields per CLAUDE.md §6:
	// "Trace context across Pub/Sub — W3C traceparent/tracestate in event
	// envelope mandatory". Tracestate may be empty (W3C optional) but
	// traceparent must be present.
	if env.Traceparent == "" {
		return errors.New("events: envelope.traceparent required (W3C trace context)")
	}
	return nil
}

// NewEnvelope mints an envelope with a fresh UUIDv7 event_id, computed
// idempotency_key, source provenance, and timestamps. Caller supplies
// tenant_id, gcid, and trace context.
//
// idempotencyKey is optional — if empty, defaults to event_id (per
// envelope.proto idempotency_key docstring "Often equals event_id").
func NewEnvelope(tenantID, gcid, traceparent, tracestate, idempotencyKey string) Envelope {
	now := time.Now().UTC()
	id := domain.NewUUIDv7()
	if idempotencyKey == "" {
		idempotencyKey = id
	}
	return Envelope{
		EventID:        id,
		IdempotencyKey: idempotencyKey,
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    traceparent,
		Tracestate:     tracestate,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
}
