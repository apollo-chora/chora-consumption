// subscriber_bus_bindings_test.go — covers the cross-domain subscriber
// bindings in wireSubscriberBusBindings (the NATS pull subscriptions that
// replaced the retired Pub/Sub push routes). Each bound subscriber, when
// its ExtServer field is set, registers a durable subscription on its event
// subject. We inject a recording bus and assert the subjects resolve to the
// expected bindings, not nothing.
package main

import (
	"context"
	"testing"

	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestBusBindings_AllSubscribersBoundWhenWired(t *testing.T) {
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)
	// The pg-backed subscribers are constructed over nil repos: binding only
	// wraps them in handlers, it makes no repo calls.
	ext.CourseMetadataSub = subscribers.NewCourseMetadataSubscriber(nil)
	ext.LearnerProfileSub = subscribers.NewLearnerProfileSubscriber(nil)
	bus := newStubBus()

	wireSubscriberBusBindings(context.Background(), srv, ext, bus)

	subs := bus.await(t, 13)
	got := map[string]bool{}
	for _, s := range subs {
		got[s.subject] = true
	}
	for _, want := range []string{
		"chora.delivery.enrollment.created.v1",
		"chora.creation.atom.created.v1",
		"chora.creation.atom.published.v1",
		"chora.consumption.learning_path.bootstrapped.v1",
		"chora.delivery.course.content_composed.v1",
		"chora.delivery.course.created.v1",
		"chora.delivery.course.released.v1",
		"chora.delivery.certification.issued.v1",
		"chora.consumption.learning_path.completed.v1",
		"chora.delivery.submission.graded.v1",
		"chora.delivery.enrollment.completed.v1",
		"chora.consumption.preferences.updated.v1",
	} {
		if !got[want] {
			t.Errorf("%s not bound (bound: %v)", want, got)
		}
	}
}

func TestBusBindings_NoneWhenBusNil(t *testing.T) {
	// With no bus (NATS_URL unset) the subscribers stay constructed but
	// unbound — mirroring the chora-identity pattern.
	srv := httpadapter.NewServer()
	ext := httpadapter.NewExtServer(nil)
	wireSubscriberBusBindings(context.Background(), srv, ext, nil)
}
