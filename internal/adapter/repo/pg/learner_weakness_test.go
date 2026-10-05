// learner_weakness_test.go — Growth-Edge (LearnerWeakness) pg adapter: RLS
// contract + SQL-shape + arg-binding + the dedup-fold branch logic, verified
// against in-memory stubs (no live pgvector). Production atomic behaviour is
// exercised under integration_test.go (build tag `integration`).
//
// Reuses package-scope test helpers from user_kg_test.go: withCtx() (sets
// chora.tenant_id + chora.user_gcid on ctx), pgTenantID, pgUserGCID, contains().
package pg

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	lw "github.com/apollo-chora/chora-consumption/internal/domain/learner_weakness"
)

func base64RawURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ---- stubs (configurable nearest Query + full-load QueryRow) ----

func lwScanInto(row []any, dest []any) error {
	if len(dest) != len(row) {
		return errors.New("lwStub: argc mismatch")
	}
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			if v, ok := row[i].(string); ok {
				*tgt = v
			}
		case *float64:
			if v, ok := row[i].(float64); ok {
				*tgt = v
			}
		case *time.Time:
			if v, ok := row[i].(time.Time); ok {
				*tgt = v
			}
		case *[]string:
			if v, ok := row[i].([]string); ok {
				*tgt = v
			}
		}
	}
	return nil
}

type lwStubRows struct {
	rows [][]any
	idx  int
}

func (m *lwStubRows) Next() bool {
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}
func (m *lwStubRows) Scan(dest ...any) error { return lwScanInto(m.rows[m.idx-1], dest) }
func (m *lwStubRows) Close() error           { return nil }
func (m *lwStubRows) Err() error             { return nil }

type lwStubRow struct {
	vals []any
	err  error
}

func (r *lwStubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return lwScanInto(r.vals, dest)
}

type lwStubQuerier struct {
	execCalls  []string
	execArgs   [][]any
	queryCalls []string
	queryArgs  [][]any
	rowCalls   []string
	rows       *lwStubRows // returned by Query (nearest / list)
	row        *lwStubRow  // returned by QueryRow (full-load / get)
	queryErr   error       // when set, Query returns it (RLS uses Exec, so safe)
	failExec   string      // when an Exec SQL contains this, that Exec errors (RLS execs unaffected)
}

func (s *lwStubQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	s.execArgs = append(s.execArgs, args)
	if s.failExec != "" && contains(sql, s.failExec) {
		return rls.CommandTag{}, errors.New("lwStub: exec boom")
	}
	return rls.CommandTag{RowsAffected: 1}, nil
}
func (s *lwStubQuerier) QueryRow(_ context.Context, sql string, _ ...any) Row {
	s.rowCalls = append(s.rowCalls, sql)
	if s.row != nil {
		return s.row
	}
	return &lwStubRow{err: ErrNoRows}
}
func (s *lwStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	s.queryCalls = append(s.queryCalls, sql)
	s.queryArgs = append(s.queryArgs, args)
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.rows != nil {
		return s.rows, nil
	}
	return &lwStubRows{}, nil
}

type lwStubTxRunner struct{ q *lwStubQuerier }

func (r *lwStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &lwStubQuerier{}
	}
	return fn(ctx, r.q)
}

const (
	lwTenant = "01970000-0000-7000-8000-000000000001"
	lwGCID   = "01970000-0000-7000-9000-000000000001"
)

func lwNow() time.Time { return time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC) }

// fullRow mirrors getLearnerWeaknessSQL column order. ADR-238 appended
// target_concept_id as the LAST projected column, so the stub row carries a
// trailing "" (UNMATCHED) to keep the scan's argc aligned; use
// lwFullRowWithTarget when a test needs a resolved target_concept_id.
func lwFullRow(id, key, label, category, topic string, tags []string, strength float64,
	sources []string, descriptorJSON string, cached []string, status string,
	firstSeen, lastEvid time.Time) []any {
	return lwFullRowWithTarget(id, key, label, category, topic, tags, strength, sources,
		descriptorJSON, cached, status, firstSeen, lastEvid, "")
}

// lwFullRowWithTarget mirrors getLearnerWeaknessSQL column order INCLUDING the
// trailing ADR-238 target_concept_id (COALESCE'd to "" for a pre-0098/UNMATCHED row).
func lwFullRowWithTarget(id, key, label, category, topic string, tags []string, strength float64,
	sources []string, descriptorJSON string, cached []string, status string,
	firstSeen, lastEvid time.Time, targetConceptID string) []any {
	return []any{id, key, label, category, topic, tags, strength, sources,
		descriptorJSON, cached, status, firstSeen, lastEvid, targetConceptID}
}

func lwInput() lw.UpsertInput {
	return lw.UpsertInput{
		TenantID:     pgTenantID,
		LearnerGCID:  pgUserGCID,
		ConceptLabel: "Causes of Riverine Flooding",
		Embedding:    []float32{0.1, -0.2, 0.3},
		Category:     "physical-geography",
		Tags:         []string{"flooding"},
		Strength:     0.7,
		Source:       lw.SourceExplicit,
		Now:          lwNow(),
	}
}

// ---- Upsert: insert branch (no nearest match) ----

func TestLearnerWeaknessRepo_Upsert_NoNearest_Inserts(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)

	res, err := repo.Upsert(withCtx(), lwInput())
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if res.Merged {
		t.Error("Merged = true; want false (no nearest match)")
	}
	if res.ID == "" {
		t.Error("expected a new edge id")
	}
	// RLS pair first.
	if len(tx.q.execCalls) < 3 {
		t.Fatalf("expected RLS pair + INSERT exec; got %d execs", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("exec[0] missing tenant SET LOCAL: %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("exec[1] missing gcid SET LOCAL: %q", tx.q.execCalls[1])
	}
	if len(tx.q.queryCalls) != 1 || !contains(tx.q.queryCalls[0], "<=>") {
		t.Errorf("expected one cosine nearest Query; got %v", tx.q.queryCalls)
	}
	ins := tx.q.execCalls[2]
	if !contains(ins, "INSERT INTO learner_weakness") || !contains(ins, "::vector") || !contains(ins, "::jsonb") {
		t.Errorf("INSERT shape wrong: %q", ins)
	}
	// Vector literal + status active bound.
	var sawVec, sawActive bool
	for _, a := range tx.q.execArgs[2] {
		if s, ok := a.(string); ok {
			if s == "[0.1,-0.2,0.3]" {
				sawVec = true
			}
			if s == "active" {
				sawActive = true
			}
		}
	}
	if !sawVec {
		t.Errorf("INSERT args missing vector literal; got %#v", tx.q.execArgs[2])
	}
	if !sawActive {
		t.Errorf("INSERT args missing status active; got %#v", tx.q.execArgs[2])
	}
}

// ---- Upsert: merge branch (nearest within threshold) ----

func TestLearnerWeaknessRepo_Upsert_NearestWithinThreshold_Merges(t *testing.T) {
	stub := &lwStubQuerier{
		rows: &lwStubRows{rows: [][]any{{"existing-id", float64(0.05)}}}, // id, distance
		row: &lwStubRow{vals: lwFullRow(
			"existing-id", "causes-of-riverine-flooding", "Causes of Riverine Flooding",
			"physical-geography", "", []string{"flooding"}, 0.6, []string{"explicit"},
			"{}", []string{}, "active", lwNow().Add(-48*time.Hour), lwNow().Add(-48*time.Hour),
		)},
	}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	in := lwInput()
	in.Strength = 0.9
	in.Source = lw.SourceDerived
	res, err := repo.Upsert(withCtx(), in)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !res.Merged || res.ID != "existing-id" {
		t.Errorf("res = %+v; want Merged=true id=existing-id", res)
	}
	// Must UPDATE (not INSERT) — find an UPDATE exec.
	var updIdx = -1
	for i, c := range stub.execCalls {
		if contains(c, "UPDATE learner_weakness") {
			updIdx = i
		}
		if contains(c, "INSERT INTO learner_weakness") {
			t.Errorf("merge path must not INSERT: %q", c)
		}
	}
	if updIdx < 0 {
		t.Fatalf("no UPDATE exec found; calls=%v", stub.execCalls)
	}
	// Strength take-max -> 0.9 bound on UPDATE.
	var sawMax bool
	for _, a := range stub.execArgs[updIdx] {
		if f, ok := a.(float64); ok && f == 0.9 {
			sawMax = true
		}
	}
	if !sawMax {
		t.Errorf("UPDATE args missing take-max strength 0.9; got %#v", stub.execArgs[updIdx])
	}
}

// ---- Upsert: nearest beyond threshold -> sibling insert ----

func TestLearnerWeaknessRepo_Upsert_NearestBeyondThreshold_Inserts(t *testing.T) {
	stub := &lwStubQuerier{
		rows: &lwStubRows{rows: [][]any{{"other-id", float64(0.5)}}}, // distance 0.5 > 0.1
	}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	res, err := repo.Upsert(withCtx(), lwInput())
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if res.Merged {
		t.Error("Merged = true; want false (nearest beyond threshold)")
	}
	if len(stub.rowCalls) != 0 {
		t.Errorf("beyond-threshold must NOT full-load; rowCalls=%v", stub.rowCalls)
	}
	var sawInsert bool
	for _, c := range stub.execCalls {
		if contains(c, "INSERT INTO learner_weakness") {
			sawInsert = true
		}
	}
	if !sawInsert {
		t.Error("expected INSERT on sibling path")
	}
}

// ---- Upsert: target_concept_id round-trips (ADR-238 goal-scoped Diagnose) ----

func TestLearnerWeaknessRepo_Upsert_Insert_PersistsTargetConceptID(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)

	in := lwInput()
	in.TargetConceptID = "01970000-0000-7000-b000-000000000001"
	if _, err := repo.Upsert(withCtx(), in); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !contains(insertLearnerWeaknessSQL, "target_concept_id") {
		t.Errorf("INSERT SQL missing target_concept_id column:\n%s", insertLearnerWeaknessSQL)
	}
	// Find the INSERT exec + assert the target_concept_id is bound as its LAST arg.
	var insArgs []any
	for i, c := range tx.q.execCalls {
		if contains(c, "INSERT INTO learner_weakness") {
			insArgs = tx.q.execArgs[i]
		}
	}
	if insArgs == nil {
		t.Fatalf("no INSERT exec; calls=%v", tx.q.execCalls)
	}
	if last := insArgs[len(insArgs)-1]; last != "01970000-0000-7000-b000-000000000001" {
		t.Errorf("INSERT last arg = %v; want the target_concept_id", last)
	}
}

func TestLearnerWeaknessRepo_Upsert_Merge_PersistsTargetConceptID(t *testing.T) {
	stub := &lwStubQuerier{
		rows: &lwStubRows{rows: [][]any{{"existing-id", float64(0.05)}}}, // id, distance (within threshold)
		row: &lwStubRow{vals: lwFullRow(
			"existing-id", "causes-of-riverine-flooding", "Causes of Riverine Flooding",
			"physical-geography", "", []string{"flooding"}, 0.6, []string{"explicit"},
			"{}", []string{}, "active", lwNow().Add(-48*time.Hour), lwNow().Add(-48*time.Hour),
		)},
	}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	in := lwInput()
	in.TargetConceptID = "01970000-0000-7000-b000-000000000002"
	if _, err := repo.Upsert(withCtx(), in); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !contains(updateLearnerWeaknessSQL, "target_concept_id") {
		t.Errorf("UPDATE SQL missing target_concept_id column:\n%s", updateLearnerWeaknessSQL)
	}
	// Find the UPDATE exec + assert the merged target_concept_id is its LAST arg
	// (the existing edge had an empty target_concept_id, so Merge fills it).
	var updArgs []any
	for i, c := range stub.execCalls {
		if contains(c, "UPDATE learner_weakness") && contains(c, "target_concept_id") {
			updArgs = stub.execArgs[i]
		}
	}
	if updArgs == nil {
		t.Fatalf("no UPDATE exec with target_concept_id; calls=%v", stub.execCalls)
	}
	if last := updArgs[len(updArgs)-1]; last != "01970000-0000-7000-b000-000000000002" {
		t.Errorf("UPDATE last arg = %v; want the merged target_concept_id", last)
	}
}

func TestLearnerWeaknessRepo_Get_ScansTargetConceptID(t *testing.T) {
	stub := &lwStubQuerier{row: &lwStubRow{vals: lwFullRowWithTarget(
		"id-9", "photosynthesis", "Photosynthesis", "biology", "", []string{"plants"}, 0.4,
		[]string{"derived"}, "{}", []string{}, "active", lwNow(), lwNow(),
		"01970000-0000-7000-b000-0000000000aa")}}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	got, err := repo.Get(withCtx(), pgUserGCID, "id-9")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.TargetConceptID != "01970000-0000-7000-b000-0000000000aa" {
		t.Errorf("target_concept_id not scanned: %+v", got)
	}
}

func TestLearnerWeaknessRepo_Upsert_RejectsInvalidInput(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{})
	in := lwInput()
	in.ConceptLabel = ""
	if _, err := repo.Upsert(withCtx(), in); err == nil {
		t.Error("expected validation error for blank label")
	}
}

func TestLearnerWeaknessRepo_Upsert_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{})
	_, err := repo.Upsert(context.Background(), lwInput())
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ---- List ----

func TestLearnerWeaknessRepo_List_BuildsFiltersAndScans(t *testing.T) {
	desc := `{"summary":"confuses fluvial vs pluvial","misconceptions":["all flooding is rainfall"]}`
	stub := &lwStubQuerier{
		rows: &lwStubRows{rows: [][]any{
			lwFullRow("id-1", "riverine-flood-causes", "Riverine flood causes", "physical-geography", "topic-1",
				[]string{"flooding"}, 0.8, []string{"explicit", "derived"}, desc, []string{"atom-1"}, "active", lwNow(), lwNow()),
		}},
	}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	res, err := repo.List(withCtx(), lw.ListQuery{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		Category: "physical-geography", Tags: []string{"flooding"}, TopicID: "01970000-0000-7000-a000-000000000001",
		MinStrength: 0.2, IncludeGrown: false, Sort: lw.SortStrengthDesc, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	q := stub.queryCalls[0]
	for _, frag := range []string{"learner_gcid = $1", "deleted_at IS NULL", "status <> 'grown'",
		"category = $", "topic_id = $", "strength >= $", "tags && $", "ORDER BY strength DESC"} {
		if !contains(q, frag) {
			t.Errorf("list query missing %q in:\n%s", frag, q)
		}
	}
	if len(res.Items) != 1 {
		t.Fatalf("got %d items; want 1", len(res.Items))
	}
	got := res.Items[0]
	if got.ID != "id-1" || got.Category != "physical-geography" || got.TopicID != "topic-1" {
		t.Errorf("scan mismatch: %+v", got)
	}
	if len(got.Sources) != 2 || got.Sources[0] != lw.SourceExplicit {
		t.Errorf("sources scan = %v", got.Sources)
	}
	if got.Descriptor.Summary != "confuses fluvial vs pluvial" || len(got.Descriptor.Misconceptions) != 1 {
		t.Errorf("descriptor not parsed: %+v", got.Descriptor)
	}
	if len(got.CachedDrillAtomIDs) != 1 || got.CachedDrillAtomIDs[0] != "atom-1" {
		t.Errorf("cached drill atoms scan = %v", got.CachedDrillAtomIDs)
	}
	if got.LearnerGCID != pgUserGCID {
		t.Errorf("learner_gcid not set on result: %q", got.LearnerGCID)
	}
}

func TestLearnerWeaknessRepo_List_IncludeGrown_DropsStatusFilter(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)
	_, _ = repo.List(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID, IncludeGrown: true})
	if contains(tx.q.queryCalls[0], "status <> 'grown'") {
		t.Errorf("include_grown must drop the status filter: %s", tx.q.queryCalls[0])
	}
}

func TestLearnerWeaknessRepo_List_SortVariants(t *testing.T) {
	cases := map[lw.ListSort]string{
		lw.SortLastEvidencedDesc: "ORDER BY last_evidenced_at DESC",
		lw.SortFirstSeenDesc:     "ORDER BY first_seen_at DESC",
		"":                       "ORDER BY strength DESC", // default
	}
	for sort, want := range cases {
		tx := &lwStubTxRunner{}
		repo := NewLearnerWeaknessRepo(tx)
		_, _ = repo.List(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID, Sort: sort})
		if !contains(tx.q.queryCalls[0], want) {
			t.Errorf("sort %q: query missing %q in:\n%s", sort, want, tx.q.queryCalls[0])
		}
	}
}

func TestLearnerWeaknessRepo_List_Paginates(t *testing.T) {
	// PageSize 2; stub returns 3 rows (pageSize+1) -> trimmed + next token.
	mk := func(id string) []any {
		return lwFullRow(id, id, id, "", "", []string{}, 0.5, []string{"derived"}, "{}", []string{}, "active", lwNow(), lwNow())
	}
	stub := &lwStubQuerier{rows: &lwStubRows{rows: [][]any{mk("a"), mk("b"), mk("c")}}}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	res, err := repo.List(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID, PageSize: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Items) != 2 {
		t.Errorf("got %d items; want trimmed to page_size 2", len(res.Items))
	}
	if res.NextPageToken == "" {
		t.Error("expected a next_page_token when an extra row was fetched")
	}
	// Feed the token back -> OFFSET arg advances to 2.
	stub2 := &lwStubQuerier{}
	tx2 := &lwStubTxRunner{q: stub2}
	repo2 := NewLearnerWeaknessRepo(tx2)
	_, _ = repo2.List(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID, PageSize: 2, PageToken: res.NextPageToken})
	args := stub2.queryArgs[0]
	var sawOffset2 bool
	for _, a := range args {
		if n, ok := a.(int); ok && n == 2 {
			sawOffset2 = true
		}
	}
	if !sawOffset2 {
		t.Errorf("page token did not advance OFFSET to 2; args=%#v", args)
	}
}

// ---- ListAll (rollup feed: full set, filters + sort, NO pagination) ----

func TestLearnerWeaknessRepo_ListAll_BuildsFiltersNoPagination(t *testing.T) {
	stub := &lwStubQuerier{
		rows: &lwStubRows{rows: [][]any{
			lwFullRow("id-1", "k1", "L1", "scrum", "", []string{"a"}, 0.8, []string{"explicit"}, "{}", []string{}, "active", lwNow(), lwNow()),
			lwFullRow("id-2", "k2", "L2", "scrum", "", []string{"b"}, 0.6, []string{"derived"}, "{}", []string{}, "active", lwNow(), lwNow()),
		}},
	}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	out, err := repo.ListAll(withCtx(), lw.ListQuery{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		Category: "scrum", Tags: []string{"a"}, MinStrength: 0.2,
		Sort: lw.SortLastEvidencedDesc,
		// PageSize/PageToken MUST be ignored by ListAll.
		PageSize: 1, PageToken: encodeOffsetToken(99),
	})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	q := stub.queryCalls[0]
	for _, frag := range []string{"learner_gcid = $1", "deleted_at IS NULL", "status <> 'grown'",
		"category = $", "strength >= $", "tags && $", "ORDER BY last_evidenced_at DESC"} {
		if !contains(q, frag) {
			t.Errorf("ListAll query missing %q in:\n%s", frag, q)
		}
	}
	if contains(q, "OFFSET") {
		t.Errorf("ListAll must NOT paginate (no OFFSET); got:\n%s", q)
	}
	// Both rows returned (no page trim), tenant/learner stamped.
	if len(out) != 2 {
		t.Fatalf("got %d edges; want all 2 (no pagination)", len(out))
	}
	if out[0].LearnerGCID != pgUserGCID || out[0].TenantID != pgTenantID {
		t.Errorf("scope not stamped: %+v", out[0])
	}
}

func TestLearnerWeaknessRepo_ListAll_IncludeGrown_DropsStatusFilter(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)
	_, _ = repo.ListAll(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID, IncludeGrown: true})
	if contains(tx.q.queryCalls[0], "status <> 'grown'") {
		t.Errorf("include_grown must drop the status filter: %s", tx.q.queryCalls[0])
	}
}

func TestLearnerWeaknessRepo_ListAll_PropagatesQueryError(t *testing.T) {
	stub := &lwStubQuerier{queryErr: errors.New("listall boom")}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.ListAll(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID}); err == nil {
		t.Error("expected listall-query error to propagate")
	}
}

func TestLearnerWeaknessRepo_ListAll_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{})
	if _, err := repo.ListAll(context.Background(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID}); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ---- Get ----

func TestLearnerWeaknessRepo_Get_Found(t *testing.T) {
	stub := &lwStubQuerier{row: &lwStubRow{vals: lwFullRow(
		"id-9", "photosynthesis", "Photosynthesis", "biology", "", []string{"plants"}, 0.4,
		[]string{"derived"}, "{}", []string{}, "active", lwNow(), lwNow())}}
	tx := &lwStubTxRunner{q: stub}
	repo := NewLearnerWeaknessRepo(tx)

	got, err := repo.Get(withCtx(), pgUserGCID, "id-9")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.ID != "id-9" || got.ConceptLabel != "Photosynthesis" {
		t.Errorf("Get returned %+v", got)
	}
}

func TestLearnerWeaknessRepo_Get_NotFound(t *testing.T) {
	tx := &lwStubTxRunner{} // QueryRow default -> ErrNoRows
	repo := NewLearnerWeaknessRepo(tx)
	got, err := repo.Get(withCtx(), pgUserGCID, "missing")
	if err != nil {
		t.Fatalf("Get: unexpected err %v", err)
	}
	if got != nil {
		t.Errorf("Get(missing) = %+v; want nil", got)
	}
}

// ---- SoftDelete ----

func TestLearnerWeaknessRepo_SoftDelete(t *testing.T) {
	tx := &lwStubTxRunner{}
	repo := NewLearnerWeaknessRepo(tx)
	if err := repo.SoftDelete(withCtx(), pgUserGCID, "id-1", lwNow()); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	var sawDel bool
	for _, c := range tx.q.execCalls {
		if contains(c, "UPDATE learner_weakness") && contains(c, "deleted_at") {
			sawDel = true
		}
	}
	if !sawDel {
		t.Errorf("expected soft-delete UPDATE; calls=%v", tx.q.execCalls)
	}
}

func TestLearnerWeaknessRepo_SoftDelete_FailsLoud_OnMissingTenant(t *testing.T) {
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{})
	if err := repo.SoftDelete(context.Background(), pgUserGCID, "id-1", lwNow()); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ---- error-path propagation ----

func TestLearnerWeaknessRepo_Upsert_PropagatesQueryError(t *testing.T) {
	stub := &lwStubQuerier{queryErr: errors.New("nearest boom")}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.Upsert(withCtx(), lwInput()); err == nil {
		t.Error("expected nearest-query error to propagate")
	}
}

func TestLearnerWeaknessRepo_Upsert_PropagatesInsertError(t *testing.T) {
	stub := &lwStubQuerier{failExec: "INSERT"} // RLS SET LOCALs unaffected
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.Upsert(withCtx(), lwInput()); err == nil {
		t.Error("expected insert error to propagate")
	}
}

func TestLearnerWeaknessRepo_Upsert_PropagatesUpdateError(t *testing.T) {
	stub := &lwStubQuerier{
		rows:     &lwStubRows{rows: [][]any{{"existing-id", float64(0.02)}}},
		row:      &lwStubRow{vals: lwFullRow("existing-id", "k", "L", "", "", []string{}, 0.5, []string{"explicit"}, "{}", []string{}, "active", lwNow(), lwNow())},
		failExec: "UPDATE",
	}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.Upsert(withCtx(), lwInput()); err == nil {
		t.Error("expected update error to propagate")
	}
}

func TestLearnerWeaknessRepo_List_PropagatesQueryError(t *testing.T) {
	stub := &lwStubQuerier{queryErr: errors.New("list boom")}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.List(withCtx(), lw.ListQuery{TenantID: pgTenantID, LearnerGCID: pgUserGCID}); err == nil {
		t.Error("expected list-query error to propagate")
	}
}

func TestLearnerWeaknessRepo_Get_PropagatesScanError(t *testing.T) {
	stub := &lwStubQuerier{row: &lwStubRow{err: errors.New("scan boom")}}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if _, err := repo.Get(withCtx(), pgUserGCID, "id"); err == nil {
		t.Error("expected non-ErrNoRows scan error to propagate")
	}
}

func TestLearnerWeaknessRepo_SoftDelete_PropagatesExecError(t *testing.T) {
	stub := &lwStubQuerier{failExec: "UPDATE"}
	repo := NewLearnerWeaknessRepo(&lwStubTxRunner{q: stub})
	if err := repo.SoftDelete(withCtx(), pgUserGCID, "id", lwNow()); err == nil {
		t.Error("expected soft-delete exec error to propagate")
	}
}

// ---- pure helper tests ----

func TestLW_OffsetToken_RoundTripAndGuards(t *testing.T) {
	if decodeOffsetToken("") != 0 {
		t.Error("empty token -> 0")
	}
	if decodeOffsetToken("!!!not-base64!!!") != 0 {
		t.Error("invalid base64 -> 0")
	}
	tok := encodeOffsetToken(40)
	if decodeOffsetToken(tok) != 40 {
		t.Errorf("round-trip = %d; want 40", decodeOffsetToken(tok))
	}
	// non "o:" prefix decodes to 0
	bad := []byte("x:5")
	if decodeOffsetToken(base64RawURL(bad)) != 0 {
		t.Error("non o: prefix -> 0")
	}
	if decodeOffsetToken(base64RawURL([]byte("o:-3"))) != 0 {
		t.Error("negative offset -> 0")
	}
}

func TestLW_NullableText(t *testing.T) {
	if nullableText("  ") != nil {
		t.Error("blank -> nil")
	}
	if v := nullableText("x"); v != "x" {
		t.Errorf("non-blank -> %v; want x", v)
	}
}

func TestLW_UnmarshalDescriptor(t *testing.T) {
	if d := unmarshalDescriptor(""); d.Summary != "" || len(d.Misconceptions) != 0 {
		t.Error("empty -> zero descriptor")
	}
	if d := unmarshalDescriptor(`{"summary":"s"}`); d.Summary != "s" {
		t.Errorf("parse failed: %+v", d)
	}
	if d := unmarshalDescriptor(`not json`); d.Summary != "" {
		t.Error("invalid json -> zero descriptor (ignored)")
	}
}

func TestLW_StringsToSources(t *testing.T) {
	if stringsToSources(nil) != nil {
		t.Error("nil -> nil")
	}
	got := stringsToSources([]string{"explicit", "derived"})
	if len(got) != 2 || got[0] != lw.SourceExplicit || got[1] != lw.SourceDerived {
		t.Errorf("convert = %v", got)
	}
}

// compile-time port assertion lives in the impl file; this guards the SQL consts.
func TestLearnerWeakness_SQLTemplates_NonEmpty(t *testing.T) {
	for name, s := range map[string]string{
		"nearest": nearestActiveLearnerWeaknessSQL,
		"get":     getLearnerWeaknessSQL,
		"insert":  insertLearnerWeaknessSQL,
		"update":  updateLearnerWeaknessSQL,
		"delete":  softDeleteLearnerWeaknessSQL,
	} {
		if s == "" {
			t.Errorf("%s SQL template empty", name)
		}
	}
}
