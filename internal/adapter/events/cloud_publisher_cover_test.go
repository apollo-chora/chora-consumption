package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
)

// recRecorder is a minimal Recorder that records the rows written.
type recRecorder struct {
	rows   []*cgcoutbox.Row
	recErr error
}

func (s *recRecorder) Record(_ context.Context, _ cgcoutbox.Tx, row *cgcoutbox.Row) error {
	if s.recErr != nil {
		return s.recErr
	}
	s.rows = append(s.rows, row)
	return nil
}
func (s *recRecorder) Claim(_ context.Context, _ int) ([]*cgcoutbox.Row, error) { return nil, nil }
func (s *recRecorder) MarkPublished(_ context.Context, _ []string) error        { return nil }
func (s *recRecorder) MarkFailed(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}

func validCloudEnvelope() events.Envelope {
	now := time.Now().UTC()
	return events.Envelope{
		EventID:        "01970000-0000-7000-8000-000000000010",
		IdempotencyKey: "01970000-0000-7000-8000-000000000010",
		TenantID:       "22222222-2222-7222-8222-222222222222",
		GCID:           "00000000-0000-7000-8000-000000001999",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-consumption",
		SchemaVersion:  1,
	}
}

// TestCloudPublisher_NewDefaultsAggregateType asserts the documented default
// aggregate_type when the caller leaves it blank.
func TestCloudPublisher_NewDefaultsAggregateType(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
	require.NoError(t, p.Publish(events.TopicDailyDoseServed, validCloudEnvelope(), map[string]any{
		"companion_id": "fam-1",
	}))
	require.Len(t, rec.rows, 1)
	assert.Equal(t, "atom_session", rec.rows[0].AggregateType,
		"blank AggregateType must default to atom_session")
}

// TestCloudPublisher_AggregateIDPrecedence characterises the aggregate_id
// selection rule: a domain ID in the payload (in priority order) wins over
// the envelope GCID, which itself wins over the EventID when GCID is empty.
func TestCloudPublisher_AggregateIDPrecedence(t *testing.T) {
	t.Run("session_id_wins_over_gcid", func(t *testing.T) {
		rec := &recRecorder{}
		p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
		require.NoError(t, p.Publish(events.TopicDailyDoseServed, validCloudEnvelope(), map[string]any{
			"session_id": "sess-xyz",
		}))
		require.Len(t, rec.rows, 1)
		assert.Equal(t, "sess-xyz", rec.rows[0].AggregateID)
	})

	t.Run("map_cluster_id_used_when_no_session", func(t *testing.T) {
		rec := &recRecorder{}
		p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
		require.NoError(t, p.Publish(events.TopicDailyDoseServed, validCloudEnvelope(), map[string]any{
			"map_cluster_id": "cl-1",
		}))
		require.Len(t, rec.rows, 1)
		assert.Equal(t, "cl-1", rec.rows[0].AggregateID)
	})

	t.Run("falls_back_to_gcid_when_no_domain_id", func(t *testing.T) {
		rec := &recRecorder{}
		p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
		env := validCloudEnvelope()
		require.NoError(t, p.Publish(events.TopicDailyDoseServed, env, map[string]any{
			"unrelated": "x",
		}))
		require.Len(t, rec.rows, 1)
		assert.Equal(t, env.GCID, rec.rows[0].AggregateID)
	})

	t.Run("falls_back_to_event_id_when_gcid_blank", func(t *testing.T) {
		rec := &recRecorder{}
		p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
		env := validCloudEnvelope()
		env.GCID = "" // system-emitted event allows blank gcid
		require.NoError(t, p.Publish(events.TopicDailyDoseServed, env, nil))
		require.Len(t, rec.rows, 1)
		assert.Equal(t, env.EventID, rec.rows[0].AggregateID,
			"blank GCID + no payload domain id must fall back to event_id")
	})
}

// TestCloudPublisher_DeriveEventType_FromTopic asserts EventType is the
// {aggregate}.{event_type}.v{N} remainder after the chora.{domain}. prefix.
func TestCloudPublisher_DeriveEventType_FromTopic(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
	require.NoError(t, p.Publish(events.TopicDailyDoseServed, validCloudEnvelope(), nil))
	require.Len(t, rec.rows, 1)
	// chora.consumption.daily_dose.served.v1 → "daily_dose.served.v1"
	assert.Equal(t, "daily_dose.served.v1", rec.rows[0].EventType)
	assert.Equal(t, events.TopicDailyDoseServed, rec.rows[0].Topic)
}

// TestCloudPublisher_JSONFallbackForUnsupportedTopic characterises the
// degraded path: a canonical-but-binary-unwired topic (KG cluster) passes
// topic validation, so the publisher logs once + JSON-marshals the payload
// into a pending outbox row rather than failing. This is the documented
// regression-safe fallback before a binary encoder exists for the topic.
func TestCloudPublisher_JSONFallbackForUnsupportedTopic(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{AggregateType: "kg_map_cluster"})
	err := p.Publish(events.TopicKGMapClusterCreated, validCloudEnvelope(), map[string]any{
		"map_cluster_id": "cl-7",
		"display_name":   "Topology",
	})
	require.NoError(t, err, "unsupported topic must JSON-fall-back, not error")
	require.Len(t, rec.rows, 1)
	row := rec.rows[0]
	assert.Equal(t, "cl-7", row.AggregateID)
	// Body is valid JSON (the fallback encoding).
	assert.Contains(t, string(row.Payload), `"display_name":"Topology"`)
}

// TestCloudPublisher_MarshalErrorPropagates asserts a payload that cannot be
// JSON-marshalled (e.g. a channel value) on an otherwise-valid topic produces
// a marshal error rather than writing a corrupt outbox row.
func TestCloudPublisher_MarshalErrorPropagates(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{AggregateType: "kg_map_cluster"})
	// Unsupported topic → JSON fallback path → json.Marshal on an unmarshalable value fails.
	err := p.Publish(events.TopicKGJunctionDetected, validCloudEnvelope(), map[string]any{
		"bad": make(chan int),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal payload")
	assert.Empty(t, rec.rows, "no outbox row written when payload marshal fails")
}

// TestCloudPublisher_BinaryEncoderErrorPropagates asserts that when a topic
// HAS a binary encoder but the payload has a wrong-typed field, the
// (non-unsupported) marshal error is returned verbatim — the publisher must
// NOT silently fall back to JSON for a schema-attached topic, since that would
// dead-letter at the Schema Registry. Characterises the fail-loud branch.
func TestCloudPublisher_BinaryEncoderErrorPropagates(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{AggregateType: "companion"})
	// chat_turn_completed expects session_id as string; supply an int to force
	// a typed-writer error inside the binary marshaller.
	err := p.Publish(events.TopicCompanionChatTurnCompleted, validCloudEnvelope(), map[string]any{
		"session_id": 12345, // wrong type
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal payload")
	assert.Contains(t, err.Error(), "expected string")
	assert.Empty(t, rec.rows, "no outbox row written when binary encode fails")
}

// TestCloudPublisher_RejectsBadTopics is a table over the validateCloudTopic
// branches — each must reject before any outbox row is written.
func TestCloudPublisher_RejectsBadTopics(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"whitespace_only":    "   ",
		"too_few_parts":      "chora.consumption.x",
		"wrong_prefix":       "acme.consumption.atom.created.v1",
		"wrong_domain":       "chora.delivery.atom.created.v1",
		"missing_v_suffix":   "chora.consumption.atom.created.1",
		"non_numeric_suffix": "chora.consumption.atom.created.vX",
		"bare_v_suffix":      "chora.consumption.atom.created.v",
	}
	for name, topic := range cases {
		topic := topic
		t.Run(name, func(t *testing.T) {
			rec := &recRecorder{}
			p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
			err := p.Publish(topic, validCloudEnvelope(), nil)
			require.Error(t, err, "topic %q must be rejected", topic)
			assert.Empty(t, rec.rows)
		})
	}
}

// TestCloudPublisher_AcceptsGovernanceDomain asserts the second allowed domain
// (governance) passes topic validation — the OR branch in validateCloudTopic.
func TestCloudPublisher_AcceptsGovernanceDomain(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{AggregateType: "decision"})
	// governance topic has no binary encoder → JSON fallback, but must NOT be
	// rejected at topic validation.
	err := p.Publish("chora.governance.decision.explained.v1", validCloudEnvelope(), map[string]any{"k": "v"})
	require.NoError(t, err)
	require.Len(t, rec.rows, 1)
	assert.Equal(t, "decision.explained.v1", rec.rows[0].EventType)
}

// TestCloudPublisher_RejectsInvalidEnvelope asserts envelope validation runs
// after topic validation — a valid topic with a missing mandatory field is
// rejected and no row is written.
func TestCloudPublisher_RejectsInvalidEnvelope(t *testing.T) {
	rec := &recRecorder{}
	p := events.NewCloudPublisher(rec, events.CloudPublisherConfig{})
	env := validCloudEnvelope()
	env.Traceparent = "" // mandatory W3C trace context missing
	err := p.Publish(events.TopicDailyDoseServed, env, nil)
	require.Error(t, err)
	assert.Empty(t, rec.rows)
}
