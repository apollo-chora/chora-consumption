// grpc_call_log.go: one structured line per served gRPC call, naming the
// METHOD that served it.
//
// # Why this exists
//
// Until this, nothing anywhere could say which gRPC method served a call to
// chora-consumption, which made a whole class of question unanswerable:
//
//   - the server was built with a bare grpc.NewServer(), no interceptor;
//   - the handlers log nothing on the success path;
//   - the istio-proxy sidecar has Envoy access logging OFF (measured live: the
//     entire 131-line sidecar log holds zero access-log lines and zero method
//     names), so "read the :path off the sidecar" silently returns nothing,
//     which reads as no traffic rather than no logging;
//   - the calling side is blind too. The companion-chat agent invokes
//     weakness.read as a MODEL-DECIDED tool, logs nothing about tool calls, and
//     runs without a sidecar, so it cannot tell whether it dialled at all.
//
// So a successful round trip could not be distinguished from a round trip that
// went via a legacy method alias, and an absent call could not be distinguished
// from a denied one. Both of the ADR-254 W4 seams found on 2026-08-23 needed
// exactly that distinction: the missing artifact each time was an observation
// of which method actually ran.
//
// # What it deliberately does NOT log
//
// Never the request or the response. It reads exactly TWO identifier fields,
// through narrow interfaces every consumption request type already satisfies,
// and nothing else. A gRPC log that renders bodies is a data-protection finding
// waiting to be read off the O+ evidence; tenant plus method is what makes the
// line useful, and gcid is an opaque UUIDv7 carrying no personal data itself.
//
// # Coverage
//
// UNARY only. Every RPC registered on this server (Consumption,
// CompanionGrowth, KnowledgeGraphService) is unary; the generated service
// descriptors declare no streams. If a streaming method is ever added it will
// NOT appear in this log, and an absent line must never be read as an absent
// call, so a stream interceptor has to land in the same commit as the stream.
package main

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// tenantIdentified and callerIdentified are the narrow reads the interceptor
// makes on a request. Kept as two separate interfaces because not every
// request carries both, and a request that carries neither must still be
// logged rather than skipped.
type tenantIdentified interface{ GetTenantId() string }

type callerIdentified interface{ GetCallerGcid() string }

// logf is the sink, injected so the tests can read the rendered line rather
// than scrape stderr.
type logf func(format string, args ...any)

// grpcCallLogInterceptor returns the unary interceptor. It is transparent: the
// handler's own response and error are returned unchanged, and the interceptor
// itself never fails a call. An observability instrument that can break the
// thing it observes is worse than no instrument.
func grpcCallLogInterceptor(emit logf) grpc.UnaryServerInterceptor {
	if emit == nil {
		emit = log.Printf
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		resp, err := handler(ctx, req)

		method := "unknown"
		if info != nil && info.FullMethod != "" {
			method = info.FullMethod
		}
		var tenant, gcid string
		if r, ok := req.(tenantIdentified); ok {
			tenant = r.GetTenantId()
		}
		if r, ok := req.(callerIdentified); ok {
			gcid = r.GetCallerGcid()
		}
		// status.Code maps a nil error to OK and a non-status error to Unknown,
		// so the outcome is always a named gRPC code rather than a bare
		// "error": a denial has to read as a denial.
		emit("consumption grpc: method=%s tenant=%s gcid=%s code=%s dur=%s",
			method, tenant, gcid, status.Code(err).String(), time.Since(started).Round(time.Millisecond))

		return resp, err
	}
}
