package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	wu "github.com/apollo-chora/chora-consumption/internal/domain/weakness_upload"
)

// --- self-contained Querier/TxRunner/Row stub (the shared lwScanInto can't
// scan *int / **time.Time, and editing it would touch a sibling-owned file) ---

type wuExec struct {
	sql  string
	args []any
}

type wuQuerier struct {
	execs   []wuExec
	row     Row
	execErr error
}

func (q *wuQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	q.execs = append(q.execs, wuExec{sql, args})
	if q.execErr != nil && strings.Contains(sql, "weakness_doc_uploads") {
		return rls.CommandTag{}, q.execErr
	}
	return rls.CommandTag{RowsAffected: 1}, nil
}
func (q *wuQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row {
	if q.row != nil {
		return q.row
	}
	return wuRow{scan: func(...any) error { return ErrNoRows }}
}
func (q *wuQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return &lwStubRows{}, nil
}
func (q *wuQuerier) find(marker string) (wuExec, bool) {
	for _, e := range q.execs {
		if strings.Contains(e.sql, marker) {
			return e, true
		}
	}
	return wuExec{}, false
}

type wuTx struct{ q *wuQuerier }

func (t *wuTx) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if t.q == nil {
		t.q = &wuQuerier{}
	}
	return fn(ctx, t.q)
}

type wuRow struct{ scan func(dest ...any) error }

func (r wuRow) Scan(dest ...any) error { return r.scan(dest...) }

func wuCtx() context.Context {
	return tracing.WithGCID(tracing.WithTenantID(context.Background(), "tnt-1"), "gcid-1")
}

func wuSample() wu.Upload {
	u, _ := wu.New(wu.NewInput{
		UploadID: "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee", TenantID: "tnt-1", LearnerGCID: "gcid-1",
		UploadKind: wu.KindMarkedTest, SourceMIME: "application/pdf",
		SourceBlobURI: "gs://b/o.pdf", Now: time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	})
	return u
}

func TestWeaknessUploadRepo_Insert(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	if err := repo.Insert(wuCtx(), wuSample()); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	e, ok := tx.q.find("INSERT INTO weakness_doc_uploads")
	if !ok {
		t.Fatalf("no INSERT exec; got %d execs", len(tx.q.execs))
	}
	if e.args[0] != "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee" || e.args[6] != "QUEUED" {
		t.Errorf("insert args = %v", e.args)
	}
}

func TestWeaknessUploadRepo_Insert_CarriesGoalID(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	u, _ := wu.New(wu.NewInput{
		UploadID: "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee", TenantID: "tnt-1", LearnerGCID: "gcid-1",
		UploadKind: wu.KindMarkedTest, SourceMIME: "application/pdf", SourceBlobURI: "gs://b/o.pdf",
		Now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC), GoalID: "0190aaaa-bbbb-7ccc-8ddd-000000000009",
		EntryConceptID: "0190bbbb-cccc-7ddd-8eee-000000000042",
	})
	if err := repo.Insert(wuCtx(), u); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	e, ok := tx.q.find("INSERT INTO weakness_doc_uploads")
	if !ok {
		t.Fatalf("no INSERT exec; got %d execs", len(tx.q.execs))
	}
	if !strings.Contains(e.sql, "goal_id") {
		t.Errorf("INSERT SQL missing goal_id column:\n%s", e.sql)
	}
	if !strings.Contains(e.sql, "entry_concept_id") {
		t.Errorf("INSERT SQL missing entry_concept_id column (ADR-238 D2):\n%s", e.sql)
	}
	// Positional, not "last": goal_id then entry_concept_id are appended after
	// upload_rights_consent_at. Asserting position rather than tail means adding a
	// further column can never silently shift goal_id out from under this test.
	if got := e.args[len(e.args)-2]; got != "0190aaaa-bbbb-7ccc-8ddd-000000000009" {
		t.Errorf("INSERT goal_id arg = %v; want the goal_id", got)
	}
	if got := e.args[len(e.args)-1]; got != "0190bbbb-cccc-7ddd-8eee-000000000042" {
		t.Errorf("INSERT entry_concept_id arg = %v; want the entry concept hint", got)
	}
}

func TestWeaknessUploadRepo_Get_ScansGoalID(t *testing.T) {
	ts := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	row := wuRow{scan: func(dest ...any) error {
		*dest[0].(*string) = "up-1"
		*dest[1].(*string) = "gcid-1"
		*dest[2].(*string) = "marked_test"
		*dest[3].(*string) = "application/pdf"
		*dest[4].(*string) = "gs://b/o.pdf"
		*dest[5].(*string) = "QUEUED"
		*dest[6].(*[]string) = []string{}
		*dest[7].(*int) = 0
		*dest[8].(*string) = ""
		*dest[9].(*time.Time) = ts
		// dest[10] analyzed_at (**time.Time), dest[11] review_payload (*[]byte): left nil.
		*dest[12].(*string) = "0190aaaa-bbbb-7ccc-8ddd-000000000009" // goal_id (COALESCE'd)
		return nil
	}}
	tx := &wuTx{q: &wuQuerier{row: row}}
	repo := NewWeaknessUploadRepo(tx)

	got, err := repo.Get(wuCtx(), "gcid-1", "up-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.GoalID != "0190aaaa-bbbb-7ccc-8ddd-000000000009" {
		t.Errorf("goal_id not scanned: %+v", got)
	}
	if !strings.Contains(getWeaknessUploadSQL, "goal_id") {
		t.Errorf("SELECT SQL missing goal_id column:\n%s", getWeaknessUploadSQL)
	}
}

func TestWeaknessUploadRepo_ScopeForUpload(t *testing.T) {
	// ADR-238 M-D2/D2: ScopeForUpload reuses Get and returns the scanned goal_id
	// AND the entry-concept hint from the same row.
	row := wuRow{scan: func(dest ...any) error {
		*dest[0].(*string) = "up-1"
		*dest[1].(*string) = "gcid-1"
		*dest[2].(*string) = "marked_test"
		*dest[3].(*string) = "application/pdf"
		*dest[4].(*string) = "gs://b/o.pdf"
		*dest[5].(*string) = "QUEUED"
		*dest[6].(*[]string) = []string{}
		*dest[7].(*int) = 0
		*dest[8].(*string) = ""
		*dest[9].(*time.Time) = time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
		*dest[12].(*string) = "0190aaaa-bbbb-7ccc-8ddd-000000000009" // goal_id
		*dest[13].(*string) = "0190bbbb-cccc-7ddd-8eee-000000000042" // entry_concept_id
		return nil
	}}
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{row: row}})
	got, err := repo.ScopeForUpload(wuCtx(), "gcid-1", "up-1")
	if err != nil {
		t.Fatalf("ScopeForUpload: %v", err)
	}
	if got.GoalID != "0190aaaa-bbbb-7ccc-8ddd-000000000009" {
		t.Errorf("goal_id = %q; want the scanned goal id", got.GoalID)
	}
	// ADR-238 D2: the entry-concept hint must ride out of the SAME row read, or
	// the bias silently never fires no matter how correct the matcher is.
	if got.EntryConceptID != "0190bbbb-cccc-7ddd-8eee-000000000042" {
		t.Errorf("entry_concept_id = %q; want the scanned entry concept id", got.EntryConceptID)
	}
}

func TestWeaknessUploadRepo_ScopeForUpload_NoGoalOrAbsent(t *testing.T) {
	// Absent upload → (zero Scope, nil) (Get returns nil,nil for ErrNoRows).
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{}})
	got, err := repo.ScopeForUpload(wuCtx(), "gcid-1", "missing")
	if err != nil {
		t.Fatalf("absent upload must be (zero Scope,nil), got err %v", err)
	}
	if got.GoalID != "" || got.EntryConceptID != "" {
		t.Errorf("scope = %+v; want the zero Scope for an absent/non-goal upload", got)
	}
}

func TestWeaknessUploadRepo_ScopeForUpload_RLSError(t *testing.T) {
	// No-tenant ctx → rls.ApplySession fails → the error surfaces (fail-loud).
	repo := NewWeaknessUploadRepo(&wuTx{})
	if _, err := repo.ScopeForUpload(context.Background(), "gcid-1", "up-1"); err == nil {
		t.Error("ScopeForUpload: want RLS error on no-tenant ctx")
	}
}

func TestWeaknessUploadRepo_Insert_FailsLoud(t *testing.T) {
	tx := &wuTx{q: &wuQuerier{execErr: errors.New("boom")}}
	repo := NewWeaknessUploadRepo(tx)
	if err := repo.Insert(wuCtx(), wuSample()); err == nil {
		t.Fatal("expected insert error to propagate")
	}
}

func TestWeaknessUploadRepo_Get_Found(t *testing.T) {
	ts := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	row := wuRow{scan: func(dest ...any) error {
		*dest[0].(*string) = "up-1"
		*dest[1].(*string) = "gcid-1"
		*dest[2].(*string) = "marked_test"
		*dest[3].(*string) = "application/pdf"
		*dest[4].(*string) = "gs://b/o.pdf"
		*dest[5].(*string) = "COMPLETED"
		*dest[6].(*[]string) = []string{"e1", "e2"}
		*dest[7].(*int) = 2
		*dest[8].(*string) = ""
		*dest[9].(*time.Time) = ts.Add(-time.Hour)
		*dest[10].(**time.Time) = &ts
		return nil
	}}
	tx := &wuTx{q: &wuQuerier{row: row}}
	repo := NewWeaknessUploadRepo(tx)

	got, err := repo.Get(wuCtx(), "gcid-1", "up-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Status != wu.StatusCompleted || got.EdgeCount != 2 || len(got.UpsertedEdgeIDs) != 2 {
		t.Errorf("got = %+v", got)
	}
	if got.AnalyzedAt == nil || !got.AnalyzedAt.Equal(ts) {
		t.Errorf("analyzed_at = %v", got.AnalyzedAt)
	}
}

func TestWeaknessUploadRepo_Get_NotFound(t *testing.T) {
	tx := &wuTx{q: &wuQuerier{}} // QueryRow returns ErrNoRows
	repo := NewWeaknessUploadRepo(tx)
	got, err := repo.Get(wuCtx(), "gcid-1", "missing")
	if err != nil {
		t.Fatalf("Get not-found should be nil error, got %v", err)
	}
	if got != nil {
		t.Fatalf("Get not-found should be nil, got %+v", got)
	}
}

func TestWeaknessUploadRepo_MarkCompleted(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	now := time.Date(2026, 6, 10, 14, 0, 0, 0, time.UTC)
	if err := repo.MarkCompleted(wuCtx(), "gcid-1", "up-1", []string{"e1", "e2", "e3"}, now); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	e, ok := tx.q.find("status = 'COMPLETED'")
	if !ok {
		t.Fatal("no COMPLETED update exec")
	}
	// args: upload_id, learner_gcid, edge_ids, count, now
	if e.args[3] != 3 {
		t.Errorf("edge_count arg = %v want 3", e.args[3])
	}
}

func TestWeaknessUploadRepo_MarkCompleted_NilEdges(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	if err := repo.MarkCompleted(wuCtx(), "gcid-1", "up-1", nil, time.Now()); err != nil {
		t.Fatalf("MarkCompleted nil edges: %v", err)
	}
	e, _ := tx.q.find("status = 'COMPLETED'")
	if e.args[3] != 0 {
		t.Errorf("nil edges count = %v want 0", e.args[3])
	}
}

func TestWeaknessUploadRepo_RLSErrors_NoTenant(t *testing.T) {
	// A ctx without a tenant makes rls.ApplySession fail → every method must
	// surface the error (RLS is the first line in each RunInTx).
	repo := NewWeaknessUploadRepo(&wuTx{})
	bg := context.Background()
	if err := repo.Insert(bg, wuSample()); err == nil {
		t.Error("Insert: want RLS error on no-tenant ctx")
	}
	if _, err := repo.Get(bg, "gcid-1", "up-1"); err == nil {
		t.Error("Get: want RLS error on no-tenant ctx")
	}
	if err := repo.MarkCompleted(bg, "gcid-1", "up-1", nil, time.Now()); err == nil {
		t.Error("MarkCompleted: want RLS error on no-tenant ctx")
	}
	if err := repo.MarkFailed(bg, "gcid-1", "up-1", "x", time.Now()); err == nil {
		t.Error("MarkFailed: want RLS error on no-tenant ctx")
	}
}

func TestWeaknessUploadRepo_ExecErrors(t *testing.T) {
	mk := func() *WeaknessUploadRepo {
		return NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{execErr: errors.New("boom")}})
	}
	if err := mk().MarkCompleted(wuCtx(), "gcid-1", "up-1", []string{"e"}, time.Now()); err == nil {
		t.Error("MarkCompleted: want exec error")
	}
	if err := mk().MarkFailed(wuCtx(), "gcid-1", "up-1", "r", time.Now()); err == nil {
		t.Error("MarkFailed: want exec error")
	}
}

func TestWeaknessUploadRepo_Get_ScanError(t *testing.T) {
	row := wuRow{scan: func(...any) error { return errors.New("scan boom") }}
	repo := NewWeaknessUploadRepo(&wuTx{q: &wuQuerier{row: row}})
	if _, err := repo.Get(wuCtx(), "gcid-1", "up-1"); err == nil {
		t.Fatal("Get: want wrapped scan error")
	}
}

func TestWeaknessUploadRepo_MarkFailed(t *testing.T) {
	tx := &wuTx{}
	repo := NewWeaknessUploadRepo(tx)
	if err := repo.MarkFailed(wuCtx(), "gcid-1", "up-1", "analyser timeout", time.Now()); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	e, ok := tx.q.find("status = 'FAILED'")
	if !ok {
		t.Fatal("no FAILED update exec")
	}
	if e.args[2] != "analyser timeout" {
		t.Errorf("reason arg = %v", e.args[2])
	}
}
