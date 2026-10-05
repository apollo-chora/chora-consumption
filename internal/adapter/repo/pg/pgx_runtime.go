// pgx_runtime.go — production pgx-backed Querier + TxRunner for the
// chora_consumption Postgres adapter.
//
// Per feedback_no_inline_config + secrets-and-env: the pgxpool is built
// in cmd/server using the connection string sourced from Secret Manager
// (`chora-consumption-postgres-dsn`) and PgBouncer endpoint config from
// Terraform. This file owns the runtime adapter that lets the
// stub-friendly `Querier` interface in user_kg.go light up against a
// real Postgres instance.
//
// Per multi-tenant-rls SKILL: every transaction emitted by `RunInTx`
// runs `SET LOCAL chora.tenant_id` + `SET LOCAL chora.user_gcid` BEFORE
// any user query. The repo methods enforce that contract via
// rls.ApplySession — but `RunInTx` here also wraps every fn call in
// `BeginTx → Commit/Rollback` so SET LOCAL is transaction-scoped (never
// connection-scoped, which would leak across PgBouncer
// transaction-pooling siblings).
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/rls"
)

// PgxQuerier wraps a `pgx.Tx` so it satisfies the local `Querier`
// contract used by the Postgres repo methods. Tests inject a stub
// instead.
type PgxQuerier struct {
	tx pgx.Tx
}

// NewPgxQuerier wraps a pgx.Tx.
func NewPgxQuerier(tx pgx.Tx) *PgxQuerier {
	return &PgxQuerier{tx: tx}
}

// Exec runs a non-returning SQL statement.
func (q *PgxQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	tag, err := q.tx.Exec(ctx, sql, args...)
	if err != nil {
		return rls.CommandTag{}, err
	}
	return rls.CommandTag{RowsAffected: tag.RowsAffected()}, nil
}

// QueryRow runs a single-row SQL query.
func (q *PgxQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return &pgxRow{r: q.tx.QueryRow(ctx, sql, args...)}
}

// Query runs a multi-row SQL query.
func (q *PgxQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	rs, err := q.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRows{r: rs}, nil
}

// pgxRow is the bridge between `pgx.Row` and the local `Row` interface.
//
// pgx returns sql.ErrNoRows for empty queries; we surface
// pgx.ErrNoRows so callers can errors.Is-detect "not found" without
// dragging the pgx import into repo methods.
type pgxRow struct {
	r pgx.Row
}

func (r *pgxRow) Scan(dest ...any) error {
	err := r.r.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

// pgxRows wraps `pgx.Rows` to satisfy the local `Rows` interface.
type pgxRows struct {
	r pgx.Rows
}

func (r *pgxRows) Next() bool          { return r.r.Next() }
func (r *pgxRows) Scan(d ...any) error { return r.r.Scan(d...) }
func (r *pgxRows) Close() error        { r.r.Close(); return nil }
func (r *pgxRows) Err() error          { return r.r.Err() }

// PgxTxRunner wraps a pgxpool.Pool to satisfy the local `TxRunner`
// interface. Production cmd/server calls NewPgxTxRunner(pool); tests
// inject a stubTxRunner.
type PgxTxRunner struct {
	pool *pgxpool.Pool
}

// NewPgxTxRunner builds a TxRunner around the supplied pool.
func NewPgxTxRunner(pool *pgxpool.Pool) *PgxTxRunner {
	return &PgxTxRunner{pool: pool}
}

// RunInTx runs `fn` inside a single transaction. Commits on success,
// rolls back on error. Panics propagate to the caller after rollback.
//
// Mandatory: rls.ApplySession is called by the repo methods on the
// supplied Querier BEFORE any user query, so SET LOCAL chora.tenant_id
// + SET LOCAL chora.user_gcid land within the transaction scope (which
// is what makes RLS leak-safe under PgBouncer transaction-pooling).
func (t *PgxTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) (err error) {
	// CHO-2039: join an ambient transaction when the caller already opened
	// one (the AwardExpTx PostAwardInTx seam, tx_ambient.go). fn runs
	// against the caller's Querier; the transaction OWNER commits or rolls
	// back, so an error here aborts the WHOLE composed transaction.
	if q, ok := AmbientQuerier(ctx); ok {
		return fn(ctx, q)
	}
	tx, err := t.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("pg: begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()
	q := NewPgxQuerier(tx)
	return fn(ctx, q)
}

// ErrNoRows is returned by `Row.Scan` when no rows match. Repos
// translate this to domain-level not-found errors. Callers should
// errors.Is against this constant rather than against pgx.ErrNoRows so
// the test stubQuerier can return the same sentinel.
var ErrNoRows = errors.New("pg: no rows in result set")
