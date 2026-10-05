// cloud_publisher.go — outbox-backed production replacement for InMemoryPublisher.
//
// Atomicity contract: writes events to chora_consumption.outbox_events via
// chora-go-common/outbox.Recorder. A separate Relay process drains pending
// rows to Cloud Pub/Sub.
//
// Aligned with services/chora-identity/internal/adapter/events/cloud_publisher.go
// + services/chora-creation/internal/adapter/events/cloud_publisher.go.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
)

// CloudPublisherConfig tunes a CloudPublisher.
type CloudPublisherConfig struct {
	// AggregateType labels every outbox row this publisher emits. Defaults
	// to "atom_session"; explicit override recommended when wired for
	// learning_path or kg_map_cluster events.
	AggregateType string
}

// CloudPublisher writes events to the chora-go-common outbox recorder.
// Satisfies events.Publisher.
type CloudPublisher struct {
	rec cgcoutbox.Recorder
	cfg CloudPublisherConfig
}

// NewCloudPublisher constructs a CloudPublisher.
func NewCloudPublisher(rec cgcoutbox.Recorder, cfg CloudPublisherConfig) *CloudPublisher {
	if cfg.AggregateType == "" {
		cfg.AggregateType = "atom_session"
	}
	return &CloudPublisher{rec: rec, cfg: cfg}
}

// Publish satisfies events.Publisher. Validates topic + envelope, marshals
// the payload to JSON, and writes a pending outbox row.
func (p *CloudPublisher) Publish(topic string, env Envelope, payload map[string]any) error {
	if err := validateCloudTopic(topic); err != nil {
		return err
	}
	if err := validateEnvelope(topic, env); err != nil {
		return err
	}

	// Producer-side encoding: emit canonical binary protobuf for topics whose
	// Pub/Sub Schema Registry schema is BINARY-encoded. JSON payloads on a
	// schema-attached topic dead-letter forever with "Invalid binary proto
	// message". Per the gap surfaced 2026-05-15.
	body, err := encodeCloudPublisherPayload(topic, env, payload)
	if err != nil {
		return fmt.Errorf("events.CloudPublisher: marshal payload: %w", err)
	}

	commonEnv := cgcenvelope.Envelope{
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

	aggregateID := env.GCID
	if aggregateID == "" {
		aggregateID = env.EventID
	}
	if payload != nil {
		// Prefer the most-relevant domain ID when present so audit + replay
		// queries on aggregate_id give useful results.
		for _, k := range []string{"session_id", "path_enrollment_id", "map_cluster_id", "exploration_id"} {
			if v, ok := payload[k].(string); ok && v != "" {
				aggregateID = v
				break
			}
		}
	}

	row := &cgcoutbox.Row{
		ID:            newRowID(),
		AggregateType: p.cfg.AggregateType,
		AggregateID:   aggregateID,
		EventType:     deriveEventType(topic),
		Topic:         topic,
		Payload:       body,
		Envelope:      commonEnv,
		OccurredAt:    env.OccurredAt,
		Status:        cgcoutbox.StatusPending,
	}
	return p.rec.Record(context.Background(), nil, row)
}

// validateCloudTopic enforces chora.{domain}.{aggregate}.{event_type}.v{N}
// shape and locks domain to "consumption" for this publisher.
func validateCloudTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("events: topic required")
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return errors.New("events: topic must follow chora.{domain}.{aggregate}.{event_type}.v{N}")
	}
	if parts[0] != "chora" {
		return errors.New("events: topic must start with 'chora.'")
	}
	if parts[1] != "consumption" && parts[1] != "governance" {
		return errors.New("events: topic domain must be 'consumption' or 'governance'")
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return errors.New("events: topic must end with v{N} version suffix")
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return errors.New("events: topic version suffix must be numeric")
		}
	}
	return nil
}

func deriveEventType(topic string) string {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) < 3 {
		return topic
	}
	return parts[2]
}

func newRowID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Compile-time check.
var _ Publisher = (*CloudPublisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder. New topics MUST
// add a case in internal/adapter/events/protomarshal/MarshalPayload.
// -----------------------------------------------------------------------------

var (
	cloudWarnedUnknownTopicsMu sync.Mutex
	cloudWarnedUnknownTopics   = map[string]bool{}
)

func encodeCloudPublisherPayload(topic string, env Envelope, payload map[string]any) ([]byte, error) {
	bz, err := protomarshal.MarshalPayload(topic, protomarshal.Envelope{
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
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		return nil, err
	}

	// Topic not yet wired for binary protobuf. Log a one-shot WARN and fall
	// back to JSON so the pre-existing path doesn't regress. These rows
	// WILL be rejected by Schema Registry at publish — track + add encoders.
	cloudWarnedUnknownTopicsMu.Lock()
	if !cloudWarnedUnknownTopics[topic] {
		cloudWarnedUnknownTopics[topic] = true
		log.Printf("WARN events.CloudPublisher: topic %q has no binary protobuf encoder — payload will JSON-marshal and Schema Registry will REJECT at publish; expect outbox_deadletter. Add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	cloudWarnedUnknownTopicsMu.Unlock()

	bz, jErr := json.Marshal(payload)
	if jErr != nil {
		return nil, fmt.Errorf("events.CloudPublisher: json fallback marshal: %w", jErr)
	}
	return bz, nil
}
