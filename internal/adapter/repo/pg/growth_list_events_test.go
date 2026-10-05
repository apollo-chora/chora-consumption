// growth_list_events_test.go — regression test for the
// E2E-BE-FAM-GROWTH §2 fix (2026-05-16).
//
// Bug: ListGrowthEvents was passing an empty Go time.Time + empty Go
// string to a query that casts the cursor params to `timestamptz` /
// `uuid`. PostgreSQL eagerly type-checks the `$4::uuid` cast even
// though the row's `OR (... AND growth_event_id > $4)` clause would
// only fire when `$3::timestamptz IS NOT NULL` — and a zero-string is
// not a valid UUID literal. Live edge symptom:
//
//	HTTP 500 — "invalid input syntax for type uuid: \"\""
//	            (SQLSTATE 22P02)
//
// Live evidence: chora-consumption pod `fix-a-99228610` logged
// status=500 on `GET /v1/me/companions/{id}/growth-events?page_size=3`
// for Phyllis demo seed (Eira 00000000-0000-7000-8000-00000000e1a0).
//
// Fix: convert cursor params to interface{} typed-nil when empty so
// pgx sends them as PostgreSQL NULL — the cast `$4::uuid` against
// NULL is well-defined (NULL).
//
// This test captures the args passed to q.Query when PageToken is
// empty and asserts BOTH cursor params are nil.
package pg

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/growth"
)

// argsCapturingQuerier wraps stubQuerier to record the args of the
// most recent Query call (in addition to the existing exec SQL capture).
type argsCapturingQuerier struct {
	stubQuerier
	lastQueryArgs []any
}

func (q *argsCapturingQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	q.stubQuerier.execCalls = append(q.stubQuerier.execCalls, "Q:"+sql)
	q.lastQueryArgs = make([]any, len(args))
	copy(q.lastQueryArgs, args)
	return nil, nil
}

func (q *argsCapturingQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	return q.stubQuerier.Exec(ctx, sql, args...)
}

func (q *argsCapturingQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return q.stubQuerier.QueryRow(ctx, sql, args...)
}

type argsCapturingTxRunner struct {
	q *argsCapturingQuerier
}

func (t *argsCapturingTxRunner) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	if t.q == nil {
		t.q = &argsCapturingQuerier{}
	}
	return fn(ctx, t.q)
}

// TestListGrowthEvents_EmptyPageToken_SendsTypedNilCursor asserts that
// when PageToken is empty, the cursor args ($3 timestamptz, $4 uuid)
// arrive at q.Query as Go nil — NOT as zero time.Time + empty string.
// Without this, PostgreSQL fails with SQLSTATE 22P02 because the
// `$4::uuid` cast cannot parse the empty string.
func TestListGrowthEvents_EmptyPageToken_SendsTypedNilCursor(t *testing.T) {
	tx := &argsCapturingTxRunner{q: &argsCapturingQuerier{}}
	repo := NewGrowthRepo(tx)

	_, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID:    pgTenantID,
		CompanionID: "01970000-0000-7000-c000-000000000001",
		CallerGCID:  pgUserGCID,
		PageSize:    25,
		PageToken:   "", // empty — first page
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}

	if tx.q == nil || tx.q.lastQueryArgs == nil {
		t.Fatal("expected Query to have been called; got no args capture")
	}
	args := tx.q.lastQueryArgs
	if len(args) != 5 {
		t.Fatalf("Query args = %d, want 5 ($1..$5)", len(args))
	}

	// $1 tenant_id, $2 companion_id, $3 cursor_ts, $4 cursor_id, $5 limit
	cursorTs := args[2]
	cursorID := args[3]

	if cursorTs != nil {
		t.Errorf("$3 cursor_ts = %#v (type %T); want nil so pgx serializes as PostgreSQL NULL",
			cursorTs, cursorTs)
	}
	if cursorID != nil {
		t.Errorf("$4 cursor_id = %#v (type %T); want nil so pgx serializes as PostgreSQL NULL "+
			"(prevents SQLSTATE 22P02 on `::uuid` cast of empty string)",
			cursorID, cursorID)
	}
}

// TestListGrowthEvents_NonEmptyPageToken_SendsConcreteCursor verifies
// the inverse: a real page token decodes to concrete cursor values
// (not nil) so subsequent pages can use the `awarded_at < $3` clause.
func TestListGrowthEvents_NonEmptyPageToken_SendsConcreteCursor(t *testing.T) {
	tx := &argsCapturingTxRunner{q: &argsCapturingQuerier{}}
	repo := NewGrowthRepo(tx)

	// 2026-05-16 00:00:00 UTC unix nano + canonical event UUID
	token := "1779580800000000000:01970000-0000-7000-d000-000000000001"
	_, err := repo.ListGrowthEvents(withCtx(), growth.ListGrowthEventsInput{
		TenantID:    pgTenantID,
		CompanionID: "01970000-0000-7000-c000-000000000001",
		CallerGCID:  pgUserGCID,
		PageSize:    25,
		PageToken:   token,
	})
	if err != nil {
		t.Fatalf("ListGrowthEvents: %v", err)
	}

	args := tx.q.lastQueryArgs
	if len(args) != 5 {
		t.Fatalf("Query args = %d, want 5", len(args))
	}
	if args[2] == nil {
		t.Errorf("$3 cursor_ts = nil; want concrete time.Time when PageToken non-empty")
	}
	if args[3] == nil {
		t.Errorf("$4 cursor_id = nil; want concrete string when PageToken non-empty")
	}
	if s, ok := args[3].(string); !ok || s != "01970000-0000-7000-d000-000000000001" {
		t.Errorf("$4 cursor_id = %#v; want decoded UUID string", args[3])
	}
}
