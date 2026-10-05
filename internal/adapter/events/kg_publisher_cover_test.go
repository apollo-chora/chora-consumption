package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// kgTenant/kgGCID are valid-shaped IDs for KG envelope assertions.
const (
	kgTenant = "01970000-0000-7000-8000-0000000000a1"
	kgGCID   = "01970000-0000-7000-9000-0000000000b2"
)

// newKGFixtures returns a wired KGPublisher backed by an InMemoryPublisher so
// we can assert the emitted envelope + payload shape that downstream
// subscribers + the Schema Registry depend on.
func newKGFixtures() (*KGPublisher, *InMemoryPublisher) {
	mem := NewInMemoryPublisher()
	return NewKGPublisher(mem), mem
}

func sampleCluster(id string) *userknowledgegraph.MapCluster {
	return &userknowledgegraph.MapCluster{
		ClusterID:   id,
		TenantID:    kgTenant,
		UserGCID:    kgGCID,
		DisplayName: "Calculus Basics",
		SeedTopic:   "calculus",
		SeedAtomID:  "atom-seed-1",
		Status:      userknowledgegraph.ClusterStatusActive,
		NodeCount:   3,
		CreatedAt:   time.Now().UTC(),
	}
}

func sampleExploration(id, clusterID string) *userknowledgegraph.Exploration {
	return &userknowledgegraph.Exploration{
		ExplorationID:      id,
		ClusterID:          clusterID,
		TenantID:           kgTenant,
		UserGCID:           kgGCID,
		StartedAtAtomID:    "atom-start-1",
		CurrentFocalAtomID: "atom-focal-1",
		Status:             userknowledgegraph.ExplorationStatus("active"),
	}
}

func sampleHexagon(id, explorationID, clusterID string) *userknowledgegraph.HexagonNode {
	return &userknowledgegraph.HexagonNode{
		HexNodeID:     id,
		ExplorationID: explorationID,
		ClusterID:     clusterID,
		TenantID:      kgTenant,
		UserGCID:      kgGCID,
		FocalAtomID:   "atom-focal-1",
		Neighbors: []userknowledgegraph.HexagonNeighbor{
			{AtomID: "atom-n1", Relation: userknowledgegraph.NeighborRelationPrerequisiteOf, Confidence: 0.8, FogLabel: "Learn this first", IsJunction: false},
			{AtomID: "atom-n2", Relation: userknowledgegraph.NeighborRelationCuriosityJump, Confidence: 0.4, FogLabel: "A curious leap", IsJunction: true},
		},
		GeneratedByRunID:   "run-1",
		GeneratedByModelID: "gemini-x",
		GeneratedAt:        time.Date(2026, 6, 30, 8, 30, 0, 0, time.UTC),
		InvalidationReason: userknowledgegraph.FogInvalidationReasonAtomPublished,
	}
}

func sampleJunction(id string) *userknowledgegraph.Junction {
	return &userknowledgegraph.Junction{
		JunctionID:        id,
		TenantID:          kgTenant,
		UserGCID:          kgGCID,
		ClusterAID:        "cluster-a",
		ClusterBID:        "cluster-b",
		ResolvedViaAtomID: "atom-bridge-1",
	}
}

// TestKGPublisher_ImplementsPort is a compile-time + behavioural assertion
// that the adapter satisfies the domain EventPublisher port. A signature
// drift on either side is a real regression this catches.
func TestKGPublisher_ImplementsPort(t *testing.T) {
	var _ userknowledgegraph.EventPublisher = (*KGPublisher)(nil)
}

// TestKGPublisher_HappyPaths drives every Publish* method and asserts the
// topic + the payload fields each subscriber + Schema Registry rely on,
// plus that the envelope carries the supplied trace context and a derived
// idempotency key. This characterises the real translation logic.
func TestKGPublisher_HappyPaths(t *testing.T) {
	ctx := WithTrace(context.Background(), tTraceparent, tTracestate)

	type check struct {
		topic          string
		idempotencyKey string // expected envelope idempotency_key
		invoke         func(kp *KGPublisher) error
		assertPayload  func(t *testing.T, p map[string]any)
	}

	cl := sampleCluster("cluster-1")
	other := sampleCluster("cluster-2")
	exp := sampleExploration("exp-1", "cluster-1")
	hop := &userknowledgegraph.TrailHop{
		TrailID:         "trail-1",
		FromFocalAtomID: "atom-from",
		ToFocalAtomID:   "atom-to",
		StepIndex:       2,
		Relation:        userknowledgegraph.NeighborRelationExtends,
	}
	hex := sampleHexagon("hex-1", "exp-1", "cluster-1")
	fog := &userknowledgegraph.FogResult{
		TotalCostMicros: 1234,
		InputTokens:     10,
		OutputTokens:    20,
		FinalState:      "SUCCESS",
	}
	jn := sampleJunction("jn-1")

	checks := []check{
		{
			topic:          TopicKGMapClusterCreated,
			idempotencyKey: "kg_cluster_created:cluster-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishMapClusterCreated(ctx, cl) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "cluster-1", p["cluster_id"])
				assert.Equal(t, kgTenant, p["tenant_id"])
				assert.Equal(t, kgGCID, p["user_gcid"])
				assert.Equal(t, "Calculus Basics", p["display_name"])
				assert.Equal(t, "calculus", p["seed_topic"])
				assert.Equal(t, "active", p["status"])
				assert.Equal(t, 3, p["node_count"])
				assert.Equal(t, "accountability", p["chora_imda_dimension"])
				assert.Equal(t, "runtime", p["imda_lifecycle_stage"])
			},
		},
		{
			topic:          TopicKGMapClusterMerged,
			idempotencyKey: "kg_cluster_merged:cluster-2",
			invoke:         func(kp *KGPublisher) error { return kp.PublishMapClusterMerged(ctx, cl, other, "atom-bridge") },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "cluster-1", p["surviving_cluster_id"])
				assert.Equal(t, "cluster-2", p["merged_cluster_id"])
				assert.Equal(t, "atom-bridge", p["via_atom_id"])
				assert.Equal(t, "Calculus Basics", p["new_display_name"])
				assert.Equal(t, 3, p["surviving_node_count"])
				assert.Equal(t, "accountability", p["chora_imda_dimension"])
			},
		},
		{
			topic:          TopicKGMapClusterArchived,
			idempotencyKey: "kg_cluster_archived:cluster-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishMapClusterArchived(ctx, cl) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "cluster-1", p["cluster_id"])
				assert.Equal(t, "accountability", p["chora_imda_dimension"])
			},
		},
		{
			topic:          TopicKGExplorationCreated,
			idempotencyKey: "kg_exp_created:exp-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishExplorationCreated(ctx, exp) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "exp-1", p["exploration_id"])
				assert.Equal(t, "cluster-1", p["cluster_id"])
				assert.Equal(t, "atom-start-1", p["started_at_atom_id"])
				assert.Equal(t, "atom-focal-1", p["current_focal_atom_id"])
			},
		},
		{
			topic:          TopicKGExplorationFocalChng,
			idempotencyKey: "kg_focal_changed:exp-1:trail-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishExplorationFocalChanged(ctx, exp, hop) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "exp-1", p["exploration_id"])
				assert.Equal(t, "trail-1", p["trail_id"])
				assert.Equal(t, "atom-from", p["from_focal_atom_id"])
				assert.Equal(t, "atom-to", p["to_focal_atom_id"])
				assert.Equal(t, 2, p["step_index"])
				assert.Equal(t, "extends", p["relation"])
			},
		},
		{
			topic:          TopicKGExplorationArchived,
			idempotencyKey: "kg_exp_archived:exp-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishExplorationArchived(ctx, exp) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "exp-1", p["exploration_id"])
				assert.Equal(t, "cluster-1", p["cluster_id"])
			},
		},
		{
			topic:          TopicKGHexagonFogGenerated,
			idempotencyKey: "kg_fog_generated:hex-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishHexagonFogGenerated(ctx, hex, fog) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "exp-1", p["exploration_id"])
				assert.Equal(t, "atom-focal-1", p["focal_atom_id"])
				// Flag-2 enrichment: user_gcid / neighbors / generated_at now
				// populated from the aggregate (were absent → schema fields empty).
				assert.Equal(t, kgGCID, p["user_gcid"])
				assert.Equal(t, time.Date(2026, 6, 30, 8, 30, 0, 0, time.UTC), p["generated_at"])
				nbrs, ok := p["neighbors"].([]map[string]any)
				require.True(t, ok, "neighbors must be []map[string]any, got %T", p["neighbors"])
				require.Len(t, nbrs, 2)
				assert.Equal(t, "atom-n1", nbrs[0]["atom_id"])
				assert.Equal(t, "prerequisite_of", nbrs[0]["relation"])
				assert.Equal(t, float32(0.8), nbrs[0]["confidence"])
				assert.Equal(t, "Learn this first", nbrs[0]["fog_label"])
				assert.Equal(t, false, nbrs[0]["is_junction"])
				assert.Equal(t, true, nbrs[1]["is_junction"])
				assert.Equal(t, "run-1", p["generated_by_run_id"])
				assert.Equal(t, "gemini-x", p["generated_by_model_id"])
				assert.Equal(t, int64(1234), p["total_cost_micros"])
				assert.Equal(t, int64(10), p["input_tokens"])
				assert.Equal(t, int64(20), p["output_tokens"])
				// Non-schema keys hex_node_id / final_state are no longer emitted.
				assert.NotContains(t, p, "hex_node_id")
				assert.NotContains(t, p, "final_state")
				// fog-generation reports under the safety dimension, not accountability.
				assert.Equal(t, "safety_and_robustness", p["chora_imda_dimension"])
			},
		},
		{
			topic:          TopicKGHexagonFogInvalidated,
			idempotencyKey: "kg_fog_invalidated:hex-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishHexagonFogInvalidated(ctx, hex) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "hex-1", p["hex_node_id"])
				assert.Equal(t, "atom_published", p["invalidation_reason"])
			},
		},
		{
			topic:          "kg_junction_detected:cluster-1:cluster-2:atom-bridge",
			idempotencyKey: "kg_junction_detected:cluster-1:cluster-2:atom-bridge",
			invoke: func(kp *KGPublisher) error {
				return kp.PublishJunctionDetected(ctx, cl, other, "atom-bridge", "run-99")
			},
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "cluster-1", p["current_cluster_id"])
				assert.Equal(t, "cluster-2", p["other_cluster_id"])
				assert.Equal(t, "atom-bridge", p["via_atom_id"])
				assert.Equal(t, "run-99", p["triggering_run_id"])
				assert.Equal(t, "accountability", p["chora_imda_dimension"])
			},
		},
		{
			topic:          "kg_junction_accepted:jn-1",
			idempotencyKey: "kg_junction_accepted:jn-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishJunctionAccepted(ctx, jn) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "jn-1", p["junction_id"])
				assert.Equal(t, "cluster-a", p["cluster_a_id"])
				assert.Equal(t, "cluster-b", p["cluster_b_id"])
				assert.Equal(t, "atom-bridge-1", p["resolved_via_atom_id"])
			},
		},
		{
			topic:          "kg_junction_rejected:jn-1",
			idempotencyKey: "kg_junction_rejected:jn-1",
			invoke:         func(kp *KGPublisher) error { return kp.PublishJunctionRejected(ctx, jn) },
			assertPayload: func(t *testing.T, p map[string]any) {
				assert.Equal(t, "jn-1", p["junction_id"])
				assert.Equal(t, "cluster-a", p["cluster_a_id"])
				assert.Equal(t, "cluster-b", p["cluster_b_id"])
			},
		},
	}

	for _, c := range checks {
		c := c
		// Use a label derived from the idempotency key for readability.
		t.Run(c.idempotencyKey, func(t *testing.T) {
			kp, mem := newKGFixtures()
			require.NoError(t, c.invoke(kp))

			got := mem.Events()
			require.Len(t, got, 1, "exactly one event emitted")
			ev := got[0]

			// The junction topic-string entries above embed the idempotency
			// key, not the topic. Assert topic only for the canonical-topic
			// cases (those whose topic differs from idempotency key).
			if c.topic != c.idempotencyKey {
				assert.Equal(t, c.topic, ev.Topic, "topic")
			} else {
				// Junction methods: still assert topic is a valid KG junction topic.
				assert.True(t, strings.HasPrefix(ev.Topic, "chora.consumption.kg_junction."),
					"junction topic = %q", ev.Topic)
			}

			// Trace context propagated from ctx into the envelope.
			assert.Equal(t, tTraceparent, ev.Envelope.Traceparent)
			assert.Equal(t, tTracestate, ev.Envelope.Tracestate)
			// Stable, content-derived idempotency key (retry-safe).
			assert.Equal(t, c.idempotencyKey, ev.Envelope.IdempotencyKey)
			assert.Equal(t, kgTenant, ev.Envelope.TenantID)
			assert.Equal(t, kgGCID, ev.Envelope.GCID)

			if c.assertPayload != nil {
				c.assertPayload(t, ev.Payload)
			}
		})
	}
}

// TestKGPublisher_NilGuards asserts every method returns a descriptive error
// (and publishes nothing) when handed a nil aggregate — the contract the
// application service relies on to fail loud rather than emit malformed events.
func TestKGPublisher_NilGuards(t *testing.T) {
	ctx := context.Background()
	cl := sampleCluster("c1")
	exp := sampleExploration("e1", "c1")
	hex := sampleHexagon("h1", "e1", "c1")
	hop := &userknowledgegraph.TrailHop{TrailID: "t1"}

	cases := map[string]func(kp *KGPublisher) error{
		"cluster_created_nil":     func(kp *KGPublisher) error { return kp.PublishMapClusterCreated(ctx, nil) },
		"cluster_archived_nil":    func(kp *KGPublisher) error { return kp.PublishMapClusterArchived(ctx, nil) },
		"cluster_merged_surv_nil": func(kp *KGPublisher) error { return kp.PublishMapClusterMerged(ctx, nil, cl, "a") },
		"cluster_merged_merg_nil": func(kp *KGPublisher) error { return kp.PublishMapClusterMerged(ctx, cl, nil, "a") },
		"exp_created_nil":         func(kp *KGPublisher) error { return kp.PublishExplorationCreated(ctx, nil) },
		"exp_focal_exp_nil":       func(kp *KGPublisher) error { return kp.PublishExplorationFocalChanged(ctx, nil, hop) },
		"exp_focal_hop_nil":       func(kp *KGPublisher) error { return kp.PublishExplorationFocalChanged(ctx, exp, nil) },
		"exp_archived_nil":        func(kp *KGPublisher) error { return kp.PublishExplorationArchived(ctx, nil) },
		"fog_gen_hex_nil": func(kp *KGPublisher) error {
			return kp.PublishHexagonFogGenerated(ctx, nil, &userknowledgegraph.FogResult{})
		},
		"fog_gen_fog_nil":      func(kp *KGPublisher) error { return kp.PublishHexagonFogGenerated(ctx, hex, nil) },
		"fog_inval_nil":        func(kp *KGPublisher) error { return kp.PublishHexagonFogInvalidated(ctx, nil) },
		"junction_det_cur_nil": func(kp *KGPublisher) error { return kp.PublishJunctionDetected(ctx, nil, cl, "a", "r") },
		"junction_det_oth_nil": func(kp *KGPublisher) error { return kp.PublishJunctionDetected(ctx, cl, nil, "a", "r") },
		"junction_acc_nil":     func(kp *KGPublisher) error { return kp.PublishJunctionAccepted(ctx, nil) },
		"junction_rej_nil":     func(kp *KGPublisher) error { return kp.PublishJunctionRejected(ctx, nil) },
	}

	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			kp, mem := newKGFixtures()
			err := fn(kp)
			require.Error(t, err, "nil aggregate must produce an error")
			assert.Contains(t, err.Error(), "kg_publisher:")
			assert.Empty(t, mem.Events(), "no event must be published on nil input")
		})
	}
}

// TestTraceFromCtx_FallbackIsUniquePerCall asserts that when ctx carries no
// trace context the publisher mints a FRESH, syntactically-valid traceparent
// so envelope validation (which requires traceparent) never fails on a code
// path invoked outside an HTTP request.
//
// ⚠ This test previously asserted the literal
// "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00", the traceparent
// printed as an EXAMPLE in the W3C spec. A constant makes every unparented
// envelope share one trace id, which put 99 unrelated spans on a single Cloud
// Trace. The green test was the bug's alibi. Uniqueness is the invariant.
func TestTraceFromCtx_FallbackIsUniquePerCall(t *testing.T) {
	const w3cExample = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00"

	tp, ts := traceFromCtx(context.Background())
	assert.Empty(t, ts)
	assert.NotEqual(t, w3cExample, tp, "fallback must never be the W3C example traceparent")
	assert.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`, tp)

	tp2, _ := traceFromCtx(context.Background())
	assert.NotEqual(t, tp, tp2, "two unparented calls must not share a trace id")

	// And the minted value passes envelope publish validation.
	kp, mem := newKGFixtures()
	require.NoError(t, kp.PublishMapClusterCreated(context.Background(), sampleCluster("c-fallback")))
	require.Len(t, mem.Events(), 1)
	assert.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`,
		mem.Events()[0].Envelope.Traceparent)
	assert.NotEqual(t, w3cExample, mem.Events()[0].Envelope.Traceparent)
}

// TestTraceFromCtx_PrefersLiveSpanContext asserts the id comes from the OTel
// span Cloud Trace actually exported, not from a minted throwaway, whenever a
// span is in flight.
func TestTraceFromCtx_PrefersLiveSpanContext(t *testing.T) {
	tid, err := oteltrace.TraceIDFromHex("1234567890abcdef1234567890abcdef")
	require.NoError(t, err)
	sid, err := oteltrace.SpanIDFromHex("abcdef0123456789")
	require.NoError(t, err)
	ctx := oteltrace.ContextWithSpanContext(context.Background(),
		oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: tid, SpanID: sid, TraceFlags: oteltrace.FlagsSampled,
		}))

	tp, _ := traceFromCtx(ctx)
	assert.Equal(t, "00-1234567890abcdef1234567890abcdef-abcdef0123456789-01", tp)
}

// TestWithTrace_BlankValuesNotStored asserts WithTrace only attaches
// non-blank trace headers, so a blank traceparent does not shadow the
// minted fallback.
func TestWithTrace_BlankValuesNotStored(t *testing.T) {
	// Blank/whitespace traceparent → not stored → fresh value minted.
	ctx := WithTrace(context.Background(), "   ", "  ")
	tp, ts := traceFromCtx(ctx)
	assert.NotEqual(t, "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00", tp,
		"blank traceparent must not fall through to the W3C example constant")
	assert.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`, tp)
	assert.Empty(t, ts)

	// Real values are stored and retrieved.
	ctx2 := WithTrace(context.Background(), tTraceparent, tTracestate)
	tp2, ts2 := traceFromCtx(ctx2)
	assert.Equal(t, tTraceparent, tp2)
	assert.Equal(t, tTracestate, ts2)

	// tracestate stored independently of traceparent.
	ctx3 := WithTrace(context.Background(), tTraceparent, "")
	_, ts3 := traceFromCtx(ctx3)
	assert.Empty(t, ts3)
}
