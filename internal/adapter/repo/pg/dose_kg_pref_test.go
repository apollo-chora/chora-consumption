// dose_kg_pref_test.go — per-KG daily-dose preference (dose_pref.Repository) pg
// adapter: RLS contract + SQL-shape + arg-binding + upsert/list/excluded scan
// logic, verified against in-memory stubs (no live Postgres). PREPARE-time parse
// coverage for the SQL consts lives in dose_kg_pref_prepare_smoke_integration_test.go
// (build tag `integration`, gated on CHORA_TEST_DSN).
//
// Reuses package-scope helpers from user_kg_test.go: withCtx() (sets
// chora.tenant_id + chora.user_gcid on ctx), pgTenantID, pgUserGCID, contains().
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	dp "github.com/apollo-chora/chora-consumption/internal/domain/dose_pref"
)

// ---- stubs (configurable Query rows + QueryRow RETURNING row) ----

func dpScanInto(row []any, dest []any) error {
	if len(dest) != len(row) {
		return errors.New("dpStub: argc mismatch")
	}
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			if v, ok := row[i].(string); ok {
				*tgt = v
			}
		case *bool:
			if v, ok := row[i].(bool); ok {
				*tgt = v
			}
		case *time.Time:
			if v, ok := row[i].(time.Time); ok {
				*tgt = v
			}
		}
	}
	return nil
}

type dpStubRows struct {
	rows [][]any
	idx  int
}

func (m *dpStubRows) Next() bool {
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}
func (m *dpStubRows) Scan(dest ...any) error { return dpScanInto(m.rows[m.idx-1], dest) }
func (m *dpStubRows) Close() error           { return nil }
func (m *dpStubRows) Err() error             { return nil }

type dpStubRow struct {
	vals []any
	err  error
}

func (r *dpStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return dpScanInto(r.vals, dest)
}

type dpStubQuerier struct {
	execCalls  []string
	queryCalls []string
	queryArgs  [][]any
	rowCalls   []string
	rowArgs    [][]any
	rows       *dpStubRows // returned by Query (list / excluded)
	row        *dpStubRow  // returned by QueryRow (upsert RETURNING)
	queryErr   error       // when set, Query returns it (RLS uses Exec, so safe)
}

func (s *dpStubQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	return rls.CommandTag{RowsAffected: 1}, nil
}
func (s *dpStubQuerier) QueryRow(_ context.Context, sql string, args ...any) Row {
	s.rowCalls = append(s.rowCalls, sql)
	s.rowArgs = append(s.rowArgs, args)
	if s.row != nil {
		return s.row
	}
	return &dpStubRow{err: ErrNoRows}
}
func (s *dpStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	s.queryCalls = append(s.queryCalls, sql)
	s.queryArgs = append(s.queryArgs, args)
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.rows != nil {
		return s.rows, nil
	}
	return &dpStubRows{}, nil
}

type dpStubTxRunner struct{ q *dpStubQuerier }

func (r *dpStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &dpStubQuerier{}
	}
	return fn(ctx, r.q)
}

const (
	dpMap  = "01970000-0000-7000-a000-000000000001"
	dpMap2 = "01970000-0000-7000-a000-000000000002"
)

func dpNow() time.Time { return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC) }

// ---- Set: upsert (RLS-first + RETURNING scan + stamp) ----

func TestDoseKGPrefRepo_Set_UpsertsAppliesRLSAndReturnsRow(t *testing.T) {
	stub := &dpStubQuerier{row: &dpStubRow{vals: []any{"row-1", dpMap, false, dpNow(), dpNow()}}}
	tx := &dpStubTxRunner{q: stub}
	repo := NewDoseKGPrefRepo(tx)

	got, err := repo.Set(withCtx(), pgTenantID, pgUserGCID, dpMap, false, dpNow())
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	// RLS SET LOCAL pair must land first (both via Exec).
	if len(stub.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %d execs", len(stub.execCalls))
	}
	if !contains(stub.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("exec[0] missing tenant SET LOCAL: %q", stub.execCalls[0])
	}
	if !contains(stub.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("exec[1] missing gcid SET LOCAL: %q", stub.execCalls[1])
	}
	// Upsert issued via QueryRow (RETURNING), with the right SQL shape.
	if len(stub.rowCalls) != 1 {
		t.Fatalf("expected one upsert QueryRow; got %v", stub.rowCalls)
	}
	up := stub.rowCalls[0]
	for _, frag := range []string{"INSERT INTO learner_dose_kg_prefs",
		"ON CONFLICT (tenant_id, learner_gcid, map_id)", "WHERE deleted_at IS NULL",
		"DO UPDATE", "RETURNING"} {
		if !contains(up, frag) {
			t.Errorf("upsert SQL missing %q in:\n%s", frag, up)
		}
	}
	// Args: minted UUIDv7 id + tenant + gcid + map + bool polarity all bound.
	var sawID, sawMap, sawFalse bool
	for _, a := range stub.rowArgs[0] {
		switch v := a.(type) {
		case string:
			if len(v) == 36 && v[14] == '7' {
				sawID = true
			}
			if v == dpMap {
				sawMap = true
			}
		case bool:
			if v == false {
				sawFalse = true
			}
		}
	}
	if !sawID {
		t.Errorf("upsert args missing a minted UUIDv7 id; got %#v", stub.rowArgs[0])
	}
	if !sawMap {
		t.Errorf("upsert args missing map_id; got %#v", stub.rowArgs[0])
	}
	if !sawFalse {
		t.Errorf("upsert args missing included=false; got %#v", stub.rowArgs[0])
	}
	// Returned row: RETURNING scan + tenant/gcid stamped from the RLS-implicit scope.
	if got.ID != "row-1" || got.MapID != dpMap || got.Included != false {
		t.Errorf("returned row mismatch: %+v", got)
	}
	if got.TenantID != pgTenantID || got.LearnerGCID != pgUserGCID {
		t.Errorf("scope not stamped on result: %+v", got)
	}
	if !got.CreatedAt.Equal(dpNow()) || !got.UpdatedAt.Equal(dpNow()) {
		t.Errorf("timestamps not scanned: %+v", got)
	}
}

func TestDoseKGPrefRepo_Set_RejectsInvalidInput(t *testing.T) {
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{})
	if _, err := repo.Set(withCtx(), pgTenantID, pgUserGCID, "  ", true, dpNow()); !errors.Is(err, dp.ErrInvalid) {
		t.Fatalf("err = %v; want dp.ErrInvalid for blank map_id", err)
	}
}

func TestDoseKGPrefRepo_Set_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{})
	if _, err := repo.Set(context.Background(), pgTenantID, pgUserGCID, dpMap, true, dpNow()); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestDoseKGPrefRepo_Set_PropagatesUpsertError(t *testing.T) {
	stub := &dpStubQuerier{row: &dpStubRow{err: errors.New("upsert boom")}}
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{q: stub})
	if _, err := repo.Set(withCtx(), pgTenantID, pgUserGCID, dpMap, true, dpNow()); err == nil {
		t.Error("expected upsert RETURNING scan error to propagate")
	}
}

// ---- List ----

func TestDoseKGPrefRepo_List_ScansStampsAndFilters(t *testing.T) {
	stub := &dpStubQuerier{rows: &dpStubRows{rows: [][]any{
		{"id-1", dpMap, false, dpNow(), dpNow()},
		{"id-2", dpMap2, true, dpNow(), dpNow()},
	}}}
	tx := &dpStubTxRunner{q: stub}
	repo := NewDoseKGPrefRepo(tx)

	out, err := repo.List(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	q := stub.queryCalls[0]
	for _, frag := range []string{"learner_gcid = $1", "deleted_at IS NULL", "learner_dose_kg_prefs"} {
		if !contains(q, frag) {
			t.Errorf("list query missing %q in:\n%s", frag, q)
		}
	}
	if len(out) != 2 {
		t.Fatalf("got %d prefs; want 2", len(out))
	}
	if out[0].ID != "id-1" || out[0].MapID != dpMap || out[0].Included != false {
		t.Errorf("row 0 scan mismatch: %+v", out[0])
	}
	if out[1].Included != true {
		t.Errorf("row 1 included polarity mismatch: %+v", out[1])
	}
	if out[0].TenantID != pgTenantID || out[0].LearnerGCID != pgUserGCID {
		t.Errorf("scope not stamped on result: %+v", out[0])
	}
}

func TestDoseKGPrefRepo_List_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{})
	if _, err := repo.List(context.Background(), pgTenantID, pgUserGCID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestDoseKGPrefRepo_List_PropagatesQueryError(t *testing.T) {
	stub := &dpStubQuerier{queryErr: errors.New("list boom")}
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{q: stub})
	if _, err := repo.List(withCtx(), pgTenantID, pgUserGCID); err == nil {
		t.Error("expected list-query error to propagate")
	}
}

// ---- ExcludedMapIDs ----

func TestDoseKGPrefRepo_ExcludedMapIDs_BuildsSetAndFilters(t *testing.T) {
	stub := &dpStubQuerier{rows: &dpStubRows{rows: [][]any{
		{dpMap},
		{dpMap2},
	}}}
	tx := &dpStubTxRunner{q: stub}
	repo := NewDoseKGPrefRepo(tx)

	out, err := repo.ExcludedMapIDs(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ExcludedMapIDs: %v", err)
	}
	q := stub.queryCalls[0]
	for _, frag := range []string{"learner_gcid = $1", "included = false", "deleted_at IS NULL"} {
		if !contains(q, frag) {
			t.Errorf("excluded query missing %q in:\n%s", frag, q)
		}
	}
	if len(out) != 2 || !out[dpMap] || !out[dpMap2] {
		t.Errorf("excluded set wrong: %#v", out)
	}
}

func TestDoseKGPrefRepo_ExcludedMapIDs_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{})
	if _, err := repo.ExcludedMapIDs(context.Background(), pgTenantID, pgUserGCID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestDoseKGPrefRepo_ExcludedMapIDs_PropagatesQueryError(t *testing.T) {
	stub := &dpStubQuerier{queryErr: errors.New("excluded boom")}
	repo := NewDoseKGPrefRepo(&dpStubTxRunner{q: stub})
	if _, err := repo.ExcludedMapIDs(withCtx(), pgTenantID, pgUserGCID); err == nil {
		t.Error("expected excluded-query error to propagate")
	}
}

// ---- SQL template guard ----

func TestDoseKGPref_SQLTemplates_NonEmpty(t *testing.T) {
	for name, s := range map[string]string{
		"upsert":   upsertDoseKGPrefSQL,
		"list":     listDoseKGPrefsSQL,
		"excluded": excludedDoseKGMapIDsSQL,
	} {
		if s == "" {
			t.Errorf("%s SQL template empty", name)
		}
	}
}

// compile-time port assertion lives in the impl file.
var _ dp.Repository = (*DoseKGPrefRepo)(nil)
