// campaign_xp_wiring_test.go — WS-C5 (CHO-2084): the boot helper that binds
// the campaign XP subscriber to the eventbus (the NATS pull subscriptions
// that replaced the retired Pub/Sub push inbox). The subscriber + handler are
// tested in their own packages; this verifies the wiring seam mirrors
// kg_invalidation_wiring_test.
package main

import (
	"context"
	"testing"
	"time"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

type xpWireAward struct{}

func (xpWireAward) AwardExp(context.Context, growth.AwardExpInput) (*growth.AwardExpResponse, error) {
	return &growth.AwardExpResponse{}, nil
}

type xpWireResolver struct{}

func (xpWireResolver) ResolveForGoal(context.Context, string, string, string) (string, error) {
	return "fam", nil
}

type xpWireHistory struct{}

func (xpWireHistory) LastAwardAt(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, nil
}

type xpWireLineage struct{}

func (xpWireLineage) PriorLadderState(context.Context, string, string, string) (int, bool, error) {
	return 0, false, nil
}

func TestWireCampaignXPPush_BindsCampaignTopics(t *testing.T) {
	srv := httpadapter.NewServer()
	bus := newStubBus()

	wireCampaignXPPush(context.Background(), srv, xpWireAward{}, xpWireResolver{}, xpWireHistory{}, xpWireLineage{}, bus)

	subs := bus.await(t, 3)
	got := map[string]bool{}
	for _, s := range subs {
		got[s.subject] = true
	}
	for _, want := range []string{
		subscribers.TopicCampaignRungCleared,
		subscribers.TopicCampaignNodeWon,
		subscribers.TopicCampaignGoalSealed,
	} {
		if !got[want] {
			t.Errorf("campaign XP subscriber not bound to %s (bound: %v)", want, got)
		}
	}
}

func TestWireCampaignXPPush_NilDepsNoOp(t *testing.T) {
	// Must not panic on nil args; a missing dep leaves the subscriber
	// unbound (the subs are only provisioned once the handler is live —
	// five-wirings discipline).
	wireCampaignXPPush(context.Background(), nil, nil, nil, nil, nil, nil)

	bus := newStubBus()
	wireCampaignXPPush(context.Background(), httpadapter.NewServer(), nil, xpWireResolver{}, xpWireHistory{}, xpWireLineage{}, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil award port: %v", got)
	}

	bus = newStubBus()
	wireCampaignXPPush(context.Background(), httpadapter.NewServer(), xpWireAward{}, nil, xpWireHistory{}, xpWireLineage{}, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil resolver: %v", got)
	}

	bus = newStubBus()
	wireCampaignXPPush(context.Background(), httpadapter.NewServer(), xpWireAward{}, xpWireResolver{}, nil, xpWireLineage{}, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil award history: %v", got)
	}

	bus = newStubBus()
	wireCampaignXPPush(context.Background(), httpadapter.NewServer(), xpWireAward{}, xpWireResolver{}, xpWireHistory{}, nil, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscriptions bound despite nil lineage history (D14 guard is mandatory wiring): %v", got)
	}
}

func TestWireCampaignXPPush_NilBusNoOp(t *testing.T) {
	// Without a bus (NATS_URL unset) the subscriber is constructed but
	// never bound — mirroring the chora-identity pattern.
	wireCampaignXPPush(context.Background(), httpadapter.NewServer(), xpWireAward{}, xpWireResolver{}, xpWireHistory{}, xpWireLineage{}, nil)
}
