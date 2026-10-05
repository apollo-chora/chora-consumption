// Package outbox — Publisher implementation.
//
// Publisher satisfies `events.Publisher` (the Phyllis MVP port at
// services/chora-consumption/internal/adapter/events/publisher.go) by
// writing each event row to outbox_events (via the Store port) instead of
// keeping it in process. The Dispatcher (see dispatcher.go) drains pending
// rows to Cloud Pub/Sub on a separate goroutine. This decouples emission
// from Pub/Sub availability — a crash between state-write and publish no
// longer loses the event.
//
// The wire shape of the payload matches the existing in-memory publisher
// (JSON serialisation of the payload map) so subscribers see identical bytes
// whether they receive from the legacy direct-publish path or the new
// outbox path. Migration is a constructor swap in main().
//
// Per `feedback_d6_resilience_first_class` B.6.2.a producer-side durable
// emission for the consumption domain's chora.consumption.* stream.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// timeRFC3339Nano is the canonical wire format for envelope timestamps;
// matches the chora-guardrail / chora-closure-orchestrator producers.
const timeRFC3339Nano = time.RFC3339Nano

// contextOrBackground returns context.Background() — the events.Publisher
// port intentionally does NOT carry a context (Phyllis MVP shape). The
// outbox row write is short-lived; production-grade context propagation
// will flow through a future ports.Publisher.PublishWithCtx variant.
func contextOrBackground() context.Context { return context.Background() }

// PublisherConfig wires the Publisher.
type PublisherConfig struct {
	// Store is the outbox table backend. Required.
	Store Store

	// AggregateType labels every outbox row this publisher emits. Defaults
	// to "atom_session" (the most common consumption aggregate). Callers
	// MAY override per-publisher-instance for learning_path / kg_map_cluster
	// / companion / daily_dose flows.
	AggregateType string
}

// Publisher writes events to outbox_events via Store. Satisfies
// events.Publisher.
type Publisher struct {
	cfg PublisherConfig
}

// NewPublisher constructs a Publisher.
func NewPublisher(cfg PublisherConfig) *Publisher {
	if cfg.AggregateType == "" {
		cfg.AggregateType = "atom_session"
	}
	return &Publisher{cfg: cfg}
}

// Publish satisfies events.Publisher.
//
// Validates topic + envelope (both via the existing events package helpers +
// chora-consumption's canonical-topic invariant), marshals payload to JSON,
// and writes a pending outbox row.
//
// On idempotency_key collision returns ErrDuplicateIdempotencyKey — callers
// MAY treat this as success because the prior emission is already durably
// queued.
func (p *Publisher) Publish(topic string, env events.Envelope, payload map[string]any) error {
	if p.cfg.Store == nil {
		return errors.New("outbox: store not wired")
	}
	row, err := BuildRow(p.cfg.AggregateType, topic, env, payload)
	if err != nil {
		return err
	}
	return p.cfg.Store.Insert(contextOrBackground(), row)
}

// BuildRow validates + encodes one event into the canonical outbox row shape —
// the SINGLE producer-side wire contract, shared by Publisher (post-commit
// Store.Insert) and the same-tx enqueue path (pg.TxOutbox, CHO-2129): rows
// written either way are byte-identical to the dispatcher.
func BuildRow(aggregateType, topic string, env events.Envelope, payload map[string]any) (Row, error) {
	if err := validateCanonicalTopic(topic); err != nil {
		return Row{}, err
	}
	if err := validateEnvelopeFields(topic, env); err != nil {
		return Row{}, err
	}

	// Producer-side encoding: emit canonical binary protobuf for topics whose
	// registered schema is BINARY-encoded. JSON-marshalled payloads fail
	// validation at publish time with "Invalid binary proto message" and
	// dead-letter forever. Per the gap surfaced 2026-05-15 on
	// chora.consumption.daily_dose.served.v1 + chora.consumption.companion.
	// chat_turn_completed.v1.
	body, err := encodeOutboxPayload(topic, env, payload)
	if err != nil {
		return Row{}, fmt.Errorf("outbox: marshal payload: %w", err)
	}

	envelopeMap := map[string]string{
		"event_id":        env.EventID,
		"idempotency_key": env.IdempotencyKey,
		"tenant_id":       env.TenantID,
		"gcid":            env.GCID,
		"occurred_at":     env.OccurredAt.UTC().Format(timeRFC3339Nano),
		"published_at":    env.PublishedAt.UTC().Format(timeRFC3339Nano),
		"traceparent":     env.Traceparent,
		"tracestate":      env.Tracestate,
		"source_project":  env.SourceProject,
		"source_service":  env.SourceService,
		"schema_version":  strconv.Itoa(int(env.SchemaVersion)),
	}

	aggregateID := env.GCID
	if aggregateID == "" {
		aggregateID = env.EventID
	}
	// Prefer a domain-aggregate ID from the payload when available so
	// audit + replay queries on aggregate_id give useful results.
	if payload != nil {
		// assist_id first: the cross-domain ai_assist request rows key their
		// audit/replay queries on the batch ref (CHO-2040). No consumption-
		// domain payload carries assist_id, so ordering is side-effect-free.
		for _, k := range []string{"assist_id", "session_id", "path_enrollment_id", "map_cluster_id", "exploration_id", "companion_id", "growth_edge_id", "goal_id"} {
			if v, ok := payload[k].(string); ok && v != "" {
				aggregateID = v
				break
			}
		}
	}

	return Row{
		ID:             env.EventID,
		TenantID:       env.TenantID,
		GCID:           env.GCID,
		AggregateType:  aggregateType,
		AggregateID:    aggregateID,
		EventType:      deriveEventType(topic),
		Topic:          topic,
		Payload:        body,
		Envelope:       envelopeMap,
		IdempotencyKey: env.IdempotencyKey,
		OccurredAt:     env.OccurredAt.UTC(),
	}, nil
}

// crossDomainRequestTopics is the EXPLICIT, CLOSED allowlist of non-
// chora.consumption.* topics this publisher may emit. Exactly ONE entry:
// the qgen crew's generation-request lane — terraform m10-data-plane
// documents chora.creation.ai_assist.started.v2 as "the only CROSS-SERVICE
// topic". The proofing-test composed runner (CHO-2040, R8-6) publishes its
// batch request onto it; the LIVE orchestrator subscription consumes it and
// chora-creation's terminal subscriber ACKs the unknown assist_id.
//
// Extending this map is a DOMAIN-BOUNDARY decision (events are the only
// inter-domain mechanism; a request topic is the narrow exception) — add a
// topic ONLY with an owner ruling, mirroring how the RLS-bypass allowlist is
// ADR-gated.
var crossDomainRequestTopics = map[string]bool{
	"chora.creation.ai_assist.started.v2": true,
}

// validateCanonicalTopic enforces chora.consumption.{aggregate}.{event_type}.v{N}
// shape. Legacy topics (chora.gamification.events, chora.consumption.events)
// are rejected — they must migrate to the canonical format before they can
// flow through this publisher. The crossDomainRequestTopics allowlist admits
// the single cross-service request lane (structural checks still apply).
func validateCanonicalTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("outbox: topic required")
	}
	if !strings.HasPrefix(t, CanonicalDomainPrefix) && !crossDomainRequestTopics[t] {
		return fmt.Errorf("outbox: topic %q must start with %q (chora-consumption domain invariant)",
			t, CanonicalDomainPrefix)
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return fmt.Errorf("outbox: topic %q must follow chora.consumption.{aggregate}.{event_type}.v{N}", t)
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return fmt.Errorf("outbox: topic %q must end with v{N} version suffix", t)
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("outbox: topic %q version suffix must be numeric", t)
		}
	}
	return nil
}

// validateEnvelopeFields enforces the CLAUDE.md §6 mandatory event-envelope
// shape. Mirrors `events.validateEnvelope` so the contract stays in lockstep
// with the legacy in-memory publisher.
func validateEnvelopeFields(topic string, env events.Envelope) error {
	if strings.TrimSpace(topic) == "" {
		return errors.New("outbox: topic required")
	}
	if env.EventID == "" {
		return errors.New("outbox: envelope.event_id required")
	}
	if env.IdempotencyKey == "" {
		return errors.New("outbox: envelope.idempotency_key required")
	}
	if env.TenantID == "" {
		return errors.New("outbox: envelope.tenant_id required")
	}
	if env.OccurredAt.IsZero() {
		return errors.New("outbox: envelope.occurred_at required")
	}
	if env.PublishedAt.IsZero() {
		return errors.New("outbox: envelope.published_at required")
	}
	if env.SourceProject == "" {
		return errors.New("outbox: envelope.source_project required")
	}
	if env.SourceService == "" {
		return errors.New("outbox: envelope.source_service required")
	}
	if env.SchemaVersion < 1 {
		return errors.New("outbox: envelope.schema_version >= 1 required")
	}
	if env.Traceparent == "" {
		return errors.New("outbox: envelope.traceparent required (W3C trace context)")
	}
	return nil
}

// deriveEventType extracts the {domain}.{aggregate}.{event_type} segment from
// the canonical topic for storage in the event_type column. Domain-aware
// (NOT a hardcoded "consumption." prefix) so the allowlisted cross-domain
// request topic labels honestly as creation.ai_assist.started.
func deriveEventType(topic string) string {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) < 3 {
		return topic
	}
	// strip trailing .vN suffix
	tail := parts[2]
	if idx := strings.LastIndex(tail, "."); idx > 0 {
		if v := tail[idx+1:]; len(v) >= 2 && v[0] == 'v' {
			tail = tail[:idx]
		}
	}
	return parts[1] + "." + tail
}

// Compile-time port assertion.
var _ events.Publisher = (*Publisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder.
//
// JSON fallback exists to preserve behaviour for the ~30 chora.consumption.*
// topics that pre-date the protomarshal package. Each unknown topic logs a
// one-time WARN so its missing encoder is visible in production. New topics
// MUST add a case in protomarshal.MarshalPayload.
// -----------------------------------------------------------------------------

var (
	warnedUnknownTopicsMu sync.Mutex
	warnedUnknownTopics   = map[string]bool{}
)

// toProtoEnvelope projects the local events.Envelope onto the encoder's flat
// envelope type. Keeps protomarshal import-cycle-free.
func toProtoEnvelope(env events.Envelope) protomarshal.Envelope {
	return protomarshal.Envelope{
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
	}
}

func encodeOutboxPayload(topic string, env events.Envelope, payload map[string]any) ([]byte, error) {
	bz, err := protomarshal.MarshalPayload(topic, toProtoEnvelope(env), payload)
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		// Real encoding error (e.g. type mismatch) — fail loud.
		return nil, err
	}

	// Topic has no protobuf encoder yet. Log a one-shot WARN and fall back
	// to JSON. These rows WILL be rejected by Schema Registry on publish —
	// follow-on cleanup must add encoders for each.
	warnedUnknownTopicsMu.Lock()
	if !warnedUnknownTopics[topic] {
		warnedUnknownTopics[topic] = true
		log.Printf("WARN outbox: topic %q has no binary protobuf encoder — payload will JSON-marshal and Schema Registry will REJECT at publish; expect outbox_deadletter. Add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	warnedUnknownTopicsMu.Unlock()

	bz, mErr := json.Marshal(payload)
	if mErr != nil {
		return nil, fmt.Errorf("outbox: json fallback marshal: %w", mErr)
	}
	return bz, nil
}
