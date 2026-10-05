// delivery_graded_comments_grpc_client.go — gRPC adapter for the ceremony
// edge-scout's ONLY cross-domain seam (CHO-2040, CR §8 R7-3): reading the
// learner's past graded-assessment overall comments from chora-delivery via
// Delivery.ListLearnerGradedSubmissions (contract-first per AP-01; the RPC
// shape lives in chora-contracts proto/services/delivery/v1/delivery.proto,
// server implemented delivery-side at 5459e7642).
//
// Per ddd-enforcement HARD RULE: chora-consumption MUST NOT query the
// chora_delivery database — this typed RPC is the seam; cross-DB stays zero.
//
// Per feedback_no_inline_config the dial target comes from env, resolved at
// cmd/server boot (mirrors breed_distribution_grpc_client.go):
//
//	CHORA_DELIVERY_GRPC_BASE_URL   gRPC target for chora-delivery
//	                               (e.g. "chora-delivery:9090" via mesh DNS)
//
// Mesh mTLS is handled by Cloud Service Mesh at L4 — the adapter defaults to
// insecure credentials between sidecars, exactly like the breed client.
// ⚠ Deploy-time: a NEW gRPC method needs a mesh-authz allowlist entry +
// caller restart ([[feedback_grpc_method_needs_mesh_authz_allowlist]]) —
// tracked in the CHO-2040 deploy notes, NOT applied from here.
//
// Mapping: only rows carrying a non-blank overall_comment become
// edgescout.CommentSignal — a released submission without a grader comment
// has no extraction signal (skipping it is a mapping rule, not a swallowed
// error; RPC failures ALWAYS propagate).
package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/delivery/v1"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/edgescout"
)

// DefaultDeliveryGradedCommentsCallTimeout is the per-RPC deadline applied
// when the caller does not override via options.
const DefaultDeliveryGradedCommentsCallTimeout = 5 * time.Second

// DeliveryGradedCommentsGRPCClientOptions configures the client.
type DeliveryGradedCommentsGRPCClientOptions struct {
	// Target is the gRPC dial target. Production = mesh DNS
	// "chora-delivery:9090" (env CHORA_DELIVERY_GRPC_BASE_URL); tests =
	// "bufconn" + a custom dialer DialOption.
	Target string

	// CallTimeout — per-RPC deadline. Defaults to 5s if zero.
	CallTimeout time.Duration

	// DialOpts overrides the default insecure credentials. Tests inject
	// bufconn; production relies on the mesh sidecar mTLS (plain HTTP/2
	// between sidecars), mirroring BreedDistributionGRPCClientOptions.
	DialOpts []grpc.DialOption
}

// DeliveryGradedCommentsGRPCClient calls chora-delivery's
// Delivery.ListLearnerGradedSubmissions RPC and maps the released graded
// submissions onto edgescout.CommentSignal.
//
// Construction always returns a *DeliveryGradedCommentsGRPCClient. Close()
// MUST be called at service shutdown to drain the underlying *grpc.ClientConn.
type DeliveryGradedCommentsGRPCClient struct {
	target      string
	callTimeout time.Duration

	conn   *grpc.ClientConn
	client deliveryv1.DeliveryClient
}

// NewDeliveryGradedCommentsGRPCClient builds the client. Target must be
// non-empty (fail-loud at the composition root, never a half-dialled adapter).
func NewDeliveryGradedCommentsGRPCClient(opts DeliveryGradedCommentsGRPCClientOptions) (*DeliveryGradedCommentsGRPCClient, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, errors.New("clients: gRPC target required")
	}
	timeout := opts.CallTimeout
	if timeout <= 0 {
		timeout = DefaultDeliveryGradedCommentsCallTimeout
	}
	dialOpts := opts.DialOpts
	if len(dialOpts) == 0 {
		dialOpts = []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		}
	}
	// Use Dial — bufconn requires the legacy API; grpc.NewClient doesn't
	// support custom dialers that bypass DNS resolution (mirrors the breed
	// client).
	//nolint:staticcheck // bufconn requires legacy Dial.
	conn, err := grpc.Dial(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("clients: grpc.Dial %s: %w", target, err)
	}
	return &DeliveryGradedCommentsGRPCClient{
		target:      target,
		callTimeout: timeout,
		conn:        conn,
		client:      deliveryv1.NewDeliveryClient(conn),
	}, nil
}

// Close drains the underlying *grpc.ClientConn. Idempotent.
func (c *DeliveryGradedCommentsGRPCClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// ListLearnerGradedComments returns the learner's released graded-assessment
// overall comments, newest grading-completion first, windowed by limit (the
// edge-scout passes edgescout.GradedCommentsWindow — the locked ~20-activity
// crawl). Rows without a comment are skipped (no signal); any RPC error
// propagates verbatim — an unreachable delivery must never read as "no
// comments" (that would silently flip a learner into the virgin fallback).
func (c *DeliveryGradedCommentsGRPCClient) ListLearnerGradedComments(ctx context.Context, tenantID, learnerGCID string, limit int) ([]edgescout.CommentSignal, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("clients: tenant_id required")
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return nil, errors.New("clients: learner_gcid required")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("clients: limit must be positive, got %d", limit)
	}
	if c == nil || c.client == nil {
		return nil, errors.New("clients: gRPC client not initialised")
	}

	// Deadline derives from the CALLER's ctx (trace + cancellation preserved).
	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	resp, err := c.client.ListLearnerGradedSubmissions(callCtx, &deliveryv1.ListLearnerGradedSubmissionsRequest{
		TenantId:    tenantID,
		LearnerGcid: learnerGCID,
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("clients: ListLearnerGradedSubmissions: %w", err)
	}

	out := make([]edgescout.CommentSignal, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		comment := strings.TrimSpace(item.GetOverallComment())
		if comment == "" {
			continue // released but uncommented — no extraction signal
		}
		out = append(out, edgescout.CommentSignal{
			Excerpt: comment,
			Outcome: strings.TrimSpace(item.GetOutcome()),
		})
	}
	return out, nil
}
