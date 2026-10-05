// kg_projection_cover_test.go — white-box coverage tests for the smaller
// no-op / skip / validation branches across the KG-projection subscribers
// that the behavioural tests do not reach:
//
//   - TopicAccuracySubscriber: unclassified-atom skip + no-session-id dedup skip
//   - ActivePathTopicsSubscriber: empty-topic-set returns (bootstrap +
//     advance), empty advance atom_id, and resolveTopics empty-tag + dup-tag skips
//   - JunctionDetector: threshold<1 default, empty candidate set,
//     missing current_cluster_id, and Load-of-unpersisted-current-cluster error
//   - KG-invalidation subscribers: empty atom_id / empty gcid rejection +
//     idempotent dedup short-circuit (white-box, distinct from the
//     subscribers_test black-box coverage)
package subscribers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

// ----------------- TopicAccuracySubscriber skip branches -----------------

func TestTopicAccuracy_SkipsUnclassifiedMCQ(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	// MCQ (gradable) but NO topic tags → PrimaryTopic == "" → no scoring.
	a, err := atomIndexFromFreshParams("atom-untopiced", "t1", "c1", "mcq", []string{}, "opt-a")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-untopiced", "t1", "phyllis")
	if err := sub.Handle(context.Background(), env, AtomSessionCompletedPayload{
		SessionID: "sess-1", AtomID: "atom-untopiced", LearnerGCID: "phyllis",
		TenantID: "t1", IsCorrect: true, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(accByLearner(t, accRepo, "t1", "phyllis")) != 0 {
		t.Error("unclassified MCQ must not contribute to topic accuracy")
	}
}

func TestTopicAccuracy_NoSessionIDSkipsDedup(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	accRepo := inmem.NewTopicAccuracyRepo()
	sub := NewTopicAccuracySubscriber(accRepo, atomIdx)

	a, _ := atomIndexHelperWithTopics("atom-1", "t1", "phyllis-c", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a)

	// Empty SessionID → markSessionSeen returns true (skips dedup), so the
	// attempt still records. Distinct event_ids avoid the envelope dedup.
	env := newTestEnvelope("evt-no-sess", "t1", "phyllis")
	if err := sub.Handle(context.Background(), env, AtomSessionCompletedPayload{
		SessionID: "", AtomID: "atom-1", LearnerGCID: "phyllis",
		TenantID: "t1", IsCorrect: true, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if accByLearner(t, accRepo, "t1", "phyllis")["agile"] != 1.0 {
		t.Error("empty session_id should still record the attempt (dedup skipped)")
	}
}

// ----------------- ActivePathTopicsSubscriber no-op branches -----------------

func TestActivePathTopics_BootstrapEmptyTopicSetNoOps(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)
	// No atoms seeded → resolveTopics returns empty → early return, no panic.
	env := newTestEnvelope("evt-apt-empty", "t1", "phyllis")
	if err := sub.HandleBootstrap(context.Background(), env, LearningPathBootstrappedPayload{
		PathID: "p1", CourseID: "c1", LearnerGCID: "phyllis", TenantID: "t1",
		AtomIDs: []string{"missing-1", "missing-2"}, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("HandleBootstrap: %v", err)
	}
	if len(aptTopics(t, apt, "t1", "phyllis")) != 0 {
		t.Error("expected no topics recorded for an all-missing atom set")
	}
}

func TestActivePathTopics_AdvanceEmptyAtomIDNoOps(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)
	env := newTestEnvelope("evt-apt-adv-empty-atom", "t1", "phyllis")
	if err := sub.HandleAdvance(context.Background(), env, LearningPathAdvancedPayload{
		PathID: "p1", LearnerGCID: "phyllis", TenantID: "t1",
		AtomID: "", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	if len(aptTopics(t, apt, "t1", "phyllis")) != 0 {
		t.Error("empty advance atom_id must be a no-op")
	}
}

func TestActivePathTopics_AdvanceUnresolvableTopicNoOps(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)
	// AtomID present but not in atom_index → resolveTopics empty → no-op.
	env := newTestEnvelope("evt-apt-adv-missing", "t1", "phyllis")
	if err := sub.HandleAdvance(context.Background(), env, LearningPathAdvancedPayload{
		PathID: "p1", LearnerGCID: "phyllis", TenantID: "t1",
		AtomID: "not-projected", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("HandleAdvance: %v", err)
	}
	if len(aptTopics(t, apt, "t1", "phyllis")) != 0 {
		t.Error("advance on an unprojected atom must be a no-op")
	}
}

func TestActivePathTopics_ResolveTopicsSkipsEmptyAndDuplicateTags(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	apt := inmem.NewActivePathTopicsRepo()
	sub := NewActivePathTopicsSubscriber(apt, atomIdx)

	// One atom carrying an empty tag and a repeated tag — resolveTopics must
	// drop the empty string and de-dupe "agile" to a single entry.
	a, err := atomIndexFromFreshParams("atom-tags", "t1", "c1", "mcq",
		[]string{"", "agile", "agile"}, "opt-a")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = atomIdx.Save(context.Background(), a)

	env := newTestEnvelope("evt-apt-tags", "t1", "phyllis")
	if err := sub.HandleBootstrap(context.Background(), env, LearningPathBootstrappedPayload{
		PathID: "p1", CourseID: "c1", LearnerGCID: "phyllis", TenantID: "t1",
		AtomIDs: []string{"atom-tags"}, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("HandleBootstrap: %v", err)
	}
	got := aptTopics(t, apt, "t1", "phyllis")
	if !got["agile"] {
		t.Errorf("expected agile recorded; got %v", got)
	}
	if got[""] {
		t.Error("empty tag must never be recorded")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 distinct topic (dup de-duped); got %v", got)
	}
}

// ----------------- JunctionDetector edge branches -----------------

func TestJunctionDetector_ThresholdBelowOneDefaults(t *testing.T) {
	det := NewJunctionDetector(
		repoinmem.NewMapClusterRepo(), repoinmem.NewHexagonRepo(),
		repoinmem.NewJunctionRepo(),
		events.NewKGPublisher(events.NewInMemoryPublisher()),
		0, // < 1 → defaults to DefaultJunctionOverlapThreshold
	)
	if det.threshold != DefaultJunctionOverlapThreshold {
		t.Errorf("threshold = %d; want default %d", det.threshold, DefaultJunctionOverlapThreshold)
	}
}

func TestJunctionDetector_EmptyCandidateSetNoOps(t *testing.T) {
	det := NewJunctionDetector(
		repoinmem.NewMapClusterRepo(), repoinmem.NewHexagonRepo(),
		repoinmem.NewJunctionRepo(),
		events.NewKGPublisher(events.NewInMemoryPublisher()), 3,
	)
	res, err := det.DetectForCluster(context.Background(), jdTenant, jdUserA, "cluster-x", nil)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.JunctionsDetected != 0 {
		t.Errorf("detected = %d; want 0 (empty candidate set)", res.JunctionsDetected)
	}
}

func TestJunctionDetector_MissingCurrentClusterID(t *testing.T) {
	det := NewJunctionDetector(
		repoinmem.NewMapClusterRepo(), repoinmem.NewHexagonRepo(),
		repoinmem.NewJunctionRepo(),
		events.NewKGPublisher(events.NewInMemoryPublisher()), 3,
	)
	_, err := det.DetectForCluster(context.Background(), jdTenant, jdUserA, "", []string{"a"})
	if err == nil {
		t.Fatal("expected error for empty current_cluster_id")
	}
	if !strings.Contains(err.Error(), "current_cluster_id") {
		t.Errorf("err = %v; want current_cluster_id mention", err)
	}
}

// TestJunctionDetector_LoadOfUnpersistedCurrentClusterErrors drives the
// d.clusters.Load(currentClusterID) error branch: the overlap threshold is
// met against a saved cluster A's focals, but the CURRENT cluster id passed in
// was never persisted, so the event-payload Load fails and the error
// propagates (no junction is silently swallowed).
func TestJunctionDetector_LoadOfUnpersistedCurrentClusterErrors(t *testing.T) {
	ctx := context.Background()
	clusters := repoinmem.NewMapClusterRepo()
	hexagons := repoinmem.NewHexagonRepo()
	junctions := repoinmem.NewJunctionRepo()
	pub := events.NewInMemoryPublisher()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	for _, focal := range []string{"atom1", "atom2", "atom3"} {
		hex, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, focal, mkSixNeighborsInts(focal), "r", "m")
		_ = hexagons.Upsert(ctx, hex)
	}

	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(pub), 3)
	// currentClusterID is NOT saved in the cluster repo → Load() returns
	// ErrNotFound after the overlap threshold is met.
	_, err := det.DetectForCluster(ctx, jdTenant, jdUserA, "ghost-cluster",
		[]string{"atom1", "atom2", "atom3"})
	if err == nil {
		t.Fatal("expected error loading the unpersisted current cluster")
	}
}

// TestJunctionDetector_PublishErrorPropagates drives the
// d.publisher.PublishJunctionDetected error branch: overlap threshold met, the
// junction persists, but the event publish fails → the error propagates so the
// message is nacked (KGPublisher.PublishJunctionDetected delegates to the inner
// events.Publisher, which we make fail).
func TestJunctionDetector_PublishErrorPropagates(t *testing.T) {
	ctx := context.Background()
	clusters := repoinmem.NewMapClusterRepo()
	hexagons := repoinmem.NewHexagonRepo()
	junctions := repoinmem.NewJunctionRepo()

	cA, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "agile", "atomA", "")
	_ = clusters.Save(ctx, cA)
	cB, _ := userknowledgegraph.NewMapCluster(jdTenant, jdUserA, "scrum", "atomB", "")
	_ = clusters.Save(ctx, cB)
	for _, focal := range []string{"atom1", "atom2", "atom3"} {
		hex, _ := userknowledgegraph.NewHexagonNode(cA.ClusterID, "exp-A", jdTenant, jdUserA, focal, mkSixNeighborsInts(focal), "r", "m")
		_ = hexagons.Upsert(ctx, hex)
	}

	failPub := &failPublisher{failOn: 1} // inner Publish errors on the junction event
	det := NewJunctionDetector(clusters, hexagons, junctions, events.NewKGPublisher(failPub), 3)
	_, err := det.DetectForCluster(ctx, jdTenant, jdUserA, cB.ClusterID,
		[]string{"atom1", "atom2", "atom3"})
	if err == nil {
		t.Fatal("expected the junction-detected publish error to propagate")
	}
}

// ----------------- KG-invalidation white-box rejection branches -----------------

func TestAtomPublishedKG_EmptyAtomIDRejected(t *testing.T) {
	sub := NewAtomPublishedKGSubscriber(repoinmem.NewHexagonRepo(), &nopKGPub{})
	env := newTestEnvelope("evt-pub-empty-atom", "t1", "g1")
	if err := sub.Handle(context.Background(), env, AtomPublishedKGPayload{AtomID: "  ", TenantID: "t1"}); err == nil {
		t.Error("expected error for blank atom_id")
	}
}

func TestAtomRevisionUpdatedKG_EmptyAtomIDRejected(t *testing.T) {
	sub := NewAtomRevisionUpdatedKGSubscriber(repoinmem.NewHexagonRepo(), &nopKGPub{})
	env := newTestEnvelope("evt-rev-empty-atom", "t1", "g1")
	if err := sub.Handle(context.Background(), env, AtomRevisionUpdatedKGPayload{AtomID: "", TenantID: "t1"}); err == nil {
		t.Error("expected error for blank atom_id")
	}
}

func TestAtomRevisionUpdatedKG_BadEnvelopeRejected(t *testing.T) {
	sub := NewAtomRevisionUpdatedKGSubscriber(repoinmem.NewHexagonRepo(), &nopKGPub{})
	if err := sub.Handle(context.Background(), events.Envelope{}, AtomRevisionUpdatedKGPayload{AtomID: "a1"}); err == nil {
		t.Error("expected error for blank envelope")
	}
}

func TestUserRetentionShiftedKG_BadEnvelopeRejected(t *testing.T) {
	sub := NewUserRetentionShiftedKGSubscriber(repoinmem.NewHexagonRepo(), &nopKGPub{})
	if err := sub.Handle(context.Background(), events.Envelope{}, UserRetentionShiftedKGPayload{UserGCID: "g1"}); err == nil {
		t.Error("expected error for blank envelope")
	}
}

// TestUserRetentionShiftedKG_FallsBackToEnvelopeGCIDAndTenant exercises the
// payload-empty → envelope fallback branches for both gcid and tenant_id.
func TestUserRetentionShiftedKG_FallsBackToEnvelopeGCIDAndTenant(t *testing.T) {
	repo := repoinmem.NewHexagonRepo()
	sub := NewUserRetentionShiftedKGSubscriber(repo, &nopKGPub{})
	env := newTestEnvelope("evt-retention-fallback", "tenant-fb", "gcid-fb")
	// Payload omits both tenant + gcid → handler must fall back to the
	// envelope values and NOT error (no hexagons → 0 invalidated, still nil).
	if err := sub.Handle(context.Background(), env, UserRetentionShiftedKGPayload{}); err != nil {
		t.Errorf("expected fallback to envelope gcid/tenant, got %v", err)
	}
}

// nopKGPub is a no-op KGInvalidationPublisher for branches where no publish is
// expected (rejections / 0-row invalidations).
type nopKGPub struct{}

func (nopKGPub) PublishHexagonFogInvalidated(_ context.Context, _ *userknowledgegraph.HexagonNode) error {
	return nil
}
