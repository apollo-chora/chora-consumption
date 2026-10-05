// kg_claim_first_split_test.go — RED-phase tests for CHO-2130 (KG-invalidation
// trio, audit finding #5): the three fog-cache invalidation subscribers marked
// the event_id seen BEFORE the repo mutation, so a transient invalidation
// failure NACKed the event and the Pub/Sub redelivery was ack-dropped as a
// duplicate — the fog cache silently kept stale hexagons until the next
// unrelated invalidation (self-heal masks, not fixes, the loss).
//
// Pattern per test: arm a transient repo failure → Handle NACKs; heal →
// redeliver the SAME envelope → the invalidation runs; deliver a third time →
// mark() gates the duplicate.
package subscribers_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// flakyKGHexRepo wraps a real hexagon repo; while armed every mutation fails
// (transient infra); disarm (nil) to heal before the Pub/Sub redelivery.
type flakyKGHexRepo struct {
	inner      subscribers.KGHexagonInvalidator
	err        error
	focalCalls int
	userCalls  int
}

func (r *flakyKGHexRepo) MarkInvalidatedByFocal(ctx context.Context, atomID string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.focalCalls++
	return r.inner.MarkInvalidatedByFocal(ctx, atomID, reason)
}

func (r *flakyKGHexRepo) MarkInvalidatedByUser(ctx context.Context, tenantID, gcid string, reason userknowledgegraph.FogInvalidationReason) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.userCalls++
	return r.inner.MarkInvalidatedByUser(ctx, tenantID, gcid, reason)
}

func (r *flakyKGHexRepo) Find(ctx context.Context, explorationID, focalAtomID string) (*userknowledgegraph.HexagonNode, error) {
	return r.inner.Find(ctx, explorationID, focalAtomID)
}

func (r *flakyKGHexRepo) FindInvalidatedOlderThan(ctx context.Context, cutoff string) ([]*userknowledgegraph.HexagonNode, error) {
	return r.inner.FindInvalidatedOlderThan(ctx, cutoff)
}

func TestAtomPublishedKG_RedeliveryAfterTransientFailureLands(t *testing.T) {
	flaky := &flakyKGHexRepo{inner: inmem.NewHexagonRepo(), err: errFakeRepoFailure}
	sub := subscribers.NewAtomPublishedKGSubscriber(flaky, &fakeKGEventPublisher{})
	env := kgInvalidEnvelope("evt-cfs-kg1", "t1", "g1")
	p := subscribers.AtomPublishedKGPayload{AtomID: "atom-kg-1", TenantID: "t1"}

	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient invalidation failure must surface (Pub/Sub NACK)")
	}
	flaky.err = nil // infra healed before the redelivery

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if flaky.focalCalls != 1 {
		t.Errorf("invalidations = %d; want 1 (redelivery must run — claim-first swallowed it)", flaky.focalCalls)
	}
	// Third delivery of the SAME event: mark() now gates the skip.
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if flaky.focalCalls != 1 {
		t.Errorf("duplicate re-ran the invalidation: %d calls; want 1", flaky.focalCalls)
	}
}

func TestAtomRevisionUpdatedKG_RedeliveryAfterTransientFailureLands(t *testing.T) {
	flaky := &flakyKGHexRepo{inner: inmem.NewHexagonRepo(), err: errFakeRepoFailure}
	sub := subscribers.NewAtomRevisionUpdatedKGSubscriber(flaky, &fakeKGEventPublisher{})
	env := kgInvalidEnvelope("evt-cfs-kg2", "t1", "g1")
	p := subscribers.AtomRevisionUpdatedKGPayload{AtomID: "atom-kg-2", TenantID: "t1"}

	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient invalidation failure must surface (Pub/Sub NACK)")
	}
	flaky.err = nil

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if flaky.focalCalls != 1 {
		t.Errorf("invalidations = %d; want 1 (redelivery must run — claim-first swallowed it)", flaky.focalCalls)
	}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if flaky.focalCalls != 1 {
		t.Errorf("duplicate re-ran the invalidation: %d calls; want 1", flaky.focalCalls)
	}
}

func TestUserRetentionShiftedKG_RedeliveryAfterTransientFailureLands(t *testing.T) {
	flaky := &flakyKGHexRepo{inner: inmem.NewHexagonRepo(), err: errFakeRepoFailure}
	sub := subscribers.NewUserRetentionShiftedKGSubscriber(flaky, &fakeKGEventPublisher{})
	env := kgInvalidEnvelope("evt-cfs-kg3", "t1", "g1")
	p := subscribers.UserRetentionShiftedKGPayload{TenantID: "t1", UserGCID: "g1"}

	if err := sub.Handle(context.Background(), env, p); err == nil {
		t.Fatal("transient invalidation failure must surface (Pub/Sub NACK)")
	}
	flaky.err = nil

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if flaky.userCalls != 1 {
		t.Errorf("invalidations = %d; want 1 (redelivery must run — claim-first swallowed it)", flaky.userCalls)
	}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if flaky.userCalls != 1 {
		t.Errorf("duplicate re-ran the invalidation: %d calls; want 1", flaky.userCalls)
	}
}
