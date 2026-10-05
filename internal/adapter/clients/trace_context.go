// trace_context.go - W3C trace context carried on a context.Context across the
// consumption adapters (CLAUDE.md "OTLP everywhere"). Callers attach the inbound
// request's traceparent with WithTraceparent; publishers and clients read it
// back with TraceparentFromContext so the outbound envelope / header links the
// downstream span to the handler span. (Moved out of the deleted /orchestrate
// client, ADR-254 D12: the helper outlived the route.)
package clients

import "context"

type traceparentKey struct{}

// WithTraceparent returns a context carrying the W3C traceparent value.
func WithTraceparent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return context.WithValue(ctx, traceparentKey{}, traceparent)
}

// TraceparentFromContext returns the traceparent attached by WithTraceparent
// ("" when none).
func TraceparentFromContext(ctx context.Context) string {
	v, _ := ctx.Value(traceparentKey{}).(string)
	return v
}
