// weakness_blob_dek_wrap_test.go — RLS-contract + SQL-shape verification for the
// per-blob wrapped-DEK pg adapter (ADR-205 WS-5 / CHO-1957). Mirrors the
// stub-Querier review-via-test style used across this package: assert that
// rls.ApplySession (SET LOCAL chora.tenant_id) lands BEFORE every user query and
// that the SQL targets the migration-0053 table + columns. Live behaviour is
// exercised under the `integration` build tag.
package pg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	wb "github.com/apollo-chora/chora-consumption/internal/domain/weakness_blob"
)

// dekRow is a controllable single-row stub.
type dekRow struct {
	scan func(dest ...any) error
}

func (r dekRow) Scan(dest ...any) error {
	if r.scan == nil {
		return ErrNoRows
	}
	return r.scan(dest...)
}

// dekRows is a controllable multi-row stub.
type dekRows struct {
	rows [][]any
	i    int
}

func (r *dekRows) Next() bool { return r.i < len(r.rows) }
func (r *dekRows) Scan(dest ...any) error {
	row := r.rows[r.i]
	r.i++
	for k := range dest {
		switch d := dest[k].(type) {
		case *string:
			*d = row[k].(string)
		case *time.Time:
			*d = row[k].(time.Time)
		}
	}
	return nil
}
func (r *dekRows) Close() error { return nil }
func (r *dekRows) Err() error   { return nil }

// dekQuerier records every SQL string + returns scripted Row/Rows.
type dekQuerier struct {
	sqls    []string
	row     dekRow
	rows    *dekRows
	execTag rls.CommandTag
}

func (q *dekQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.sqls = append(q.sqls, sql)
	return q.execTag, nil
}
func (q *dekQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.sqls = append(q.sqls, sql)
	return q.row
}
func (q *dekQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.sqls = append(q.sqls, sql)
	if q.rows == nil {
		return &dekRows{}, nil
	}
	return q.rows, nil
}

type dekTxRunner struct{ q *dekQuerier }

func (t *dekTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &dekQuerier{}
	}
	return fn(ctx, t.q)
}

func assertRLSFirst(t *testing.T, sqls []string) {
	t.Helper()
	if len(sqls) < 2 || !contains(sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("expected SET LOCAL chora.tenant_id before the query; got %v", sqls)
	}
}

func TestWeaknessBlobDEKWrap_PutAppliesRLSAndTargetsTable(t *testing.T) {
	tx := &dekTxRunner{}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	err := repo.Put(withCtx(), wb.WrappedDEK{
		UploadID: "01970000-0000-7000-a000-0000000000cc", TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		Wrapped: []byte("w"), KEKVersion: "kek/v1", CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	assertRLSFirst(t, tx.q.sqls)
	last := tx.q.sqls[len(tx.q.sqls)-1]
	if !contains(last, "INSERT INTO weakness_blob_dek_wrap") || !contains(last, "ON CONFLICT") {
		t.Fatalf("Put SQL wrong: %q", last)
	}
}

func TestWeaknessBlobDEKWrap_GetAppliesRLSAndReturnsRecord(t *testing.T) {
	tx := &dekTxRunner{q: &dekQuerier{row: dekRow{scan: func(dest ...any) error {
		// upload_id, tenant_id, learner_gcid, wrapped(bytea→[]byte), kek_version, created_at, deleted(bool)
		*(dest[0].(*string)) = "01970000-0000-7000-a000-0000000000cc"
		*(dest[1].(*string)) = pgTenantID
		*(dest[2].(*string)) = pgUserGCID
		*(dest[3].(*[]byte)) = []byte("wrapped")
		*(dest[4].(*string)) = "kek/v1"
		*(dest[5].(*time.Time)) = time.Unix(1700000000, 0).UTC()
		*(dest[6].(*bool)) = false
		return nil
	}}}}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	got, err := repo.Get(withCtx(), "01970000-0000-7000-a000-0000000000cc")
	if err != nil || got == nil {
		t.Fatalf("Get: %v rec=%v", err, got)
	}
	if string(got.Wrapped) != "wrapped" || got.KEKVersion != "kek/v1" || got.Deleted {
		t.Fatalf("Get scanned wrong: %+v", got)
	}
	assertRLSFirst(t, tx.q.sqls)
	if last := tx.q.sqls[len(tx.q.sqls)-1]; !contains(last, "FROM weakness_blob_dek_wrap") {
		t.Fatalf("Get SQL wrong: %q", last)
	}
}

func TestWeaknessBlobDEKWrap_GetAbsentReturnsNilNil(t *testing.T) {
	tx := &dekTxRunner{q: &dekQuerier{row: dekRow{scan: func(...any) error { return ErrNoRows }}}}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	got, err := repo.Get(withCtx(), "missing")
	if err != nil || got != nil {
		t.Fatalf("absent Get = (%v,%v); want (nil,nil)", got, err)
	}
}

func TestWeaknessBlobDEKWrap_ShredAppliesRLSAndScrubs(t *testing.T) {
	tx := &dekTxRunner{}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	if err := repo.Shred(withCtx(), "01970000-0000-7000-a000-0000000000cc", time.Now()); err != nil {
		t.Fatalf("Shred: %v", err)
	}
	assertRLSFirst(t, tx.q.sqls)
	last := tx.q.sqls[len(tx.q.sqls)-1]
	if !contains(last, "UPDATE weakness_blob_dek_wrap") || !contains(last, "wrapped_dek = NULL") || !contains(last, "deleted_at") {
		t.Fatalf("Shred SQL must NULL the wrapped DEK + set deleted_at: %q", last)
	}
	if !contains(last, "deleted_at IS NULL") {
		t.Fatalf("Shred must be idempotent (only un-shredded rows): %q", last)
	}
}

func TestWeaknessBlobDEKWrap_ListExpiredAppliesRLSAndShape(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	tx := &dekTxRunner{q: &dekQuerier{rows: &dekRows{rows: [][]any{
		{"01970000-0000-7000-a000-000000000001", pgTenantID, pgUserGCID, "kek/v1", now},
		{"01970000-0000-7000-a000-000000000002", pgTenantID, pgUserGCID, "kek/v1", now},
	}}}}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	got, err := repo.ListExpiredUnshredded(withCtx(), now.Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("ListExpiredUnshredded: %v", err)
	}
	if len(got) != 2 || got[0].UploadID == "" || got[0].TenantID != pgTenantID {
		t.Fatalf("list scanned wrong: %+v", got)
	}
	assertRLSFirst(t, tx.q.sqls)
	last := tx.q.sqls[len(tx.q.sqls)-1]
	if !contains(last, "FROM weakness_blob_dek_wrap") || !contains(last, "deleted_at IS NULL") || !strings.Contains(last, "created_at <") {
		t.Fatalf("list SQL must filter un-shredded + by age: %q", last)
	}
}

func TestWeaknessBlobDEKWrap_RejectsBareContext(t *testing.T) {
	// Defence-in-depth: a context with no tenant must fail at rls.ApplySession,
	// never silently span tenants.
	tx := &dekTxRunner{}
	repo := NewWeaknessBlobDEKWrapRepo(tx)
	err := repo.Put(context.Background(), wb.WrappedDEK{UploadID: "x", TenantID: pgTenantID, KEKVersion: "v"})
	if err == nil {
		t.Fatalf("expected a bare-context Put to fail (rls.ErrNoTenantContext)")
	}
}

var _ wb.WrappedDEKStore = (*WeaknessBlobDEKWrapRepo)(nil)
