package observability

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	commontracing "github.com/apollo-chora/chora-common/tracing"
)

// TestWiringOrder_TraceFieldSurvivesLateTracerProvider pins the exact
// composition cmd/server builds:
//
//	commontracing.Middleware()( AccessLogMiddleware()( composite ) )
//
// and the exact ORDER main runs it in. Two things could silently break it,
// and neither is visible from a unit test that injects a span by hand:
//
//  1. cmd/server calls observability.SetupAsync, so the SDK TracerProvider is
//     registered on a GOROUTINE that can finish AFTER the middleware chain is
//     built. tracing.Middleware captures otel.Tracer(...) once at construction
//     time. If the global tracer did not re-delegate on a later
//     SetTracerProvider, every span would be a no-op for the life of the
//     process and every log line would lose its trace field.
//  2. If the access log were mounted OUTSIDE the tracing middleware there
//     would be no span on the request context yet, and the field would be
//     absent for a different reason.
//
// The test therefore builds the chain BEFORE registering the provider, which
// is the worst-case ordering.
func TestWiringOrder_TraceFieldSurvivesLateTracerProvider(t *testing.T) {
	buf := &bytes.Buffer{}

	// Chain built FIRST, before any TracerProvider exists.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := commontracing.Middleware()(
		newAccessLog(buf).middleware()(inner),
	)

	// Provider registered AFTERWARDS, as SetupAsync does.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(rec),
	)
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeOne(t, buf)

	if entry["severity"] != "INFO" {
		t.Errorf("severity = %v; want INFO", entry["severity"])
	}
	trace, _ := entry["trace_id"].(string)
	if trace == "" {
		t.Fatal("no trace field: the global tracer did not pick up the late TracerProvider")
	}

	// The id in the log must be the id of a span the SDK actually EXPORTED.
	// A trace field that resolves to nothing is the defect, not the fix.
	ended := rec.Ended()
	if len(ended) != 1 {
		t.Fatalf("exported %d spans; want exactly 1 server span", len(ended))
	}
	wantTrace := ended[0].SpanContext().TraceID().String()
	if trace != wantTrace {
		t.Errorf("logged trace = %q; exported span trace = %q", trace, wantTrace)
	}
	if entry["span_id"] != ended[0].SpanContext().SpanID().String() {
		t.Errorf("logged span_id = %v; exported span id = %s",
			entry["span_id"], ended[0].SpanContext().SpanID())
	}
}

// An inbound W3C traceparent must be CONTINUED, so a log line here shares a
// trace id with the caller's. This is what makes one query follow a request
// across services rather than just within this one.
func TestWiringOrder_ContinuesInboundTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(rec),
	)
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	buf := &bytes.Buffer{}
	// Chain built BEFORE the globals are registered, as cmd/server does.
	handler := commontracing.Middleware()(
		newAccessLog(buf).middleware()(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})),
	)

	// commonotel.Init registers BOTH. Without the propagator the middleware
	// cannot read an inbound traceparent and starts a fresh root instead,
	// which is exactly how cross-service correlation goes quiet.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	const upstreamTrace = "1234567890abcdef1234567890abcdef"
	req := httptest.NewRequest(http.MethodGet, "/v1/me/goals", nil)
	req.Header.Set("traceparent", "00-"+upstreamTrace+"-abcdef0123456789-01")
	req.Header.Set("X-Tenant-Id", "tenant-mtm")
	req.Header.Set("gcid", "0197f0aa-1111-7000-8000-abcdefabcdef")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeOne(t, buf)
	if entry["trace_id"] != upstreamTrace {
		t.Errorf("trace_id = %v; want the CALLER's trace id %s",
			entry["trace_id"], upstreamTrace)
	}
	if entry["tenant"] != "tenant-mtm" {
		t.Errorf("tenant = %v; want tenant-mtm", entry["tenant"])
	}
	if entry["gcid"] != "0197f0aa-1111-7000-8000-abcdefabcdef" {
		t.Errorf("gcid = %v; want the request gcid", entry["gcid"])
	}
}
