// dosing_projection_port_test.go — CHO-2167 RED.
//
// Three defects this locks down, all of which the in-memory-only wiring hid:
//
//  1. The subscribers took the CONCRETE *inmem.* repos, so the durable pg
//     adapter (which has existed since CHO-1471) could never be bound by the
//     composition root. They must take a domain-owned port instead.
//  2. ActivePathTopicsSubscriber dropped the learning_path_id on the floor.
//     The pg table's PK is (tenant_id, gcid, learning_path_id, topic_tag) and
//     the column is NOT NULL — an absent path id fails every real write.
//  3. Both subscribers IGNORED the write result. Against an in-memory map that
//     is harmless (it cannot fail); against Postgres it means a transient
//     failure is ACKed and the learner's topics are lost forever. A failed
//     projection write MUST NACK.
package subscribers

import (
	"context"
	"errors"
	"testing"

	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
)

// --- fakes: the durable side, observable ------------------------------------

type fakeActivePathTopics struct {
	recordedTenant string
	recordedGCID   string
	recordedPathID string
	recordedTopics []string
	calls          int
	err            error
}

func (f *fakeActivePathTopics) RecordPathTopics(_ context.Context, tenantID, gcid, learningPathID string, topics []string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.recordedTenant = tenantID
	f.recordedGCID = gcid
	f.recordedPathID = learningPathID
	f.recordedTopics = append([]string(nil), topics...)
	return nil
}

func (f *fakeActivePathTopics) GetTopics(context.Context, string, string) (map[string]bool, error) {
	out := make(map[string]bool, len(f.recordedTopics))
	for _, t := range f.recordedTopics {
		out[t] = true
	}
	return out, nil
}

type fakeTopicAccuracy struct {
	calls int
	err   error
}

func (f *fakeTopicAccuracy) RecordAttempt(context.Context, string, string, string, bool) error {
	f.calls++
	return f.err
}

func (f *fakeTopicAccuracy) GetByLearner(context.Context, string, string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// --- 1. the port + the learning_path_id -------------------------------------

func TestActivePathTopicsSubscriber_WritesThroughPortWithPathID(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	repo := &fakeActivePathTopics{}
	sub := NewActivePathTopicsSubscriber(repo, atomIdx)

	const pathID = "0197a000-0000-7000-8000-000000000001"
	err := sub.HandleBootstrap(context.Background(), newTestEnvelope("evt-apt-port", "t1", "phyllis"),
		LearningPathBootstrappedPayload{
			PathID:      pathID,
			CourseID:    "c1",
			LearnerGCID: "phyllis",
			TenantID:    "t1",
			AtomIDs:     []string{"atom-1"},
		})
	if err != nil {
		t.Fatalf("HandleBootstrap: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("want 1 durable write, got %d", repo.calls)
	}
	// The PK column a dropped path id would violate.
	if repo.recordedPathID != pathID {
		t.Errorf("learning_path_id not carried to the projection: got %q want %q", repo.recordedPathID, pathID)
	}
	if repo.recordedTenant != "t1" || repo.recordedGCID != "phyllis" {
		t.Errorf("tenant/gcid not carried: got %q/%q", repo.recordedTenant, repo.recordedGCID)
	}
}

// --- 2. fail-loud: a failed projection write must NACK -----------------------

func TestActivePathTopicsSubscriber_NACKsOnWriteFailure(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	boom := errors.New("pg: connection reset by peer")
	repo := &fakeActivePathTopics{err: boom}
	sub := NewActivePathTopicsSubscriber(repo, atomIdx)

	err := sub.HandleBootstrap(context.Background(), newTestEnvelope("evt-apt-fail", "t1", "phyllis"),
		LearningPathBootstrappedPayload{
			PathID:      "0197a000-0000-7000-8000-000000000001",
			LearnerGCID: "phyllis",
			TenantID:    "t1",
			AtomIDs:     []string{"atom-1"},
		})
	if err == nil {
		t.Fatal("a failed projection write was ACKed — the learner's topics vanish silently")
	}
	if !errors.Is(err, boom) {
		t.Errorf("want the underlying error wrapped, got %v", err)
	}
}

// A write that failed must NOT be marked seen, or the Pub/Sub redelivery — the
// only chance to recover — is dropped as a duplicate. This is the claim-first
// bug CHO-2130 already fixed in TopicAccuracySubscriber; the active-path
// subscriber still claims BEFORE it writes.
func TestActivePathTopicsSubscriber_RedeliveryAfterFailureStillWrites(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	a1, _ := atomIndexHelperWithTopics("atom-1", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	repo := &fakeActivePathTopics{err: errors.New("pg: connection reset by peer")}
	sub := NewActivePathTopicsSubscriber(repo, atomIdx)

	env := newTestEnvelope("evt-apt-redeliver", "t1", "phyllis")
	p := LearningPathBootstrappedPayload{
		PathID:      "0197a000-0000-7000-8000-000000000001",
		LearnerGCID: "phyllis",
		TenantID:    "t1",
		AtomIDs:     []string{"atom-1"},
	}
	if err := sub.HandleBootstrap(context.Background(), env, p); err == nil {
		t.Fatal("first delivery: want NACK")
	}

	// Pub/Sub redelivers the SAME event_id; the projection must be retried.
	repo.err = nil
	if err := sub.HandleBootstrap(context.Background(), env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if repo.recordedPathID == "" {
		t.Error("redelivery after a failed write was dropped as a duplicate — the topics never land")
	}
}

func TestTopicAccuracySubscriber_NACKsOnWriteFailure(t *testing.T) {
	atomIdx := repoinmem.NewAtomIndexRepo()
	a1, _ := atomIndexHelperWithTopics("atom-mcq", "t1", "c1", []string{"agile"})
	_ = atomIdx.Save(context.Background(), a1)

	boom := errors.New("pg: deadlock detected")
	repo := &fakeTopicAccuracy{err: boom}
	sub := NewTopicAccuracySubscriber(repo, atomIdx)

	err := sub.Handle(context.Background(), newTestEnvelope("evt-acc-fail", "t1", "phyllis"),
		AtomSessionCompletedPayload{
			SessionID:   "sess-1",
			AtomID:      "atom-mcq",
			LearnerGCID: "phyllis",
			TenantID:    "t1",
			IsCorrect:   true,
		})
	if err == nil {
		t.Fatal("a failed accuracy write was ACKed — the attempt is lost silently")
	}
	if !errors.Is(err, boom) {
		t.Errorf("want the underlying error wrapped, got %v", err)
	}
}
