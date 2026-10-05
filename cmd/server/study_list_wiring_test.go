// study_list_wiring_test.go — nil-guard + happy-path coverage for the WS-4
// collection→study-list boot helper (ADR-233).
//
// The guards are the point: binding this subscriber with a missing repo or a
// missing publisher would produce a subscription that ACKs real conversion
// events into a void (no durable path, or a path that never emits
// learning_path.bootstrapped.v1 and therefore never reaches the daily dose's
// curiosity slot). Both failures LOOK like success. The helper must refuse to
// bind instead.
package main

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/eventbus"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	httpadapter "github.com/apollo-chora/chora-consumption/internal/adapter/http"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestWireCollectionConvertedToStudyList_NilArgsAreNoOp(t *testing.T) {
	// Must not panic and must not bind anything.
	wireCollectionConvertedToStudyList(context.Background(), nil, nil, nil)

	bus := newStubBus()
	wireCollectionConvertedToStudyList(context.Background(), &httpadapter.Server{}, nil, bus)
	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscription bound with a nil ExtServer: %v", got)
	}
}

// No LearningPath repo ⇒ no bind (a study list lost on every pod restart is
// not a study list).
func TestWireCollectionConvertedToStudyList_RefusesWithoutPathsRepo(t *testing.T) {
	bus := newStubBus()
	ext := &httpadapter.ExtServer{Publisher: events.NewInMemoryPublisher()}

	wireCollectionConvertedToStudyList(context.Background(), &httpadapter.Server{}, ext, bus)

	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscription bound with no LearningPath repo; want a loud refusal to bind: %v", got)
	}
}

// No Publisher ⇒ no bind. A bound subscriber without a publisher would build
// the path but never emit learning_path.bootstrapped.v1 → active_path_topics is
// never fed → the study list reaches NO dose slot. An INERT study list is worse
// than an unbound subscription because it reports success.
func TestWireCollectionConvertedToStudyList_RefusesWithoutPublisher(t *testing.T) {
	bus := newStubBus()
	ext := &httpadapter.ExtServer{Paths: inmem.NewLearningPathRepo()}

	wireCollectionConvertedToStudyList(context.Background(), &httpadapter.Server{}, ext, bus)

	if got := bus.drain(t); len(got) != 0 {
		t.Errorf("subscription bound with no Publisher — it would build study lists that never " +
			"emit learning_path.bootstrapped.v1 and so never reach the dose's curiosity slot " +
			"(ADR-233). Want a loud refusal to bind: %v", got)
	}
}

func TestWireCollectionConvertedToStudyList_BindsWhenFullyWired(t *testing.T) {
	bus := newStubBus()
	ext := &httpadapter.ExtServer{
		Paths:     inmem.NewLearningPathRepo(),
		Publisher: events.NewInMemoryPublisher(),
	}

	wireCollectionConvertedToStudyList(context.Background(), &httpadapter.Server{}, ext, bus)

	subs := bus.await(t, 1)
	if subs[0].subject != subscribers.TopicCollectionConvertedToStudyList {
		t.Errorf("bound subject = %q; want %q", subs[0].subject, subscribers.TopicCollectionConvertedToStudyList)
	}
}

func TestWireCollectionConvertedToStudyList_NilBusNoOp(t *testing.T) {
	ext := &httpadapter.ExtServer{
		Paths:     inmem.NewLearningPathRepo(),
		Publisher: events.NewInMemoryPublisher(),
	}
	wireCollectionConvertedToStudyList(context.Background(), &httpadapter.Server{}, ext, nil)
}

var _ eventbus.Bus = (*stubBus)(nil)
