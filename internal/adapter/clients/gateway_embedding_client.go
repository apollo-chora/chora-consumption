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
// Identity on the wire (all REQUIRED by the gateway, refused loud here first):
//   - tenant_id: EmbedInput.TenantID, falling back to the shared tracing ctx
//     key (tracing.WithTenantID stamps it at every handler entry).
//   - gcid: tracing.GCIDFromContext(ctx) ONLY. The adapter never fabricates an
//     actor; an unstamped call site is a bug named loudly by the error.
//   - agent_id: consumption_memory_embedder (fixed adapter identity).
//
// The logical model is PINNED client-side ("text-embedding-004") and ModelID()
// returns the same constant, so the label recorded beside each pgvector row
// can never drift from what was requested, even if the gateway default moves.
//
// Error contract: parity with the retired VertexEmbeddingClient, the same
// agentengine sentinels, so every caller's soft-fail handling is untouched
// (memory stays supplementary, never load-bearing for a chat turn).
package clients

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain"
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

	// gatewayEmbedDefaultLogicalModelID is the default embedding model
	// requested on the wire. Override with EMBEDDING_LOGICAL_MODEL_ID.
	gatewayEmbedDefaultLogicalModelID = "text-embedding-004"

	// gatewayEmbedOutputDimensions matches every pgvector(1024) column this
	// service writes — the LiquidAI LFM2.5 embedding route's native width (see
	// the registry's `text-embedding-004` entry). Sent explicitly, never left
	// to a remote default.
	gatewayEmbedOutputDimensions = 1024
)

// embedGRPCClient is the minimal slice of mgv1.ModelGatewayServiceClient the
// adapter calls (Embed only). The generated client satisfies it; tests inject
// a fake.
type embedGRPCClient interface {
	Embed(ctx context.Context, in *mgv1.EmbedRequest, opts ...grpc.CallOption) (*mgv1.EmbedResponse, error)
}

// GatewayEmbeddingClient is the production companion.Embedder implementation.
type GatewayEmbeddingClient struct {
	client  embedGRPCClient
	timeout time.Duration
	modelID string
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
	modelID := strings.TrimSpace(os.Getenv("EMBEDDING_LOGICAL_MODEL_ID"))
	if modelID == "" {
		modelID = gatewayEmbedDefaultLogicalModelID
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gateway_embedding_client: dial %q: %w", target, err)
	}
	return &GatewayEmbeddingClient{client: mgv1.NewModelGatewayServiceClient(conn), timeout: timeout, modelID: modelID}, nil
}

// NewGatewayEmbeddingClientFromStub injects a fake gRPC client (tests).
func NewGatewayEmbeddingClientFromStub(stub embedGRPCClient, timeout time.Duration) *GatewayEmbeddingClient {
	if timeout <= 0 {
		timeout = gatewayEmbedDefaultTimeout
	}
	modelID := strings.TrimSpace(os.Getenv("EMBEDDING_LOGICAL_MODEL_ID"))
	if modelID == "" {
		modelID = gatewayEmbedDefaultLogicalModelID
	}
	return &GatewayEmbeddingClient{client: stub, timeout: timeout, modelID: modelID}
}

// ModelID reports the logical embedding model, recorded beside each
// pgvector row (Server.CompanionEmbeddingModelID) for recall-compatibility
// checks.
func (c *GatewayEmbeddingClient) ModelID() string {
	return c.modelID
}

// Embed implements companion.Embedder over the gateway Embed RPC. The three
// identity fields the gateway requires are validated here first so a mis-wired
// call site fails loud, with the fix named, before any RPC is attempted.
func (c *GatewayEmbeddingClient) Embed(ctx context.Context, in companion.EmbedInput) ([]float32, error) {
	if c == nil || c.client == nil {
		return nil, agentengine.ErrEngineNotConfigured
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("%w: embed text required", agentengine.ErrInvalidRequest)
	}
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(tracing.TenantIDFromContext(ctx))
	}
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant required for ledger attribution (set EmbedInput.TenantID or stamp ctx via tracing.WithTenantID)", agentengine.ErrInvalidRequest)
	}
	gcid := strings.TrimSpace(tracing.GCIDFromContext(ctx))
	if gcid == "" {
		return nil, fmt.Errorf("%w: gcid required for ledger attribution (stamp ctx via tracing.WithGCID at the call site)", agentengine.ErrInvalidRequest)
	}

	// Parity with the retired direct-Vertex client: the task-type default is
	// applied BEFORE the wire, never left to a remote default.
	taskType := string(in.TaskType)
	if taskType == "" {
		taskType = string(companion.EmbedTaskDocument)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.Embed(callCtx, &mgv1.EmbedRequest{
		InvocationId:     domain.NewUUIDv7(),
		TenantId:         tenantID,
		Gcid:             gcid,
		AgentId:          gatewayEmbedAgentID,
		CrewKind:         gatewayEmbedCrewKind,
		LogicalModelId:   c.modelID,
		Text:             in.Text,
		TaskType:         taskType,
		OutputDimensions: gatewayEmbedOutputDimensions,
		Traceparent:      TraceparentFromContext(ctx),
	})
	if err != nil {
		return nil, mapEmbedRPCError(err)
	}
	if resp == nil || len(resp.GetValues()) == 0 {
		return nil, fmt.Errorf("%w: empty embedding response from gateway", agentengine.ErrStreamAborted)
	}
	return resp.GetValues(), nil
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
