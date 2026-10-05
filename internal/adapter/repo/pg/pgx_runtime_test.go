// pgx_runtime_test.go — interface-shape verification for the production
// pgx-backed Querier + TxRunner. Real integration tests against a
// testcontainers Postgres land in M12 (per the M11 → M12 transition
// plan documented at the top of user_kg.go); this file verifies the
// runtime types satisfy the local interfaces so they can be swapped
// with the test stubQuerier without compile-time drift.
package pg

import (
	"errors"
	"testing"
)

// TestPgxQuerier_SatisfiesQuerier confirms PgxQuerier (pre-construction)
// — once a real pgx.Tx is supplied — satisfies the local Querier
// interface. We don't construct one here (would need a real pgx.Tx);
// we only assert the method set via a typed pointer.
func TestPgxQuerier_SatisfiesQuerier(t *testing.T) {
	var _ Querier = (*PgxQuerier)(nil)
}

// TestPgxTxRunner_SatisfiesTxRunner confirms PgxTxRunner satisfies
// TxRunner.
func TestPgxTxRunner_SatisfiesTxRunner(t *testing.T) {
	var _ TxRunner = (*PgxTxRunner)(nil)
}

// TestPgxRow_SatisfiesRow confirms pgxRow satisfies Row.
func TestPgxRow_SatisfiesRow(t *testing.T) {
	var _ Row = (*pgxRow)(nil)
}

// TestPgxRows_SatisfiesRows confirms pgxRows satisfies Rows.
func TestPgxRows_SatisfiesRows(t *testing.T) {
	var _ Rows = (*pgxRows)(nil)
}

// TestErrNoRows_NonEmpty smoke-checks the sentinel.
func TestErrNoRows_NonEmpty(t *testing.T) {
	if ErrNoRows == nil {
		t.Fatal("ErrNoRows must not be nil")
	}
	if ErrNoRows.Error() == "" {
		t.Fatal("ErrNoRows must have a message")
	}
	if !errors.Is(ErrNoRows, ErrNoRows) {
		t.Fatal("errors.Is must match self")
	}
}
