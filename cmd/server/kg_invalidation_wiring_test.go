// kg_invalidation_wiring_test.go — ADR-204 Slice F.1: the boot helper that
// binds the three KG fog-cache invalidation subscribers to the eventbus
// (the NATS pull subscriptions that replaced the retired Pub/Sub push
// inbox). The subscribers + handler already exist + are tested
// (internal/adapter/subscribers/kg_invalidation_subscriber_test.go); this
// verifies the wiring seam mirrors growth_wiring_test.go.
package main

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/eventbus"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestWireKGInvalidation_BindsKGTopics(t *testing.T) {
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil) // seeds in-memory KGHexagons + KGEvents
	bus := newStubBus()

	wireKGInvalidation(context.Background(), srv, ext, bus)

	subs := bus.await(t, 3)
	got := map[string]bool{}
	for _, s := range subs {
		got[s.subject] = true
	}
	for _, want := range []string{
		subscribers.TopicCreationAtomPublished,
		"chora.creation.atom.updated.v1",
		"chora.consumption.user_retention.shifted.v1",
	} {
		if !got[want] {
			t.Errorf("KG invalidation subscriber not bound to %s (bound: %v)", want, got)
		}
	}
}

func TestWireKGInvalidation_NilArgsNoOp(t *testing.T) {
	// Must not panic on nil args.
	wireKGInvalidation(context.Background(), nil, nil, nil)

	bus := newStubBus()
	wireKGInvalidation(context.Background(), httpadapter.NewServer(), nil, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil ext: %v", got)
	}
}

// When the KG hexagon repo is unavailable the helper logs + skips rather than
// constructing a handler over a nil invalidator (defensive guard branch).
func TestWireKGInvalidation_MissingKGDepsNoOp(t *testing.T) {
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)
	ext.KGHexagons = nil // simulate KG repos not wired
	bus := newStubBus()

	wireKGInvalidation(context.Background(), srv, ext, bus)

	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil KGHexagons invalidator: %v", got)
	}
}

func TestWireKGInvalidation_NilBusNoOp(t *testing.T) {
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)
	wireKGInvalidation(context.Background(), srv, ext, nil)
}

var _ eventbus.Bus = (*stubBus)(nil)
