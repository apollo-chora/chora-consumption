// Package subscribers — chaos-readiness tests for the idempotencyTracker
// (W1.8 inbox swap, 2026-05-12).
//
// Verifies the Store-backed dedup semantics that propagate to all 4
// cross-domain subscribers (AtomCreated / EnrollmentCreated / TopicAccuracy /
// ActivePathTopics) via their shared idempotencyTracker primitive. The 4
// scenarios mirror the W1.7 closure_subscriber canonical pattern:
//
//  1. SurvivesRecreation — same Store survives subscriber recreation
//  2. FreshStoreReprocesses — negative control (per-subscriber Stores fail)
//  3. ConcurrentReplicas — multi-pod race against the same Store
//  4. TTLExpiryReprocesses — re-processes after dedup-key retention window
//
// Production wires PostgresStore so these properties hold across real
// pod-death + multi-replica deployment.
package subscribers

import (
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/idempotent"
)

func TestIdempotencyTracker_InboxSurvivesRecreation(t *testing.T) {
	t.Parallel()
	// Shared Store crosses the pod-restart boundary.
	store := idempotent.NewMemoryStore()
	t1 := newIdempotencyTrackerWithStore(store, time.Hour)

	if !t1.markSeen("evt-301") {
		t.Fatalf("first markSeen on t1: expected true")
	}

	// Simulate pod restart: drop t1, recreate t2 with the SAME Store.
	t2 := newIdempotencyTrackerWithStore(store, time.Hour)
	if t2.markSeen("evt-301") {
		t.Errorf("post-restart markSeen: expected false (already-seen via shared Store)")
	}
}

func TestIdempotencyTracker_InboxFreshStoreReprocesses(t *testing.T) {
	t.Parallel()
	// Negative control: per-tracker fresh Stores fail to dedup across
	// restart — exactly the failure mode the PostgresStore (or a shared
	// MemoryStore) is designed to fix.
	t1 := newIdempotencyTrackerWithStore(idempotent.NewMemoryStore(), time.Hour)
	t2 := newIdempotencyTrackerWithStore(idempotent.NewMemoryStore(), time.Hour)

	if !t1.markSeen("evt-302") {
		t.Fatalf("first markSeen on t1: expected true")
	}
	if !t2.markSeen("evt-302") {
		t.Errorf("FRESH-store negative control: expected true (no shared dedup)")
	}
}

func TestIdempotencyTracker_InboxConcurrentReplicas(t *testing.T) {
	t.Parallel()
	// Two trackers (two replicas) sharing one Store + concurrent calls
	// for the same eventID. Exactly one MUST observe ran=true.
	store := idempotent.NewMemoryStore()
	tA := newIdempotencyTrackerWithStore(store, time.Hour)
	tB := newIdempotencyTrackerWithStore(store, time.Hour)

	var wg sync.WaitGroup
	wg.Add(2)
	var resA, resB bool
	go func() { defer wg.Done(); resA = tA.markSeen("evt-303") }()
	go func() { defer wg.Done(); resB = tB.markSeen("evt-303") }()
	wg.Wait()

	if resA == resB {
		t.Fatalf("multi-replica race: exactly one markSeen must return true; got A=%v B=%v", resA, resB)
	}
}

func TestIdempotencyTracker_InboxTTLExpiryReprocesses(t *testing.T) {
	t.Parallel()
	store := idempotent.NewMemoryStore()
	tr := newIdempotencyTrackerWithStore(store, 50*time.Millisecond)

	if !tr.markSeen("evt-304") {
		t.Fatalf("first markSeen: expected true")
	}
	// Advance the Store's clock past the TTL.
	store.Advance(100 * time.Millisecond)
	if !tr.markSeen("evt-304") {
		t.Errorf("post-TTL markSeen: expected true (dedup-key expired, ok to re-process)")
	}
}

func TestIdempotencyTracker_EmptyEventIDShortCircuits(t *testing.T) {
	t.Parallel()
	tr := newIdempotencyTracker()
	if !tr.markSeen("") {
		t.Errorf("empty eventID markSeen: expected true (short-circuit; no dedup possible)")
	}
}

// ----- CHO-2107: seen/mark — process-then-mark primitives -----

func TestIdempotencyTracker_SeenIsNonMarking(t *testing.T) {
	t.Parallel()
	tr := newIdempotencyTracker()
	if tr.seen("evt-401") {
		t.Fatalf("seen before any mark: expected false")
	}
	// A second read must still be false — seen must NOT claim the key.
	if tr.seen("evt-401") {
		t.Errorf("seen must be non-marking; second read expected false")
	}
	if !tr.markSeen("evt-401") {
		t.Errorf("markSeen after non-marking reads: expected true (key never claimed)")
	}
}

func TestIdempotencyTracker_MarkThenSeen_SharedKeySpace_TTL(t *testing.T) {
	t.Parallel()
	store := idempotent.NewMemoryStore()
	tr := newIdempotencyTrackerWithStore(store, 50*time.Millisecond)
	tr.mark("evt-402")
	if !tr.seen("evt-402") {
		t.Fatalf("seen after mark: expected true")
	}
	// Interop: markSeen observes mark's claim (shared "envID:" key-space).
	if tr.markSeen("evt-402") {
		t.Errorf("markSeen after mark: expected false (already seen)")
	}
	// TTL expiry re-opens the key for reprocessing.
	store.Advance(100 * time.Millisecond)
	if tr.seen("evt-402") {
		t.Errorf("seen after TTL expiry: expected false")
	}
}

func TestIdempotencyTracker_SeenMarkEmptyKeyShortCircuits(t *testing.T) {
	t.Parallel()
	tr := newIdempotencyTracker()
	if tr.seen("") {
		t.Errorf("empty key seen: expected false (no dedup possible without a key)")
	}
	tr.mark("") // must not panic nor record anything
	if tr.seen("") {
		t.Errorf("empty key seen after mark: expected false")
	}
}

func TestIdempotencyTracker_SeenMarkConcurrent(t *testing.T) {
	t.Parallel()
	// Race-safety smoke (run with -race): concurrent seen/mark on a shared
	// tracker must be data-race free and converge on the marked state.
	tr := newIdempotencyTracker()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = tr.seen("evt-403") }()
		go func() { defer wg.Done(); tr.mark("evt-403") }()
	}
	wg.Wait()
	if !tr.seen("evt-403") {
		t.Errorf("after concurrent marks: expected seen=true")
	}
}
