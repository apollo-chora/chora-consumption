package main

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/envelope"
	"github.com/apollo-chora/chora-common/eventbus"
)

// recordedSub is one Subscribe call the wiring made on the stub bus.
type recordedSub struct {
	name    string
	subject string
}

// stubBus is an eventbus.Bus test double that records every subscription
// the wiring makes. Subscribe is synchronous from the caller's perspective
// (the wiring wraps it in a goroutine), so tests drain subs with await.
type stubBus struct {
	subs chan recordedSub
}

func newStubBus() *stubBus { return &stubBus{subs: make(chan recordedSub, 256)} }

func (b *stubBus) Publish(context.Context, string, envelope.Envelope, []byte) error { return nil }

func (b *stubBus) Subscribe(_ context.Context, cfg eventbus.ConsumerConfig, _ eventbus.Handler) error {
	b.subs <- recordedSub{name: cfg.Name, subject: cfg.Subject}
	return nil
}

func (b *stubBus) Close() error { return nil }

// await blocks until n subscriptions have been recorded (or the deadline
// passes), then returns everything recorded including any extras.
func (b *stubBus) await(t *testing.T, n int) []recordedSub {
	t.Helper()
	var got []recordedSub
	deadline := time.After(2 * time.Second)
	for len(got) < n {
		select {
		case s := <-b.subs:
			got = append(got, s)
		case <-deadline:
			t.Fatalf("stubBus: got %d subscriptions, want %d", len(got), n)
		}
	}
	for {
		select {
		case s := <-b.subs:
			got = append(got, s)
		default:
			return got
		}
	}
}

// subjects returns the recorded subjects, failing on timeout if none arrive.
func (b *stubBus) subjects(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, s := range b.await(t, 1) {
		out = append(out, s.subject)
	}
	return out
}

// drain returns whatever has been recorded so far after a brief settle
// window, without failing when the wiring bound nothing (nil-guard paths).
func (b *stubBus) drain(t *testing.T) []recordedSub {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	var got []recordedSub
	for {
		select {
		case s := <-b.subs:
			got = append(got, s)
		default:
			return got
		}
	}
}
