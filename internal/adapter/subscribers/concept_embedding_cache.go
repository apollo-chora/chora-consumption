// concept_embedding_cache.go - ADR-238 section 5 remediation: the goal concept
// resolver embedded every in-subtree concept TITLE on every upload, uncached, so
// each Diagnose ingest fanned out N synchronous Vertex round-trips inside a
// Pub/Sub handler, N growing with the learner's map.
//
// This is a decorator over lw.Embedder, so the resolver is unchanged - caching is
// a composition-root decision (cmd/server wraps the embedder it hands the
// resolver) and the pure resolution logic stays free of caching concerns.
//
// WHY A TITLE-KEYED CACHE NEEDS NO INVALIDATION: the key is the exact (tenant,
// text) pair and an embedding is a deterministic function of that pair under a
// fixed model. Renaming a concept changes its title, hence its key, so a stale
// vector can never be served for a renamed node - there is no invalidation
// protocol to get wrong. The only input outside the key is the embedding MODEL,
// which changes by deploy, and a deploy replaces the process (and the cache).
//
// Deliberately NOT wrapped around the per-EDGE embed in the subscriber: analysed
// edge labels are effectively unique per upload, so caching them would evict
// genuinely hot concept titles to store single-use entries.
package subscribers

import (
	"context"
	"sync"

	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

// defaultConceptEmbeddingCacheSize bounds the cache. Concept titles are short
// and an embedding is ~1-3 KiB, so a few thousand entries is a small, fixed
// ceiling that still spans many learners' maps.
const defaultConceptEmbeddingCacheSize = 4096

// conceptEmbeddingCache memoises concept-title embeddings for a bounded number
// of (tenant, text) pairs. Eviction is FIFO: the workload is a repeated sweep
// over one goal's concept set, where insertion order tracks usage closely enough
// that LRU bookkeeping would not pay for itself.
type conceptEmbeddingCache struct {
	inner lw.Embedder

	mu      sync.Mutex
	entries map[string][]float32
	order   []string // insertion order, for FIFO eviction
	max     int
}

// NewConceptEmbeddingCache wraps inner with the default-sized cache. This is the
// composition-root entry point: cmd/server wraps the embedder it hands the goal
// concept resolver, leaving the resolver itself free of caching concerns.
func NewConceptEmbeddingCache(inner lw.Embedder) lw.Embedder {
	return newConceptEmbeddingCache(inner, defaultConceptEmbeddingCacheSize)
}

// newConceptEmbeddingCache wraps inner with a bounded cache. max <= 0 disables
// caching entirely (pure pass-through) rather than building a 0-slot cache.
func newConceptEmbeddingCache(inner lw.Embedder, max int) *conceptEmbeddingCache {
	return &conceptEmbeddingCache{
		inner:   inner,
		entries: make(map[string][]float32),
		order:   make([]string, 0, maxInt(max, 0)),
		max:     max,
	}
}

// Compile-time check: the decorator is substitutable for the port it wraps.
var _ lw.Embedder = (*conceptEmbeddingCache)(nil)

// Embed returns the cached vector for (tenantID, text) or delegates and stores.
// An error from the inner embedder is surfaced verbatim and NEVER cached - a
// transient Vertex failure must not be memoised into a permanent unmatchable
// concept for the life of the process.
func (c *conceptEmbeddingCache) Embed(ctx context.Context, text, tenantID string) ([]float32, error) {
	if c.max <= 0 {
		return c.inner.Embed(ctx, text, tenantID)
	}

	// NUL separates the two key components so no (tenant, text) pair can collide
	// with another by concatenation - NUL cannot occur in either.
	key := tenantID + "\x00" + text

	c.mu.Lock()
	if v, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	// Deliberately OUTSIDE the lock: a Vertex round-trip must not block every
	// other concept in the sweep. A concurrent duplicate miss costs one extra
	// call and stores an identical vector - cheaper than serialising the fan-out.
	v, err := c.inner.Embed(ctx, text, tenantID)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		if len(c.order) >= c.max {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.entries[key] = v
		c.order = append(c.order, key)
	}
	return v, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
