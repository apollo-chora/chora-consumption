package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protomarshal"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// protoEnv mirrors the events.Envelope -> protomarshal.Envelope copy that
// CloudPublisher.encodeCloudPublisherPayload performs, so the round-trip test
// below exercises the exact producer → binary-wire path a real publish takes.
func protoEnv(env Envelope) protomarshal.Envelope {
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

// TestKGPublisher_HexagonFogGenerated_ProducerRoundTrip proves the Flag-2
// enrichment END TO END: the payload KGPublisher.PublishHexagonFogGenerated
// emits now round-trips through the binary encoder into the registered
// HexagonFogGenerated schema shape with user_gcid + neighbors + generated_at
// populated. Before the enrichment the producer omitted those three fields (and
// sent non-schema hex_node_id / final_state the encoder dropped), so the wire
// bytes carried empty user_gcid / no neighbors / zero generated_at. This is the
// regression gate that the producer keeps emitting exactly what the encoder +
// Pub/Sub Schema Registry expect.
func TestKGPublisher_HexagonFogGenerated_ProducerRoundTrip(t *testing.T) {
	kp, mem := newKGFixtures()
	ctx := WithTrace(context.Background(), tTraceparent, tTracestate)

	hex := sampleHexagon("hex-rt1", "exp-rt1", "clu-rt1")
	fog := &userknowledgegraph.FogResult{
		ModelID:         "gemini-x",
		TotalCostMicros: 4242,
		InputTokens:     100,
		OutputTokens:    50,
		FinalState:      "SUCCESS", // non-schema — must NOT reach the wire
	}
	require.NoError(t, kp.PublishHexagonFogGenerated(ctx, hex, fog))

	got := mem.Events()
	require.Len(t, got, 1)
	ev := got[0]
	require.Equal(t, TopicKGHexagonFogGenerated, ev.Topic)

	bz, err := protomarshal.MarshalPayload(ev.Topic, protoEnv(ev.Envelope), ev.Payload)
	require.NoError(t, err, "producer payload must round-trip through the binary encoder")

	var m consumptionv1.HexagonFogGenerated
	require.NoError(t, proto.Unmarshal(bz, &m), "encoded bytes must parse as the registered schema")

	// The three Flag-2 enrichment fields.
	assert.Equal(t, kgGCID, m.GetUserGcid(), "user_gcid (5) enriched")
	require.NotNil(t, m.GetGeneratedAt(), "generated_at (12) enriched")
	assert.True(t, m.GetGeneratedAt().AsTime().Equal(time.Date(2026, 6, 30, 8, 30, 0, 0, time.UTC)))
	require.Len(t, m.GetNeighbors(), 2, "neighbors (7) enriched")
	n0 := m.GetNeighbors()[0]
	assert.Equal(t, "atom-n1", n0.GetAtomId())
	assert.Equal(t, consumptionv1.NeighborRelation_NEIGHBOR_RELATION_PREREQUISITE_OF, n0.GetRelation())
	assert.InDelta(t, 0.8, float64(n0.GetConfidence()), 1e-6)
	assert.Equal(t, "Learn this first", n0.GetFogLabel())
	assert.False(t, n0.GetIsJunction())
	assert.True(t, m.GetNeighbors()[1].GetIsJunction(), "curiosity-jump neighbour flagged is_junction")

	// The rest of the schema still round-trips (run_id/model_id via producer
	// aliases generated_by_run_id / generated_by_model_id).
	assert.Equal(t, "clu-rt1", m.GetClusterId())
	assert.Equal(t, "exp-rt1", m.GetExplorationId())
	assert.Equal(t, "atom-focal-1", m.GetFocalAtomId())
	assert.Equal(t, "run-1", m.GetRunId())
	assert.Equal(t, "gemini-x", m.GetModelId())
	assert.Equal(t, int64(4242), m.GetTotalCostMicros())
	assert.Equal(t, int64(100), m.GetInputTokens())
	assert.Equal(t, int64(50), m.GetOutputTokens())
}
