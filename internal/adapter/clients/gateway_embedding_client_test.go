// gateway_embedding_client_test.go: G1' gap 1 (register 6.2): the F4 memory
// embedder rides the gateway Embed RPC so every embedding produces a ledger
// row. Wire-realistic assertions: every REQUIRED EmbedRequest field must be
// non-empty on the captured request, because a template keyed on a field the
// wire never carries passes hand-filled tests forever.
package clients

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mgv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/model_gateway/v1"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// fakeEmbedGRPC captures the outbound EmbedRequest and plays back a canned
// response or error. It satisfies the client's private gRPC seam.
type fakeEmbedGRPC struct {
	lastReq *mgv1.EmbedRequest
	calls   int
	resp    *mgv1.EmbedResponse
	err     error
}

func (f *fakeEmbedGRPC) Embed(_ context.Context, in *mgv1.EmbedRequest, _ ...grpc.CallOption) (*mgv1.EmbedResponse, error) {
	f.calls++
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func okEmbedResponse() *mgv1.EmbedResponse {
	return &mgv1.EmbedResponse{
		InvocationId: "0198f000-0000-7000-8000-000000000001",
		Values:       []float32{0.25, -0.5, 0.75},
		Vendor:       "vertex_ai_gemini",
		ModelVersion: "embedding-model-version",
	}
}

// configuredEmbeddingModelID is the logical model id the tests configure via
// EMBEDDING_LOGICAL_MODEL_ID. It is deliberately not a vendor model name: the id
// is env-provided in production, so the tests assert the CONFIGURED value — not a
// literal — is what reaches the wire.
const configuredEmbeddingModelID = "embedding-model-configured"

// newStubClient builds a client over the fake gRPC seam with the configured
// logical model id set the way production reads it (EMBEDDING_LOGICAL_MODEL_ID).
func newStubClient(t *testing.T, fake embedGRPCClient) *GatewayEmbeddingClient {
	t.Helper()
	t.Setenv("EMBEDDING_LOGICAL_MODEL_ID", configuredEmbeddingModelID)
	return NewGatewayEmbeddingClientFromStub(fake, 0)
}

// stampedCtx carries the values a real chat-handler call site stamps before
// reaching the adapter: tenant + gcid via the shared tracing keys, and the
// inbound W3C traceparent via the clients-package key.
func stampedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx := tracing.WithTenantID(context.Background(), "0198aaaa-0000-7000-8000-0000000000aa")
	ctx = tracing.WithGCID(ctx, "0198bbbb-0000-7000-8000-0000000000bb")
	return WithTraceparent(ctx, "00-11111111111111111111111111111111-2222222222222222-01")
}

func TestGatewayEmbeddingClient_SendsWireCompleteRequest(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	got, err := c.Embed(stampedCtx(t), companion.EmbedInput{
		Text:     "the learner asked about fractions",
		TaskType: companion.EmbedTaskQuery,
		TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 || got[0] != 0.25 {
		t.Fatalf("values not passed through, got %v", got)
	}
	if fake.calls != 1 {
		t.Fatalf("expected exactly 1 RPC, got %d", fake.calls)
	}

	req := fake.lastReq
	if req.GetTenantId() != "0198aaaa-0000-7000-8000-0000000000aa" {
		t.Errorf("tenant_id = %q", req.GetTenantId())
	}
	if req.GetGcid() != "0198bbbb-0000-7000-8000-0000000000bb" {
		t.Errorf("gcid = %q", req.GetGcid())
	}
	if req.GetAgentId() != "consumption_memory_embedder" {
		t.Errorf("agent_id = %q", req.GetAgentId())
	}
	if req.GetCrewKind() != "companion" {
		t.Errorf("crew_kind = %q", req.GetCrewKind())
	}
	if req.GetText() != "the learner asked about fractions" {
		t.Errorf("text = %q", req.GetText())
	}
	if req.GetTaskType() != "RETRIEVAL_QUERY" {
		t.Errorf("task_type = %q (passthrough broken)", req.GetTaskType())
	}
	if req.GetLogicalModelId() != configuredEmbeddingModelID {
		t.Errorf("logical_model_id = %q; want the configured %q (must not be empty)", req.GetLogicalModelId(), configuredEmbeddingModelID)
	}
	if req.GetOutputDimensions() != 1024 {
		t.Errorf("output_dimensions = %d (must match the vector(1024) columns)", req.GetOutputDimensions())
	}
	if strings.TrimSpace(req.GetInvocationId()) == "" {
		t.Errorf("invocation_id empty; ledger idempotency requires a caller UUIDv7")
	}
	if req.GetTraceparent() != "00-11111111111111111111111111111111-2222222222222222-01" {
		t.Errorf("traceparent = %q", req.GetTraceparent())
	}
}

func TestGatewayEmbeddingClient_EmptyTaskTypeDefaultsToDocumentClientSide(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	if _, err := c.Embed(stampedCtx(t), companion.EmbedInput{
		Text:     "stored memory turn",
		TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Parity with the old VertexEmbeddingClient: the default is applied BEFORE
	// the wire, never left to a remote default that could drift.
	if fake.lastReq.GetTaskType() != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("task_type = %q, want RETRIEVAL_DOCUMENT", fake.lastReq.GetTaskType())
	}
}

func TestGatewayEmbeddingClient_TenantFallsBackToContext(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	if _, err := c.Embed(stampedCtx(t), companion.EmbedInput{Text: "hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if fake.lastReq.GetTenantId() != "0198aaaa-0000-7000-8000-0000000000aa" {
		t.Fatalf("tenant_id = %q, want the ctx-stamped tenant", fake.lastReq.GetTenantId())
	}
}

func TestGatewayEmbeddingClient_RefusesEmptyText(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	_, err := c.Embed(stampedCtx(t), companion.EmbedInput{Text: "   "})
	if err == nil {
		t.Fatal("expected error for empty text")
	}
	if !errors.Is(err, agentengine.ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	if !strings.Contains(err.Error(), "text") {
		t.Fatalf("error does not name the missing field: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("RPC must not fire on refusal, got %d calls", fake.calls)
	}
}

func TestGatewayEmbeddingClient_RefusesMissingGCID(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	// Tenant present, gcid deliberately NOT stamped: the gateway would refuse
	// this request, so the adapter refuses first, loudly, without fabricating
	// an actor.
	ctx := tracing.WithTenantID(context.Background(), "0198aaaa-0000-7000-8000-0000000000aa")
	_, err := c.Embed(ctx, companion.EmbedInput{Text: "hello", TenantID: "0198aaaa-0000-7000-8000-0000000000aa"})
	if err == nil {
		t.Fatal("expected error for missing gcid")
	}
	if !errors.Is(err, agentengine.ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	if !strings.Contains(err.Error(), "gcid") || !strings.Contains(err.Error(), "tracing.WithGCID") {
		t.Fatalf("error must name gcid and the fix (tracing.WithGCID): %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("RPC must not fire on refusal, got %d calls", fake.calls)
	}
}

func TestGatewayEmbeddingClient_RefusesMissingTenant(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)

	// gcid stamped, tenant absent from BOTH the input and the ctx.
	ctx := tracing.WithGCID(context.Background(), "0198bbbb-0000-7000-8000-0000000000bb")
	_, err := c.Embed(ctx, companion.EmbedInput{Text: "hello"})
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
	if !errors.Is(err, agentengine.ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	if !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("error does not name the missing field: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("RPC must not fire on refusal, got %d calls", fake.calls)
	}
}

func TestGatewayEmbeddingClient_MapsRPCErrorsToEngineSentinels(t *testing.T) {
	cases := []struct {
		name string
		rpc  error
		want error
	}{
		{"deadline to timeout", status.Error(codes.DeadlineExceeded, "deadline exceeded"), agentengine.ErrEngineTimeout},
		{"unavailable to unavailable", status.Error(codes.Unavailable, "connect refused"), agentengine.ErrEngineUnavailable},
		{"invalid argument to invalid request", status.Error(codes.InvalidArgument, "logical_model_id not an embedding model"), agentengine.ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEmbedGRPC{err: tc.rpc}
			c := newStubClient(t, fake)
			_, err := c.Embed(stampedCtx(t), companion.EmbedInput{
				Text:     "hello",
				TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want sentinel %v, got %v", tc.want, err)
			}
		})
	}
}

func TestGatewayEmbeddingClient_EmptyValuesIsLoud(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: &mgv1.EmbedResponse{InvocationId: "x", Values: nil}}
	c := newStubClient(t, fake)

	_, err := c.Embed(stampedCtx(t), companion.EmbedInput{
		Text:     "hello",
		TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
	})
	if err == nil {
		t.Fatal("expected error for empty values")
	}
	if !errors.Is(err, agentengine.ErrStreamAborted) {
		t.Fatalf("want ErrStreamAborted, got %v", err)
	}
}

func TestGatewayEmbeddingClient_NilResponseIsLoud(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: nil}
	c := newStubClient(t, fake)

	_, err := c.Embed(stampedCtx(t), companion.EmbedInput{
		Text:     "hello",
		TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
	})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
	if !errors.Is(err, agentengine.ErrStreamAborted) {
		t.Fatalf("want ErrStreamAborted, got %v", err)
	}
}

func TestGatewayEmbeddingClient_ModelIDComesFromEnv(t *testing.T) {
	c := newStubClient(t, &fakeEmbedGRPC{resp: okEmbedResponse()})
	if got := c.ModelID(); got != configuredEmbeddingModelID {
		t.Fatalf("ModelID() = %q; want the configured %q", got, configuredEmbeddingModelID)
	}
}

func TestNewGatewayEmbeddingClient_EmptyTargetFailsLoud(t *testing.T) {
	if _, err := NewGatewayEmbeddingClient("  ", 0); err == nil {
		t.Fatal("expected constructor error on empty target")
	} else if !strings.Contains(err.Error(), "CHORA_MODEL_GATEWAY_GRPC_URL") {
		t.Fatalf("error must name the env var to set: %v", err)
	}
}

func TestNewGatewayEmbeddingClient_EmptyModelIDFailsLoud(t *testing.T) {
	t.Setenv("EMBEDDING_LOGICAL_MODEL_ID", "")
	_, err := NewGatewayEmbeddingClient("model-gateway:9090", 0)
	if err == nil {
		t.Fatal("expected constructor error on blank EMBEDDING_LOGICAL_MODEL_ID")
	}
	if !errors.Is(err, ErrGatewayEmbedEmptyModelID) {
		t.Fatalf("want ErrGatewayEmbedEmptyModelID, got %v", err)
	}
	if !strings.Contains(err.Error(), "EMBEDDING_LOGICAL_MODEL_ID") {
		t.Fatalf("error must name the env var to set: %v", err)
	}
}

// The adapter must satisfy the untouched domain port.
var _ companion.Embedder = (*GatewayEmbeddingClient)(nil)
