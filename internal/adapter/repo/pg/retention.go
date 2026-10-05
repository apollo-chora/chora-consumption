// retention.go — Postgres adapters for the Maya retention loop ports
// (companion.SM2Store / StreakStore / XPStore) over migration 0048's
// sm2_states / learner_seen_topics / learner_streaks / learner_xp tables.
//
// Introduced by the no-debt directive 2026-06-10 (L4 / CHO-1702): the loop
// previously lived ONLY in in-memory repos, so every pod restart wiped each
// learner's SM-2 state, streak and XP. Every method establishes the RLS
// tenant session first (rls.ApplySession — tenant_id from ctx via
// tracing.WithTenantID); errors propagate so handlers fail loud.
package pg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ---------- SM2StateRepo ----------

const (
	upsertSM2StateSQL = `
		INSERT INTO sm2_states (tenant_id, learner_gcid, atom_id, repetitions,
		    interval_days, easiness_factor, last_reviewed_at, next_review_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id, learner_gcid, atom_id) DO UPDATE SET
		    repetitions      = EXCLUDED.repetitions,
		    interval_days    = EXCLUDED.interval_days,
		    easiness_factor  = EXCLUDED.easiness_factor,
		    last_reviewed_at = EXCLUDED.last_reviewed_at,
		    next_review_at   = EXCLUDED.next_review_at,
		    updated_at       = now();`

	loadSM2StateSQL = `
		SELECT atom_id, repetitions, interval_days, easiness_factor,
		       last_reviewed_at, next_review_at
		FROM sm2_states
		WHERE tenant_id = $1 AND learner_gcid = $2 AND atom_id = $3;`

	listSM2StatesSQL = `
		SELECT atom_id, repetitions, interval_days, easiness_factor,
		       last_reviewed_at, next_review_at
		FROM sm2_states
		WHERE tenant_id = $1 AND learner_gcid = $2;`

	markTopicSeenSQL = `
		INSERT INTO learner_seen_topics (tenant_id, learner_gcid, topic)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, learner_gcid, topic) DO NOTHING;`

	listSeenTopicsSQL = `
		SELECT topic FROM learner_seen_topics
		WHERE tenant_id = $1 AND learner_gcid = $2;`
)

// SM2StateRepo is the Postgres-backed companion.SM2Store.
type SM2StateRepo struct {
	tx TxRunner
}

// NewSM2StateRepo constructs the repo.
func NewSM2StateRepo(tx TxRunner) *SM2StateRepo {
	return &SM2StateRepo{tx: tx}
}

// Compile-time check.
var _ companion.SM2Store = (*SM2StateRepo)(nil)

// nullableTime maps a zero time to SQL NULL.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// derefTime maps a SQL NULL back to the zero time.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// SaveState upserts the SM-2 state for state.AtomID.
func (r *SM2StateRepo) SaveState(ctx context.Context, tenantID, gcid string, state companion.SM2State) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertSM2StateSQL,
			tenantID, gcid, state.AtomID, state.Repetitions, state.IntervalDays,
			state.EasinessFactor, nullableTime(state.LastReviewedAt), nullableTime(state.NextReviewAt),
		)
		return err
	})
}

// GetState returns the SM-2 state for one atom; ok=false when never reviewed.
func (r *SM2StateRepo) GetState(ctx context.Context, tenantID, gcid, atomID string) (companion.SM2State, bool, error) {
	var (
		out   companion.SM2State
		found bool
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadSM2StateSQL, tenantID, gcid, atomID)
		if row == nil {
			return nil // stub Querier — not found
		}
		var lastReviewed, nextReview *time.Time
		if err := row.Scan(&out.AtomID, &out.Repetitions, &out.IntervalDays,
			&out.EasinessFactor, &lastReviewed, &nextReview); err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		out.LastReviewedAt = derefTime(lastReviewed)
		out.NextReviewAt = derefTime(nextReview)
		found = true
		return nil
	})
	return out, found, err
}

// AllStatesForLearner returns every SM-2 state keyed by atom_id.
func (r *SM2StateRepo) AllStatesForLearner(ctx context.Context, tenantID, gcid string) (map[string]companion.SM2State, error) {
	out := map[string]companion.SM2State{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listSM2StatesSQL, tenantID, gcid)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier — empty
		}
		defer rows.Close()
		for rows.Next() {
			var s companion.SM2State
			var lastReviewed, nextReview *time.Time
			if err := rows.Scan(&s.AtomID, &s.Repetitions, &s.IntervalDays,
				&s.EasinessFactor, &lastReviewed, &nextReview); err != nil {
				return err
			}
			s.LastReviewedAt = derefTime(lastReviewed)
			s.NextReviewAt = derefTime(nextReview)
			out[s.AtomID] = s
		}
		return rows.Err()
	})
	return out, err
}

// MarkTopicSeen records a topic interaction. Empty topics are ignored (real
// atoms may carry no topic tag).
func (r *SM2StateRepo) MarkTopicSeen(ctx context.Context, tenantID, gcid, topic string) error {
	if strings.TrimSpace(topic) == "" {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, markTopicSeenSQL, tenantID, gcid, topic)
		return err
	})
}

// SeenTopics returns the learner's seen-topic set.
func (r *SM2StateRepo) SeenTopics(ctx context.Context, tenantID, gcid string) (map[string]bool, error) {
	out := map[string]bool{}
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listSeenTopicsSQL, tenantID, gcid)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var topic string
			if err := rows.Scan(&topic); err != nil {
				return err
			}
			out[topic] = true
		}
		return rows.Err()
	})
	return out, err
}

// ---------- LearnerStreakRepo ----------

const (
	loadStreakForUpdateSQL = `
		SELECT streak_count, last_activity_at FROM learner_streaks
		WHERE tenant_id = $1 AND learner_gcid = $2
		FOR UPDATE;`

	loadStreakSQL = `
		SELECT streak_count, last_activity_at FROM learner_streaks
		WHERE tenant_id = $1 AND learner_gcid = $2;`

	upsertStreakSQL = `
		INSERT INTO learner_streaks (tenant_id, learner_gcid, streak_count, last_activity_at, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (tenant_id, learner_gcid) DO UPDATE SET
		    streak_count     = EXCLUDED.streak_count,
		    last_activity_at = EXCLUDED.last_activity_at,
		    updated_at       = now();`
)

// LearnerStreakRepo is the Postgres-backed companion.StreakStore.
type LearnerStreakRepo struct {
	tx TxRunner
}

// NewLearnerStreakRepo constructs the repo.
func NewLearnerStreakRepo(tx TxRunner) *LearnerStreakRepo {
	return &LearnerStreakRepo{tx: tx}
}

// Compile-time check.
var _ companion.StreakStore = (*LearnerStreakRepo)(nil)

// scanStreak loads the streak row inside an open tx; returns a fresh
// zero-count streak when no row exists.
func scanStreak(ctx context.Context, q Querier, sql, tenantID, gcid string) (*companion.Streak, error) {
	s := companion.NewStreak(gcid)
	row := q.QueryRow(ctx, sql, tenantID, gcid)
	if row == nil {
		return s, nil // stub Querier — fresh streak
	}
	var lastActivity *time.Time
	if err := row.Scan(&s.Count, &lastActivity); err != nil {
		if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
			return s, nil
		}
		return nil, err
	}
	s.LastActivityAt = derefTime(lastActivity)
	return s, nil
}

// Get returns the learner's streak; zero-count when never active.
func (r *LearnerStreakRepo) Get(ctx context.Context, tenantID, gcid string) (*companion.Streak, error) {
	var out *companion.Streak
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		s, err := scanStreak(ctx, q, loadStreakSQL, tenantID, gcid)
		if err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}

// RecordActivity advances the streak per the companion.Streak entity rules
// (same-UTC-day idempotent; +1 consecutive; reset after a gap) under a
// row lock so concurrent grades don't double-count a day.
func (r *LearnerStreakRepo) RecordActivity(ctx context.Context, tenantID, gcid string, now time.Time) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		s, err := scanStreak(ctx, q, loadStreakForUpdateSQL, tenantID, gcid)
		if err != nil {
			return err
		}
		s.RecordActivity(now)
		_, err = q.Exec(ctx, upsertStreakSQL,
			tenantID, gcid, s.Count, nullableTime(s.LastActivityAt))
		return err
	})
}

// ---------- LearnerXPRepo ----------

const (
	loadXPSQL = `
		SELECT xp FROM learner_xp
		WHERE tenant_id = $1 AND learner_gcid = $2;`

	awardXPSQL = `
		INSERT INTO learner_xp (tenant_id, learner_gcid, xp, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (tenant_id, learner_gcid) DO UPDATE SET
		    xp         = learner_xp.xp + EXCLUDED.xp,
		    updated_at = now();`
)

// LearnerXPRepo is the Postgres-backed companion.XPStore.
type LearnerXPRepo struct {
	tx TxRunner
}

// NewLearnerXPRepo constructs the repo.
func NewLearnerXPRepo(tx TxRunner) *LearnerXPRepo {
	return &LearnerXPRepo{tx: tx}
}

// Compile-time check.
var _ companion.XPStore = (*LearnerXPRepo)(nil)

// Get returns cumulative XP (0 if never awarded).
func (r *LearnerXPRepo) Get(ctx context.Context, tenantID, gcid string) (int, error) {
	var xp int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadXPSQL, tenantID, gcid)
		if row == nil {
			return nil // stub Querier — 0
		}
		if err := row.Scan(&xp); err != nil {
			if errors.Is(err, ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		return nil
	})
	return xp, err
}

// Award adds delta XP atomically. Non-positive deltas are no-ops.
func (r *LearnerXPRepo) Award(ctx context.Context, tenantID, gcid string, delta int) error {
	if delta <= 0 {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, awardXPSQL, tenantID, gcid, delta)
		return err
	})
}
