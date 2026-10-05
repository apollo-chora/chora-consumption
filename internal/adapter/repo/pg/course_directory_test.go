// course_directory_test.go — unit tests for pg.CourseDirectoryRepo (the
// course_id → title projection, chora-consumption side; CHO-2059 follow-up).
//
// Mirrors chora-delivery's user_directory_test.go: a local stub Querier
// exercises the SQL surface + arg binding without a live DB. A DEDICATED stub
// (not the shared package stubQuerier, which records only Exec + returns nil
// rows) is used here so the tests can assert bind args AND return rows.
//
// What these guarantee:
//  1. Nil-tx returns ErrNotImplemented (fail-loud, matches sibling repos).
//  2. Upsert emits SQLUpsertCourseDirectory with (course_id, title, updated_at)
//     in order — and NO SET LOCAL / rls.ApplySession runs (tenant-agnostic,
//     RLS-free global table).
//  3. Empty course_id is rejected at the boundary (loud sentinel).
//  4. LookupTitles short-circuits on empty input (no query) + maps rows on a hit
//     via ANY($1::uuid[]).
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
)

const (
	cdCID1 = "01970000-0000-7000-b000-000000000001"
	cdCID2 = "01970000-0000-7000-b000-000000000002"
)

// --- dedicated stub recording args + returning rows ---

type cdStubQuerier struct {
	execSQL   []string
	execArgs  [][]any
	querySQL  []string
	queryArgs [][]any
	rows      *cdStubRows
}

func (q *cdStubQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	q.execSQL = append(q.execSQL, sql)
	q.execArgs = append(q.execArgs, args)
	return rls.CommandTag{}, nil
}
func (q *cdStubQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row { return nil }
func (q *cdStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	q.querySQL = append(q.querySQL, sql)
	q.queryArgs = append(q.queryArgs, args)
	if q.rows == nil {
		return nil, nil
	}
	return q.rows, nil
}

type cdStubRows struct {
	data [][2]string // course_id, title
	i    int
}

func (r *cdStubRows) Next() bool { return r.i < len(r.data) }
func (r *cdStubRows) Scan(dest ...any) error {
	row := r.data[r.i]
	r.i++
	*(dest[0].(*string)) = row[0]
	*(dest[1].(*string)) = row[1]
	return nil
}
func (r *cdStubRows) Close() error { return nil }
func (r *cdStubRows) Err() error   { return nil }

type cdStubTxRunner struct{ q *cdStubQuerier }

func (t *cdStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &cdStubQuerier{}
	}
	return fn(ctx, t.q)
}

func TestCourseDirectoryRepo_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	r := NewCourseDirectoryRepo(nil)
	if err := r.Upsert(context.Background(), cdEntry(cdCID1, "x", time.Now())); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Upsert: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.LookupTitles(context.Background(), []string{cdCID1}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("LookupTitles: expected ErrNotImplemented; got %v", err)
	}
}

func TestCourseDirectoryRepo_Upsert_EmitsLWWSQLWithArgs_NoRLS(t *testing.T) {
	q := &cdStubQuerier{}
	r := NewCourseDirectoryRepo(&cdStubTxRunner{q: q})
	ts := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	if err := r.Upsert(context.Background(), cdEntry(cdCID1, "Algebra I", ts)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Exactly ONE exec — no rls.ApplySession SET LOCAL pair (global table).
	if len(q.execSQL) != 1 {
		t.Fatalf("expected exactly 1 exec (no rls SET LOCAL); got %d: %v", len(q.execSQL), q.execSQL)
	}
	sql := q.execSQL[0]
	if strings.Contains(sql, "SET LOCAL") {
		t.Fatalf("upsert must NOT run rls.ApplySession (SET LOCAL): %s", sql)
	}
	if !strings.Contains(sql, "INSERT INTO course_directory") || !strings.Contains(sql, "ON CONFLICT (course_id)") {
		t.Fatalf("unexpected upsert SQL: %s", sql)
	}
	if !strings.Contains(sql, "course_directory.updated_at < EXCLUDED.updated_at") {
		t.Fatalf("upsert SQL missing last-writer-wins guard: %s", sql)
	}
	args := q.execArgs[0]
	if len(args) != 3 {
		t.Fatalf("expected 3 bind args (course_id, title, updated_at); got %d: %v", len(args), args)
	}
	if args[0] != cdCID1 {
		t.Errorf("arg[0] course_id = %v, want %s", args[0], cdCID1)
	}
	if args[1] != "Algebra I" {
		t.Errorf("arg[1] title = %v, want Algebra I", args[1])
	}
	if args[2] != ts {
		t.Errorf("arg[2] updated_at = %v, want %v", args[2], ts)
	}
}

func TestCourseDirectoryRepo_Upsert_RejectsEmptyCourseID(t *testing.T) {
	r := NewCourseDirectoryRepo(&cdStubTxRunner{q: &cdStubQuerier{}})
	if err := r.Upsert(context.Background(), cdEntry("", "x", time.Now())); !errors.Is(err, ErrCourseDirectoryMissingCourseID) {
		t.Fatalf("expected ErrCourseDirectoryMissingCourseID; got %v", err)
	}
}

func TestCourseDirectoryRepo_LookupTitles_EmptyInput_NoQuery(t *testing.T) {
	q := &cdStubQuerier{}
	r := NewCourseDirectoryRepo(&cdStubTxRunner{q: q})
	titles, err := r.LookupTitles(context.Background(), nil)
	if err != nil {
		t.Fatalf("LookupTitles(nil): %v", err)
	}
	if len(titles) != 0 {
		t.Errorf("expected empty map; got %v", titles)
	}
	if len(q.querySQL) != 0 || len(q.execSQL) != 0 {
		t.Errorf("empty input should short-circuit before any query; ran query=%v exec=%v", q.querySQL, q.execSQL)
	}
}

func TestCourseDirectoryRepo_LookupTitles_MapsRows_AnyArray(t *testing.T) {
	q := &cdStubQuerier{rows: &cdStubRows{data: [][2]string{
		{cdCID1, "Algebra I"},
		{cdCID2, "Data Science 101"},
	}}}
	r := NewCourseDirectoryRepo(&cdStubTxRunner{q: q})
	titles, err := r.LookupTitles(context.Background(), []string{cdCID1, cdCID2})
	if err != nil {
		t.Fatalf("LookupTitles: %v", err)
	}
	if titles[cdCID1] != "Algebra I" {
		t.Errorf("cid1 = %q, want Algebra I", titles[cdCID1])
	}
	if titles[cdCID2] != "Data Science 101" {
		t.Errorf("cid2 = %q, want Data Science 101", titles[cdCID2])
	}
	// SQL uses ANY($1::uuid[]) so the PK index is used, NO SET LOCAL.
	if len(q.querySQL) != 1 {
		t.Fatalf("expected exactly 1 query; got %d: %v", len(q.querySQL), q.querySQL)
	}
	if !strings.Contains(q.querySQL[0], "ANY($1::uuid[])") {
		t.Errorf("lookup SQL missing ANY($1::uuid[]): %s", q.querySQL[0])
	}
	if strings.Contains(q.querySQL[0], "SET LOCAL") {
		t.Errorf("lookup must NOT run rls.ApplySession: %s", q.querySQL[0])
	}
	// Bind arg is the []string slice cast to uuid[] in SQL.
	if len(q.queryArgs[0]) != 1 {
		t.Fatalf("expected 1 bind arg (the id slice); got %v", q.queryArgs[0])
	}
}

func cdEntry(courseID, title string, ts time.Time) course_directory.CourseDirectoryEntry {
	return course_directory.CourseDirectoryEntry{CourseID: courseID, Title: title, UpdatedAt: ts}
}
