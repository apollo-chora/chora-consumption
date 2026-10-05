// topic_accuracy.go — Postgres adapter for the topic_accuracy
// projection (S6.4) + the topic_accuracy_session_dedup auxiliary table
// that enforces (gcid, atom_session_id) idempotency at the DB layer.
//
// Read path: dose composer's WEAKNESS slot reads
// `GetByLearner(tenant, gcid)` and picks atoms where the topic accuracy
// is below a threshold (default 0.7).
//
// Write path: TopicAccuracySubscriber consumes
// `chora.consumption.atom_session.completed.v1` and calls:
//
//  1. MarkSessionSeen(tenant, gcid, session_id) — DB-level dedup; if
//     the row already exists (PK conflict), the subscriber drops the
//     attempt as a duplicate.
//  2. RecordAttempt(tenant, gcid, topic_tag, correct) — upserts
//     counters; the SQL atomically increments attempts (+1) and
//     correct (+1 when correct).
//
// Per multi-tenant-rls SKILL: `rls.ApplySession` runs BEFORE every
// query so SET LOCAL chora.tenant_id + chora.user_gcid land in the
// same transaction scope as the subsequent SQL.
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/apollo-chora/chora-common/rls"

	"github.com/apollo-chora/chora-consumption/internal/domain/topic_accuracy"
)

// TopicAccuracyRepo is the Postgres-backed accuracy projection — the DURABLE
// adapter cmd/server binds whenever a pgx pool is available (CHO-2167).
type TopicAccuracyRepo struct {
	tx TxRunner
}

// Compile-time proof this adapter is substitutable for the in-memory one
// through the domain port. DerivedWeaknessProjector already held this adapter
// (and has been writing real rows); the dose composer was reading a different,
// volatile store. The port is what lets both read the same one.
var _ topic_accuracy.Repository = (*TopicAccuracyRepo)(nil)

// NewTopicAccuracyRepo constructs a Postgres repo.
func NewTopicAccuracyRepo(tx TxRunner) *TopicAccuracyRepo {
	return &TopicAccuracyRepo{tx: tx}
}

// RecordAttempt upserts an MCQ attempt for one topic. atomic increment
// of attempts (+1) AND correct (+1 if isCorrect).
//
// SQL:
//
//	INSERT INTO topic_accuracy (tenant_id, gcid, topic_tag, attempts,
//	    correct, last_attempted_at)
//	VALUES ($1, $2, $3, 1, CASE WHEN $4 THEN 1 ELSE 0 END, now())
//	ON CONFLICT (tenant_id, gcid, topic_tag) DO UPDATE SET
//	    attempts = topic_accuracy.attempts + 1,
//	    correct  = topic_accuracy.correct + (CASE WHEN $4 THEN 1 ELSE 0 END),
//	    last_attempted_at = now();
func (r *TopicAccuracyRepo) RecordAttempt(ctx context.Context, tenantID, gcid, topicTag string, isCorrect bool) error {
	if topicTag == "" {
		return fmt.Errorf("pg: topic_tag required")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, recordTopicAccuracyAttemptSQL, tenantID, gcid, topicTag, isCorrect)
		return err
	})
}

// GetByLearner returns map[topic_tag]accuracy with accuracy ∈ [0, 1].
// Empty map is returned when no rows exist.
//
// SQL:
//
//	SELECT topic_tag, attempts, correct
//	FROM topic_accuracy
//	WHERE tenant_id = $1 AND gcid = $2 AND deleted_at IS NULL
//	    AND attempts > 0;
func (r *TopicAccuracyRepo) GetByLearner(ctx context.Context, tenantID, gcid string) (map[string]float64, error) {
	out := make(map[string]float64)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, getTopicAccuracyByLearnerSQL, tenantID, gcid)
		if err != nil {
			return err
		}
		if rows == nil {
			return nil // stub Querier returns nil — treat as empty result
		}
		defer rows.Close()
		for rows.Next() {
			var (
				topic    string
				attempts int
				correct  int
			)
			if err := rows.Scan(&topic, &attempts, &correct); err != nil {
				return err
			}
			if attempts <= 0 {
				continue
			}
			out[topic] = float64(correct) / float64(attempts)
		}
		return rows.Err()
	})
	return out, err
}

// MarkSessionSeen DB-level dedup for the (tenant, gcid, session_id)
// tuple. Returns (true, nil) when the row was newly inserted (= first
// time we see this session); (false, nil) when the row already exists
// (= duplicate event we should skip).
//
// SQL:
//
//	INSERT INTO topic_accuracy_session_dedup (tenant_id, gcid, atom_session_id)
//	VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;
//
// rows-affected interpretation: `RowsAffected = 1` ⇒ first time;
// `RowsAffected = 0` ⇒ duplicate.
func (r *TopicAccuracyRepo) MarkSessionSeen(ctx context.Context, tenantID, gcid, sessionID string) (bool, error) {
	var firstSeen bool
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, markSessionSeenSQL, tenantID, gcid, sessionID)
		if err != nil {
			return err
		}
		firstSeen = tag.RowsAffected > 0
		return nil
	})
	return firstSeen, err
}

const (
	recordTopicAccuracyAttemptSQL = `
		INSERT INTO topic_accuracy (tenant_id, gcid, topic_tag, attempts, correct, last_attempted_at)
		VALUES ($1, $2, $3, 1, CASE WHEN $4 THEN 1 ELSE 0 END, now())
		ON CONFLICT (tenant_id, gcid, topic_tag) DO UPDATE SET
			attempts          = topic_accuracy.attempts + 1,
			correct           = topic_accuracy.correct + (CASE WHEN $4 THEN 1 ELSE 0 END),
			last_attempted_at = now()`

	getTopicAccuracyByLearnerSQL = `
		SELECT topic_tag, attempts, correct
		FROM topic_accuracy
		WHERE tenant_id = $1 AND gcid = $2 AND deleted_at IS NULL
			AND attempts > 0`

	markSessionSeenSQL = `
		INSERT INTO topic_accuracy_session_dedup (tenant_id, gcid, atom_session_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, gcid, atom_session_id) DO NOTHING`
)

// _ keeps the errors import live in case future repo methods need it.
var _ = errors.New
