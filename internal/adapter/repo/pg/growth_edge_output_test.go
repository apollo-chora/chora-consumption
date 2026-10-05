package pg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	geo "github.com/apollo-chora/chora-consumption/internal/domain/growth_edge_output"
)

// --- self-contained Querier/TxRunner stubs (sibling test files own theirs) ---

type geoExec struct {
	sql  string
	args []any
}

type geoQuerier struct {
	execs    []geoExec
	rowsData [][]any
	queries  []geoExec
	rowsAff  int64
}

func (q *geoQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	q.execs = append(q.execs, geoExec{sql, args})
	if strings.Contains(sql, "growth_edge_outputs") {
		return rls.CommandTag{RowsAffected: q.rowsAff}, nil
	}
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (q *geoQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return wuRow{scan: func(...any) error { return ErrNoRows }}
}

func (q *geoQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	q.queries = append(q.queries, geoExec{sql, args})
	return &geoRows{data: q.rowsData}, nil
}

type geoRows struct {
	data [][]any
	i    int
}

func (r *geoRows) Next() bool   { r.i++; return r.i <= len(r.data) }
func (r *geoRows) Close() error { return nil }
func (r *geoRows) Err() error   { return nil }
func (r *geoRows) Scan(dest ...any) error {
	row := r.data[r.i-1]
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = row[i].(string)
		case *bool:
			*d = row[i].(bool)
		case *[]byte:
			*d = row[i].([]byte)
		case *time.Time:
			*d = row[i].(time.Time)
		}
	}
	return nil
}

type geoTx struct{ q *geoQuerier }

func (t geoTx) RunInTx(ctx context.Context, fn func(context.Context, Querier) error) error {
	return fn(ctx, t.q)
}

func geoCtx() context.Context {
	return tracing.WithTenantID(context.Background(), "11111111-1111-7111-8111-111111111111")
}

func geoOutput(kind geo.Kind, content string) geo.Output {
	o, err := geo.New(geo.NewArgs{
		OutputID:      "019f89c7-3439-74cc-892d-5fb7d2726ea6",
		TenantID:      "11111111-1111-7111-8111-111111111111",
		LearnerGCID:   "00000000-0000-7000-8000-000000001999",
		UploadID:      "019f89c7-3418-70c4-bc6a-1396c35166fc",
		Kind:          kind,
		Content:       json.RawMessage(content),
		Metered:       true,
		SourceEventID: "019f8a00-0000-7000-e000-000000000001",
		GeneratedAt:   time.Now().UTC(),
	})
	if err != nil {
		panic(err)
	}
	return o
}

// -----------------------------------------------------------------------------
// RLS: the trap this table is most exposed to.
// -----------------------------------------------------------------------------

func TestCreateBatch_AppliesRLSSessionBeforeInsert(t *testing.T) {
	// growth_edge_outputs is FORCE ROW LEVEL SECURITY. A write that skips
	// ApplySession does not error, it silently matches nothing, so a missing
	// SET LOCAL reads as "nothing to project" rather than as a failure.
	q := &geoQuerier{rowsAff: 1}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	if _, err := repo.CreateBatch(geoCtx(), []geo.Output{geoOutput(geo.KindStudyAids, `"prose"`)}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if len(q.execs) < 2 {
		t.Fatalf("expected a session exec then the insert, got %d execs", len(q.execs))
	}
	if !strings.Contains(strings.ToLower(q.execs[0].sql), "chora.tenant_id") {
		t.Errorf("first exec must set the tenant GUC, got %q", q.execs[0].sql)
	}
	if !strings.Contains(q.execs[1].sql, "growth_edge_outputs") {
		t.Errorf("second exec must be the insert, got %q", q.execs[1].sql)
	}
}

func TestListForUpload_AppliesRLSSessionBeforeQuery(t *testing.T) {
	q := &geoQuerier{}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	if _, err := repo.ListForUpload(geoCtx(), "t", "g", "u"); err != nil {
		t.Fatalf("ListForUpload: %v", err)
	}
	if len(q.execs) == 0 || !strings.Contains(strings.ToLower(q.execs[0].sql), "chora.tenant_id") {
		t.Fatal("read must run ApplySession before the query")
	}
}

// -----------------------------------------------------------------------------
// Idempotency + scoping
// -----------------------------------------------------------------------------

func TestCreateBatch_InsertIsIdempotent(t *testing.T) {
	// At-least-once delivery must not put two copies of a paid-for artifact on
	// the learner's surface.
	q := &geoQuerier{rowsAff: 1}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	if _, err := repo.CreateBatch(geoCtx(), []geo.Output{geoOutput(geo.KindStudyAids, `"prose"`)}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	sql := q.execs[1].sql
	if !strings.Contains(sql, "ON CONFLICT") || !strings.Contains(sql, "DO NOTHING") {
		t.Errorf("insert must be ON CONFLICT ... DO NOTHING, got %q", sql)
	}
}

func TestCreateBatch_ReturnsNewlyInsertedCount(t *testing.T) {
	// 0 distinguishes a pure redelivery from real work, which is what lets the
	// subscriber log a duplicate instead of silently acking it.
	repo := NewGrowthEdgeOutputRepo(geoTx{q: &geoQuerier{rowsAff: 0}})
	n, err := repo.CreateBatch(geoCtx(), []geo.Output{geoOutput(geo.KindStudyAids, `"prose"`)})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if n != 0 {
		t.Errorf("redelivery: inserted = %d, want 0", n)
	}
}

func TestCreateBatch_EmptySliceTouchesNothing(t *testing.T) {
	q := &geoQuerier{}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	n, err := repo.CreateBatch(geoCtx(), nil)
	if err != nil || n != 0 {
		t.Fatalf("CreateBatch(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if len(q.execs) != 0 {
		t.Errorf("empty batch must not open a transaction, got %d execs", len(q.execs))
	}
}

func TestListForUpload_ScopesAndFiltersSoftDeleted(t *testing.T) {
	q := &geoQuerier{}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	if _, err := repo.ListForUpload(geoCtx(), "t1", "g1", "u1"); err != nil {
		t.Fatalf("ListForUpload: %v", err)
	}
	if len(q.queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(q.queries))
	}
	sql := q.queries[0].sql
	for _, want := range []string{"tenant_id = $1", "learner_gcid = $2", "upload_id = $3", "deleted_at IS NULL"} {
		if !strings.Contains(sql, want) {
			t.Errorf("read SQL missing %q; got %q", want, sql)
		}
	}
	args := q.queries[0].args
	if len(args) != 3 || args[0] != "t1" || args[1] != "g1" || args[2] != "u1" {
		t.Errorf("read args = %v, want [t1 g1 u1]", args)
	}
}

// -----------------------------------------------------------------------------
// Decode
// -----------------------------------------------------------------------------

func TestListForUpload_DecodesBothKinds(t *testing.T) {
	now := time.Now().UTC()
	q := &geoQuerier{rowsData: [][]any{
		{"o1", "t1", "g1", "u1", "study_aids", []byte(`"prose"`), true, "e1", now, now},
		{"o2", "t1", "g1", "u1", "practice_test", []byte(`{"questions":[]}`), true, "e1", now, now},
	}}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	got, err := repo.ListForUpload(geoCtx(), "t1", "g1", "u1")
	if err != nil {
		t.Fatalf("ListForUpload: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d outputs, want 2", len(got))
	}
	if got[0].Kind != geo.KindStudyAids || got[1].Kind != geo.KindPracticeTest {
		t.Errorf("kinds = %q,%q", got[0].Kind, got[1].Kind)
	}
	if string(got[1].Content) != `{"questions":[]}` {
		t.Errorf("practice_test content = %q", got[1].Content)
	}
}

func TestListForUpload_FailsLoudOnUnknownKind(t *testing.T) {
	// The DB CHECK and the domain must agree. Dropping the row instead would be
	// the WS-7 gap all over again: a paid-for artifact quietly missing.
	now := time.Now().UTC()
	q := &geoQuerier{rowsData: [][]any{
		{"o1", "t1", "g1", "u1", "mystery_kind", []byte(`"x"`), true, "e1", now, now},
	}}
	repo := NewGrowthEdgeOutputRepo(geoTx{q: q})
	if _, err := repo.ListForUpload(geoCtx(), "t1", "g1", "u1"); err == nil {
		t.Fatal("want error on an unknown kind, got nil")
	}
}
