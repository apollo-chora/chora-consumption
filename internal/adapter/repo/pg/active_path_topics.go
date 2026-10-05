// active_path_topics.go — Postgres adapter for the active_path_topics
// projection (S6.4). Read path: dose composer's CURIOSITY slot reads
// `GetTopics(tenant, gcid)`. Write path: ActivePathTopicsSubscriber
// consumes learning_path.bootstrapped.v1 + advanced.v1.
//
// Uniqueness: PRIMARY KEY (tenant_id, gcid, learning_path_id, topic_tag).
// One row per (learner, path, topic). When the same topic appears on
// multiple paths the row is duplicated across path_ids — the dose
// composer's `GetTopics` deduplicates back to a topic-set.
package pg

import (
	"context"

	"github.com/apollo-chora/chora-common/rls"

	"github.com/apollo-chora/chora-consumption/internal/domain/active_path_topics"
)

// ActivePathTopicsRepo is the Postgres-backed projection — the DURABLE adapter
// cmd/server binds whenever a pgx pool is available (CHO-2167).
type ActivePathTopicsRepo struct {
	tx TxRunner
}

// Compile-time proof this adapter is substitutable for the in-memory one
// through the domain port. Without it, the composition root drifts back to a
// concrete type and the durable adapter silently stops being bindable — which
// is exactly how this projection spent its life unwired.
var _ active_path_topics.Repository = (*ActivePathTopicsRepo)(nil)

// NewActivePathTopicsRepo constructs the repo.
func NewActivePathTopicsRepo(tx TxRunner) *ActivePathTopicsRepo {
	return &ActivePathTopicsRepo{tx: tx}
}

// RecordPathTopics upserts one row per (gcid, path, topic). Empty
// topics is a no-op.
//
// SQL per topic (one INSERT per loop iteration; bulk INSERT could
// replace this in M14 perf pass):
//
//	INSERT INTO active_path_topics (tenant_id, gcid, learning_path_id, topic_tag)
//	VALUES ($1, $2, $3, $4)
//	ON CONFLICT (tenant_id, gcid, learning_path_id, topic_tag) DO NOTHING;
func (r *ActivePathTopicsRepo) RecordPathTopics(ctx context.Context, tenantID, gcid, learningPathID string, topics []string) error {
	if len(topics) == 0 {
		return nil
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		for _, topic := range topics {
			if topic == "" {
				continue
			}
			if _, err := q.Exec(ctx, upsertActivePathTopicsSQL, tenantID, gcid, learningPathID, topic); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkAdvanced updates the current_position on every (gcid, path, topic)
// row. Used by HandleAdvance when the learner crosses a topic
// boundary; topics whose position is now < current_position are
// flagged `explored=true` (S6.4 future scope; current MVP keeps the
// boolean false until the dose composer needs it).
//
// SQL:
//
//	UPDATE active_path_topics SET current_position = $4
//	WHERE tenant_id = $1 AND gcid = $2 AND learning_path_id = $3
//	    AND deleted_at IS NULL;
func (r *ActivePathTopicsRepo) MarkAdvanced(ctx context.Context, tenantID, gcid, learningPathID string, currentPosition int) error {
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		_, err := q.Exec(ctx, markActivePathTopicsAdvancedSQL, tenantID, gcid, learningPathID, currentPosition)
		return err
	})
}

// GetTopics returns the topic-set covered by one learner's active
// LearningPath(s). Always returns a non-nil map.
//
// SQL:
//
//	SELECT DISTINCT topic_tag FROM active_path_topics
//	WHERE tenant_id = $1 AND gcid = $2 AND deleted_at IS NULL;
func (r *ActivePathTopicsRepo) GetTopics(ctx context.Context, tenantID, gcid string) (map[string]bool, error) {
	out := make(map[string]bool)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, getActivePathTopicsSQL, tenantID, gcid)
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

const (
	upsertActivePathTopicsSQL = `
		INSERT INTO active_path_topics (tenant_id, gcid, learning_path_id, topic_tag)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, gcid, learning_path_id, topic_tag) DO NOTHING`

	markActivePathTopicsAdvancedSQL = `
		UPDATE active_path_topics
		SET current_position = $4
		WHERE tenant_id = $1 AND gcid = $2 AND learning_path_id = $3
			AND deleted_at IS NULL`

	getActivePathTopicsSQL = `
		SELECT DISTINCT topic_tag FROM active_path_topics
		WHERE tenant_id = $1 AND gcid = $2 AND deleted_at IS NULL`
)
