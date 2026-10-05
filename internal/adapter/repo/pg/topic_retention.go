// topic_retention.go — Postgres adapter for the per-(tenant, gcid,
// topic_id) Ebbinghaus retention aggregate. Owns the
// chora_consumption.topic_retention table (introduced in 0004).
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/topic_retention"
)

// ErrInvalidTopicScore is the sentinel for nil-input writes.
var ErrInvalidTopicScore = errors.New("pg: topic_retention.TopicScore is nil")

// TopicRetentionRepo is the Postgres-backed retention repo.
type TopicRetentionRepo struct {
	tx TxRunner
}

// NewTopicRetentionRepo constructs the repo.
func NewTopicRetentionRepo(tx TxRunner) *TopicRetentionRepo {
	return &TopicRetentionRepo{tx: tx}
}

// Compile-time check: the pg adapter satisfies the domain port.
var _ topic_retention.Repository = (*TopicRetentionRepo)(nil)

// Save upserts a TopicScore by (tenant, gcid, topic_id).
func (r *TopicRetentionRepo) Save(ctx context.Context, s *topic_retention.TopicScore) error {
	if s == nil {
		return ErrInvalidTopicScore
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, upsertTopicRetentionSQL,
			s.TenantID, s.GCID, s.TopicID, s.Strength,
			s.RetentionScore, s.ReviewCount, s.LastReviewedAt,
		)
		return err
	})
}

// Get returns the live score for (tenant, gcid, topic_id), or (nil, nil) when
// none matches (ErrNoRows / soft-deleted) — the port's not-found contract.
func (r *TopicRetentionRepo) Get(ctx context.Context, tenantID, gcid, topicID string) (*topic_retention.TopicScore, error) {
	var out *topic_retention.TopicScore
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		row := q.QueryRow(ctx, loadTopicRetentionSQL, tenantID, gcid, topicID)
		if row == nil {
			return nil
		}
		var (
			s              topic_retention.TopicScore
			lastReviewedAt time.Time
		)
		if err := row.Scan(
			&s.ScoreID, &s.TenantID, &s.GCID, &s.TopicID,
			&s.Strength, &s.RetentionScore, &s.ReviewCount,
			&lastReviewedAt, &s.UpdatedAt,
		); err != nil {
			if errors.Is(err, ErrNoRows) {
				return nil
			}
			return err
		}
		s.LastReviewedAt = lastReviewedAt
		out = &s
		return nil
	})
	return out, err
}

// ListByLearner returns up to `limit` scores for one learner.
func (r *TopicRetentionRepo) ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*topic_retention.TopicScore, error) {
	out := make([]*topic_retention.TopicScore, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, listTopicRetentionByLearnerSQL, tenantID, gcid, sqlLimit(limit))
		if err != nil {
			return err
		}
		if rows == nil {
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var (
				s              topic_retention.TopicScore
				lastReviewedAt time.Time
			)
			if err := rows.Scan(
				&s.ScoreID, &s.TenantID, &s.GCID, &s.TopicID,
				&s.Strength, &s.RetentionScore, &s.ReviewCount,
				&lastReviewedAt, &s.UpdatedAt,
			); err != nil {
				return err
			}
			s.LastReviewedAt = lastReviewedAt
			out = append(out, &s)
		}
		return rows.Err()
	})
	return out, err
}

const (
	upsertTopicRetentionSQL = `
		INSERT INTO topic_retention (
			tenant_id, gcid, topic_id, strength, retention_score,
			review_count, last_reviewed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, gcid, topic_id) DO UPDATE SET
			strength         = EXCLUDED.strength,
			retention_score  = EXCLUDED.retention_score,
			review_count     = EXCLUDED.review_count,
			last_reviewed_at = EXCLUDED.last_reviewed_at`

	loadTopicRetentionSQL = `
		SELECT score_id, tenant_id, gcid, topic_id, strength,
			retention_score, review_count, last_reviewed_at, updated_at
		FROM topic_retention
		WHERE tenant_id = $1 AND gcid = $2 AND topic_id = $3
			AND deleted_at IS NULL`

	listTopicRetentionByLearnerSQL = `
		SELECT score_id, tenant_id, gcid, topic_id, strength,
			retention_score, review_count, last_reviewed_at, updated_at
		FROM topic_retention
		WHERE tenant_id = $1 AND gcid = $2 AND deleted_at IS NULL
		ORDER BY last_reviewed_at DESC
		LIMIT $3`
)
