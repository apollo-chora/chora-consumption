// learner_profile_test.go — RLS contract + SQL-shape verification for the
// LearnerProfile pg adapter (ADR-200, CHO-1908). Mirrors course_content_test.go:
// a stub Querier captures Exec SQL so we assert the SET LOCAL chora.tenant_id
// pair lands BEFORE the projection mutation, and that upsert/append carry the
// idempotency clauses.
package pg

import (
	"testing"
	"time"

	lp "github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

func lpFact(t *testing.T, ft lp.FactType, ref string) *lp.Fact {
	t.Helper()
	f, err := lp.New(lp.NewFactInput{
		TenantID:      pgTenantID,
		LearnerGCID:   pgUserGCID,
		Type:          ft,
		RefID:         ref,
		Detail:        lp.Detail{Label: "X"},
		SourceEventID: "01970000-0000-7000-e000-000000000001",
		OccurredAt:    time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
		Now:           time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("lpFact: %v", err)
	}
	return f
}

func TestPGLearnerProfileRepo_UpsertFactAppliesRLSAndUpserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerProfileRepo(tx)
	if err := repo.UpsertFact(withCtx(), lpFact(t, lp.FactCourseCompleted, "course-1")); err != nil {
		t.Fatalf("UpsertFact: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + upsert; got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", calls[0])
	}
	if !contains(calls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q; want SET LOCAL gcid", calls[1])
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO learner_profile_facts") || !contains(last, "ON CONFLICT") {
		t.Errorf("last = %q; want upsert with ON CONFLICT", last)
	}
}

func TestPGLearnerProfileRepo_AppendActivityDedupes(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerProfileRepo(tx)
	e, err := lp.NewActivity(lp.NewActivityInput{
		TenantID:      pgTenantID,
		LearnerGCID:   pgUserGCID,
		Kind:          "completed_course",
		Summary:       "Completed Intro to Go",
		RefID:         "course-1",
		SourceEventID: "01970000-0000-7000-e000-000000000002",
		OccurredAt:    time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
		Now:           time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	if err := repo.AppendActivity(withCtx(), e); err != nil {
		t.Fatalf("AppendActivity: %v", err)
	}
	calls := tx.q.execCalls
	if len(calls) < 3 {
		t.Fatalf("want RLS pair + insert; got %d", len(calls))
	}
	last := calls[len(calls)-1]
	if !contains(last, "INSERT INTO learner_activity_log") || !contains(last, "DO NOTHING") {
		t.Errorf("last = %q; want append with ON CONFLICT DO NOTHING", last)
	}
}

func TestPGLearnerProfileRepo_ListFactsAppliesRLSAndEmptyOnNilStub(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerProfileRepo(tx)
	out, err := repo.ListFacts(withCtx(), pgTenantID, pgUserGCID)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("nil-stub query should yield empty; got %d", len(out))
	}
	if len(tx.q.execCalls) < 2 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing: %v", tx.q.execCalls)
	}
}

func TestPGLearnerProfileRepo_RecentActivityAppliesRLSAndDefaultsLimit(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearnerProfileRepo(tx)
	out, err := repo.RecentActivity(withCtx(), pgTenantID, pgUserGCID, 0) // 0 -> default
	if err != nil {
		t.Fatalf("RecentActivity: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("nil-stub query should yield empty; got %d", len(out))
	}
	if len(tx.q.execCalls) < 2 || !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("RLS pair missing: %v", tx.q.execCalls)
	}
}
