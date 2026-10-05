// concept_embedding_cache_test.go - ADR-238 section 5 ("the resolver issues one
// synchronous Vertex round-trip per in-subtree titled concept, per upload,
// uncached, inside a Pub/Sub handler"). Covers the decorator that removes that
// fan-out: hit/miss accounting, tenant scoping, error non-caching, eviction
// bound, and concurrency safety.
package subscribers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// countingEmbedder records every call so a test can assert round-trips, not just
// returned values - the whole point of the cache is the call it does NOT make.
type countingEmbedder struct {
	mu    sync.Mutex
	calls []string // "tenant|text" per invocation, in order
	err   error    // when set, every call fails with it
}

func (c *countingEmbedder) Embed(_ context.Context, text, tenantID string) ([]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, tenantID+"|"+text)
	if c.err != nil {
		return nil, c.err
	}
	// Deterministic, input-derived vector so a wrong cache hit is detectable.
	return []float32{float32(len(text)), float32(len(tenantID))}, nil
}

func (c *countingEmbedder) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func TestConceptEmbeddingCache_SecondCallIsAHit(t *testing.T) {
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 128)

	first, err := cache.Embed(context.Background(), "Scrum Roles", "t1")
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	second, err := cache.Embed(context.Background(), "Scrum Roles", "t1")
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}
	if inner.count() != 1 {
		t.Fatalf("inner embed calls = %d; want 1 - the second lookup must be served from cache", inner.count())
	}
	if len(first) != len(second) || first[0] != second[0] {
		t.Fatalf("cached vector = %v; want the same as the first %v", second, first)
	}
}

func TestConceptEmbeddingCache_IsTenantScoped(t *testing.T) {
	// Two tenants may legitimately own a concept with an identical title. They
	// must never share a cache entry - the embedder takes tenantID for a reason.
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 128)

	a, _ := cache.Embed(context.Background(), "Photosynthesis", "tenant-a")
	b, _ := cache.Embed(context.Background(), "Photosynthesis", "tenant-bb")
	if inner.count() != 2 {
		t.Fatalf("inner embed calls = %d; want 2 - a different tenant must miss", inner.count())
	}
	if a[1] == b[1] {
		t.Fatalf("tenant-a and tenant-bb returned the same tenant-derived component (%v/%v); cache key is not tenant-scoped", a, b)
	}
}

func TestConceptEmbeddingCache_DistinctTitlesMiss(t *testing.T) {
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 128)
	cache.Embed(context.Background(), "Sprint Backlog", "t1")
	cache.Embed(context.Background(), "Product Backlog", "t1")
	if inner.count() != 2 {
		t.Fatalf("inner embed calls = %d; want 2 - distinct titles are distinct keys", inner.count())
	}
}

func TestConceptEmbeddingCache_ErrorsAreNotCached(t *testing.T) {
	// A transient Vertex failure must not be memoised as a permanent "no vector"
	// - that would silently unmatch every edge for the life of the process.
	inner := &countingEmbedder{err: errors.New("vertex unavailable")}
	cache := newConceptEmbeddingCache(inner, 128)

	if _, err := cache.Embed(context.Background(), "Scrum Roles", "t1"); err == nil {
		t.Fatal("want the inner error surfaced, got nil - failures must be fail-loud")
	}
	inner.mu.Lock()
	inner.err = nil
	inner.mu.Unlock()

	got, err := cache.Embed(context.Background(), "Scrum Roles", "t1")
	if err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("retry returned an empty vector; the failure was cached")
	}
	if inner.count() != 2 {
		t.Fatalf("inner embed calls = %d; want 2 - an error must not occupy a cache slot", inner.count())
	}
}

func TestConceptEmbeddingCache_EvictsAtCapacity(t *testing.T) {
	// The cache is a bounded FIFO: an unbounded map keyed by free-text titles is
	// a slow leak in a long-lived subscriber process.
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 2)

	cache.Embed(context.Background(), "one", "t1")
	cache.Embed(context.Background(), "two", "t1")
	cache.Embed(context.Background(), "three", "t1") // evicts "one"
	if inner.count() != 3 {
		t.Fatalf("warm-up calls = %d; want 3", inner.count())
	}
	if _, err := cache.Embed(context.Background(), "two", "t1"); err != nil {
		t.Fatalf("Embed two: %v", err)
	}
	if inner.count() != 3 {
		t.Fatalf("inner calls = %d; want 3 - 'two' must still be resident", inner.count())
	}
	if _, err := cache.Embed(context.Background(), "one", "t1"); err != nil {
		t.Fatalf("Embed one: %v", err)
	}
	if inner.count() != 4 {
		t.Fatalf("inner calls = %d; want 4 - the oldest entry must have been evicted", inner.count())
	}
}

func TestConceptEmbeddingCache_ZeroCapacityIsPassThrough(t *testing.T) {
	// A non-positive bound disables caching rather than creating a 0-slot cache
	// that thrashes or panics.
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 0)
	cache.Embed(context.Background(), "same", "t1")
	cache.Embed(context.Background(), "same", "t1")
	if inner.count() != 2 {
		t.Fatalf("inner calls = %d; want 2 - capacity<=0 must pass through", inner.count())
	}
}

func TestConceptEmbeddingCache_ConcurrentAccessIsSafe(t *testing.T) {
	// The resolver runs inside a Pub/Sub handler; concurrent deliveries share one
	// cache instance. Run with -race.
	inner := &countingEmbedder{}
	cache := newConceptEmbeddingCache(inner, 64)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			title := fmt.Sprintf("concept-%d", i%4)
			if _, err := cache.Embed(context.Background(), title, "t1"); err != nil {
				t.Errorf("concurrent Embed: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if inner.count() > 32 {
		t.Fatalf("inner calls = %d; want at most 32 (one per goroutine)", inner.count())
	}
}
