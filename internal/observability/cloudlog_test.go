package observability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// decodeOne unmarshals the single JSON object the access log wrote.
func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("access log wrote nothing")
	}
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("access log must write exactly one line per request; got %d", strings.Count(line, "\n")+1)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("access log line is not JSON: %v\nline=%s", err, line)
	}
	return m
}

// serve runs one request through the access-log middleware and returns the
// decoded entry.
func serve(t *testing.T, r *http.Request, status int) map[string]any {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := newAccessLog(buf).middleware()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	return decodeOne(t, buf)
}

// withSpan returns a request whose context carries a valid, sampled OTel
// span context (as tracing.Middleware would have installed).
func withSpan(r *http.Request, traceHex, spanHex string, sampled bool) *http.Request {
	tid, _ := oteltrace.TraceIDFromHex(traceHex)
	sid, _ := oteltrace.SpanIDFromHex(spanHex)
	flags := oteltrace.TraceFlags(0)
	if sampled {
		flags = oteltrace.FlagsSampled
	}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: flags,
		Remote:     false,
	})
	return r.WithContext(oteltrace.ContextWithSpanContext(r.Context(), sc))
}

// ---------------------------------------------------------------------------
// Severity, a 200 on /healthz is INFO, not ERROR (FINDING-02: 1,274 of 1,472
// entries were stamped ERROR because the line went to stderr as logfmt).
// ---------------------------------------------------------------------------

func TestAccessLog_Healthz200IsInfoNotError(t *testing.T) {
	got := serve(t, httptest.NewRequest(http.MethodGet, "/healthz", nil), http.StatusOK)
	if got["severity"] != "INFO" {
		t.Errorf("severity = %v; want INFO", got["severity"])
	}
}

func TestAccessLog_SeverityByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNoContent, "INFO"},
		{http.StatusMovedPermanently, "INFO"},
		{http.StatusBadRequest, "WARNING"},
		{http.StatusForbidden, "WARNING"},
		{http.StatusNotFound, "WARNING"},
		{http.StatusInternalServerError, "ERROR"},
		{http.StatusBadGateway, "ERROR"},
	}
	for _, c := range cases {
		got := serve(t, httptest.NewRequest(http.MethodGet, "/x", nil), c.status)
		if got["severity"] != c.want {
			t.Errorf("status %d severity = %v; want %s", c.status, got["severity"], c.want)
		}
	}
}

func TestAccessLog_HandlerThatNeverWritesHeaderIs200Info(t *testing.T) {
	buf := &bytes.Buffer{}
	mw := newAccessLog(buf).middleware()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	got := decodeOne(t, buf)
	if got["severity"] != "INFO" {
		t.Errorf("implicit 200 severity = %v; want INFO", got["severity"])
	}
	req, _ := got["httpRequest"].(map[string]any)
	if req == nil || req["status"] != float64(200) {
		t.Errorf("httpRequest.status = %v; want 200", req["status"])
	}
}

// ---------------------------------------------------------------------------
// Trace correlation, the id comes from the LIVE span context, as the plain
// W3C trace id the OTel SDK exports.
// ---------------------------------------------------------------------------

func TestAccessLog_TraceFieldFromLiveSpanContext(t *testing.T) {
	const traceHex = "1234567890abcdef1234567890abcdef"
	const spanHex = "abcdef0123456789"
	r := withSpan(httptest.NewRequest(http.MethodGet, "/v1/me/goals", nil), traceHex, spanHex, true)
	got := serve(t, r, http.StatusOK)

	if got["trace_id"] != traceHex {
		t.Errorf("trace_id = %v; want %s", got["trace_id"], traceHex)
	}
	if got["span_id"] != spanHex {
		t.Errorf("span_id = %v; want %s", got["span_id"], spanHex)
	}
	if got["trace_sampled"] != true {
		t.Errorf("trace_sampled = %v; want true", got["trace_sampled"])
	}
}

func TestAccessLog_UnsampledSpanStillCorrelates(t *testing.T) {
	r := withSpan(httptest.NewRequest(http.MethodGet, "/x", nil),
		"1234567890abcdef1234567890abcdef", "abcdef0123456789", false)
	got := serve(t, r, http.StatusOK)
	if got["trace_id"] == nil {
		t.Error("unsampled span must still carry the trace field")
	}
	if got["trace_sampled"] != false {
		t.Errorf("trace_sampled = %v; want false", got["trace_sampled"])
	}
}

// A trace field is either resolvable or absent. An entry with no live span
// must NOT emit a dangling id, that reads as correlation while resolving to
// nothing.
func TestAccessLog_NoSpanOmitsTraceFieldEntirely(t *testing.T) {
	got := serve(t, httptest.NewRequest(http.MethodGet, "/x", nil), http.StatusOK)
	for _, k := range []string{
		"trace_id",
		"span_id",
		"trace_sampled",
	} {
		if _, present := got[k]; present {
			t.Errorf("%s must be ABSENT with no live span; got %v", k, got[k])
		}
	}
}

// The span id is useful on its own and does not need any project qualifier.
func TestAccessLog_SpanIDFromLiveSpan(t *testing.T) {
	r := withSpan(httptest.NewRequest(http.MethodGet, "/x", nil),
		"1234567890abcdef1234567890abcdef", "abcdef0123456789", true)
	got := serve(t, r, http.StatusOK)
	if got["span_id"] != "abcdef0123456789" {
		t.Errorf("span_id = %v; want abcdef0123456789", got["span_id"])
	}
}

// ---------------------------------------------------------------------------
// tenant + gcid, OMITTED when there is none. "tenant=" reads as "we tried
// and got nothing"; absence reads correctly as not applicable.
// ---------------------------------------------------------------------------

func TestAccessLog_OmitsTenantAndGCIDWhenAbsent(t *testing.T) {
	got := serve(t, httptest.NewRequest(http.MethodGet, "/healthz", nil), http.StatusOK)
	if _, present := got["tenant"]; present {
		t.Errorf("tenant must be ABSENT on an unauthenticated probe; got %v", got["tenant"])
	}
	if _, present := got["gcid"]; present {
		t.Errorf("gcid must be ABSENT on an unauthenticated probe; got %v", got["gcid"])
	}
}

func TestAccessLog_PopulatesTenantAndGCIDFromRequestContext(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/goals", nil)
	r.Header.Set("X-Tenant-Id", "tenant-mtm")
	r.Header.Set("gcid", "0197f0aa-1111-7000-8000-abcdefabcdef")
	got := serve(t, r, http.StatusOK)
	if got["tenant"] != "tenant-mtm" {
		t.Errorf("tenant = %v; want tenant-mtm", got["tenant"])
	}
	if got["gcid"] != "0197f0aa-1111-7000-8000-abcdefabcdef" {
		t.Errorf("gcid = %v; want the request gcid", got["gcid"])
	}
}

func TestAccessLog_GCIDFallsBackToXChoraGCIDHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/goals", nil)
	r.Header.Set("X-Tenant-Id", "tenant-mtm")
	r.Header.Set("X-Chora-GCID", "0197f0aa-2222-7000-8000-abcdefabcdef")
	got := serve(t, r, http.StatusOK)
	if got["gcid"] != "0197f0aa-2222-7000-8000-abcdefabcdef" {
		t.Errorf("gcid = %v; want the X-Chora-GCID value", got["gcid"])
	}
}

func TestAccessLog_BlankHeaderIsTreatedAsAbsent(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/me/goals", nil)
	r.Header.Set("X-Tenant-Id", "   ")
	r.Header.Set("gcid", "")
	got := serve(t, r, http.StatusOK)
	if _, present := got["tenant"]; present {
		t.Errorf("whitespace tenant must be ABSENT; got %v", got["tenant"])
	}
	if _, present := got["gcid"]; present {
		t.Errorf("empty gcid must be ABSENT; got %v", got["gcid"])
	}
}

// ---------------------------------------------------------------------------
// Envelope shape
// ---------------------------------------------------------------------------

func TestAccessLog_CarriesHTTPRequestAndServiceFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/me/goals?x=1", nil)
	got := serve(t, r, http.StatusCreated)

	if got["message"] == nil || got["message"] == "" {
		t.Error("message must be non-empty")
	}
	if got["service"] != ServiceName {
		t.Errorf("service = %v; want %s", got["service"], ServiceName)
	}
	if got["time"] == nil {
		t.Error("time must be present")
	}
	req, _ := got["httpRequest"].(map[string]any)
	if req == nil {
		t.Fatal("httpRequest must be present")
	}
	if req["requestMethod"] != http.MethodPost {
		t.Errorf("requestMethod = %v; want POST", req["requestMethod"])
	}
	if req["requestUrl"] != "/v1/me/goals?x=1" {
		t.Errorf("requestUrl = %v; want the full request target", req["requestUrl"])
	}
	if req["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v; want 201", req["status"])
	}
	if lat, _ := req["latency"].(string); !strings.HasSuffix(lat, "s") {
		t.Errorf("latency = %v; want a duration string ending in s", req["latency"])
	}
}

// The default middleware writes to STDOUT. A container runtime's
// stream-to-severity default maps stderr to ERROR regardless of payload,
// which is the defect this replaces.
func TestAccessLogMiddleware_DefaultWriterIsStdout(t *testing.T) {
	if defaultAccessLogWriter != stdout() {
		t.Error("default access-log writer must be os.Stdout, not os.Stderr")
	}
	if AccessLogMiddleware() == nil {
		t.Error("AccessLogMiddleware returned nil")
	}
}

// Concurrent requests must not interleave partial JSON on the shared writer.
func TestAccessLog_ConcurrentWritesStayWellFormed(t *testing.T) {
	buf := &bytes.Buffer{}
	mw := newAccessLog(buf).middleware()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	done := make(chan struct{})
	for i := 0; i < 32; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
		}()
	}
	for i := 0; i < 32; i++ {
		<-done
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 32 {
		t.Fatalf("got %d lines; want 32", len(lines))
	}
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d is not well-formed JSON: %v", i, err)
		}
	}
}
