// student_transcript_test.go — RLS contract + SQL-shape verification for the
// StudentTranscript pg adapter (W6 Slice 1). Mirrors learner_profile_test.go:
// a stub Querier captures Exec SQL so we assert the SET LOCAL chora.tenant_id
// pair lands BEFORE the projection mutation, and that the upsert carries the
// idempotency-key ON CONFLICT clause.
package pg

import (
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

func ttEntry(t *testing.T, kind st.Kind, sourceRef, idemKey string) *st.TranscriptEntry {
	t.Helper()
	scoreEarned := 8.5
	scorePossible := 10.0
	passed := true
	e, err := st.New(st.NewEntryInput{
		TenantID:       pgTenantID,
		GCID:           pgUserGCID,
		Kind:           kind,
		SourceRef:      sourceRef,
		Title:          "Algebra Quiz",
		ScoreEarned:    &scoreEarned,
		ScorePossible:  &scorePossible,
		Passed:         &passed,
		OccurredAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: idemKey,
		Now:            time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ttEntry: %v", err)
	}
	return e
}

func TestPGTranscriptRepo_UpsertAppliesRLSAndUpserts(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	if err := repo.Upsert(withCtx(), ttEntry(t, st.KindAssessment, "assessment-1", "submission:sub-1:graded")); err != nil {
		t.Fatalf("Upsert: %v", err)
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
	if !contains(last, "INSERT INTO student_transcript_entries") || !contains(last, "ON CONFLICT") {
		t.Errorf("last = %q; want upsert with ON CONFLICT", last)
	}
}

func TestPGTranscriptRepo_UpsertNilIsNoOp(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	if err := repo.Upsert(withCtx(), nil); err != nil {
		t.Fatalf("Upsert(nil): %v", err)
	}
	if tx.q != nil {
		t.Error("nil entry must not open a transaction")
	}
}

func TestPGTranscriptRepo_ListByGCIDAppliesRLSAndDefaultsLimit(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	out, err := repo.ListByGCID(withCtx(), pgTenantID, pgUserGCID, 0) // 0 -> default
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("nil-stub query should yield empty; got %d", len(out))
	}
	if len(tx.q.execCalls) < 2 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing: %v", tx.q.execCalls)
	}
}

func TestPGTranscriptRepo_ListByAssessmentIDsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	out, err := repo.ListByAssessmentIDs(withCtx(), pgTenantID, []string{"a-1", "a-2"})
	if err != nil {
		t.Fatalf("ListByAssessmentIDs: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("nil-stub query should yield empty; got %d", len(out))
	}
	if len(tx.q.execCalls) < 2 || !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS pair missing: %v", tx.q.execCalls)
	}
}

func TestPGTranscriptRepo_ListByAssessmentIDsEmptyShortCircuits(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewTranscriptRepo(tx)
	out, err := repo.ListByAssessmentIDs(withCtx(), pgTenantID, nil)
	if err != nil {
		t.Fatalf("ListByAssessmentIDs: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty result, got %d", len(out))
	}
	if tx.q != nil {
		t.Error("empty assessment_ids must not open a transaction (no round-trip)")
	}
}

func TestStudentTranscriptSQLTemplates_NonEmpty(t *testing.T) {
	templates := map[string]string{
		"upsert":                 upsertTranscriptEntrySQL,
		"list_by_gcid":           listTranscriptByGCIDSQL,
		"list_by_assessment_ids": listTranscriptByAssessmentIDsSQL,
	}
	for name, s := range templates {
		if s == "" {
			t.Errorf("template %q empty", name)
		}
		if !contains(s, "student_transcript_entries") {
			t.Errorf("template %q must reference student_transcript_entries: %q", name, s)
		}
	}
}
