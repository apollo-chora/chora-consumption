// delivery_graded_comments_grpc_client_test.go — CHO-2040 (CR §8 R7-3): the
// thin gRPC client for Delivery.ListLearnerGradedSubmissions (the ceremony
// edge-scout's ONLY cross-domain seam; contract-first per AP-01, cross-DB
// forbidden). Mirrors the breed_distribution_grpc_client bufconn conventions.
//
// Coverage classes:
//   - Happy path: maps items → edgescout.CommentSignal, skipping blank
//     overall_comments (a released row without a comment carries no signal)
//   - The locked limit + tenant/gcid ride the request verbatim
//   - Upstream RPC error propagates (fail-loud, never an empty success)
//   - Input validation: blank tenant/gcid/limit ≤ 0 rejected client-side
//   - Constructor: blank target rejected
package clients_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/clients"
)

// stubDelivery implements deliveryv1.DeliveryServer for the graded-submission
// read only (everything else stays Unimplemented).
type stubDelivery struct {
	deliveryv1.UnimplementedDeliveryServer
	resp    *deliveryv1.ListLearnerGradedSubmissionsResponse
	err     error
	calls   int64
	lastReq *deliveryv1.ListLearnerGradedSubmissionsRequest
}

func (s *stubDelivery) ListLearnerGradedSubmissions(_ context.Context, req *deliveryv1.ListLearnerGradedSubmissionsRequest) (*deliveryv1.ListLearnerGradedSubmissionsResponse, error) {
	atomic.AddInt64(&s.calls, 1)
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func newDeliveryBufconn(t *testing.T, stub *stubDelivery) (grpc.DialOption, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	deliveryv1.RegisterDeliveryServer(srv, stub)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server stopped: %v", err)
		}
	}()
	dialer := grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	})
	return dialer, func() {
		srv.GracefulStop()
		_ = lis.Close()
	}
}

func newGradedCommentsClient(t *testing.T, stub *stubDelivery) *clients.DeliveryGradedCommentsGRPCClient {
	t.Helper()
	dialer, cleanup := newDeliveryBufconn(t, stub)
	t.Cleanup(cleanup)
	c, err := clients.NewDeliveryGradedCommentsGRPCClient(clients.DeliveryGradedCommentsGRPCClientOptions{
		Target:      "bufconn",
		CallTimeout: 5 * time.Second,
		DialOpts:    []grpc.DialOption{dialer, grpc.WithTransportCredentials(insecure.NewCredentials())},
	})
	if err != nil {
		t.Fatalf("NewDeliveryGradedCommentsGRPCClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestDeliveryGradedComments_HappyPath_SkipsBlankComments(t *testing.T) {
	t.Parallel()
	stub := &stubDelivery{resp: &deliveryv1.ListLearnerGradedSubmissionsResponse{
		Items: []*deliveryv1.GradedSubmissionSummary{
			{SubmissionId: "s1", AssessmentId: "a1", OverallComment: "Work on unit conversions.", Outcome: "FAILED",
				CompletedAt: timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))},
			{SubmissionId: "s2", AssessmentId: "a2", OverallComment: "   ", Outcome: "PASSED"}, // no signal → skipped
			{SubmissionId: "s3", AssessmentId: "a3", OverallComment: "Great structure; graphs need drilling.", Outcome: "PASSED"},
		},
	}}
	c := newGradedCommentsClient(t, stub)

	got, err := c.ListLearnerGradedComments(context.Background(), "tenant-A", "gcid-1", 20)
	if err != nil {
		t.Fatalf("ListLearnerGradedComments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (blank comment skipped), got=%+v", len(got), got)
	}
	if got[0].Excerpt != "Work on unit conversions." || got[0].Outcome != "FAILED" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Excerpt != "Great structure; graphs need drilling." {
		t.Errorf("got[1] = %+v", got[1])
	}
	// The request carried the locked window + scope verbatim.
	if stub.lastReq.GetTenantId() != "tenant-A" || stub.lastReq.GetLearnerGcid() != "gcid-1" || stub.lastReq.GetLimit() != 20 {
		t.Errorf("request = %+v", stub.lastReq)
	}
}

func TestDeliveryGradedComments_UpstreamErrorPropagates(t *testing.T) {
	t.Parallel()
	stub := &stubDelivery{err: status.Error(codes.Unavailable, "delivery paused")}
	c := newGradedCommentsClient(t, stub)
	if _, err := c.ListLearnerGradedComments(context.Background(), "tenant-A", "gcid-1", 20); err == nil {
		t.Fatalf("want the upstream error surfaced, got nil (silent empty success forbidden)")
	} else if !strings.Contains(err.Error(), "delivery paused") {
		t.Errorf("err = %v, want the upstream detail preserved", err)
	}
}

func TestDeliveryGradedComments_InputValidation(t *testing.T) {
	t.Parallel()
	c := newGradedCommentsClient(t, &stubDelivery{resp: &deliveryv1.ListLearnerGradedSubmissionsResponse{}})
	for name, call := range map[string]func() error{
		"blank_tenant": func() error {
			_, err := c.ListLearnerGradedComments(context.Background(), " ", "g", 20)
			return err
		},
		"blank_gcid": func() error {
			_, err := c.ListLearnerGradedComments(context.Background(), "t", "", 20)
			return err
		},
		"zero_limit": func() error {
			_, err := c.ListLearnerGradedComments(context.Background(), "t", "g", 0)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: want a client-side validation error, got nil", name)
		}
	}
}

func TestDeliveryGradedComments_BlankTargetRejected(t *testing.T) {
	t.Parallel()
	if _, err := clients.NewDeliveryGradedCommentsGRPCClient(clients.DeliveryGradedCommentsGRPCClientOptions{Target: "  "}); err == nil {
		t.Fatalf("blank target must be rejected at construction")
	}
}
