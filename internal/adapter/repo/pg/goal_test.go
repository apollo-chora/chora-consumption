// goal_test.go — RLS-contract + SQL-shape + scan-mapping verification for the
// Goal pg adapter (ADR-204 §2). Mirrors learner_profile_test.go: a stub Querier
// captures Exec SQL so we assert the SET LOCAL chora.tenant_id pair lands BEFORE
// the mutation; a richer fake Querier (goalFakeQuerier) returns canned rows so
// the GetByID/ListByLearner scan→domain mapping is exercised. Schema mirrors
// migrations/0051_goals.up.sql.
package pg

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
)

func goalFixture(t *testing.T) *goal.Goal {
	t.Helper()
	ref := "cert:pmp-2026"
	g, err := goal.NewGoal(goal.NewGoalInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, Kind: goal.KindThemeMastery,
		ChoraTargetRef: &ref, ConceptSet: []string{"agile", "risk"},
		NorthStarNote: "pass the real PMP",
		Now:           time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("goalFixture: %v", err)
	}
	return g
}

func TestPGGoalRepo_CreateAppliesRLSAndInserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewGoalRepo(tx)
	if err := repo.Create(withCtx(), goalFixture(t)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + insert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", calls[0])
	}
	if !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q; want SET LOCAL gcid", calls[1])
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO goals") {
		t.Errorf("last = %q; want INSERT INTO goals", last)
	}
	if !contains(last, "mastered_concept_count") {
		t.Errorf("insert = %q; want mastered_concept_count column", last)
	}
}

func TestPGGoalRepo_UpdateAppliesRLSAndUpdatesScoped(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewGoalRepo(tx)
	g := goalFixture(t)
	if err := repo.Update(withCtx(), g); err != nil {
		t.Fatalf("Update: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 || !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("RLS pair missing: %v", calls)
	}
	last := calls[len(calls)-1]
	if !contains(last, "UPDATE goals") {
		t.Errorf("last = %q; want UPDATE goals", last)
	}
	if !contains(last, "mastered_concept_count") {
		t.Errorf("update = %q; want mastered_concept_count column", last)
	}
	// Scoped write — must filter by id AND tenant AND learner (defence in depth).
	if !contains(last, "WHERE id") || !contains(last, "tenant_id") || !contains(last, "learner_gcid") {
		t.Errorf("update not scoped to id/tenant/learner: %q", last)
	}
}

func TestPGGoalRepo_CreateNilIsNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewGoalRepo(tx)
	if err := repo.Create(withCtx(), nil); err != nil {
		t.Fatalf("Create(nil): %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil create should not touch the DB: %v", tx.q.execCalls)
	}
}

func TestPGGoalRepo_ListByLearnerScansLiveRowsNewestFirst(t *testing.T) {
	created := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	ref := "cert:pmp-2026"
	fam := "01970000-0000-7000-b000-000000000001"
	fake := &goalFakeQuerier{rows: &goalFakeRows{rows: []*goalFakeRow{
		{cols: []any{
			"01970000-0000-7000-a000-000000000001", pgTenantID, pgUserGCID,
			"theme_mastery", &ref, []string{"agile", "risk"}, 1, "active", "pass PMP",
			&fam, nil, nil, nil, nil, created, created, nil,
		}},
		{cols: []any{
			"01970000-0000-7000-a000-000000000002", pgTenantID, pgUserGCID,
			"curiosity", nil, []string{}, 0, "active", "",
			nil, nil, nil, nil, nil, created, created, nil,
		}},
	}}}
	repo := NewGoalRepo(&stubTxRunnerWith{q: fake})

	out, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d goals; want 2", len(out))
	}
	// First row: full credential goal with target + companion bond.
	g0 := out[0]
	if g0.Kind != goal.KindThemeMastery || g0.Status != goal.StatusActive {
		t.Errorf("g0 kind/status = %q/%q", g0.Kind, g0.Status)
	}
	if g0.ChoraTargetRef == nil || *g0.ChoraTargetRef != ref {
		t.Errorf("g0 target = %v; want %q", g0.ChoraTargetRef, ref)
	}
	if g0.AttachedCompanionID == nil || *g0.AttachedCompanionID != fam {
		t.Errorf("g0 companion = %v; want %q", g0.AttachedCompanionID, fam)
	}
	if len(g0.ConceptSet) != 2 {
		t.Errorf("g0 concept_set = %v", g0.ConceptSet)
	}
	// Second row: open curiosity goal — nil target + nil companion.
	g1 := out[1]
	if g1.Kind != goal.KindCuriosity || g1.ChoraTargetRef != nil || g1.AttachedCompanionID != nil {
		t.Errorf("g1 not an open curiosity goal: %+v", g1)
	}
	// mastered_concept_count maps from the row (subscriber high-water mark).
	if g0.MasteredConceptCount != 1 {
		t.Errorf("g0 mastered_concept_count = %d; want 1", g0.MasteredConceptCount)
	}
	if g1.MasteredConceptCount != 0 {
		t.Errorf("g1 mastered_concept_count = %d; want 0", g1.MasteredConceptCount)
	}
	// SQL shape: live-only, learner-scoped, newest-first, with the new column.
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM goals") || !contains(q, "deleted_at IS NULL") || !contains(q, "ORDER BY created_at DESC") {
		t.Errorf("list SQL = %q; want live-only newest-first select", q)
	}
	if !contains(q, "mastered_concept_count") {
		t.Errorf("list SQL = %q; want mastered_concept_count selected", q)
	}
	// RLS applied before the read.
	if len(fake.sqls) < 3 || !contains(fake.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing before list: %v", fake.sqls)
	}
}

func TestPGGoalRepo_ListEmptyOnNilStub(t *testing.T) {
	repo := NewGoalRepo(&stubTxRunner{})
	out, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListByLearner: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("nil-stub list should be empty; got %d", len(out))
	}
}

func TestPGGoalRepo_GetByIDScansRow(t *testing.T) {
	created := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	ref := "course:intro-go"
	fake := &goalFakeQuerier{row: &goalFakeRow{cols: []any{
		"01970000-0000-7000-a000-000000000003", pgTenantID, pgUserGCID,
		"theme_mastery", &ref, []string{"go"}, 2, "achieved", "ship it",
		nil, nil, nil, nil, nil, created, created, nil,
	}}}
	repo := NewGoalRepo(&stubTxRunnerWith{q: fake})

	g, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, "01970000-0000-7000-a000-000000000003")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if g == nil {
		t.Fatal("GetByID returned nil for an existing row")
	}
	if g.Kind != goal.KindThemeMastery || g.Status != goal.StatusAchieved {
		t.Errorf("kind/status = %q/%q", g.Kind, g.Status)
	}
	if g.ChoraTargetRef == nil || *g.ChoraTargetRef != ref {
		t.Errorf("target = %v", g.ChoraTargetRef)
	}
	if g.MasteredConceptCount != 2 {
		t.Errorf("mastered_concept_count = %d; want 2", g.MasteredConceptCount)
	}
	q := fake.sqls[len(fake.sqls)-1]
	if !contains(q, "FROM goals") || !contains(q, "WHERE id") || !contains(q, "deleted_at IS NULL") {
		t.Errorf("get SQL = %q; want by-id live select", q)
	}
}

func TestPGGoalRepo_GetByIDNotFound(t *testing.T) {
	// nil-row stub → not found, (nil, nil).
	repo := NewGoalRepo(&stubTxRunner{})
	g, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, "missing")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if g != nil {
		t.Errorf("missing row should yield nil; got %+v", g)
	}
	// ErrNoRows scan → also (nil, nil).
	fake := &goalFakeQuerier{row: &goalFakeRow{err: ErrNoRows}}
	repo2 := NewGoalRepo(&stubTxRunnerWith{q: fake})
	g2, err := repo2.GetByID(withCtx(), pgTenantID, pgUserGCID, "missing")
	if err != nil {
		t.Fatalf("GetByID(ErrNoRows): %v", err)
	}
	if g2 != nil {
		t.Errorf("ErrNoRows should yield nil; got %+v", g2)
	}
}

func TestPGGoalRepo_UpdateNilIsNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewGoalRepo(tx)
	if err := repo.Update(withCtx(), nil); err != nil {
		t.Fatalf("Update(nil): %v", err)
	}
	if tx.q != nil && len(tx.q.execCalls) != 0 {
		t.Errorf("nil update should not touch the DB: %v", tx.q.execCalls)
	}
}

// Fail-loud: a DB error on the mutation/read bubbles up (never swallowed).
func TestPGGoalRepo_ErrorsBubble(t *testing.T) {
	boom := context.DeadlineExceeded
	t.Run("create exec error", func(t *testing.T) {
		repo := NewGoalRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: boom}})
		if err := repo.Create(withCtx(), goalFixture(t)); err == nil {
			t.Fatal("want create error to bubble")
		}
	})
	t.Run("update exec error", func(t *testing.T) {
		repo := NewGoalRepo(&stubTxRunnerWith{q: &goalFakeQuerier{execErr: boom}})
		if err := repo.Update(withCtx(), goalFixture(t)); err == nil {
			t.Fatal("want update error to bubble")
		}
	})
	t.Run("list query error", func(t *testing.T) {
		repo := NewGoalRepo(&stubTxRunnerWith{q: &goalFakeQuerier{queryErr: boom}})
		if _, err := repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID); err == nil {
			t.Fatal("want list error to bubble")
		}
	})
	t.Run("get scan error", func(t *testing.T) {
		repo := NewGoalRepo(&stubTxRunnerWith{q: &goalFakeQuerier{row: &goalFakeRow{err: boom}}})
		if _, err := repo.GetByID(withCtx(), pgTenantID, pgUserGCID, "x"); err == nil {
			t.Fatal("want get scan error to bubble")
		}
	})
}

func TestPGGoalRepo_SatisfiesPort(t *testing.T) {
	var _ goal.Repository = (*GoalRepo)(nil)
}

// --- fakes: a Querier that returns canned rows (the basic stubQuerier in
// user_kg_test.go returns nil for Query/QueryRow, which only exercises the
// empty/not-found paths). ---

type goalFakeRow struct {
	cols []any
	err  error
}

func (r *goalFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.cols) {
		return fmt.Errorf("goalFakeRow: scan arity got %d want %d", len(dest), len(r.cols))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			p2, ok := r.cols[i].(string)
			if !ok {
				return fmt.Errorf("col %d: want string, got %T", i, r.cols[i])
			}
			*p = p2
		case **string:
			if r.cols[i] == nil {
				*p = nil
			} else {
				*p = r.cols[i].(*string)
			}
		case *[]string:
			*p = r.cols[i].([]string)
		case *int:
			p2, ok := r.cols[i].(int)
			if !ok {
				return fmt.Errorf("col %d: want int, got %T", i, r.cols[i])
			}
			*p = p2
		case *time.Time:
			*p = r.cols[i].(time.Time)
		case **time.Time:
			if r.cols[i] == nil {
				*p = nil
			} else {
				*p = r.cols[i].(*time.Time)
			}
		case *[]time.Time:
			*p = r.cols[i].([]time.Time)
		default:
			return fmt.Errorf("goalFakeRow: unhandled dest type %T at %d", d, i)
		}
	}
	return nil
}

type goalFakeRows struct {
	rows []*goalFakeRow
	i    int
}

func (r *goalFakeRows) Next() bool          { r.i++; return r.i <= len(r.rows) }
func (r *goalFakeRows) Scan(d ...any) error { return r.rows[r.i-1].Scan(d...) }
func (r *goalFakeRows) Close() error        { return nil }
func (r *goalFakeRows) Err() error          { return nil }

type goalFakeQuerier struct {
	sqls     []string
	row      *goalFakeRow
	rows     *goalFakeRows
	execErr  error // returned by Exec AFTER the RLS SET LOCALs land
	queryErr error
}

func (q *goalFakeQuerier) Exec(_ context.Context, sql string, _ ...any) (rls.CommandTag, error) {
	q.sqls = append(q.sqls, sql)
	// Let the two rls.ApplySession SET LOCALs through; fail the domain mutation.
	if q.execErr != nil && !contains(sql, "SET LOCAL") {
		return rls.CommandTag{}, q.execErr
	}
	return rls.CommandTag{}, nil
}
func (q *goalFakeQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	q.sqls = append(q.sqls, sql)
	if q.row == nil {
		return nil
	}
	return q.row
}
func (q *goalFakeQuerier) Query(_ context.Context, sql string, _ ...any) (Rows, error) {
	q.sqls = append(q.sqls, sql)
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	if q.rows == nil {
		return nil, nil
	}
	return q.rows, nil
}

// TestPGGoalRepo_SQLCarriesCampaignColumns pins the WS-C1 (ADR-227) goal-side
// campaign state onto every SQL surface: the D11 focus pointer and the D3
// seal history must round-trip through insert/update/select or focus/seal
// silently vanish on the next write.
func TestPGGoalRepo_SQLCarriesCampaignColumns(t *testing.T) {
	for name, sql := range map[string]string{
		"insert": insertGoalSQL,
		"update": updateGoalSQL,
		"get":    getGoalSQL,
		"list":   listGoalsSQL,
	} {
		if !contains(sql, "focus_concept_id") {
			t.Errorf("%s SQL missing focus_concept_id", name)
		}
		if !contains(sql, "campaign_sealed_at") {
			t.Errorf("%s SQL missing campaign_sealed_at", name)
		}
	}
}
