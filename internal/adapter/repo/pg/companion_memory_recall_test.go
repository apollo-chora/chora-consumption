// companion_memory_recall_test.go — F4 per-Companion pgvector RAG adapter
// RLS contract + SQL-shape + arg-binding verification (per multi-tenant-rls
// SKILL).
//
// Production atomic-transaction behaviour (Record INSERT + Recall cosine
// search) is exercised under integration_test.go against a live Cloud SQL
// instance gated by the `integration` build tag. The tests in THIS file
// verify (no live pgvector needed):
//
//  1. Every method runs rls.ApplySession (SET LOCAL chora.tenant_id +
//     chora.user_gcid pair) BEFORE issuing the domain query.
//  2. Record binds the embedding as a Postgres vector literal `[a,b,...]`,
//     defaults memory_type to "chat_turn", mints scope_key via
//     companion.MemoryScopeKey, and passes expires_at nil for TTL 0 / a time
//     for TTL > 0.
//  3. Recall emits the cosine `embedding <=> $2::vector` query with the
//     topK LIMIT, scans rows into []companion.MemoryRow, and defaults topK<=0.
//  4. Input validation: empty companion_id / content_text / embedding errs.
//  5. Defence-in-depth: bare context (no tenant_id) returns
//     rls.ErrNoTenantContext.
package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/domain/companion"
)

// ----------------------------------------------------------------------------
// memStubRows — minimal multi-row fixture for Recall.
// ----------------------------------------------------------------------------

type memStubRows struct {
	rows [][]any
	idx  int
}

func (m *memStubRows) Next() bool {
	if m.idx >= len(m.rows) {
		return false
	}
	m.idx++
	return true
}

func (m *memStubRows) Scan(dest ...any) error {
	row := m.rows[m.idx-1]
	if len(dest) != len(row) {
		return errors.New("memStubRows.Scan: argc mismatch")
	}
	for i, d := range dest {
		switch tgt := d.(type) {
		case *string:
			if v, ok := row[i].(string); ok {
				*tgt = v
			}
		case *time.Time:
			if v, ok := row[i].(time.Time); ok {
				*tgt = v
			}
		case *float64:
			if v, ok := row[i].(float64); ok {
				*tgt = v
			}
		case *[]byte:
			// JSONB (source_metadata). A nil fixture cell models SQL NULL.
			if v, ok := row[i].([]byte); ok {
				*tgt = v
			} else {
				*tgt = nil
			}
		}
	}
	return nil
}

func (m *memStubRows) Close() error { return nil }
func (m *memStubRows) Err() error   { return nil }

// memStubQuerier records every Exec + Query call (SQL + args) so tests can
// verify the RLS pair lands first AND inspect the SQL + bound arguments.
type memStubQuerier struct {
	execCalls  []string
	execArgs   [][]any
	queryCalls []string
	queryArgs  [][]any
	nextRows   *memStubRows
	execErr    error // injected Exec failure (CHO-2096 purge error-path test)
}

func (s *memStubQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	if s.execErr != nil {
		return rls.CommandTag{}, s.execErr
	}
	s.execCalls = append(s.execCalls, sql)
	s.execArgs = append(s.execArgs, args)
	return rls.CommandTag{RowsAffected: 1}, nil
}

func (s *memStubQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return &chatStubRow{err: ErrNoRows}
}

func (s *memStubQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	s.queryCalls = append(s.queryCalls, sql)
	s.queryArgs = append(s.queryArgs, args)
	if s.nextRows != nil {
		return s.nextRows, nil
	}
	return &memStubRows{}, nil
}

// memStubTxRunner runs fn against a fresh memStubQuerier.
type memStubTxRunner struct {
	q *memStubQuerier
}

func (r *memStubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &memStubQuerier{}
	}
	return fn(ctx, r.q)
}

const memCompanionID = "01970000-0000-7000-a000-000000000001"

// memPurgeOwnerGCID is the learner whose tenant/gcid context the retire-time
// purge runs under (the retire handler builds exactly this via repoCtx).
const memPurgeOwnerGCID = "01970000-0000-7000-b000-000000000002"

func sampleEmbedding() []float32 {
	return []float32{0.1, -0.2, 0.3}
}

// ----------------------------------------------------------------------------
// Record
// ----------------------------------------------------------------------------

func TestCompanionMemoryRepo_Record_AppliesRLS_AndBuildsInsert(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	err := repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		ContentText: "the mitochondria is the powerhouse of the cell",
		Embedding:   sampleEmbedding(),
		ModelID:     "text-embedding-004",
		Now:         time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected RLS pair + INSERT; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", tx.q.execCalls[1])
	}
	if !contains(tx.q.execCalls[2], "INSERT INTO companion_memory_recall") {
		t.Errorf("call[2] = %q (missing INSERT)", tx.q.execCalls[2])
	}
	if !contains(tx.q.execCalls[2], "::vector") {
		t.Errorf("call[2] = %q (missing ::vector cast)", tx.q.execCalls[2])
	}

	// Inspect the bound INSERT args (call index 2 in execArgs).
	args := tx.q.execArgs[2]
	// Locate the embedding literal + scope_key + memory_type + expires_at among args.
	var foundEmbedding, foundScope, foundType bool
	var sawExpiresNil bool
	for _, a := range args {
		switch v := a.(type) {
		case string:
			if v == "[0.1,-0.2,0.3]" {
				foundEmbedding = true
			}
			if v == companion.MemoryScopeKey(memCompanionID) {
				foundScope = true
			}
			if v == "chat_turn" {
				foundType = true
			}
		case nil:
			sawExpiresNil = true
		}
	}
	if !foundEmbedding {
		t.Errorf("INSERT args missing vector literal [0.1,-0.2,0.3]; got %#v", args)
	}
	if !foundScope {
		t.Errorf("INSERT args missing scope_key %q; got %#v", companion.MemoryScopeKey(memCompanionID), args)
	}
	if !foundType {
		t.Errorf("INSERT args missing default memory_type chat_turn; got %#v", args)
	}
	if !sawExpiresNil {
		t.Errorf("INSERT args expected nil expires_at for TTL=0; got %#v", args)
	}
}

func TestCompanionMemoryRepo_Record_DefaultsMemoryType(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)
	// memory_type left empty → must default to "chat_turn"
	_ = repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		MemoryType:  "",
		ContentText: "hello",
		Embedding:   sampleEmbedding(),
		ModelID:     "m",
		Now:         time.Now(),
	})
	args := tx.q.execArgs[2]
	var found bool
	for _, a := range args {
		if s, ok := a.(string); ok && s == "chat_turn" {
			found = true
		}
	}
	if !found {
		t.Errorf("empty memory_type did not default to chat_turn; args=%#v", args)
	}
}

func TestCompanionMemoryRepo_Record_HonoursExplicitMemoryType(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)
	_ = repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		MemoryType:  "fact",
		ContentText: "hello",
		Embedding:   sampleEmbedding(),
		ModelID:     "m",
		Now:         time.Now(),
	})
	args := tx.q.execArgs[2]
	var found bool
	for _, a := range args {
		if s, ok := a.(string); ok && s == "fact" {
			found = true
		}
	}
	if !found {
		t.Errorf("explicit memory_type 'fact' not bound; args=%#v", args)
	}
}

func TestCompanionMemoryRepo_Record_TTLSetsExpiresAt(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	_ = repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		ContentText: "hello",
		Embedding:   sampleEmbedding(),
		ModelID:     "m",
		Now:         now,
		TTL:         ttl,
	})
	args := tx.q.execArgs[2]
	var found bool
	want := now.Add(ttl)
	for _, a := range args {
		if ts, ok := a.(time.Time); ok && ts.Equal(want) {
			found = true
		}
	}
	if !found {
		t.Errorf("TTL>0 did not bind expires_at=%v; args=%#v", want, args)
	}
}

func TestCompanionMemoryRepo_Record_BindsOptionalSourceIDs(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)
	_ = repo.Record(withCtx(), companion.RecordMemoryInput{
		TenantID:        pgTenantID,
		OwnerGCID:       pgUserGCID,
		CompanionID:     memCompanionID,
		ContentText:     "hello",
		Embedding:       sampleEmbedding(),
		ModelID:         "m",
		SourceTurnID:    "01970000-0000-7000-b000-000000000002",
		SourceSessionID: "01970000-0000-7000-c000-000000000003",
		Now:             time.Now(),
	})
	args := tx.q.execArgs[2]
	var turnOK, sessOK bool
	for _, a := range args {
		if s, ok := a.(string); ok {
			if s == "01970000-0000-7000-b000-000000000002" {
				turnOK = true
			}
			if s == "01970000-0000-7000-c000-000000000003" {
				sessOK = true
			}
		}
	}
	if !turnOK || !sessOK {
		t.Errorf("optional source ids not bound (turn=%v sess=%v); args=%#v", turnOK, sessOK, args)
	}
}

func TestCompanionMemoryRepo_Record_RejectsBlankInputs(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	base := companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		ContentText: "hello",
		Embedding:   sampleEmbedding(),
		ModelID:     "m",
		Now:         time.Now(),
	}

	noFam := base
	noFam.CompanionID = ""
	if err := repo.Record(withCtx(), noFam); err == nil {
		t.Error("expected error on blank companion_id")
	}

	noText := base
	noText.ContentText = ""
	if err := repo.Record(withCtx(), noText); err == nil {
		t.Error("expected error on blank content_text")
	}

	noEmb := base
	noEmb.Embedding = nil
	if err := repo.Record(withCtx(), noEmb); err == nil {
		t.Error("expected error on empty embedding")
	}
}

func TestCompanionMemoryRepo_Record_FailsLoud_OnMissingTenantContext(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	err := repo.Record(context.Background(), companion.RecordMemoryInput{
		TenantID:    pgTenantID,
		OwnerGCID:   pgUserGCID,
		CompanionID: memCompanionID,
		ContentText: "hello",
		Embedding:   sampleEmbedding(),
		ModelID:     "m",
		Now:         time.Now(),
	})
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ----------------------------------------------------------------------------
// Recall
// ----------------------------------------------------------------------------

func TestCompanionMemoryRepo_Recall_AppliesRLS_AndBuildsCosineQuery(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	_, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
	if len(tx.q.queryCalls) != 1 {
		t.Fatalf("expected one cosine Query; got %d", len(tx.q.queryCalls))
	}
	q := tx.q.queryCalls[0]
	if !contains(q, "companion_memory_recall") {
		t.Errorf("query missing table: %q", q)
	}
	if !contains(q, "<=>") {
		t.Errorf("query missing cosine operator <=>: %q", q)
	}
	if !contains(q, "::vector") {
		t.Errorf("query missing ::vector cast: %q", q)
	}
	if !contains(q, "deleted_at IS NULL") {
		t.Errorf("query missing soft-delete filter: %q", q)
	}

	// Args: $1 companion_id, $2 query-embedding literal, $3 limit.
	args := tx.q.queryArgs[0]
	if len(args) != 3 {
		t.Fatalf("expected 3 query args; got %d (%#v)", len(args), args)
	}
	if args[0] != memCompanionID {
		t.Errorf("arg[0] = %v; want companion_id %q", args[0], memCompanionID)
	}
	if s, ok := args[1].(string); !ok || s != "[0.1,-0.2,0.3]" {
		t.Errorf("arg[1] = %v; want vector literal [0.1,-0.2,0.3]", args[1])
	}
	if args[2] != 5 {
		t.Errorf("arg[2] = %v; want topK limit 5", args[2])
	}
}

func TestCompanionMemoryRepo_Recall_ScansRows(t *testing.T) {
	created := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)
	stub := &memStubQuerier{
		nextRows: &memStubRows{
			// 6th cell = source_metadata (CHO-2192). nil models SQL NULL — neither of
			// these is a grounded note, so neither carries provenance.
			rows: [][]any{
				{"01970000-0000-7000-d000-000000000001", "chat_turn", "first memory", created, float64(0.12), nil},
				{"01970000-0000-7000-d000-000000000002", "fact", "second memory", created.Add(time.Minute), float64(0.34), nil},
			},
		},
	}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryRepo(tx)

	got, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows; want 2", len(got))
	}
	if got[0].ID != "01970000-0000-7000-d000-000000000001" {
		t.Errorf("row[0].ID = %q", got[0].ID)
	}
	if got[0].MemoryType != "chat_turn" {
		t.Errorf("row[0].MemoryType = %q", got[0].MemoryType)
	}
	if got[0].ContentText != "first memory" {
		t.Errorf("row[0].ContentText = %q", got[0].ContentText)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Errorf("row[0].CreatedAt = %v; want %v", got[0].CreatedAt, created)
	}
	if got[0].Distance != 0.12 {
		t.Errorf("row[0].Distance = %v; want 0.12", got[0].Distance)
	}
	if got[1].Distance != 0.34 {
		t.Errorf("row[1].Distance = %v; want 0.34", got[1].Distance)
	}
}

func TestCompanionMemoryRepo_Recall_DefaultsTopK(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)
	if _, err := repo.Recall(withCtx(), memCompanionID, sampleEmbedding(), 0); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	args := tx.q.queryArgs[0]
	if args[2] != defaultRecallTopK {
		t.Errorf("topK<=0 did not default to %d; got %v", defaultRecallTopK, args[2])
	}

	// Negative also defaults.
	tx2 := &memStubTxRunner{}
	repo2 := NewCompanionMemoryRepo(tx2)
	_, _ = repo2.Recall(withCtx(), memCompanionID, sampleEmbedding(), -7)
	if tx2.q.queryArgs[0][2] != defaultRecallTopK {
		t.Errorf("negative topK did not default; got %v", tx2.q.queryArgs[0][2])
	}
}

func TestCompanionMemoryRepo_Recall_RejectsBlankCompanionID(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	if _, err := repo.Recall(withCtx(), "  ", sampleEmbedding(), 5); err == nil {
		t.Error("expected error on blank companion_id")
	}
}

func TestCompanionMemoryRepo_Recall_RejectsEmptyEmbedding(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	if _, err := repo.Recall(withCtx(), memCompanionID, nil, 5); err == nil {
		t.Error("expected error on empty query embedding")
	}
}

func TestCompanionMemoryRepo_Recall_FailsLoud_OnMissingTenantContext(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	_, err := repo.Recall(context.Background(), memCompanionID, sampleEmbedding(), 5)
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

// ----------------------------------------------------------------------------
// vector literal formatting
// ----------------------------------------------------------------------------

func TestVectorLiteral(t *testing.T) {
	got := vectorLiteral([]float32{0.1, -0.2, 0.3})
	if got != "[0.1,-0.2,0.3]" {
		t.Errorf("vectorLiteral = %q; want [0.1,-0.2,0.3]", got)
	}
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Errorf("vectorLiteral not bracket-wrapped: %q", got)
	}
	if vectorLiteral(nil) != "[]" {
		t.Errorf("vectorLiteral(nil) = %q; want []", vectorLiteral(nil))
	}
}

// ----------------------------------------------------------------------------
// SQL template review-via-test
// ----------------------------------------------------------------------------

func TestCompanionMemoryRecall_SQLTemplates_NonEmpty(t *testing.T) {
	if insertCompanionMemoryRecallSQL == "" {
		t.Error("insert template empty")
	}
	if recallCompanionMemoryRecallSQL == "" {
		t.Error("recall template empty")
	}
}

// ----------------------------------------------------------------------------
// SoftDeleteByCompanion (CHO-2096 — retire-time memory purge)
// ----------------------------------------------------------------------------

func TestCompanionMemoryRepo_SoftDeleteByCompanion_PurgesLiveRows(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	n, err := repo.SoftDeleteByCompanion(ctx, "t-1", memCompanionID)
	if err != nil {
		t.Fatalf("SoftDeleteByCompanion: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged = %d, want the stub's RowsAffected (1)", n)
	}

	// The RLS pair MUST land before the UPDATE, exactly as it does for Record and
	// Recall. This is not ceremony: companion_memory_recall is FORCE-RLS'd on
	// current_setting('chora.tenant_id'), and the app role is NOBYPASSRLS, so a
	// purge that skips it tombstones NOTHING and returns (0, nil) — a silent
	// no-op the learner is told was a permanent deletion. (Live-verified
	// 2026-07-14: 62 rows visible with the GUC set, 0 without.)
	//
	// This assertion previously read `len(execCalls) != 1`, which pinned the SQL
	// to the UPDATE alone and thereby ENFORCED the missing RLS — a green test that
	// encoded the defect.
	if len(tx.q.execCalls) != 3 {
		t.Fatalf("exec calls = %d, want 3 (SET LOCAL tenant, SET LOCAL gcid, UPDATE)", len(tx.q.execCalls))
	}
	if !strings.Contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q — the purge must set the tenant GUC or it silently deletes nothing", tx.q.execCalls[0])
	}
	if !strings.Contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", tx.q.execCalls[1])
	}

	sql := tx.q.execCalls[2]
	if !strings.Contains(sql, "UPDATE companion_memory_recall") ||
		!strings.Contains(sql, "SET deleted_at = now()") ||
		!strings.Contains(sql, "deleted_at IS NULL") {
		t.Fatalf("purge SQL must be a live-row soft-delete, got: %s", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM") {
		t.Fatal("purge must never hard-delete (ddd-enforcement #4)")
	}
	args := tx.q.execArgs[2]
	if len(args) != 2 || args[0] != "t-1" || args[1] != memCompanionID {
		t.Fatalf("args = %v, want [t-1 %s]", args, memCompanionID)
	}
}

// A purge invoked without tenant context must FAIL LOUD, never quietly no-op.
// Before the RLS fix this call succeeded and reported 0 rows purged — which the
// method's own doc comment excused as "nothing was remembered".
func TestCompanionMemoryRepo_SoftDeleteByCompanion_RefusesWithoutTenantContext(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	n, err := repo.SoftDeleteByCompanion(context.Background(), "t-1", memCompanionID)
	if err == nil {
		t.Fatalf("want a loud error with no tenant on ctx, got nil (purged=%d) — a silent no-op is how CHO-2096 shipped broken", n)
	}
}

func TestCompanionMemoryRepo_SoftDeleteByCompanion_PropagatesError(t *testing.T) {
	tx := &memStubTxRunner{q: &memStubQuerier{execErr: errors.New("pg down")}}
	repo := NewCompanionMemoryRepo(tx)

	if _, err := repo.SoftDeleteByCompanion(context.Background(), "t-1", memCompanionID); err == nil {
		t.Fatal("want error when the purge exec fails")
	}
}

// ----------------------------------------------------------------------------
// PurgeExpired + DeleteOwnedMemory (register 6.4 R6)
// ----------------------------------------------------------------------------
//
// WHAT WAS ACTUALLY MISSING. expires_at has existed since mig 0045 and Recall
// has always filtered it, so the expiry story LOOKS finished. It is not: not one
// of the five Record call sites sets a TTL, so expires_at is universally NULL,
// the filter can never exclude a row, and no memory has ever expired. Worse, an
// expiry that only hid the row would leave the learner's conversation text and
// its 768-d embedding on disk forever, which is not what "expired" means to the
// person whose conversation it was.
//
// So the sweep REDACTS as well as tombstones. Both columns, deliberately:
// content_text is the conversation, and the embedding is a lossy but real
// representation of it that stays cosine-matchable and is a known inversion
// target. Redacting only the text would be a half-measure that still answers
// "what did they talk about" approximately.
//
// It is a redact, not a DELETE, because ddd-enforcement #4 forbids hard-delete.
// The tombstone row keeps its ids and timestamps for audit and carries no
// content. Note content_text has CHECK (length(content_text) > 0), so the
// tombstone is a marker string rather than ''.

func TestCompanionMemoryRepo_PurgeExpired_RedactsContentAndEmbedding(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	n, err := repo.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged = %d, want the stub's RowsAffected (1)", n)
	}

	if len(tx.q.execCalls) != 3 {
		t.Fatalf("exec calls = %d, want 3 (SET LOCAL tenant, SET LOCAL gcid, UPDATE)", len(tx.q.execCalls))
	}
	if !strings.Contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; without the GUC this sweep redacts NOTHING and reports success", tx.q.execCalls[0])
	}

	sql := tx.q.execCalls[2]
	if !strings.Contains(sql, "UPDATE companion_memory_recall") {
		t.Fatalf("want an UPDATE on the memory table, got: %s", sql)
	}
	// Tombstoned...
	if !strings.Contains(sql, "deleted_at = now()") {
		t.Errorf("expired rows must be tombstoned, got: %s", sql)
	}
	// ...AND emptied. An expiry that only hides the row is not an expiry.
	if !strings.Contains(sql, "content_text =") {
		t.Errorf("expiry must redact the conversation text, got: %s", sql)
	}
	if !strings.Contains(sql, "embedding =") {
		t.Errorf("expiry must redact the embedding too; it is a recoverable "+
			"representation of the same conversation, got: %s", sql)
	}
	// Only genuinely expired, still-live rows.
	if !strings.Contains(sql, "expires_at IS NOT NULL") || !strings.Contains(sql, "expires_at <= now()") {
		t.Errorf("sweep must target only expired rows, got: %s", sql)
	}
	if !strings.Contains(sql, "deleted_at IS NULL") {
		t.Errorf("sweep must not re-purge already-tombstoned rows, got: %s", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM") {
		t.Errorf("hard-delete is forbidden (ddd-enforcement #4), got: %s", sql)
	}
}

func TestCompanionMemoryRepo_PurgeExpired_RefusesWithoutTenantContext(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	if _, err := repo.PurgeExpired(context.Background()); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v, want rls.ErrNoTenantContext", err)
	}
}

// DeleteOwnedMemory is the learner's own delete. The OWNERSHIP predicate is the
// load-bearing part: RLS fences the tenant, and nothing else would stop one
// learner erasing another learner's memory inside the same tenant.

func TestCompanionMemoryRepo_DeleteOwnedMemory_RedactsAndFencesOwner(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	n, err := repo.DeleteOwnedMemory(ctx, "t-1", memPurgeOwnerGCID, "019ffb00-0000-7000-8000-00000000dead")
	if err != nil {
		t.Fatalf("DeleteOwnedMemory: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted = %d, want 1", n)
	}

	if len(tx.q.execCalls) != 3 {
		t.Fatalf("exec calls = %d, want 3 (SET LOCAL tenant, SET LOCAL gcid, UPDATE)", len(tx.q.execCalls))
	}
	sql := tx.q.execCalls[2]
	if !strings.Contains(sql, "owner_gcid = ") {
		t.Fatalf("the delete MUST be fenced on owner_gcid; RLS only fences the "+
			"tenant, so without it any learner could erase a co-tenant's memory. got: %s", sql)
	}
	if !strings.Contains(sql, "deleted_at = now()") ||
		!strings.Contains(sql, "content_text =") ||
		!strings.Contains(sql, "embedding =") {
		t.Errorf("a learner delete must erase the content, not merely hide the row, got: %s", sql)
	}
	if !strings.Contains(sql, "deleted_at IS NULL") {
		t.Errorf("must target a live row, got: %s", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "DELETE FROM") {
		t.Errorf("hard-delete is forbidden (ddd-enforcement #4), got: %s", sql)
	}
}

func TestCompanionMemoryRepo_DeleteOwnedMemory_ValidatesInputs(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)

	for _, tc := range []struct{ name, tenant, gcid, id string }{
		{"empty tenant", "", memPurgeOwnerGCID, "m-1"},
		{"empty gcid", "t-1", "", "m-1"},
		{"empty memory id", "t-1", memPurgeOwnerGCID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := repo.DeleteOwnedMemory(ctx, tc.tenant, tc.gcid, tc.id); err == nil {
				t.Fatalf("want an error for %s", tc.name)
			}
		})
	}
}

func TestCompanionMemoryRepo_DeleteOwnedMemory_RefusesWithoutTenantContext(t *testing.T) {
	repo := NewCompanionMemoryRepo(&memStubTxRunner{})
	_, err := repo.DeleteOwnedMemory(context.Background(), "t-1", memPurgeOwnerGCID, "m-1")
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v, want rls.ErrNoTenantContext", err)
	}
}

// ----------------------------------------------------------------------------
// Default retention (register 6.4 R6)
// ----------------------------------------------------------------------------
//
// The TTL lives on the STORE, not on each caller. There are five Record call
// sites (chat turn, ceremony note, skill-invoke note, the p1b gRPC tool note and
// the weakness-RAG subscriber) and not one of them ever set RecordMemoryInput.TTL,
// which is why expires_at was universally NULL and nothing had ever expired.
// Threading a duration through five call sites would leave the sixth to be
// forgotten; a store-level default covers every writer, present and future, from
// one wiring point.
//
// An explicit per-call TTL still wins, so a caller that genuinely wants a
// different retention can say so.

func TestCompanionMemoryRepo_Record_AppliesTheStoreDefaultTTL(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx).WithDefaultTTL(24 * time.Hour)

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	if err := repo.Record(ctx, companion.RecordMemoryInput{
		TenantID: "t-1", OwnerGCID: memPurgeOwnerGCID, CompanionID: memCompanionID,
		ContentText: "hello", Embedding: []float32{0.1, 0.2}, ModelID: "m", Now: now,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	expiresAt, ok := memInsertArgs(t, tx)[11].(time.Time)
	if !ok {
		t.Fatalf("expires_at = %#v, want a time from the store default; a nil here is "+
			"the original defect: the column exists, Recall filters it, and nothing ever set it", memInsertArgs(t, tx)[11])
	}
	if want := now.Add(24 * time.Hour); !expiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", expiresAt, want)
	}
}

func TestCompanionMemoryRepo_Record_ExplicitTTLBeatsTheStoreDefault(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx).WithDefaultTTL(24 * time.Hour)

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	if err := repo.Record(ctx, companion.RecordMemoryInput{
		TenantID: "t-1", OwnerGCID: memPurgeOwnerGCID, CompanionID: memCompanionID,
		ContentText: "hello", Embedding: []float32{0.1}, ModelID: "m", Now: now,
		TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	expiresAt, _ := memInsertArgs(t, tx)[11].(time.Time)
	if want := now.Add(time.Hour); !expiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want the explicit %v", expiresAt, want)
	}
}

func TestCompanionMemoryRepo_Record_NoDefaultMeansNoExpiryAsBefore(t *testing.T) {
	// Behaviour-neutral: an unconfigured store must write NULL exactly as today.
	// Defaulting to some retention here would silently start destroying learner
	// memory on the next deploy, which is a decision for an operator and not for
	// this constructor.
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryRepo(tx)

	ctx := tracing.WithGCID(tracing.WithTenantID(context.Background(), "t-1"), memPurgeOwnerGCID)
	if err := repo.Record(ctx, companion.RecordMemoryInput{
		TenantID: "t-1", OwnerGCID: memPurgeOwnerGCID, CompanionID: memCompanionID,
		ContentText: "hello", Embedding: []float32{0.1}, ModelID: "m", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := memInsertArgs(t, tx)[11]; got != nil {
		t.Fatalf("expires_at = %#v, want nil (no expiry) when no default is configured", got)
	}
}

// memInsertArgs returns the bind args of the INSERT, which is the exec AFTER the
// two SET LOCAL statements rls.ApplySession issues.
func memInsertArgs(t *testing.T, tx *memStubTxRunner) []any {
	t.Helper()
	if len(tx.q.execArgs) < 3 {
		t.Fatalf("exec calls = %d, want 3 (SET LOCAL tenant, SET LOCAL gcid, INSERT)", len(tx.q.execArgs))
	}
	return tx.q.execArgs[2]
}
