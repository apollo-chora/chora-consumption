// gateway_embedding_client_test.go: G1' gap 1 (register 6.2): the F4 memory
// embedder rides the gateway Embed RPC so every embedding produces a ledger
// row. Wire-realistic assertions: every REQUIRED EmbedRequest field must be
// non-empty on the captured request, because a template keyed on a field the
// wire never carries passes hand-filled tests forever.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-common/agentengine"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"

	gatewayv1 "github.com/apollo-chora/chora-consumption/internal/adapter/clients/gatewayv1"
)

// fakeEmbedGRPC captures the outbound EmbedRequest and plays back a canned
// response or error. It satisfies the client's private gRPC seam.
type fakeEmbedGRPC struct {
	lastReq *gatewayv1.EmbedRequest
	lastMD  metadata.MD
	calls   int
	resp    *gatewayv1.EmbedResponse
	err     error
}

func (f *fakeEmbedGRPC) Embed(ctx context.Context, in *gatewayv1.EmbedRequest, _ ...grpc.CallOption) (*gatewayv1.EmbedResponse, error) {
	f.calls++
	f.lastReq = in
	f.lastMD, _ = metadata.FromOutgoingContext(ctx)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func okEmbedResponse() *gatewayv1.EmbedResponse {
	// Every pgvector column this service writes is vector(1024), so the
	// fixture carries the full width the client now requires.
	vec := make([]float64, gatewayEmbedOutputDimensions)
	vec[0], vec[1], vec[2] = 0.25, -0.5, 0.75
	return &gatewayv1.EmbedResponse{
		InvocationId:  "0198f000-0000-7000-8000-000000000001",
		UpstreamModel: "embedding-route-v1",
		Data:          []*gatewayv1.Embedding{{Index: 0, Embedding: vec}},
	}
}

func newStubClient(t *testing.T, fake embedGRPCClient) *GatewayEmbeddingClient {
	t.Helper()
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
	if len(got) != gatewayEmbedOutputDimensions || got[0] != 0.25 {
		t.Fatalf("vector not passed through: len=%d first=%v", len(got), got[:2])
	}
	if fake.calls != 1 {
		t.Fatalf("expected exactly 1 RPC, got %d", fake.calls)
	}

	req := fake.lastReq
	// The gateway's EmbedRequest claims no tenant, gcid or agent — the
	// credential is the tenant — so none are sent. The learner's gcid rides in
	// `user`, the one identity-shaped field the request has.
	if req.GetUser() != "0198bbbb-0000-7000-8000-0000000000bb" {
		t.Errorf("user = %q, want the learner's gcid", req.GetUser())
	}
	if len(req.GetInput()) != 1 || req.GetInput()[0] != "the learner asked about fractions" {
		t.Errorf("input = %q", req.GetInput())
	}
	if req.GetDimensions() != 1024 {
		t.Errorf("dimensions = %d (must match the vector(1024) columns)", req.GetDimensions())
	}
	if req.GetModel() != "" {
		t.Errorf("model = %q, want empty (the gateway resolves the operation)", req.GetModel())
	}

	// crew_kind and task_type have no EmbedRequest field; both are ledger
	// bookkeeping rather than routing inputs.
	var meta map[string]any
	if err := json.Unmarshal(req.GetMetadataJson(), &meta); err != nil {
		t.Fatalf("metadata_json is not valid JSON: %v (bytes: %q)", err, req.GetMetadataJson())
	}
	if meta["crew_kind"] != "companion" {
		t.Errorf("crew_kind = %v", meta["crew_kind"])
	}
	if meta["task_type"] != "RETRIEVAL_QUERY" {
		t.Errorf("task_type = %v (passthrough broken)", meta["task_type"])
	}

	// traceparent has no EmbedRequest field; it travels as gRPC metadata.
	if got := fake.lastMD.Get("traceparent"); len(got) != 1 || got[0] != "00-11111111111111111111111111111111-2222222222222222-01" {
		t.Errorf("traceparent metadata = %v", got)
	}
}

func TestGatewayEmbeddingClient_EmptyTaskTypeDefaultsToDocument(t *testing.T) {
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
	var meta map[string]any
	if err := json.Unmarshal(fake.lastReq.GetMetadataJson(), &meta); err != nil {
		t.Fatalf("metadata_json: %v", err)
	}
	if meta["task_type"] != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("task_type = %v, want RETRIEVAL_DOCUMENT", meta["task_type"])
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

	// Tenant present, gcid deliberately NOT stamped: the caller refuses first,
	// loudly, without fabricating an actor.
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
		{"invalid argument to invalid request", status.Error(codes.InvalidArgument, "bad request"), agentengine.ErrInvalidRequest},
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

// Every pgvector column this service writes is vector(1024), so a vector of
// the wrong width must be refused before it reaches the write path.
func TestGatewayEmbeddingClient_RejectsMalformedResponses(t *testing.T) {
	short := okEmbedResponse()
	short.Data[0].Embedding = short.Data[0].Embedding[:8]

	wrongIndex := okEmbedResponse()
	wrongIndex.Data[0].Index = 3

	noModel := okEmbedResponse()
	noModel.UpstreamModel = "  "

	nonFinite := okEmbedResponse()
	nonFinite.Data[0].Embedding[7] = math.Inf(1)

	cases := []struct {
		name string
		resp *gatewayv1.EmbedResponse
	}{
		{"empty response", nil},
		{"no data", &gatewayv1.EmbedResponse{InvocationId: "x"}},
		{"wrong width", short},
		{"wrong index", wrongIndex},
		{"no upstream model", noModel},
		{"non-finite value", nonFinite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEmbedGRPC{resp: tc.resp}
			c := newStubClient(t, fake)
			if _, err := c.Embed(stampedCtx(t), companion.EmbedInput{
				Text:     "hello",
				TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
			}); err == nil {
				t.Fatal("expected an error for a malformed embedding response")
			}
		})
	}
}

// ModelID is the upstream model that answered the most recent Embed. The
// gateway resolves which model serves the operation, so it is empty until the
// first call — there is no caller-side value to report before that.
func TestGatewayEmbeddingClient_ModelIDTracksTheAnsweringModel(t *testing.T) {
	fake := &fakeEmbedGRPC{resp: okEmbedResponse()}
	c := newStubClient(t, fake)
	if got := c.ModelID(); got != "" {
		t.Fatalf("ModelID() = %q before any call, want empty", got)
	}
	if _, err := c.Embed(stampedCtx(t), companion.EmbedInput{
		Text:     "hello",
		TenantID: "0198aaaa-0000-7000-8000-0000000000aa",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := c.ModelID(); got != "embedding-route-v1" {
		t.Fatalf("ModelID() = %q, want the upstream model from the response", got)
	}
}

func TestNewGatewayEmbeddingClient_EmptyTargetFailsLoud(t *testing.T) {
	if _, err := NewGatewayEmbeddingClient("  ", 0); err == nil {
		t.Fatal("expected constructor error on empty target")
	} else if !strings.Contains(err.Error(), "CHORA_MODEL_GATEWAY_GRPC_URL") {
		t.Fatalf("error must name the env var to set: %v", err)
	}
}

// The adapter must satisfy the untouched domain port.
var _ companion.Embedder = (*GatewayEmbeddingClient)(nil)
