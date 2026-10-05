// proofingtest_repo_test.go — CHO-2040: in-memory proofingtest.Repository
// (unit servers + handler/subscriber suites), RED-first.
package inmem

import (
	"context"
	"errors"
	"testing"
	"time"

	pt "github.com/apollo-chora/chora-consumption/internal/domain/proofingtest"
)

func seedPT(t *testing.T, goalID string, now time.Time) *pt.ProofingTest {
	t.Helper()
	row, err := pt.NewRequested(pt.NewRequestedInput{
		TenantID:    "11111111-1111-4111-8111-111111111111",
		LearnerGCID: "22222222-2222-4222-8222-222222222222",
		GoalID:      goalID,
		CompanionID: "44444444-4444-4444-8444-444444444444",
		TargetEdges: []pt.TargetEdge{{
			ConceptID: "55555555-5555-4555-8555-555555555555",
			Key:       "k.a", Title: "A", Intent: "remediate",
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("NewRequested: %v", err)
	}
	return row
}

func TestInmemProofing_CreateGetListNewestFirst(t *testing.T) {
	repo := NewProofingTestRepo()
	ctx := context.Background()
	base := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	goalA := "33333333-3333-4333-8333-333333333333"
	goalB := "33333333-3333-4333-8333-333333333334"

	older := seedPT(t, goalA, base)
	newer := seedPT(t, goalA, base.Add(time.Minute))
	other := seedPT(t, goalB, base.Add(2*time.Minute))
	for _, r := range []*pt.ProofingTest{older, newer, other} {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	got, err := repo.GetByID(ctx, older.TenantID, older.LearnerGCID, older.ID)
	if err != nil || got == nil || got.ID != older.ID {
		t.Fatalf("GetByID: %+v (%v)", got, err)
	}
	if miss, err := repo.GetByID(ctx, older.TenantID, older.LearnerGCID, "01970000-aaaa-7000-8000-00000000dead"); err != nil || miss != nil {
		t.Fatalf("GetByID miss must be (nil, nil), got %+v (%v)", miss, err)
	}

	byAssist, err := repo.GetByAssistID(ctx, older.TenantID, older.AssistID)
	if err != nil || byAssist == nil || byAssist.ID != older.ID {
		t.Fatalf("GetByAssistID: %+v (%v)", byAssist, err)
	}

	all, err := repo.ListByLearner(ctx, older.TenantID, older.LearnerGCID, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("ListByLearner all = %d (%v)", len(all), err)
	}
	if all[0].ID != other.ID || all[2].ID != older.ID {
		t.Fatalf("list must be newest-first: %v %v %v", all[0].CreatedAt, all[1].CreatedAt, all[2].CreatedAt)
	}

	scoped, err := repo.ListByLearner(ctx, older.TenantID, older.LearnerGCID, goalA)
	if err != nil || len(scoped) != 2 {
		t.Fatalf("goal-filtered = %d (%v)", len(scoped), err)
	}
}

func TestInmemProofing_UpdatePersistsAndCopies(t *testing.T) {
	repo := NewProofingTestRepo()
	ctx := context.Background()
	row := seedPT(t, "33333333-3333-4333-8333-333333333333", time.Now().UTC())
	if err := repo.Create(ctx, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Mutating the caller's copy must NOT leak into the store (defensive copy).
	row.Status = pt.StatusFailed
	stored, _ := repo.GetByID(ctx, row.TenantID, row.LearnerGCID, row.ID)
	if stored.Status != pt.StatusRequested {
		t.Fatalf("store must hold its own copy, got %q", stored.Status)
	}
	// Real transition via Update.
	if err := stored.MarkReady([]byte(`{"proposed_test_set":{}}`), time.Now().UTC()); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, _ := repo.GetByAssistID(ctx, row.TenantID, row.AssistID)
	if again.Status != pt.StatusReady {
		t.Fatalf("update lost: %q", again.Status)
	}
	// Tenant misses return (nil, nil).
	if miss, err := repo.GetByAssistID(ctx, "99999999-9999-4999-8999-999999999999", row.AssistID); err != nil || miss != nil {
		t.Fatalf("foreign-tenant read must be (nil, nil), got %+v (%v)", miss, err)
	}
}

func TestInmemProofing_UpdateUnknownFailsLoud(t *testing.T) {
	repo := NewProofingTestRepo()
	row := seedPT(t, "33333333-3333-4333-8333-333333333333", time.Now().UTC())
	if err := repo.Update(context.Background(), row); err == nil {
		t.Fatalf("updating a never-created row must fail loud")
	}
}

func TestInmemProofing_Create_NilAggregateFailsLoud(t *testing.T) {
	repo := NewProofingTestRepo()
	if err := repo.Create(context.Background(), nil); err == nil {
		t.Fatal("Create(nil) must fail loud")
	}
}

func TestInmemProofing_Create_DuplicateIDFailsLoud(t *testing.T) {
	repo := NewProofingTestRepo()
	ctx := context.Background()
	row := seedPT(t, "33333333-3333-4333-8333-333333333333", time.Now().UTC())
	if err := repo.Create(ctx, row); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := repo.Create(ctx, row); err == nil {
		t.Fatal("Create of a duplicate id must fail loud")
	}
}

func TestInmemProofing_FailCreate_ArmsInjectedFailure(t *testing.T) {
	repo := NewProofingTestRepo()
	sentinel := errors.New("injected create failure")
	repo.FailCreate(sentinel)
	row := seedPT(t, "33333333-3333-4333-8333-333333333333", time.Now().UTC())
	if err := repo.Create(context.Background(), row); err != sentinel {
		t.Fatalf("Failed Create err = %v, want injected sentinel", err)
	}
	if _, ok := repo.rows[row.ID]; ok {
		t.Errorf("row persisted despite injected Create failure")
	}
}

func TestInmemProofing_copyRow_FullPointerCopy(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	ref := "test-set-ref-1"
	in := &pt.ProofingTest{
		TestSetRef:     &ref,
		TestSetPayload: []byte(`{"proposed_test_set":{}}`),
		DeletedAt:      &now,
	}
	out := copyRow(in)
	if out == nil {
		t.Fatal("copyRow(non-nil) returned nil")
	}
	if out.TestSetRef == nil || *out.TestSetRef != ref {
		t.Errorf("TestSetRef not deep-copied: %v", out.TestSetRef)
	}
	if out.TestSetRef == in.TestSetRef {
		t.Errorf("TestSetRef pointer shared with source, want deep copy")
	}
	if out.DeletedAt == nil || !out.DeletedAt.Equal(now) {
		t.Errorf("DeletedAt not deep-copied: %v", out.DeletedAt)
	}
	if out.DeletedAt == in.DeletedAt {
		t.Errorf("DeletedAt pointer shared with source, want deep copy")
	}
	if string(out.TestSetPayload) != `{"proposed_test_set":{}}` {
		t.Errorf("TestSetPayload not copied: %q", out.TestSetPayload)
	}
	// copyRow must clone the slice headers too.
	in.TestSetPayload[0] = 'X'
	copied := append([]byte(nil), out.TestSetPayload...)
	if copied[0] == 'X' {
		t.Errorf("TestSetPayload slice header shared with source")
	}
	// nil input is pass-through nil.
	if cp := copyRow(nil); cp != nil {
		t.Errorf("copyRow(nil) = %v, want nil", cp)
	}
}
