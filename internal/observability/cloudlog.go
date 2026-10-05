// cloudlog.go, structured access log for chora-consumption.
//
// One JSON object per request on STDOUT, in a neutral structured-logging
// shape (the cloud-neutral replacement for the retired Cloud Logging
// structured-payload contract). Writing to stdout keeps the stream correct:
// a container runtime's stream-to-severity default stamps stderr ERROR
// regardless of what happened, so a served 200 must never go to stderr.
//
// The trace id is taken from the LIVE OTel span context on the request,
// installed by chora-common/tracing.Middleware. It is never parsed out of
// a header by hand, and never falls back to a constant: an id that resolves
// to nothing is worse than no id, so an absent span means an absent field.
package observability

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// stdout returns the process stdout as an io.Writer. Wrapped in a function
// so the default-writer assertion in the tests compares the same interface
// value the middleware actually uses.
func stdout() io.Writer { return os.Stdout }

// defaultAccessLogWriter is STDOUT on purpose. Writing to stderr is what
// made every served request an ERROR; the runtime's stream-to-severity
// default overrides any severity in the payload only for unstructured text,
// but keeping the stream correct removes the ambiguity entirely.
var defaultAccessLogWriter = stdout()

// httpRequestPayload is the native HttpRequest shape a log explorer renders
// as a first-class request row.
type httpRequestPayload struct {
	RequestMethod string `json:"requestMethod"`
	RequestURL    string `json:"requestUrl"`
	Status        int    `json:"status"`
	Latency       string `json:"latency"`
	UserAgent     string `json:"userAgent,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

// accessEntry is one structured log line. Every optional field uses
// omitempty so an absent value is ABSENT, not an empty string.
type accessEntry struct {
	Severity    string              `json:"severity"`
	Message     string              `json:"message"`
	Time        string              `json:"time"`
	Service     string              `json:"service"`
	Trace       string              `json:"trace_id,omitempty"`
	SpanID      string              `json:"span_id,omitempty"`
	TraceSampled *bool              `json:"trace_sampled,omitempty"`
	HTTPRequest *httpRequestPayload `json:"httpRequest,omitempty"`
	Tenant      string              `json:"tenant,omitempty"`
	GCID        string              `json:"gcid,omitempty"`
	DurationUS  int64               `json:"duration_us"`
}

// accessLog emits one structured entry per request.
type accessLog struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// newAccessLog builds an access logger over an explicit writer.
func newAccessLog(w io.Writer) *accessLog {
	return &accessLog{enc: json.NewEncoder(w)}
}

// AccessLogMiddleware returns the production access-log middleware:
// structured JSON on stdout, trace id from the live span context.
//
// It MUST be mounted INSIDE tracing.Middleware so a span context is already
// on the request; mounted outside it, every entry loses its trace field.
func AccessLogMiddleware() func(http.Handler) http.Handler {
	return newAccessLog(defaultAccessLogWriter).middleware()
}

// statusRecorder captures the final status code and forwards the optional
// Flusher / Hijacker interfaces. It embeds the ResponseWriter *interface*, so
// Go cannot auto-promote those methods, an unwrapped middleware silently
// masks them and SSE (the companion chat stream) stops flushing.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *accessLog) middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			a.emit(r, rec.statusOrOK(), time.Since(start))
		})
	}
}

// statusOrOK reports 200 for a handler that returned without ever calling
// WriteHeader or Write, which is what net/http puts on the wire.
func (s *statusRecorder) statusOrOK() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

func (a *accessLog) emit(r *http.Request, status int, dur time.Duration) {
	e := accessEntry{
		Severity:   severityForStatus(status),
		Message:    r.Method + " " + r.URL.Path + " " + strconv.Itoa(status),
		Time:       time.Now().UTC().Format(time.RFC3339Nano),
		Service:    ServiceName,
		DurationUS: dur.Microseconds(),
		HTTPRequest: &httpRequestPayload{
			RequestMethod: r.Method,
			RequestURL:    r.URL.RequestURI(),
			Status:        status,
			Latency:       formatLatency(dur),
			UserAgent:     r.Header.Get("User-Agent"),
			Protocol:      r.Proto,
		},
	}

	// Trace correlation from the LIVE span context, the id the OTLP exporter
	// actually emitted for this request. No header parsing, no constant.
	if sc := oteltrace.SpanContextFromContext(r.Context()); sc.IsValid() {
		e.Trace = sc.TraceID().String()
		e.SpanID = sc.SpanID().String()
		sampled := sc.IsSampled()
		e.TraceSampled = &sampled
	}

	// Request auth context. Omitted entirely when there is none: an
	// unauthenticated liveness probe has no tenant, and "tenant":"" would
	// claim we looked one up and got nothing.
	e.Tenant = firstNonBlank(r.Header.Get("X-Tenant-Id"))
	e.GCID = firstNonBlank(r.Header.Get("gcid"), r.Header.Get("X-Chora-GCID"))

	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.enc.Encode(&e)
}

// severityForStatus maps the served status onto a log severity.
// A 2xx/3xx is INFO, that is the whole point of this file. 4xx is the
// caller's fault, so WARNING; 5xx is ours, so ERROR.
func severityForStatus(status int) string {
	switch {
	case status >= 500:
		return "ERROR"
	case status >= 400:
		return "WARNING"
	default:
		return "INFO"
	}
}

// formatLatency renders a duration in the seconds-with-suffix form the
// HttpRequest.latency field expects (e.g. "0.000076s").
func formatLatency(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 9, 64) + "s"
}

// firstNonBlank returns the first argument that is non-empty after trimming,
// or "" when all of them are blank.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
