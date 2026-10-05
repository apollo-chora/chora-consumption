// otel_middleware.go — OTel HTTP middleware shim per ai-observability-cloud-trace.
//
// This is a LIGHTWEIGHT middleware that:
//
//   - Reads inbound W3C traceparent header (mandatory propagation)
//   - Logs span attributes (method, path, status, dur, tenant_id, gcid)
//   - Stamps the response Server-Timing header so the player can show
//     real-time latency without dropping a third-party SDK
//
// Real OTel SDK wiring lives in `internal/observability/otlp.go` (M12+).
// Per CLAUDE.md "OTLP everywhere" + Tier 3 D12: every service emits OTLP
// directly to Cloud Trace; the middleware here is the per-route
// instrumentation point that hooks into the SDK once it lands.
package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// statusRecorder wraps http.ResponseWriter so middleware sees the final
// status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// OTelMiddleware wraps an http.Handler with span emission + W3C trace
// propagation logging.
//
// Per CLAUDE.md §6 trace context across Pub/Sub: the inbound traceparent
// MUST flow into outbound event envelopes. Handler code already pulls
// it via extTraceFromHeaders — this middleware just logs the link.
func OTelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Surface the inbound trace as a Server-Timing header — gives
		// the SPA visibility into the upstream traceparent without
		// pulling a header library.
		if tp := r.Header.Get("traceparent"); tp != "" {
			w.Header().Set("Server-Timing", "tp;desc=\""+sanitizeTraceparent(tp)+"\"")
		}

		next.ServeHTTP(rec, r)

		dur := time.Since(start)

		// The "otel: ..." logfmt line that stood here was REMOVED 2026-08-07
		// (FINDING-02). It went to stderr as unstructured text, so the GKE
		// agent stamped it ERROR on a served 200 and it had nowhere to carry
		// `logging.googleapis.com/trace`. Its replacement is the single
		// Cloud Logging structured entry from
		// observability.AccessLogMiddleware, which reads the trace id from
		// the live span rather than re-parsing the inbound header.

		// Stamp final dur as another Server-Timing entry.
		w.Header().Add("Server-Timing", "app;dur="+strconv.FormatInt(dur.Milliseconds(), 10))
	})
}

// maskGCID and truncTraceparent were DELETED 2026-08-07 (FINDING-02) along
// with the logfmt line they formatted. The replacement structured entry
// carries the full gcid, matching the unmasked `chora.gcid` span attribute
// so a log line and its span agree on the actor, and it takes the trace id
// from the span context rather than truncating a header string.

// sanitizeTraceparent strips chars that would break the Server-Timing
// header (per RFC 8941 unquoted-token grammar — colons, semicolons,
// commas need quoting; we already wrap in "" so we just strip
// double-quotes for safety).
func sanitizeTraceparent(tp string) string {
	return strings.ReplaceAll(tp, "\"", "")
}
