// content_retrieval_client_test.go — W4: the ContentRetrieval gRPC client
// (stub-injected; no live creation service).
package clients

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
)

type fakeContentRetrievalStub struct {
	gotReq *creationv1.SearchEmbeddingsRequest
	resp   *creationv1.SearchEmbeddingsResponse
	err    error
}

func (f *fakeContentRetrievalStub) SearchEmbeddings(_ context.Context, req *creationv1.SearchEmbeddingsRequest, _ ...grpc.CallOption) (*creationv1.SearchEmbeddingsResponse, error) {
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestSearchByEmbedding_MapsMatches(t *testing.T) {
	stub := &fakeContentRetrievalStub{resp: &creationv1.SearchEmbeddingsResponse{
		Matches: []*creationv1.EmbeddingMatch{
			{AtomId: "a-1", CosineDistance: 0.1, Title: "T1"},
			{AtomId: "a-2", CosineDistance: 0.2, Title: "T2"},
			{AtomId: "  "}, // blank id dropped
		},
	}}
	c := NewContentRetrievalClientFromStub(stub)

	ids, err := c.SearchByEmbedding(context.Background(), "tnt-1", []float32{0.5, 0.25}, 5)
	if err != nil {
		t.Fatalf("SearchByEmbedding: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a-1" || ids[1] != "a-2" {
		t.Fatalf("ids = %v", ids)
	}
	if stub.gotReq.GetTenantId() != "tnt-1" || stub.gotReq.GetLimit() != 5 || len(stub.gotReq.GetQueryEmbedding()) != 2 {
		t.Fatalf("req = %+v", stub.gotReq)
	}
}

func TestSearchByEmbedding_ErrorWraps(t *testing.T) {
	c := NewContentRetrievalClientFromStub(&fakeContentRetrievalStub{err: errors.New("boom")})
	if _, err := c.SearchByEmbedding(context.Background(), "tnt-1", []float32{0.5}, 5); err == nil {
		t.Fatal("rpc error must surface")
	}
}

func TestNewContentRetrievalClient_RequiresTarget(t *testing.T) {
	if _, err := NewContentRetrievalClient("  "); err == nil {
		t.Fatal("empty target must error")
	}
}
