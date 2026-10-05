// retention_ports.go — persistence ports for the Maya retention loop
// (SM-2 spaced-repetition state, seen-topics, daily streak, lifetime XP).
//
// Introduced by the no-debt directive 2026-06-10 (L4 / CHO-1702): the loop
// previously lived ONLY in in-memory repos, so every pod restart wiped each
// learner's SM-2 state, streak, and XP — the Ebbinghaus review slot could
// never survive a deploy. The pg adapters (internal/adapter/repo/pg/
// retention.go) implement these ports over chora_consumption tables
// (migration 0048); the in-memory adapters remain the test/dev fallback.
//
// All methods take a context so the pg adapters can establish the
// per-request RLS tenant session (rls.ApplySession reads tenant_id from
// ctx via tracing.WithTenantID). Errors surface to callers — handlers fail
// loud (500) on storage errors instead of silently degrading.
package companion

import (
	"context"
	"time"
)

// SM2Store persists per-(tenant, learner, atom) SM-2 spaced-repetition
// state + the learner's seen-topics set.
type SM2Store interface {
	// GetState returns the SM-2 state for one atom; ok=false when the
	// learner has never reviewed it (not an error).
	GetState(ctx context.Context, tenantID, gcid, atomID string) (SM2State, bool, error)
	// SaveState upserts the SM-2 state for state.AtomID.
	SaveState(ctx context.Context, tenantID, gcid string, state SM2State) error
	// AllStatesForLearner returns every SM-2 state keyed by atom_id.
	AllStatesForLearner(ctx context.Context, tenantID, gcid string) (map[string]SM2State, error)
	// SeenTopics returns the set of topics the learner has interacted with.
	SeenTopics(ctx context.Context, tenantID, gcid string) (map[string]bool, error)
	// MarkTopicSeen records a topic interaction. Empty topics are ignored.
	MarkTopicSeen(ctx context.Context, tenantID, gcid, topic string) error
}

// StreakStore persists the learner's daily activity streak.
type StreakStore interface {
	// Get returns the learner's streak; a zero-count Streak (never nil)
	// when no activity has been recorded yet.
	Get(ctx context.Context, tenantID, gcid string) (*Streak, error)
	// RecordActivity advances the streak per Streak.RecordActivity
	// semantics (same-UTC-day idempotent; +1 on consecutive day; reset to
	// 1 after a gap).
	RecordActivity(ctx context.Context, tenantID, gcid string, now time.Time) error
}

// XPStore persists the learner's lifetime engagement XP (independent of
// any Companion's level).
type XPStore interface {
	// Get returns cumulative XP (0 if never awarded).
	Get(ctx context.Context, tenantID, gcid string) (int, error)
	// Award adds delta XP. Non-positive deltas are no-ops.
	Award(ctx context.Context, tenantID, gcid string, delta int) error
}
