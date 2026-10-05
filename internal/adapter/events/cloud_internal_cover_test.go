package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeriveEventType characterises the topic→event_type derivation,
// including the defensive short-topic branch (fewer than 3 dot-segments
// returns the topic unchanged).
func TestDeriveEventType(t *testing.T) {
	cases := map[string]struct {
		topic string
		want  string
	}{
		"canonical":      {"chora.consumption.daily_dose.served.v1", "daily_dose.served.v1"},
		"three_segments": {"chora.consumption.x", "x"},
		"two_segments":   {"chora.consumption", "chora.consumption"},
		"one_segment":    {"chora", "chora"},
		"empty":          {"", ""},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, deriveEventType(c.topic))
		})
	}
}

// TestEncodeCloudPublisherPayload_WarnsOnceThenStaysSilent covers the
// one-shot WARN dedup: the first encode of an unsupported topic records it in
// the package-global warned-set; subsequent encodes of the SAME topic take the
// already-warned branch. Both still fall back to JSON successfully.
func TestEncodeCloudPublisherPayload_WarnsOnceThenStaysSilent(t *testing.T) {
	// A canonical-but-unwired topic unique to this test so we don't depend on
	// whether other tests already warmed the global map.
	topic := "chora.consumption.cover_only_unwired.event.v1"
	env := Envelope{
		EventID:        "e1",
		IdempotencyKey: "e1",
		TenantID:       "t1",
		OccurredAt:     time.Now().UTC(),
		PublishedAt:    time.Now().UTC(),
		Traceparent:    tTraceparent,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
	payload := map[string]any{"k": "v"}

	b1, err := encodeCloudPublisherPayload(topic, env, payload)
	require.NoError(t, err)
	assert.Contains(t, string(b1), `"k":"v"`)

	// Second call hits the already-warned branch (no new log) and still
	// returns the JSON fallback body.
	b2, err := encodeCloudPublisherPayload(topic, env, payload)
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2))
}

// TestEncodeCloudPublisherPayload_BinaryTopic asserts that a topic WITH a
// binary encoder takes the protobuf path and does NOT produce a JSON body
// (the bytes differ from a naive json.Marshal of the payload).
func TestEncodeCloudPublisherPayload_BinaryTopic(t *testing.T) {
	env := Envelope{
		EventID:        "01970000-0000-7000-8000-000000000099",
		IdempotencyKey: "01970000-0000-7000-8000-000000000099",
		TenantID:       "t1",
		GCID:           "g1",
		OccurredAt:     time.Now().UTC(),
		PublishedAt:    time.Now().UTC(),
		Traceparent:    tTraceparent,
		SourceProject:  SourceProject,
		SourceService:  SourceService,
		SchemaVersion:  1,
	}
	bz, err := encodeCloudPublisherPayload(TopicDailyDoseServed, env, map[string]any{
		"companion_id": "fam-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, bz)
	// The protobuf encoding is not the literal JSON text of the payload.
	assert.NotContains(t, string(bz), `"companion_id"`)
}
