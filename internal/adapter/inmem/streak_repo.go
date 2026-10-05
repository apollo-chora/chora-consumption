// Package inmem — S5.1 streak / XP / topic-accuracy / active-path repos.
//
// Per the S5.1 A-Companion briefing the new endpoints `/v1/me/streak`,
// `/v1/me/xp`, and the dose composer's `TopicAccuracy` /
// `ActivePathTopics` inputs require per-(tenant, gcid) state. This file
// provides thread-safe in-memory adapters; production (M12) replaces
// them with PostgreSQL repos under multi-tenant-rls policies.
//
// All repos enforce isolation by composite key — a leak between
// tenants OR between learners within a tenant is a test-detected bug.
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// The in-memory dosing projections are the DEV FALLBACK behind the same domain
// ports the pg adapters satisfy (CHO-2167). Asserting conformance here keeps
// the two adapters honestly interchangeable.
var (
	_ topic_accuracy.Repository     = (*TopicAccuracyRepo)(nil)
	_ active_path_topics.Repository = (*ActivePathTopicsRepo)(nil)
)

// ---------- StreakRepo ----------

// StreakRepo stores per-(tenant, gcid) streak state. Used by the
// `/v1/me/streak` endpoint + the SessionCompletion subscriber's
// activity recorder.
type StreakRepo struct {
	mu    sync.RWMutex
	store map[string]*companion.Streak // key: tenant|gcid
}

// NewStreakRepo constructs an empty StreakRepo.
func NewStreakRepo() *StreakRepo {
	return &StreakRepo{store: make(map[string]*companion.Streak)}
}

// Compile-time check: satisfies the domain port (companion.StreakStore).
var _ companion.StreakStore = (*StreakRepo)(nil)

// Get returns the streak for a learner; a zero-count streak when no
// activity has been recorded yet. NEVER returns nil.
func (r *StreakRepo) Get(_ context.Context, tenantID, gcid string) (*companion.Streak, error) {
	key := learnerKey(tenantID, gcid)
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.store[key]
	if !ok {
		s = companion.NewStreak(gcid)
		r.store[key] = s
	}
	return s, nil
}

// RecordActivity advances the streak for a learner. Idempotent on
// same-day calls.
func (r *StreakRepo) RecordActivity(_ context.Context, tenantID, gcid string, now time.Time) error {
	key := learnerKey(tenantID, gcid)
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.store[key]
	if !ok {
		s = companion.NewStreak(gcid)
		r.store[key] = s
	}
	s.RecordActivity(now)
	return nil
}

// ---------- XPRepo ----------

// XPRepo stores per-(tenant, gcid) cumulative XP. Distinct from the
// Companion's per-instance XP because the streak/XP surface tracks the
// learner's lifetime engagement-loop accrual independent of any
// specific Companion's level (a learner can have multiple Companions).
type XPRepo struct {
	mu    sync.RWMutex
	store map[string]int // key: tenant|gcid → XP
}

// NewXPRepo constructs an empty XPRepo.
func NewXPRepo() *XPRepo {
	return &XPRepo{store: make(map[string]int)}
}

// Compile-time check: satisfies the domain port (companion.XPStore).
var _ companion.XPStore = (*XPRepo)(nil)

// Get returns the cumulative XP for a learner (0 if never awarded).
func (r *XPRepo) Get(_ context.Context, tenantID, gcid string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store[learnerKey(tenantID, gcid)], nil
}

// Award adds delta XP to the learner's cumulative total. Negative or
// zero deltas are no-ops (defensive).
func (r *XPRepo) Award(_ context.Context, tenantID, gcid string, delta int) error {
	if delta <= 0 {
		return nil
	}
	key := learnerKey(tenantID, gcid)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store[key] += delta
	return nil
}

// ---------- TopicAccuracyRepo ----------

// topicAttempt holds the rolling counters for a single (learner, topic).
type topicAttempt struct {
	correct int
	total   int
}

// TopicAccuracyRepo stores per-(tenant, gcid, topic) rolling MCQ accuracy used
// by the dose composer's weakness slot.
//
// DEV/TEST FALLBACK ONLY (CHO-2167). Production binds the pg adapter
// (repo/pg/topic_accuracy.go) via topic_accuracy.Repository — cmd/server
// swaps this out whenever a pgx pool is available and logs which adapter is
// live. This one is volatile: everything it holds dies with the pod.
//
// It satisfies topic_accuracy.Repository, so it takes a context (ignored — no
// RLS session to establish) and returns an error (always nil — a heap map
// cannot fail). Those parameters exist so the durable adapter can be
// substituted for it, which is the whole point of the port.
type TopicAccuracyRepo struct {
	mu    sync.RWMutex
	store map[string]map[string]*topicAttempt // key: tenant|gcid → topic → counters
}

// NewTopicAccuracyRepo constructs an empty TopicAccuracyRepo.
func NewTopicAccuracyRepo() *TopicAccuracyRepo {
	return &TopicAccuracyRepo{store: make(map[string]map[string]*topicAttempt)}
}

// RecordAttempt records one MCQ attempt against a topic. Increments
// total + (if correct) the correct counter. Never errors.
func (r *TopicAccuracyRepo) RecordAttempt(_ context.Context, tenantID, gcid, topic string, correct bool) error {
	if topic == "" {
		return nil
	}
	key := learnerKey(tenantID, gcid)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.store[key]; !ok {
		r.store[key] = make(map[string]*topicAttempt)
	}
	att, ok := r.store[key][topic]
	if !ok {
		att = &topicAttempt{}
		r.store[key][topic] = att
	}
	att.total++
	if correct {
		att.correct++
	}
	return nil
}

// GetByLearner returns map[topic]accuracy with accuracy ∈ [0, 1].
// Defensive copy — caller-side mutation does not affect the repo state.
func (r *TopicAccuracyRepo) GetByLearner(_ context.Context, tenantID, gcid string) (map[string]float64, error) {
	key := learnerKey(tenantID, gcid)
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.store[key]
	out := make(map[string]float64, len(src))
	for topic, att := range src {
		if att.total == 0 {
			continue
		}
		out[topic] = float64(att.correct) / float64(att.total)
	}
	return out, nil
}

// ---------- ActivePathTopicsRepo ----------

// ActivePathTopicsRepo stores the per-(tenant, gcid) topic-set covered by the
// learner's active LearningPath(s). Drives the daily dose's CURIOSITY slot.
//
// DEV/TEST FALLBACK ONLY (CHO-2167). Production binds the pg adapter
// (repo/pg/active_path_topics.go) via active_path_topics.Repository —
// cmd/server swaps this out whenever a pgx pool is available and logs which
// adapter is live. This one is volatile: everything it holds dies with the pod.
//
// It satisfies active_path_topics.Repository, so it takes a context (ignored —
// no RLS session to establish) and returns an error (always nil). It also takes
// the learningPathID, which the durable adapter needs as part of its primary
// key; the in-memory store unions topics per learner, so it keys the paths to
// keep the same read semantics (GetTopics returns the deduplicated union).
type ActivePathTopicsRepo struct {
	mu    sync.RWMutex
	store map[string]map[string]bool // key: tenant|gcid → topic-set (union across paths)
	paths map[string]map[string]bool // key: tenant|gcid → learning_path_id set
}

// NewActivePathTopicsRepo constructs an empty ActivePathTopicsRepo.
func NewActivePathTopicsRepo() *ActivePathTopicsRepo {
	return &ActivePathTopicsRepo{
		store: make(map[string]map[string]bool),
		paths: make(map[string]map[string]bool),
	}
}

// RecordPathTopics unions the topics for a learner. Multiple paths active
// concurrently → topics accumulate. Never errors.
func (r *ActivePathTopicsRepo) RecordPathTopics(_ context.Context, tenantID, gcid, learningPathID string, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	key := learnerKey(tenantID, gcid)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.store[key]; !ok {
		r.store[key] = make(map[string]bool)
	}
	if _, ok := r.paths[key]; !ok {
		r.paths[key] = make(map[string]bool)
	}
	if learningPathID != "" {
		r.paths[key][learningPathID] = true
	}
	for _, t := range topics {
		if t == "" {
			continue
		}
		r.store[key][t] = true
	}
	return nil
}

// GetTopics returns a defensive copy of the topic-set for a learner.
func (r *ActivePathTopicsRepo) GetTopics(_ context.Context, tenantID, gcid string) (map[string]bool, error) {
	key := learnerKey(tenantID, gcid)
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.store[key]
	out := make(map[string]bool, len(src))
	for t, v := range src {
		out[t] = v
	}
	return out, nil
}
