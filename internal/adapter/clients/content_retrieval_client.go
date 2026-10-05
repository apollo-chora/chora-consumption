// content_retrieval_client.go — gRPC client for chora-creation's
// ContentRetrieval.SearchEmbeddings (Epic-1b W4). Resolves the nearest
// PUBLISHED atoms for a Growth-Edge concept embedding so the drillcache
// decorator can cache them on the edge.
//
// Dial shape mirrors breed_distribution_grpc_client.go: insecure transport
// credentials (Cloud Service Mesh injects mTLS), mesh DNS target from env
// SVC_CREATION_GRPC_URL (e.g. "chora-creation.creation.svc.cluster.local:9090").
package clients

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"
)

// contentRetrievalCallTimeout bounds one SearchEmbeddings round-trip — the
// call rides the Growth-Edge upsert path (Pub/Sub push), so keep it tight.
const contentRetrievalCallTimeout = 5 * time.Second

// ContentRetrievalClient wraps the generated stub.
type ContentRetrievalClient struct {
	client  creationv1.ContentRetrievalClient
	timeout time.Duration
}

// NewContentRetrievalClient dials the mesh target. Empty target errors —
// callers gate on the env var before constructing.
func NewContentRetrievalClient(target string) (*ContentRetrievalClient, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("clients: content retrieval target required")
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("clients: dial content retrieval %q: %w", target, err)
	}
	return &ContentRetrievalClient{
		client:  creationv1.NewContentRetrievalClient(conn),
		timeout: contentRetrievalCallTimeout,
	}, nil
}

// NewContentRetrievalClientFromStub injects a stub (tests).
func NewContentRetrievalClientFromStub(stub creationv1.ContentRetrievalClient) *ContentRetrievalClient {
	return &ContentRetrievalClient{client: stub, timeout: contentRetrievalCallTimeout}
}

// SearchByEmbedding satisfies drillcache.AtomSearcher: nearest published atom
// IDs (best first) for the supplied concept embedding.
func (c *ContentRetrievalClient) SearchByEmbedding(ctx context.Context, tenantID string, query []float32, limit int) ([]string, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.client.SearchEmbeddings(callCtx, &creationv1.SearchEmbeddingsRequest{
		TenantId:       tenantID,
		QueryEmbedding: query,
		Limit:          int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("clients: search embeddings: %w", err)
	}
	out := make([]string, 0, len(resp.GetMatches()))
	for _, m := range resp.GetMatches() {
		if id := strings.TrimSpace(m.GetAtomId()); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}
