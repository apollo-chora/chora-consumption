// gateway_embedding_client.go: G1' gap 1 (register 6.2): the F4 per-Companion
// memory embedder, riding chora-model-gateway's Embed RPC instead of dialing
// Vertex directly. Every embedding now produces a TokenUsageLedger row at the
// single un-bypassable chokepoint (ADR-163); the calling identity enters the
// guardrail contract at `permissive` (no Armor leg on text-to-vector, ledger
// markers Bypassed, zero cost by ruling).
//
// GatewayEmbeddingClient implements the UNCHANGED companion.Embedder port. The
// dial shape mirrors grounded_search_gateway_client.go: plaintext gRPC to the
// mesh Service from env CHORA_MODEL_GATEWAY_GRPC_URL (the sidecar does mTLS),
// grpc.NewClient lazy dial, a minimal private client seam for tests.
//
// No model ID is sent. The gateway resolves the embeddings operation from the
// RPC method and serves it from that operation's own configured routes, so the
// caller no longer names a model and EMBEDDING_LOGICAL_MODEL_ID is gone. The
// model that actually answered is recorded from the response.
//
// The gateway's EmbedRequest claims no tenant, gcid or agent — the credential
// is the tenant — so none are sent. The caller still validates tenant + gcid
// locally, because the ledger row it writes is attributed to the learner.
//
// Error contract: parity with the retired VertexEmbeddingClient, the same
// agentengine sentinels, so every caller's soft-fail handling is untouched
// (memory stays supplementary, never load-bearing for a chat turn).
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gatewayv1 "github.com/apollo-chora/chora-consumption/internal/adapter/clients/gatewayv1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ErrGatewayEmbedEmptyTarget fails construction when the gRPC target is empty.
// Boot wiring treats an unset URL as "F4 memory disabled" BEFORE constructing
// this client, so reaching this error means a wiring bug, not a config choice.
var ErrGatewayEmbedEmptyTarget = errors.New("gateway_embedding_client: target empty (set CHORA_MODEL_GATEWAY_GRPC_URL)")

const (
	// gatewayEmbedDefaultTimeout bounds one embedding round-trip. Embeds are
	// fast single-vector calls; 10s is generous headroom over the gateway's
	// own vendor timeout without holding a chat turn hostage.
	gatewayEmbedDefaultTimeout = 10 * time.Second

	// gatewayEmbedAgentID is the fixed calling-adapter identity on the ledger
	// row (AgentRole) and in agent-guardrail-mapping.yaml (permissive entry).
	gatewayEmbedAgentID = "consumption_memory_embedder"

	// gatewayEmbedCrewKind classifies the caller for cost dashboards.
	gatewayEmbedCrewKind = "companion"

	// gatewayEmbedOutputDimensions matches every pgvector(1024) column this
	// service writes — the configured embedding route's native width. Sent
	// explicitly, never left to a remote default.
	gatewayEmbedOutputDimensions = 1024
)

// embedGRPCClient is the minimal slice of gatewayv1.ModelGatewayServiceClient the
// adapter calls (Embed only). The generated client satisfies it; tests inject
// a fake.
type embedGRPCClient interface {
	Embed(ctx context.Context, in *gatewayv1.EmbedRequest, opts ...grpc.CallOption) (*gatewayv1.EmbedResponse, error)
}

// GatewayEmbeddingClient is the production companion.Embedder implementation.
type GatewayEmbeddingClient struct {
	client  embedGRPCClient
	timeout time.Duration

	// lastModel is the upstream model that answered the most recent Embed.
	// The gateway resolves which model serves the operation, so the caller
	// cannot know it before a call.
	mu        sync.Mutex
	lastModel string
}

// Compile-time guarantee the adapter satisfies the unchanged domain port.
var _ companion.Embedder = (*GatewayEmbeddingClient)(nil)

// NewGatewayEmbeddingClient dials the gateway ModelGatewayService at the mesh
// target. An empty target fails loud (ErrGatewayEmbedEmptyTarget); a
// non-positive timeout falls back to gatewayEmbedDefaultTimeout.
// grpc.NewClient is lazy, so a healthy return does not guarantee reachability;
// the first RPC surfaces Unavailable, which callers soft-fail.
func NewGatewayEmbeddingClient(target string, timeout time.Duration) (*GatewayEmbeddingClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, ErrGatewayEmbedEmptyTarget
	}
	if timeout <= 0 {
		timeout = gatewayEmbedDefaultTimeout
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gateway_embedding_client: dial %q: %w", target, err)
	}
	return &GatewayEmbeddingClient{client: gatewayv1.NewModelGatewayServiceClient(conn), timeout: timeout}, nil
}

// NewGatewayEmbeddingClientFromStub injects a fake gRPC client (tests).
func NewGatewayEmbeddingClientFromStub(stub embedGRPCClient, timeout time.Duration) *GatewayEmbeddingClient {
	if timeout <= 0 {
		timeout = gatewayEmbedDefaultTimeout
	}
	return &GatewayEmbeddingClient{client: stub, timeout: timeout}
}

// ModelID reports the embedding model recorded beside each pgvector row
// (Server.CompanionEmbeddingModelID) for recall-compatibility checks.
//
// It is the upstream model that answered the most recent Embed, and is empty
// until the first one: the gateway resolves which model serves the operation,
// so there is no caller-side value to report before a call has been made.
func (c *GatewayEmbeddingClient) ModelID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastModel
}

func (c *GatewayEmbeddingClient) recordModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastModel = model
}

// Embed implements companion.Embedder over the gateway Embed RPC. The three
// identity fields the gateway requires are validated here first so a mis-wired
// call site fails loud, with the fix named, before any RPC is attempted.
func (c *GatewayEmbeddingClient) Embed(ctx context.Context, in companion.EmbedInput) ([]float32, string, error) {
	if c == nil || c.client == nil {
		return nil, "", agentengine.ErrEngineNotConfigured
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, "", fmt.Errorf("%w: embed text required", agentengine.ErrInvalidRequest)
	}
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(tracing.TenantIDFromContext(ctx))
	}
	if tenantID == "" {
		return nil, "", fmt.Errorf("%w: tenant required for ledger attribution (set EmbedInput.TenantID or stamp ctx via tracing.WithTenantID)", agentengine.ErrInvalidRequest)
	}
	gcid := strings.TrimSpace(tracing.GCIDFromContext(ctx))
	if gcid == "" {
		return nil, "", fmt.Errorf("%w: gcid required for ledger attribution (stamp ctx via tracing.WithGCID at the call site)", agentengine.ErrInvalidRequest)
	}

	// Parity with the retired direct-Vertex client: the task-type default is
	// applied BEFORE the wire, never left to a remote default.
	taskType := string(in.TaskType)
	if taskType == "" {
		taskType = string(companion.EmbedTaskDocument)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.Embed(withTraceparent(callCtx), &gatewayv1.EmbedRequest{
		Input:      []string{in.Text},
		Dimensions: proto.Int32(gatewayEmbedOutputDimensions),
		// The gateway's EmbedRequest claims no tenant, gcid or agent — the
		// credential is the tenant — so none are sent. The learner's gcid
		// rides in `user`, the one identity-shaped field the request has.
		User: gcid,
		// crew_kind and task_type have no EmbedRequest field; both are ledger
		// bookkeeping rather than routing inputs.
		MetadataJson: mustJSON(map[string]string{
			"crew_kind": gatewayEmbedCrewKind,
			"task_type": taskType,
		}),
	})
	if err != nil {
		return nil, "", mapEmbedRPCError(err)
	}
	vec, model, err := firstVector(resp)
	if err != nil {
		return nil, "", err
	}
	return vec, model, nil
}

// firstVector returns the response's vector for input index 0, converted to
// float32 — the protobuf field is float64, the companion port is float32.
//
// Every pgvector column this service writes is vector(1024), so a vector of
// the wrong width is refused here rather than reaching the write path. A
// missing or blank upstream model is refused too: stamping a vector with no
// model name would make the label a lie about what produced it.
func firstVector(resp *gatewayv1.EmbedResponse) ([]float32, string, error) {
	if resp == nil {
		return nil, "", fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	model := strings.TrimSpace(resp.GetUpstreamModel())
	if model == "" {
		return nil, "", fmt.Errorf("%w: gateway did not report which model answered", agentengine.ErrStreamAborted)
	}
	data := resp.GetData()
	if len(data) == 0 {
		return nil, "", fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	// This call sent exactly one input, so exactly one vector must come back.
	if len(data) != 1 {
		return nil, "", fmt.Errorf("%w: gateway returned %d vectors for 1 input", agentengine.ErrStreamAborted, len(data))
	}
	// The gateway places each vector at the index the caller's input holds, so
	// index 0 is the single text this call sent.
	if data[0].GetIndex() != 0 {
		return nil, "", fmt.Errorf("%w: gateway returned a vector for input index %d, want 0", agentengine.ErrStreamAborted, data[0].GetIndex())
	}
	src := data[0].GetEmbedding()
	if len(src) != gatewayEmbedOutputDimensions {
		return nil, "", fmt.Errorf("%w: gateway returned %d dimensions, want %d", agentengine.ErrStreamAborted, len(src), gatewayEmbedOutputDimensions)
	}
	out := make([]float32, len(src))
	for i, v := range src {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, "", fmt.Errorf("%w: gateway returned a non-finite embedding value at %d", agentengine.ErrStreamAborted, i)
		}
		// Check the CONVERTED value: a finite float64 outside float32's range
		// becomes an infinity on conversion, and that infinity would reach the
		// vector column.
		f := float32(v)
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, "", fmt.Errorf("%w: embedding value at %d is not representable as float32", agentengine.ErrStreamAborted, i)
		}
		out[i] = f
	}
	return out, model, nil
}

// withTraceparent moves the traceparent onto the gRPC metadata. The gateway's
// Embed RPC has no traceparent field; trace context travels as the
// `traceparent` metadata key, which is where the OTel interceptor reads it.
//
// AppendToOutgoingContext appends rather than replaces, so an existing value is
// left alone rather than sent twice, and an empty value is never emitted —
// empty trace metadata is worse than none.
func withTraceparent(ctx context.Context) context.Context {
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get("traceparent")) > 0 {
		return ctx
	}
	traceparent := strings.TrimSpace(TraceparentFromContext(ctx))
	if traceparent == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "traceparent", traceparent)
}

// mustJSON marshals caller bookkeeping into the metadata_json payload. It
// panics on failure: the input is a fixed map of strings, so a failure would be
// a programming error rather than a condition to handle.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("clients: marshal metadata: %v", err))
	}
	return b
}

// mapEmbedRPCError translates gRPC status codes onto the canonical agentengine
// sentinels the retired direct-Vertex client used, preserving every caller's
// soft-fail contract.
func mapEmbedRPCError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
	switch st.Code() {
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineTimeout, err)
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %v", agentengine.ErrInvalidRequest, err)
	default:
		return fmt.Errorf("%w: %v", agentengine.ErrEngineUnavailable, err)
	}
}
