// wiring_guards_cover_test.go — coverage for the remaining pure / nil-guard
// boot helpers that need NO external resource:
//
//   - outboxWorkerID: env-precedence resolver (CHORA_OUTBOX_WORKER_ID >
//     HOSTNAME > hardcoded default).
//   - nopCompanionResolver.ResolveActiveCompanion: the F1 fallback contract —
//     always returns ErrNoCompanion so the growth subscriber drops EXP rather
//     than nil-derefs when no CompanionInstances repo is wired.
//   - sharedBreedDistributionProvider: the process-wide memoizer around
//     bootstrapBreedDistributionProvider — must return a non-nil provider and
//     must hand back the SAME instance on repeat calls (singleton cache).
//   - wireSubscriberPushHandlers / wireGrowthPushHandlers nil-guard early
//     returns (no client/DB constructed on the guard path).
//
// The constructing bodies of the wire*/bootstrap* functions are intentionally
// NOT covered — they build real DB/PubSub/HTTP clients.
package main

import (
	"context"
	"errors"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

// --- outboxWorkerID --------------------------------------------------------

func TestOutboxWorkerID(t *testing.T) {
	tests := []struct {
		name     string
		workerID string
		hostname string
		want     string
	}{
		{name: "explicit worker id wins", workerID: "worker-7", hostname: "pod-abc", want: "worker-7"},
		{name: "falls back to HOSTNAME", workerID: "", hostname: "pod-abc", want: "pod-abc"},
		{name: "hardcoded default when both empty", workerID: "", hostname: "", want: "chora-consumption-local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CHORA_OUTBOX_WORKER_ID", tt.workerID)
			t.Setenv("HOSTNAME", tt.hostname)
			if got := outboxWorkerID(); got != tt.want {
				t.Fatalf("outboxWorkerID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- nopCompanionResolver ---------------------------------------------------

// The nop resolver is the defensive F1 fallback chosen by pickCompanionResolver
// when no CompanionInstances repo is wired. Its contract is load-bearing: it
// MUST return ErrNoCompanion (not a nil error + empty id) so the growth
// subscriber treats the award as a non-fatal drop instead of mis-attributing
// EXP to an empty companion id.
func TestNopCompanionResolver_AlwaysReturnsErrNoCompanion(t *testing.T) {
	id, err := nopCompanionResolver{}.ResolveActiveCompanion(context.Background(), "tenant-1", "gcid-1")
	if id != "" {
		t.Errorf("ResolveActiveCompanion id = %q, want empty", id)
	}
	if !errors.Is(err, subscribers.ErrNoCompanion) {
		t.Fatalf("ResolveActiveCompanion err = %v, want ErrNoCompanion", err)
	}
}

// --- sharedBreedDistributionProvider ----------------------------------------

// sharedBreedDistributionProvider memoises bootstrapBreedDistributionProvider
// behind a package-level sync.Once so every caller in the process shares one
// client/cache/table. It must always return non-nil (there is always the
// StaticBreedDistributionProvider fallback) and must return the SAME instance
// on repeat calls regardless of intermediate env changes (singleton contract).
// The underlying provider construction (gRPC/REST/static) is covered by the
// bootstrapBreedDistributionProvider tests in growth_wiring_test.go.
func TestSharedBreedDistributionProvider_NonNilAndMemoized(t *testing.T) {
	first := sharedBreedDistributionProvider()
	if first == nil {
		t.Fatal("sharedBreedDistributionProvider() returned nil — expected a non-nil provider")
	}
	second := sharedBreedDistributionProvider()
	if second != first {
		t.Error("sharedBreedDistributionProvider() returned a DIFFERENT instance on repeat call — expected the memoized singleton")
	}
}

// --- nil-guard early returns ----------------------------------------------

// wireSubscriberBusBindings returns immediately (no handler built, no
// subscription made) when either srv or ext is nil. Exercising the guard
// proves it never panics on nil deps — and never touches the external client
// constructors.
func TestWireSubscriberBusBindings_NilArgsNoPanic(t *testing.T) {
	wireSubscriberBusBindings(context.Background(), nil, nil, nil)

	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)
	bus := newStubBus()
	wireSubscriberBusBindings(context.Background(), srv, nil, bus) // ext nil -> guard returns
	wireSubscriberBusBindings(context.Background(), nil, ext, bus) // srv nil -> guard returns
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil srv/ext: %v", got)
	}
}

// wireGrowthPushHandlers returns immediately when growthSvc is nil — no
// subscriber constructed, no subscriptions made.
func TestWireGrowthPushHandlers_NilServiceNoPanic(t *testing.T) {
	srv := httpadapter.NewServer()
	bus := newStubBus()
	wireGrowthPushHandlers(context.Background(), srv, nil, nil, nil, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil growth service: %v", got)
	}
}
