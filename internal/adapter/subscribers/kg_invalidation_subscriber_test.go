// kg_invalidation_subscriber_test.go — RED tests for the three KG hexagon
// fog-cache invalidation subscribers (TDD Phase 1 Agent B — CHO-1575).
//
// Covered subscribers:
//
//   - AtomPublishedKGSubscriber   — chora.creation.atom.published.v1
//   - AtomRevisionUpdatedKGSubscriber — chora.creation.atom.revision_updated.v1
//   - UserRetentionShiftedKGSubscriber — chora.consumption.user_retention.shifted.v1
//
// Each subscriber must:
//  1. Validate the mandatory envelope fields.
//  2. Call HexagonRepo.MarkInvalidated* BEFORE acking the message
//     (ack = return nil — per agentic-resilience-d6 ack-after-process).
//  3. Be idempotent on event_id replay (second call with same event_id = no-op).
//  4. Publish chora.consumption.kg_hexagon_fog.invalidated.v1 for each
//     affected row (per EventPublisher port).
package subscribers_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/adapter/subscribers"
	"github.com/apollo-chora/chora-consumption/internal/domain"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func kgInvalidEnvelope(eventID, tenantID, gcid string) events.Envelope {
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: "idem-" + eventID,
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     validEnv().OccurredAt,
		PublishedAt:    validEnv().PublishedAt,
		Traceparent:    "00-aaa-bbb-00",
		SourceProject:  "chora-content",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

// fakeKGEventPublisher records PublishHexagonFogInvalidated calls.
type fakeKGEventPublisher struct {
	mu    sync.Mutex
	calls []*userknowledgegraph.HexagonNode
	err   error
}

func (f *fakeKGEventPublisher) PublishHexagonFogInvalidated(_ context.Context, h *userknowledgegraph.HexagonNode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	cp := *h
	f.calls = append(f.calls, &cp)
	return nil
}

// KGInvalidationPublisher satisfies the subscribers.KGInvalidationPublisher
// interface (see kg_invalidation_subscriber.go).
func (f *fakeKGEventPublisher) Calls() []*userknowledgegraph.HexagonNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*userknowledgegraph.HexagonNode, len(f.calls))
	copy(out, f.calls)
	return out
}

// buildHexagonInRepo seeds a HexagonNode into the inmem HexagonRepo and
// returns both the repo and the seeded node so tests can assert invalidation.
func buildHexagonInRepo(t *testing.T, explorationID, clusterID, tenantID, userGCID, focalAtomID string, neighborAtomIDs []string) (*inmem.HexagonRepo, *userknowledgegraph.HexagonNode) {
	t.Helper()
	neighbors := make([]userknowledgegraph.HexagonNeighbor, 0, 6)
	// Fill up to 6 neighbors; pad with distinct IDs.
	for i, aid := range neighborAtomIDs {
		if i >= userknowledgegraph.HexagonNeighborCount {
			break
		}
		neighbors = append(neighbors, userknowledgegraph.HexagonNeighbor{
			AtomID:     aid,
			Relation:   userknowledgegraph.NeighborRelationCuriosityJump,
			Confidence: 0.9,
		})
	}
	// Pad to exactly 6 if needed.
	for len(neighbors) < userknowledgegraph.HexagonNeighborCount {
		neighbors = append(neighbors, userknowledgegraph.HexagonNeighbor{
			AtomID:     domain.NewUUIDv7(),
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.5,
		})
	}
	hex, err := userknowledgegraph.NewHexagonNode(
		clusterID, explorationID, tenantID, userGCID, focalAtomID,
		neighbors,
		domain.NewUUIDv7(), "gemini-flash-lite",
	)
	if err != nil {
		t.Fatalf("buildHexagonInRepo: %v", err)
	}
	repo := inmem.NewHexagonRepo()
	if err := repo.Upsert(context.Background(), hex); err != nil {
		t.Fatalf("buildHexagonInRepo: upsert: %v", err)
	}
	return repo, hex
}

// ---------------------------------------------------------------------------
// AtomPublishedKGSubscriber
// ---------------------------------------------------------------------------

func TestAtomPublishedKGSubscriber_InvalidatesHexByFocal(t *testing.T) {
	const (
		atomID   = "atom-focal-A"
		tenantID = "tenant-1"
		gcid     = "user-1"
	)
	// Seed a hexagon that has atomID as its focal.
	repo, _ := buildHexagonInRepo(t, "exp-1", "cls-1", tenantID, gcid, atomID, nil)
	pub := &fakeKGEventPublisher{}

	sub := subscribers.NewAtomPublishedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-pub-1", tenantID, gcid)
	err := sub.Handle(context.Background(), env, subscribers.AtomPublishedKGPayload{AtomID: atomID, TenantID: tenantID})
	if err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}

	// Hexagon should now be invalidated (IsCacheFresh == false).
	hex, err := repo.Find(context.Background(), "exp-1", atomID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if hex.IsCacheFresh() {
		t.Error("hexagon should be invalidated after atom_published event")
	}
	if hex.InvalidationReason != userknowledgegraph.FogInvalidationReasonAtomPublished {
		t.Errorf("reason = %q, want atom_published", hex.InvalidationReason)
	}
}

func TestAtomPublishedKGSubscriber_InvalidatesHexByNeighbor(t *testing.T) {
	const (
		focalAtomID    = "atom-focal-B"
		neighborAtomID = "atom-neighbor-C"
		tenantID       = "tenant-1"
		gcid           = "user-1"
	)
	// Seed a hexagon where neighborAtomID is one of the 6 neighbors (not focal).
	repo, _ := buildHexagonInRepo(t, "exp-2", "cls-2", tenantID, gcid, focalAtomID, []string{neighborAtomID})
	pub := &fakeKGEventPublisher{}

	sub := subscribers.NewAtomPublishedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-pub-2", tenantID, gcid)
	err := sub.Handle(context.Background(), env, subscribers.AtomPublishedKGPayload{AtomID: neighborAtomID, TenantID: tenantID})
	if err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}

	hex, err := repo.Find(context.Background(), "exp-2", focalAtomID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if hex.IsCacheFresh() {
		t.Error("hexagon should be invalidated when a neighbor atom is published")
	}
}

func TestAtomPublishedKGSubscriber_Idempotent(t *testing.T) {
	const (
		atomID   = "atom-idem-D"
		tenantID = "tenant-1"
		gcid     = "user-1"
	)
	repo, _ := buildHexagonInRepo(t, "exp-3", "cls-3", tenantID, gcid, atomID, nil)
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewAtomPublishedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-pub-idem", tenantID, gcid)
	p := subscribers.AtomPublishedKGPayload{AtomID: atomID, TenantID: tenantID}

	// First call should succeed.
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Second call with SAME event_id must be a no-op (idempotency).
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	// Publish count should be 1, not 2.
	if len(pub.Calls()) != 1 {
		t.Errorf("expected 1 publish call (idempotent dedup), got %d", len(pub.Calls()))
	}
}

func TestAtomPublishedKGSubscriber_InvalidEnvelope_Rejected(t *testing.T) {
	repo := inmem.NewHexagonRepo()
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewAtomPublishedKGSubscriber(repo, pub)

	badEnv := events.Envelope{} // missing all mandatory fields
	err := sub.Handle(context.Background(), badEnv, subscribers.AtomPublishedKGPayload{AtomID: "x"})
	if err == nil {
		t.Error("expected error for invalid envelope, got nil")
	}
}

// ---------------------------------------------------------------------------
// AtomRevisionUpdatedKGSubscriber
// ---------------------------------------------------------------------------

func TestAtomRevisionUpdatedKGSubscriber_InvalidatesHexByFocal(t *testing.T) {
	const (
		atomID   = "atom-rev-focal-E"
		tenantID = "tenant-2"
		gcid     = "user-2"
	)
	repo, _ := buildHexagonInRepo(t, "exp-4", "cls-4", tenantID, gcid, atomID, nil)
	pub := &fakeKGEventPublisher{}

	sub := subscribers.NewAtomRevisionUpdatedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-rev-1", tenantID, gcid)
	err := sub.Handle(context.Background(), env, subscribers.AtomRevisionUpdatedKGPayload{AtomID: atomID, TenantID: tenantID})
	if err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}

	hex, err := repo.Find(context.Background(), "exp-4", atomID)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if hex.IsCacheFresh() {
		t.Error("hexagon should be invalidated after atom_revision_updated event")
	}
	if hex.InvalidationReason != userknowledgegraph.FogInvalidationReasonAtomRevisionUpdated {
		t.Errorf("reason = %q, want atom_revision_updated", hex.InvalidationReason)
	}
}

func TestAtomRevisionUpdatedKGSubscriber_Idempotent(t *testing.T) {
	const (
		atomID   = "atom-rev-idem-F"
		tenantID = "tenant-2"
		gcid     = "user-2"
	)
	repo, _ := buildHexagonInRepo(t, "exp-5", "cls-5", tenantID, gcid, atomID, nil)
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewAtomRevisionUpdatedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-rev-idem", tenantID, gcid)
	p := subscribers.AtomRevisionUpdatedKGPayload{AtomID: atomID, TenantID: tenantID}

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if len(pub.Calls()) != 1 {
		t.Errorf("expected 1 publish (idempotent), got %d", len(pub.Calls()))
	}
}

// ---------------------------------------------------------------------------
// UserRetentionShiftedKGSubscriber
// ---------------------------------------------------------------------------

func TestUserRetentionShiftedKGSubscriber_InvalidatesAllUserHexagons(t *testing.T) {
	const (
		tenantID = "tenant-3"
		gcid     = "user-3"
		atomA    = "atom-G"
		atomB    = "atom-H"
	)
	// Two hexagons for the SAME user.
	repo, _ := buildHexagonInRepo(t, "exp-6a", "cls-6a", tenantID, gcid, atomA, nil)
	// Add a second hexagon to the SAME repo.
	neighborsB := make([]userknowledgegraph.HexagonNeighbor, userknowledgegraph.HexagonNeighborCount)
	for i := range neighborsB {
		neighborsB[i] = userknowledgegraph.HexagonNeighbor{
			AtomID:     domain.NewUUIDv7(),
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.7,
		}
	}
	hexB, err := userknowledgegraph.NewHexagonNode(
		"cls-6b", "exp-6b", tenantID, gcid, atomB,
		neighborsB, domain.NewUUIDv7(), "gemini-flash-lite",
	)
	if err != nil {
		t.Fatalf("NewHexagonNode B: %v", err)
	}
	if err := repo.Upsert(context.Background(), hexB); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}

	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewUserRetentionShiftedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-retention-1", tenantID, gcid)
	err = sub.Handle(context.Background(), env, subscribers.UserRetentionShiftedKGPayload{TenantID: tenantID, UserGCID: gcid})
	if err != nil {
		t.Fatalf("Handle: unexpected error: %v", err)
	}

	// Both hexagons must be invalidated.
	hexA, _ := repo.Find(context.Background(), "exp-6a", atomA)
	if hexA.IsCacheFresh() {
		t.Error("hexagon A should be invalidated")
	}
	hexBFetched, _ := repo.Find(context.Background(), "exp-6b", atomB)
	if hexBFetched.IsCacheFresh() {
		t.Error("hexagon B should be invalidated")
	}
	if hexA.InvalidationReason != userknowledgegraph.FogInvalidationReasonUserRetentionShift {
		t.Errorf("reason A = %q, want user_retention_shifted", hexA.InvalidationReason)
	}
}

func TestUserRetentionShiftedKGSubscriber_Idempotent(t *testing.T) {
	const (
		tenantID = "tenant-3"
		gcid     = "user-3b"
		atomA    = "atom-I"
	)
	repo, _ := buildHexagonInRepo(t, "exp-7", "cls-7", tenantID, gcid, atomA, nil)
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewUserRetentionShiftedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-retention-idem", tenantID, gcid)
	p := subscribers.UserRetentionShiftedKGPayload{TenantID: tenantID, UserGCID: gcid}

	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := sub.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	// Publish count = 1 (idempotency guard).
	if len(pub.Calls()) != 1 {
		t.Errorf("expected 1 publish (idempotent), got %d", len(pub.Calls()))
	}
}

func TestUserRetentionShiftedKGSubscriber_MissingGCID_Rejected(t *testing.T) {
	repo := inmem.NewHexagonRepo()
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewUserRetentionShiftedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-retention-bad", "tenant-3", "")
	err := sub.Handle(context.Background(), env, subscribers.UserRetentionShiftedKGPayload{TenantID: "tenant-3", UserGCID: ""})
	if err == nil {
		t.Error("expected error for missing gcid, got nil")
	}
}

// ---------------------------------------------------------------------------
// Ack-after-process (resilience-d6) contract test
//
// The subscriber MUST call MarkInvalidated* before returning nil (ack).
// We simulate a repo error to verify that Handle propagates it (i.e. does
// NOT ack prematurely before invalidation is persisted).
// ---------------------------------------------------------------------------

func TestAtomPublishedKGSubscriber_AckAfterProcess_RepoErrorNacks(t *testing.T) {
	// Use a nil HexagonRepo so MarkInvalidatedByFocal panics — but
	// we need a controlled error. Use a fake repo that errors on MarkInvalidatedByFocal.
	repo := &failingHexRepo{err: errFakeRepoFailure}
	pub := &fakeKGEventPublisher{}
	sub := subscribers.NewAtomPublishedKGSubscriber(repo, pub)

	env := kgInvalidEnvelope("evt-nack", "t1", "u1")
	err := sub.Handle(context.Background(), env, subscribers.AtomPublishedKGPayload{AtomID: "atom-J", TenantID: "t1"})
	if err == nil {
		t.Error("expected error (nack) when repo MarkInvalidatedByFocal fails")
	}
	// Publisher must NOT have been called (ack-after-process: no publish before persistence).
	if len(pub.Calls()) != 0 {
		t.Errorf("publisher should not be called when repo errors; got %d calls", len(pub.Calls()))
	}
}

// ---------------------------------------------------------------------------
// failingHexRepo — minimal fake for the ack-after-process contract test.
// ---------------------------------------------------------------------------

var errFakeRepoFailure = errors.New("test: simulated repo failure")

// failingHexRepo satisfies the KGHexagonInvalidator interface by always
// returning an error on MarkInvalidatedByFocal.
type failingHexRepo struct {
	err error
}

func (r *failingHexRepo) MarkInvalidatedByFocal(_ context.Context, _ string, _ userknowledgegraph.FogInvalidationReason) (int, error) {
	return 0, r.err
}

func (r *failingHexRepo) MarkInvalidatedByUser(_ context.Context, _, _ string, _ userknowledgegraph.FogInvalidationReason) (int, error) {
	return 0, r.err
}

func (r *failingHexRepo) Find(_ context.Context, _, _ string) (*userknowledgegraph.HexagonNode, error) {
	return nil, r.err
}

func (r *failingHexRepo) FindInvalidatedOlderThan(_ context.Context, _ string) ([]*userknowledgegraph.HexagonNode, error) {
	return nil, r.err
}
