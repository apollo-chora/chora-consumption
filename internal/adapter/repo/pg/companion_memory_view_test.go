// companion_memory_view_test.go — ADR-215 WS-1 tier-(a) learner memory READ
// adapter: RLS contract + SQL-shape + arg-binding + scan verification.
//
// This is the PASSIVE learner-panel read (recency-ordered), distinct from the
// agent-facing cosine Recall in companion_memory_recall.go. It reuses the shared
// memStub* / withCtx / pgTenantID helpers defined in the sibling
// companion_memory_recall_test.go + user_kg_test.go (one test binary per package).
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	companionmind "github.com/apollo-chora/chora-consumption/internal/domain/companion_mind"
)

func TestCompanionMemoryViewRepo_RecentMemories_AppliesRLS_AndBuildsRecencyQuery(t *testing.T) {
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)

	_, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
		MemoryLimit: 5,
	})
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}

	if tx.q == nil || len(tx.q.execCalls) < 2 {
		t.Fatalf("expected RLS SET LOCAL pair; got %v", tx.q)
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q (missing tenant SET LOCAL)", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q (missing gcid SET LOCAL)", tx.q.execCalls[1])
	}
	if len(tx.q.queryCalls) != 1 {
		t.Fatalf("expected one recency Query; got %d", len(tx.q.queryCalls))
	}
	q := tx.q.queryCalls[0]
	if !contains(q, "companion_memory_recall") {
		t.Errorf("query missing table: %q", q)
	}
	if !contains(q, "created_at DESC") {
		t.Errorf("query missing recency ordering (created_at DESC): %q", q)
	}
	if !contains(q, "deleted_at IS NULL") {
		t.Errorf("query missing soft-delete filter: %q", q)
	}
	if !contains(q, "expires_at IS NULL OR expires_at > now()") {
		t.Errorf("query missing TTL filter: %q", q)
	}
	// D5: the LEARNER read must NOT surface embeddings / cosine distance / model
	// internals — no vector machinery in this SQL.
	if contains(q, "<=>") || contains(q, "::vector") || contains(q, "embedding") {
		t.Errorf("learner read leaked vector machinery: %q", q)
	}
	if contains(q, "model_id") {
		t.Errorf("learner read leaked model_id: %q", q)
	}

	// Args: $1 companion_id, $2 clamped limit, $3 memory_type filter (CHO-2185 —
	// empty here, which selects every type).
	args := tx.q.queryArgs[0]
	if len(args) != 3 {
		t.Fatalf("expected 3 query args; got %d (%#v)", len(args), args)
	}
	if args[0] != memCompanionID {
		t.Errorf("arg[0] = %v; want companion_id %q", args[0], memCompanionID)
	}
	if args[1] != 5 {
		t.Errorf("arg[1] = %v; want limit 5", args[1])
	}
	if types, ok := args[2].([]string); !ok || len(types) != 0 {
		t.Errorf("arg[2] = %#v; want an empty []string (all memory types)", args[2])
	}
}

func TestCompanionMemoryViewRepo_RecentMemories_ScansRows_NoVectorFields(t *testing.T) {
	created := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	// 5th cell = source_metadata (mig 0094). nil models SQL NULL — neither of these
	// is a grounded note, so neither carries provenance.
	stub := &memStubQuerier{
		nextRows: &memStubRows{
			rows: [][]any{
				{"01970000-0000-7000-e000-000000000001", "chat_turn", "we talked about base cases", created, nil},
				{"01970000-0000-7000-e000-000000000002", "fact", "recursion needs a base case", created.Add(-time.Hour), nil},
			},
		},
	}
	tx := &memStubTxRunner{q: stub}
	repo := NewCompanionMemoryViewRepo(tx)

	got, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID:    pgTenantID,
		LearnerGCID: pgUserGCID,
		CompanionID: memCompanionID,
		MemoryLimit: 10,
	})
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d memories; want 2", len(got))
	}
	if got[0].ID != "01970000-0000-7000-e000-000000000001" {
		t.Errorf("row[0].ID = %q", got[0].ID)
	}
	if got[0].MemoryType != "chat_turn" {
		t.Errorf("row[0].MemoryType = %q", got[0].MemoryType)
	}
	if got[0].Content != "we talked about base cases" {
		t.Errorf("row[0].Content = %q", got[0].Content)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Errorf("row[0].CreatedAt = %v; want %v", got[0].CreatedAt, created)
	}
}

func TestCompanionMemoryViewRepo_RecentMemories_DefaultsAndClampsLimit(t *testing.T) {
	// MemoryLimit 0 → DefaultMemoryLimit.
	tx := &memStubTxRunner{}
	repo := NewCompanionMemoryViewRepo(tx)
	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, CompanionID: memCompanionID, MemoryLimit: 0,
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if tx.q.queryArgs[0][1] != companionmind.DefaultMemoryLimit {
		t.Errorf("limit 0 did not default to %d; got %v", companionmind.DefaultMemoryLimit, tx.q.queryArgs[0][1])
	}

	// MemoryLimit over the cap → MaxMemoryLimit.
	tx2 := &memStubTxRunner{}
	repo2 := NewCompanionMemoryViewRepo(tx2)
	if _, err := repo2.RecentMemories(withCtx(), companionmind.Query{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, CompanionID: memCompanionID, MemoryLimit: companionmind.MaxMemoryLimit + 500,
	}); err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	if tx2.q.queryArgs[0][1] != companionmind.MaxMemoryLimit {
		t.Errorf("over-cap limit did not clamp to %d; got %v", companionmind.MaxMemoryLimit, tx2.q.queryArgs[0][1])
	}
}

func TestCompanionMemoryViewRepo_RecentMemories_RejectsBlankCompanionID(t *testing.T) {
	repo := NewCompanionMemoryViewRepo(&memStubTxRunner{})
	if _, err := repo.RecentMemories(withCtx(), companionmind.Query{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, CompanionID: "  ", MemoryLimit: 5,
	}); err == nil {
		t.Error("expected error on blank companion_id")
	}
}

func TestCompanionMemoryViewRepo_RecentMemories_FailsLoud_OnMissingTenantContext(t *testing.T) {
	repo := NewCompanionMemoryViewRepo(&memStubTxRunner{})
	_, err := repo.RecentMemories(context.Background(), companionmind.Query{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, CompanionID: memCompanionID, MemoryLimit: 5,
	})
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("err = %v; want ErrNoTenantContext", err)
	}
}

func TestCompanionMemoryViewRepo_SQLTemplate_NonEmpty(t *testing.T) {
	if recentCompanionMemoriesSQL == "" {
		t.Error("recentCompanionMemoriesSQL template empty")
	}
}
