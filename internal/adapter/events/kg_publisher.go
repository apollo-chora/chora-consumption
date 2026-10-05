// kg_publisher.go — adapter implementation of
// userknowledgegraph.EventPublisher backed by the in-memory Publisher.
//
// Per ddd-enforcement HARD RULE: cross-DB queries forbidden — all
// inter-domain side effects flow through Pub/Sub events. The EventPublisher
// port is the boundary; this adapter materialises domain calls into
// envelopes + payloads matching the asyncapi/proto schemas declared in
// chora-contracts.
//
// Hexagonal: domain code never imports this package. cmd/server wires
// it (or tests inject a stub).
package events

import (
	"context"
	"errors"
	"strings"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/apollo-chora/chora-common/tracing"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// KGPublisher implements userknowledgegraph.EventPublisher. It is a thin
// translator between domain aggregates and the Publisher port. Each
// method derives an idempotency key from stable aggregate fields so
// retries don't produce duplicate side-effects downstream.
type KGPublisher struct {
	pub Publisher
}

// NewKGPublisher constructs a KG event publisher around a generic
// publisher (in-memory for tests, Pub/Sub adapter in production).
func NewKGPublisher(p Publisher) *KGPublisher {
	return &KGPublisher{pub: p}
}

// traceFromCtx retrieves W3C trace context from context.Context. The
// `traceparent`/`tracestate` keys mirror the http handler values.
//
// When the caller did not stamp one explicitly, the id is derived from the
// LIVE OTel span context, the span Cloud Trace actually exported for the
// in-flight request, then from the tracing package's context value, and
// only as a last resort from a freshly minted crypto/rand root.
//
// ⚠ A literal "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00" used
// to sit here: the traceparent printed as an EXAMPLE in the W3C spec. Being
// a constant, every envelope published without an explicit trace collapsed
// onto one shared trace id, putting 99 unrelated spans on a single Cloud
// Trace. Fixed 2026-08-07 (FINDING-02). Any fallback here must be UNIQUE per
// call, never a literal.
func traceFromCtx(ctx context.Context) (string, string) {
	tp, _ := ctx.Value(ctxTraceparent).(string)
	ts, _ := ctx.Value(ctxTracestate).(string)
	if tp != "" {
		return tp, ts
	}
	if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
		return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String(), ts
	}
	return tracing.EnsureTraceparent(tracing.TraceparentFromContext(ctx)), ts
}

// Context keys for trace propagation. Exported helpers keep callers
// from importing internal symbols.
type ctxKey int

const (
	ctxTraceparent ctxKey = iota
	ctxTracestate
)

// WithTrace returns a derived context carrying the W3C trace headers so
// downstream EventPublisher calls pick them up. Used by HTTP handlers
// before calling into the application service.
func WithTrace(ctx context.Context, traceparent, tracestate string) context.Context {
	if strings.TrimSpace(traceparent) != "" {
		ctx = context.WithValue(ctx, ctxTraceparent, traceparent)
	}
	if strings.TrimSpace(tracestate) != "" {
		ctx = context.WithValue(ctx, ctxTracestate, tracestate)
	}
	return ctx
}

// PublishMapClusterCreated emits chora.consumption.kg_map_cluster.created.v1.
func (p *KGPublisher) PublishMapClusterCreated(ctx context.Context, c *userknowledgegraph.MapCluster) error {
	if c == nil {
		return errors.New("kg_publisher: nil cluster")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(c.TenantID, c.UserGCID, tp, ts, "kg_cluster_created:"+c.ClusterID)
	return p.pub.Publish(TopicKGMapClusterCreated, env, map[string]any{
		"cluster_id":           c.ClusterID,
		"tenant_id":            c.TenantID,
		"user_gcid":            c.UserGCID,
		"display_name":         c.DisplayName,
		"seed_topic":           c.SeedTopic,
		"seed_atom_id":         c.SeedAtomID,
		"status":               string(c.Status),
		"node_count":           c.NodeCount,
		"created_at":           c.CreatedAt,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	})
}

// PublishMapClusterMerged emits chora.consumption.kg_map_cluster.merged.v1.
func (p *KGPublisher) PublishMapClusterMerged(ctx context.Context, surviving, merged *userknowledgegraph.MapCluster, viaAtomID string) error {
	if surviving == nil || merged == nil {
		return errors.New("kg_publisher: nil cluster on merge")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(surviving.TenantID, surviving.UserGCID, tp, ts, "kg_cluster_merged:"+merged.ClusterID)
	return p.pub.Publish(TopicKGMapClusterMerged, env, map[string]any{
		"surviving_cluster_id": surviving.ClusterID,
		"merged_cluster_id":    merged.ClusterID,
		"via_atom_id":          viaAtomID,
		"new_display_name":     surviving.DisplayName,
		"surviving_node_count": surviving.NodeCount,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	})
}

// PublishMapClusterArchived emits chora.consumption.kg_map_cluster.archived.v1.
func (p *KGPublisher) PublishMapClusterArchived(ctx context.Context, c *userknowledgegraph.MapCluster) error {
	if c == nil {
		return errors.New("kg_publisher: nil cluster")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(c.TenantID, c.UserGCID, tp, ts, "kg_cluster_archived:"+c.ClusterID)
	return p.pub.Publish(TopicKGMapClusterArchived, env, map[string]any{
		"cluster_id":           c.ClusterID,
		"tenant_id":            c.TenantID,
		"user_gcid":            c.UserGCID,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	})
}

// PublishExplorationCreated emits chora.consumption.kg_exploration.created.v1.
func (p *KGPublisher) PublishExplorationCreated(ctx context.Context, e *userknowledgegraph.Exploration) error {
	if e == nil {
		return errors.New("kg_publisher: nil exploration")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(e.TenantID, e.UserGCID, tp, ts, "kg_exp_created:"+e.ExplorationID)
	return p.pub.Publish(TopicKGExplorationCreated, env, map[string]any{
		"exploration_id":        e.ExplorationID,
		"cluster_id":            e.ClusterID,
		"started_at_atom_id":    e.StartedAtAtomID,
		"current_focal_atom_id": e.CurrentFocalAtomID,
	})
}

// PublishExplorationFocalChanged emits the focal-change event.
func (p *KGPublisher) PublishExplorationFocalChanged(ctx context.Context, e *userknowledgegraph.Exploration, hop *userknowledgegraph.TrailHop) error {
	if e == nil || hop == nil {
		return errors.New("kg_publisher: nil exploration/hop")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(e.TenantID, e.UserGCID, tp, ts, "kg_focal_changed:"+e.ExplorationID+":"+hop.TrailID)
	return p.pub.Publish(TopicKGExplorationFocalChng, env, map[string]any{
		"exploration_id":     e.ExplorationID,
		"cluster_id":         e.ClusterID,
		"trail_id":           hop.TrailID,
		"from_focal_atom_id": hop.FromFocalAtomID,
		"to_focal_atom_id":   hop.ToFocalAtomID,
		"step_index":         hop.StepIndex,
		"relation":           string(hop.Relation),
	})
}

// PublishExplorationArchived emits the archived event.
func (p *KGPublisher) PublishExplorationArchived(ctx context.Context, e *userknowledgegraph.Exploration) error {
	if e == nil {
		return errors.New("kg_publisher: nil exploration")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(e.TenantID, e.UserGCID, tp, ts, "kg_exp_archived:"+e.ExplorationID)
	return p.pub.Publish(TopicKGExplorationArchived, env, map[string]any{
		"exploration_id": e.ExplorationID,
		"cluster_id":     e.ClusterID,
	})
}

// PublishHexagonFogGenerated emits the fog generation event with cost
// data for the AI Kernel ledger.
func (p *KGPublisher) PublishHexagonFogGenerated(ctx context.Context, h *userknowledgegraph.HexagonNode, fog *userknowledgegraph.FogResult) error {
	if h == nil || fog == nil {
		return errors.New("kg_publisher: nil hexagon/fog")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(h.TenantID, h.UserGCID, tp, ts, "kg_fog_generated:"+h.HexNodeID)

	// Materialise the persisted hexagon's neighbours into the event's repeated
	// HexagonNeighbor shape so the AI-Kernel cost ledger + Observability
	// consumers receive the full fog (relation / confidence / fog_label /
	// is_junction), not just cost telemetry. Sourced from the aggregate `h`
	// (validated to exactly 6 by NewHexagonNode) so user_gcid / neighbors /
	// generated_at stay internally consistent.
	neighbors := make([]map[string]any, 0, len(h.Neighbors))
	for _, n := range h.Neighbors {
		neighbors = append(neighbors, map[string]any{
			"atom_id":     n.AtomID,
			"relation":    string(n.Relation),
			"confidence":  n.Confidence,
			"fog_label":   n.FogLabel,
			"is_junction": n.IsJunction,
		})
	}

	return p.pub.Publish(TopicKGHexagonFogGenerated, env, map[string]any{
		"exploration_id":        h.ExplorationID,
		"cluster_id":            h.ClusterID,
		"user_gcid":             h.UserGCID,
		"focal_atom_id":         h.FocalAtomID,
		"neighbors":             neighbors,
		"generated_by_run_id":   h.GeneratedByRunID,
		"generated_by_model_id": h.GeneratedByModelID,
		"total_cost_micros":     fog.TotalCostMicros,
		"input_tokens":          fog.InputTokens,
		"output_tokens":         fog.OutputTokens,
		"generated_at":          h.GeneratedAt,
		"chora_imda_dimension":  "safety_and_robustness",
		"imda_lifecycle_stage":  "runtime",
	})
}

// PublishHexagonFogInvalidated emits the fog invalidation event.
func (p *KGPublisher) PublishHexagonFogInvalidated(ctx context.Context, h *userknowledgegraph.HexagonNode) error {
	if h == nil {
		return errors.New("kg_publisher: nil hexagon")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(h.TenantID, h.UserGCID, tp, ts, "kg_fog_invalidated:"+h.HexNodeID)
	return p.pub.Publish(TopicKGHexagonFogInvalidated, env, map[string]any{
		"hex_node_id":         h.HexNodeID,
		"exploration_id":      h.ExplorationID,
		"focal_atom_id":       h.FocalAtomID,
		"invalidation_reason": string(h.InvalidationReason),
	})
}

// PublishJunctionDetected emits the auto-detected join opportunity event.
func (p *KGPublisher) PublishJunctionDetected(ctx context.Context, current, other *userknowledgegraph.MapCluster, viaAtomID, triggeringRunID string) error {
	if current == nil || other == nil {
		return errors.New("kg_publisher: nil cluster on junction-detected")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(current.TenantID, current.UserGCID, tp, ts, "kg_junction_detected:"+current.ClusterID+":"+other.ClusterID+":"+viaAtomID)
	return p.pub.Publish(TopicKGJunctionDetected, env, map[string]any{
		"current_cluster_id":      current.ClusterID,
		"other_cluster_id":        other.ClusterID,
		"current_cluster_display": current.DisplayName,
		"other_cluster_display":   other.DisplayName,
		"via_atom_id":             viaAtomID,
		"triggering_run_id":       triggeringRunID,
		"chora_imda_dimension":    "accountability",
		"imda_lifecycle_stage":    "runtime",
	})
}

// PublishJunctionAccepted emits the user-consented merge event.
func (p *KGPublisher) PublishJunctionAccepted(ctx context.Context, j *userknowledgegraph.Junction) error {
	if j == nil {
		return errors.New("kg_publisher: nil junction")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(j.TenantID, j.UserGCID, tp, ts, "kg_junction_accepted:"+j.JunctionID)
	return p.pub.Publish(TopicKGJunctionAccepted, env, map[string]any{
		"junction_id":          j.JunctionID,
		"cluster_a_id":         j.ClusterAID,
		"cluster_b_id":         j.ClusterBID,
		"resolved_via_atom_id": j.ResolvedViaAtomID,
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	})
}

// PublishJunctionRejected emits the user-decline event.
func (p *KGPublisher) PublishJunctionRejected(ctx context.Context, j *userknowledgegraph.Junction) error {
	if j == nil {
		return errors.New("kg_publisher: nil junction")
	}
	tp, ts := traceFromCtx(ctx)
	env := NewEnvelope(j.TenantID, j.UserGCID, tp, ts, "kg_junction_rejected:"+j.JunctionID)
	return p.pub.Publish(TopicKGJunctionRejected, env, map[string]any{
		"junction_id":  j.JunctionID,
		"cluster_a_id": j.ClusterAID,
		"cluster_b_id": j.ClusterBID,
	})
}
