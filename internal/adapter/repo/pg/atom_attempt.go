// atom_attempt.go - Postgres adapter for the AtomAttempt state-
// machine aggregate. Owns the chora_consumption.atomic_sessions table.
//
// Per ddd-enforcement #5: atomic_sessions soft-deleted via deleted_at;
// hard delete only via the federated closure saga at crypto-shred.
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_attempt"
)

// ErrInvalidAtomSession is the sentinel for nil-input writes.
var ErrInvalidAtomSession = errors.New("pg: atom_session is nil")

// AtomSessionRepo is the Postgres-backed AtomAttempt repo.
type AtomSessionRepo struct {
	tx TxRunner
}

// NewAtomSessionRepo constructs the repo.
func NewAtomSessionRepo(tx TxRunner) *AtomSessionRepo {
	return &AtomSessionRepo{tx: tx}
}

// Save upserts an AtomAttempt by session_id. SET LOCAL pair runs
// before the upsert so RLS gates the write.
func (r *AtomSessionRepo) Save(ctx context.Context, s *atom_attempt.AtomAttempt) error {
	if s == nil {
		return ErrInvalidAtomSession
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		var completedAt, abandonedAt any
		if s.CompletedAt != nil {
			completedAt = *s.CompletedAt
		}
		if s.AbandonedAt != nil {
			abandonedAt = *s.AbandonedAt
		}
		_, err := q.Exec(ctx, upsertAtomSessionSQL,
			s.SessionID, s.TenantID, s.LearnerGCID, s.AtomID,
			string(s.Status), s.HintsUsed, s.AnswerCount, s.LastAnswer,
			s.LastAnswerID, s.LastResult.Correct, string(s.LastResult.Status),
			s.StartedAt, completedAt, abandonedAt,
		)
		return err
	})
}

// Get returns the session by id, or nil + ErrNoRows on not-found.
//
// SQL: SELECT ... FROM atomic_sessions WHERE session_id = $1
//
//	AND deleted_at IS NULL.
func (r *AtomSessionRepo) Get(ctx context.Context, sessionID string) (*atom_attempt.AtomAttempt, error) {
	var out *atom_attempt.AtomAttempt
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadAtomSessionSQL, sessionID)
		if row == nil {
			// Stub Querier — surface as not-found so handlers 404
			// instead of nil-dereferencing (CHO-2029).
			return atom_attempt.ErrNotFound
		}
		var (
			s                atom_attempt.AtomAttempt
			status           string
			lastResultStatus string
			completedAt      *time.Time
			abandonedAt      *time.Time
		)
		if err := row.Scan(
			&s.SessionID, &s.TenantID, &s.LearnerGCID, &s.AtomID,
			&status, &s.HintsUsed, &s.AnswerCount, &s.LastAnswer,
			&s.LastAnswerID, &s.LastResult.Correct, &lastResultStatus,
			&s.StartedAt, &completedAt, &abandonedAt,
		); err != nil {
			if errors.Is(err, ErrNoRows) {
				return atom_attempt.ErrNotFound
			}
			return err
		}
		s.Status = atom_attempt.Status(status)
		s.LastResult.Status = atom_attempt.Status(lastResultStatus)
		s.CompletedAt = completedAt
		s.AbandonedAt = abandonedAt
		out = &s
		return nil
	})
	return out, err
}

var _ atom_attempt.Repository = (*AtomSessionRepo)(nil)

const (
	upsertAtomSessionSQL = `
		INSERT INTO atomic_sessions (
			session_id, tenant_id, learner_gcid, atom_id, status,
			hints_used, answer_count, last_answer, last_answer_id,
			last_correct, last_result_status, started_at,
			completed_at, abandoned_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (session_id) DO UPDATE SET
			status             = EXCLUDED.status,
			hints_used         = EXCLUDED.hints_used,
			answer_count       = EXCLUDED.answer_count,
			last_answer        = EXCLUDED.last_answer,
			last_answer_id     = EXCLUDED.last_answer_id,
			last_correct       = EXCLUDED.last_correct,
			last_result_status = EXCLUDED.last_result_status,
			completed_at       = EXCLUDED.completed_at,
			abandoned_at       = EXCLUDED.abandoned_at`

	loadAtomSessionSQL = `
		SELECT session_id, tenant_id, learner_gcid, atom_id, status::text,
			hints_used, answer_count, COALESCE(last_answer, ''),
			COALESCE(last_answer_id, ''), COALESCE(last_correct, false),
			COALESCE(last_result_status, ''),
			started_at, completed_at, abandoned_at
		FROM atomic_sessions
		WHERE session_id = $1 AND deleted_at IS NULL`
)
