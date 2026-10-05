// Package observability wires the OTel exporter via the canonical
// chora-common/otel + chora-common/observability packages (the cloud-neutral
// replacement for the retired Cloud Trace exporter). Spans reach whatever
// OTLP endpoint the environment names (OTEL_EXPORTER_OTLP_ENDPOINT) — a local
// collector in the dev stack, never a hardcoded Google endpoint.
//
// Beyond plain delegation, this shim installs the W3C TraceContext + Baggage
// composite propagator after init — preserving the propagator behaviour the
// local impl provided so HTTPMiddleware + any gRPC interceptors can
// extract/inject traceparent headers across service boundaries.
//
// The async variant SetupAsync returns a bootstrap.OTLPHandle so pod
// bootstrap can run pgx pool init with the FULL bootstrap deadline while OTLP
// wiring proceeds in its own goroutine + own deadline
// (CHORA_OTLP_INIT_TIMEOUT_SECONDS, default 15s). The propagator is set
// immediately so HTTP middleware can extract/inject traceparent headers even
// before the exporter has finished initializing.
package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/apollo-chora/chora-common/bootstrap"
	commonobs "github.com/apollo-chora/chora-common/observability"
	commonotel "github.com/apollo-chora/chora-common/otel"
)

const (
	// ServiceName is the OTel service.name resource attribute.
	ServiceName = "chora-consumption"
	// ServiceVersion follows semver per OpenInference convention.
	ServiceVersion = "0.1.0"
)

// installPropagator sets the W3C TraceContext + Baggage propagator. Called
// from both Setup and SetupAsync paths so HTTPMiddleware + any future gRPC
// interceptors can extract/inject traceparent headers across service
// boundaries.
func installPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// Shutdown is called by main on graceful shutdown.
type Shutdown func(context.Context) error

// Setup wires the OTel exporter via the canonical lib and registers a global
// TracerProvider + W3C TraceContext+Baggage propagator. Returns a shutdown
// func the caller MUST defer in main.
//
// Prefer SetupAsync in new call sites — it decouples OTLP init from pgx
// pool bootstrap.
func Setup(ctx context.Context) (Shutdown, error) {
	shutdown, err := commonotel.Init(ctx, ServiceName, ServiceVersion)
	if err != nil {
		return nil, err
	}
	installPropagator()
	return shutdown, nil
}

// SetupAsync is the fail-soft non-blocking variant of Setup. Delegates to
// commonobs.InitOTLPAsync so OTLP init runs in its own goroutine with its own
// deadline (CHORA_OTLP_INIT_TIMEOUT_SECONDS, default 15s). Timeout /
// init-error degrade to a no-op shutdown so pgx pool init gets the FULL
// bootstrap budget.
//
// The W3C propagator is installed synchronously here so HTTP middleware
// + gRPC interceptors can extract/inject traceparent headers from request
// one even before the exporter completes initialization. Pre-init spans
// are dropped (no TracerProvider yet) but traceparent propagation is
// uninterrupted.
func SetupAsync(ctx context.Context) *bootstrap.OTLPHandle {
	installPropagator()
	return commonobs.InitOTLPAsync(ctx, ServiceName, ServiceVersion)
}
