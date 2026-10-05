// grpc_call_log_test.go: the served-method observability interceptor.
//
// THE GAP THIS CLOSES. Nothing anywhere could say WHICH gRPC method served a
// call to consumption. The server was built with a bare grpc.NewServer(), no
// interceptor; the handlers log nothing on the success path; and the
// istio-proxy sidecar has Envoy access logging OFF (verified live: the whole
// 131-line sidecar log contains zero access-log lines and zero method names).
// The calling side is blind too: the companion-chat agent invokes weakness.read
// as a model-decided tool, logs nothing about tool calls, and runs without a
// sidecar. So a round trip could never be distinguished from a round trip via a
// legacy alias, and an absent call could never be distinguished from a denied
// one.
//
// That is the exact instrument the two W4 seams needed and did not have: the
// only line that can say "the thing being called is the thing that is RUNNING".
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// idReq is a stand-in for a generated request that carries the two identifiers
// every consumption request type exposes.
type idReq struct {
	tenant string
	gcid   string
}

func (r idReq) GetTenantId() string   { return r.tenant }
func (r idReq) GetCallerGcid() string { return r.gcid }

// secretReq carries neither accessor: the interceptor must still log the call.
type secretReq struct{ Password string }

func captureCallLog(t *testing.T, req any, handlerErr error) string {
	t.Helper()
	var got string
	interceptor := grpcCallLogInterceptor(func(format string, args ...any) {
		got = strings.TrimSpace(fmt.Sprintf(format, args...))
	})
	info := &grpc.UnaryServerInfo{FullMethod: "/chora.services.consumption.v1.CompanionGrowth/ReadWeakness"}
	_, err := interceptor(context.Background(), req, info,
		func(_ context.Context, _ any) (any, error) {
			if handlerErr != nil {
				return nil, handlerErr
			}
			return "ok", nil
		})
	if handlerErr == nil && err != nil {
		t.Fatalf("interceptor returned an error on a successful handler: %v", err)
	}
	return got
}

// THE headline: a SUCCESSFUL call is logged. An error-only interceptor would
// reproduce the current blindness for exactly the case that needs proving.
func TestGRPCCallLog_LogsTheServedMethodOnSuccess(t *testing.T) {
	line := captureCallLog(t, idReq{tenant: "01957c8c-2222-7000-aaaa-222222222222", gcid: "01957c8c-3333-7000-aaaa-333333333333"}, nil)

	if !strings.Contains(line, "/chora.services.consumption.v1.CompanionGrowth/ReadWeakness") {
		t.Fatalf("line does not name the served method: %q", line)
	}
	if !strings.Contains(line, "01957c8c-2222-7000-aaaa-222222222222") {
		t.Errorf("line does not carry the tenant: %q", line)
	}
	if !strings.Contains(line, "01957c8c-3333-7000-aaaa-333333333333") {
		t.Errorf("line does not carry the caller gcid: %q", line)
	}
	if !strings.Contains(line, "OK") {
		t.Errorf("line does not carry the outcome: %q", line)
	}
	if !strings.Contains(line, "dur=") {
		t.Errorf("line does not carry a duration: %q", line)
	}
}

// The outcome must be the gRPC CODE, so a denial reads as a denial rather than
// as a generic failure.
func TestGRPCCallLog_CarriesTheGRPCCodeOnFailure(t *testing.T) {
	line := captureCallLog(t, idReq{tenant: "t", gcid: "g"}, status.Error(codes.PermissionDenied, "nope"))

	if !strings.Contains(line, "PermissionDenied") {
		t.Errorf("line does not carry the gRPC code: %q", line)
	}
}

// A non-status error must not be swallowed into a bare "error".
func TestGRPCCallLog_NonStatusErrorStillNamesACode(t *testing.T) {
	line := captureCallLog(t, idReq{tenant: "t", gcid: "g"}, errors.New("boom"))

	if !strings.Contains(line, "Unknown") {
		t.Errorf("a non-status error should map to the Unknown code: %q", line)
	}
}

// THE PRIVACY INVARIANT. The interceptor reads exactly two identifier fields
// through a narrow interface; it must never render the request itself, or a
// gRPC log becomes a data-protection finding waiting to be read off the O+
// evidence at G3.
func TestGRPCCallLog_NeverRendersTheRequestBody(t *testing.T) {
	line := captureCallLog(t, secretReq{Password: "hunter2"}, nil)

	if strings.Contains(line, "hunter2") {
		t.Fatalf("the request body leaked into the call log: %q", line)
	}
	if !strings.Contains(line, "/chora.services.consumption.v1.CompanionGrowth/ReadWeakness") {
		t.Errorf("a request without the identifier accessors must STILL be logged: %q", line)
	}
}

// A request lacking the accessors logs the call with empty identifiers rather
// than being skipped: an absent line must never be mistaken for an absent call.
func TestGRPCCallLog_UnidentifiedRequestIsStillLogged(t *testing.T) {
	line := captureCallLog(t, secretReq{}, nil)
	if strings.TrimSpace(line) == "" {
		t.Fatal("no line at all for a request without tenant/gcid accessors")
	}
}

// The interceptor must be transparent: the handler's own return value and
// error reach the caller unchanged.
func TestGRPCCallLog_PassesTheHandlerResultThrough(t *testing.T) {
	interceptor := grpcCallLogInterceptor(func(string, ...any) {})
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/M"}

	resp, err := interceptor(context.Background(), idReq{}, info,
		func(_ context.Context, _ any) (any, error) { return "payload", nil })
	if err != nil || resp != "payload" {
		t.Fatalf("success path altered: resp=%v err=%v", resp, err)
	}

	sentinel := status.Error(codes.NotFound, "gone")
	_, err = interceptor(context.Background(), idReq{}, info,
		func(_ context.Context, _ any) (any, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("error path altered: got %v, want the handler's own error", err)
	}
}
