// grounded_search_egress_deny_test.go — the gateway's GroundedSearch status
// contract → typed domain deny (CHO-2148 close-out, live-caught 2026-07-14).
//
// chora-model-gateway (mapGroundedError) encodes WHY an egress was refused in
// the gRPC status code, and carries the machine token in the message
// ("grounded_search <reason>: <detail>"). This adapter is the ONLY place that
// translation may live: the HTTP edge must map a governance deny to a 4xx
// WITHOUT importing grpc/codes (hexagonal — adapters depend on the domain, the
// domain never learns a transport).
//
// A deny is NOT an upstream failure: Unavailable (vendor down) and a plain
// transport error must NOT become denies — they stay 5xx at the edge.
package clients

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/apollo-chora/chora-consumption/internal/domain/companion/grounded"
)

func denyQuery() grounded.Query {
	return grounded.Query{TenantID: "t", GCID: "g", Directive: "is the sky blue", MaxHits: 4, ActionCode: testGroundedActionCode}
}

func TestSearchGround_GatewayDenyBecomesTypedDomainDeny(t *testing.T) {
	cases := []struct {
		name    string
		rpcErr  error
		wantWhy grounded.DenyReason
	}{
		{
			name:    "kill-switch engaged (platform stop beats tenant opt-in)",
			rpcErr:  status.Error(codes.FailedPrecondition, "grounded_search kill_switch_engaged: platform egress halted"),
			wantWhy: grounded.DenyKillSwitch,
		},
		{
			name:    "tenant not entitled (franchise default-deny)",
			rpcErr:  status.Error(codes.FailedPrecondition, "grounded_search external_egress_disabled: tenant has no policy row"),
			wantWhy: grounded.DenyEgressOff,
		},
		{
			name:    "tenant budget exhausted",
			rpcErr:  status.Error(codes.FailedPrecondition, "grounded_search budget_exhausted: no budget left"),
			wantWhy: grounded.DenyEgressOff,
		},
		{
			name:    "daily grounded-call ceiling spent",
			rpcErr:  status.Error(codes.ResourceExhausted, "grounded_search daily_ceiling_exceeded: 50/50 used"),
			wantWhy: grounded.DenyCeilingReached,
		},
		{
			name:    "armor pre-screen refused the query",
			rpcErr:  status.Error(codes.PermissionDenied, "grounded_search model_armor_pre_block: unsafe query"),
			wantWhy: grounded.DenyQueryBlocked,
		},
		{
			name:    "gateway rejected the query as malformed",
			rpcErr:  status.Error(codes.InvalidArgument, "grounded_search invalid_argument: directive empty"),
			wantWhy: grounded.DenyInvalidQuery,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &fakeGroundedGRPC{err: tc.rpcErr}
			c := NewGroundedSearchGatewayClientFromStub(stub, time.Second)

			_, err := c.SearchGround(context.Background(), denyQuery())

			var deny *grounded.DeniedError
			if !errors.As(err, &deny) {
				t.Fatalf("err = %v (%T), want a *grounded.DeniedError — a governance refusal must be typed, "+
					"or the HTTP edge blanket-502s it and chora-gateway erases the reason", err, err)
			}
			if deny.Reason != tc.wantWhy {
				t.Errorf("Reason = %q, want %q", deny.Reason, tc.wantWhy)
			}
		})
	}
}

// A genuine upstream failure is NOT a deny — it must stay a 5xx at the edge.
func TestSearchGround_UpstreamFailureIsNotADeny(t *testing.T) {
	cases := []struct {
		name   string
		rpcErr error
	}{
		{"vendor unavailable", status.Error(codes.Unavailable, "grounded_search vendor_unavailable: vertex down")},
		{"gateway internal", status.Error(codes.Internal, "grounded_search not_wired: misconfigured")},
		{"plain transport error", errors.New("connection reset by peer")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &fakeGroundedGRPC{err: tc.rpcErr}
			c := NewGroundedSearchGatewayClientFromStub(stub, time.Second)

			_, err := c.SearchGround(context.Background(), denyQuery())
			if err == nil {
				t.Fatal("err = nil, want a loud failure")
			}
			var deny *grounded.DeniedError
			if errors.As(err, &deny) {
				t.Fatalf("upstream failure %v was typed as a deny (%s) — a real outage must stay 5xx, "+
					"never be presented to the learner as a governance decision", tc.rpcErr, deny.Reason)
			}
		})
	}
}
