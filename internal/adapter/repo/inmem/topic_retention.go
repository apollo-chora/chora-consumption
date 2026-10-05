// topic_retention.go — in-memory store for the per-(tenant, gcid, topic)
// retention aggregate. Satisfies topic_retention.Repository for tests + dev
// without a DB; cmd/server swaps in the pg-backed adapter at boot
// (WireTopicRetentionPg) so production state survives pod restarts.
//
// The ctx argument is ignored here (no RLS / transaction) — it exists to match
// the port the RLS-bound pg adapter needs.
package inmem

import (
	"context"
	"sync"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// TopicRetentionRepo is an in-memory store keyed by tenant|gcid|topic.
type TopicRetentionRepo struct {
	mu    sync.RWMutex
	store map[string]*topic_retention.TopicScore
}

// NewTopicRetentionRepo constructs an empty repo.
func NewTopicRetentionRepo() *TopicRetentionRepo {
	return &TopicRetentionRepo{store: make(map[string]*topic_retention.TopicScore)}
}

// Compile-time check: the in-memory adapter satisfies the domain port.
var _ topic_retention.Repository = (*TopicRetentionRepo)(nil)

func trKey(tenantID, gcid, topicID string) string {
	return tenantID + "|" + gcid + "|" + topicID
}

// Save inserts or updates the score by (tenant, gcid, topic).
func (r *TopicRetentionRepo) Save(_ context.Context, s *topic_retention.TopicScore) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[trKey(s.TenantID, s.GCID, s.TopicID)] = s
	return nil
}

// Get returns the score, or (nil, nil) when absent or soft-deleted (matching
// the port's not-found contract — the caller seeds a fresh score on nil).
func (r *TopicRetentionRepo) Get(_ context.Context, tenantID, gcid, topicID string) (*topic_retention.TopicScore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.store[trKey(tenantID, gcid, topicID)]
	if !ok || s.DeletedAt != nil {
		return nil, nil
	}
	return s, nil
}

// ListByLearner returns up to `limit` non-deleted scores for one learner.
// limit <= 0 means unbounded.
func (r *TopicRetentionRepo) ListByLearner(_ context.Context, tenantID, gcid string, limit int) ([]*topic_retention.TopicScore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*topic_retention.TopicScore, 0)
	for _, s := range r.store {
		if s.DeletedAt != nil {
			continue
		}
		if s.TenantID != tenantID || s.GCID != gcid {
			continue
		}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
