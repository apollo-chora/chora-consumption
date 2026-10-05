// outbox_in_tx.go — SAME-TX outbox enqueue (CHO-2129 AC4).
//
// The canonical outbox.Publisher writes rows post-commit on its own SQLDB
// connection — a crash between a fold's commit and that insert loses the
// event. TxOutbox closes the gap for flows composed under an ambient fold
// transaction (tx_ambient.go): the row INSERT rides the SAME transaction as
// the state writes that earned it, so the emit commits or rolls back with the
// fold. outbox_events lives in the same chora_consumption database, and the
// row shape comes from outbox.BuildRow — byte-identical to the post-commit
// path, so the dispatcher drains both indistinguishably.
//
// The INSERT is ON CONFLICT DO NOTHING (any unique arbiter — id PK or the
// idempotency-key index): inside an open transaction a raised unique violation
// would poison the WHOLE fold, while a silently-skipped duplicate is exactly
// the at-least-once replay semantic the idempotency key exists for.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/outbox"
)

// RunAmbient runs fn inside one transaction whose Querier rides the ctx
// (tx_ambient.go): every same-package repo call inside fn JOINS the
// transaction instead of beginning its own, and TxOutbox enqueues ride it too.
// This is the production FoldTx seam (CHO-2129).
func (t *PgxTxRunner) RunAmbient(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		return fn(WithAmbientQuerier(ctx, q))
	})
}

// TxOutbox enqueues outbox rows through the AMBIENT transaction. It fails
// loud outside one — a silent fallback to its own connection would quietly
// reintroduce the post-commit loss window this type exists to close.
type TxOutbox struct {
	aggregateType string
}

// NewTxOutbox constructs a same-tx outbox enqueuer stamping aggregateType on
// every row (mirrors outbox.PublisherConfig.AggregateType).
func NewTxOutbox(aggregateType string) *TxOutbox {
	if aggregateType == "" {
		aggregateType = "atom_session"
	}
	return &TxOutbox{aggregateType: aggregateType}
}

// insertOutboxRowInTxSQL mirrors outbox.PostgresStore.Insert's column list;
// the ON CONFLICT clause is the in-tx difference (see the package comment).
const insertOutboxRowInTxSQL = `INSERT INTO outbox_events (
        id, tenant_id, gcid, aggregate_type, aggregate_id,
        event_type, topic, payload, envelope,
        idempotency_key, occurred_at, status
    ) VALUES (
        $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, 'pending'
    ) ON CONFLICT DO NOTHING`

// PublishGrownInTx satisfies the projector's GrownOutbox port (and is shaped
// for any future same-tx emitter): validate + encode via outbox.BuildRow, then
// INSERT through the ambient Querier.
func (t *TxOutbox) PublishGrownInTx(ctx context.Context, topic string, env events.Envelope, payload map[string]any) error {
	q, ok := AmbientQuerier(ctx)
	if !ok {
		return fmt.Errorf("pg: TxOutbox requires an ambient transaction (topic %s) — wire the fold through PgxTxRunner.RunAmbient", topic)
	}
	row, err := outbox.BuildRow(t.aggregateType, topic, env, payload)
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(row.Envelope)
	if err != nil {
		return fmt.Errorf("pg: marshal outbox envelope: %w", err)
	}
	if _, err := q.Exec(ctx, insertOutboxRowInTxSQL,
		row.ID, nilIfEmptyStr(row.TenantID), nilIfEmptyStr(row.GCID),
		row.AggregateType, row.AggregateID,
		row.EventType, row.Topic, row.Payload, string(envJSON),
		nilIfEmptyStr(row.IdempotencyKey), row.OccurredAt,
	); err != nil {
		return fmt.Errorf("pg: same-tx outbox insert (topic %s): %w", topic, err)
	}
	return nil
}

// nilIfEmptyStr maps "" to NULL (mirrors outbox.nilIfEmpty — tenant/gcid/
// idempotency_key columns are nullable and "" would violate their uuid types).
func nilIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
