// kg_invalidation_cover_test.go — ack-after-process (resilience-d6) coverage
// for the AtomRevisionUpdated + UserRetentionShifted invalidation subscribers.
//
// The existing kg_invalidation_subscriber_test.go proves the AtomPublished
// subscriber NACKs (returns the error) when the repo mutation fails. These add
// the equivalent contract for the other two subscribers: a repo error during
// MarkInvalidated* must propagate (no premature ack), and the publisher must
// NOT be called before persistence succeeds.
package subscribers_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
)

func TestAtomRevisionUpdatedKGSubscriber_RepoErrorNacks(t *testing.T) {
	repo := &failingHexRepo{err: errFakeRepoFailure}
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewAtomRevisionUpdatedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-rev-nack", "t1", "u1")
	err := sub.Handle(context.Background(), env, subscribers.AtomRevisionUpdatedKGPayload{AtomID: "atom-rev", TenantID: "t1"})
	if err == nil {
		t.Error("expected nack when MarkInvalidatedByFocal fails")
	}
	if len(pub.Calls()) != 0 {
		t.Errorf("publisher must not be called before persistence; got %d calls", len(pub.Calls()))
	}
}

func TestUserRetentionShiftedKGSubscriber_RepoErrorNacks(t *testing.T) {
	repo := &failingHexRepo{err: errFakeRepoFailure}
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewUserRetentionShiftedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-retention-nack", "t1", "u1")
	err := sub.Handle(context.Background(), env, subscribers.UserRetentionShiftedKGPayload{TenantID: "t1", UserGCID: "u1"})
	if err == nil {
		t.Error("expected nack when MarkInvalidatedByUser fails")
	}
	if len(pub.Calls()) != 0 {
		t.Errorf("publisher must not be called before persistence; got %d calls", len(pub.Calls()))
	}
}
