// student_transcript_test.go — TDD for the StudentTranscript read-model
// aggregate (W6 Slice 1, Four-Mode plan "Outcome spine"). Pure domain: no
// time.Now() leaks (Now is clock-injected), no infra imports.
package student_transcript

import (
	"testing"
	"time"
)

const (
	ttTenant = "01970000-0000-7000-8000-00000000aaaa"
	ttGCID   = "01970000-0000-7000-9000-00000000bbbb"
)

func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }

func validAssessmentInput() NewEntryInput {
	return NewEntryInput{
		TenantID:       ttTenant,
		GCID:           ttGCID,
		Kind:           KindAssessment,
		SourceRef:      "assessment-1",
		Title:          "Algebra Quiz",
		ScoreEarned:    ptrF(8.5),
		ScorePossible:  ptrF(10),
		Passed:         ptrB(true),
		OccurredAt:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
		IdempotencyKey: "submission:sub-1:graded",
	}
}

func TestNew_ValidAssessmentEntry(t *testing.T) {
	e, err := New(validAssessmentInput())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.EntryID == "" {
		t.Error("entry_id must be minted (UUIDv7)")
	}
	if e.TenantID != ttTenant || e.GCID != ttGCID {
		t.Errorf("tenant/gcid wrong: %+v", e)
	}
	if e.Kind != KindAssessment {
		t.Errorf("kind = %q, want assessment", e.Kind)
	}
	if e.SourceRef != "assessment-1" {
		t.Errorf("source_ref = %q, want assessment-1", e.SourceRef)
	}
	if e.ScoreEarned == nil || *e.ScoreEarned != 8.5 {
		t.Errorf("score_earned = %v, want 8.5", e.ScoreEarned)
	}
	if e.ScorePossible == nil || *e.ScorePossible != 10 {
		t.Errorf("score_possible = %v, want 10", e.ScorePossible)
	}
	if e.ScorePercent == nil || *e.ScorePercent != 85 {
		t.Errorf("score_percent = %v, want 85 (8.5/10*100)", e.ScorePercent)
	}
	if e.Passed == nil || !*e.Passed {
		t.Errorf("passed = %v, want true", e.Passed)
	}
	if e.IdempotencyKey != "submission:sub-1:graded" {
		t.Errorf("idempotency_key = %q, wrong", e.IdempotencyKey)
	}
	if e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		t.Error("created_at/updated_at must be stamped")
	}
	if !e.OccurredAt.Equal(time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred_at wrong: %v", e.OccurredAt)
	}
}

func TestNew_ScorePercentNilWhenPossibleZero(t *testing.T) {
	in := validAssessmentInput()
	zero := 0.0
	in.ScorePossible = &zero
	e, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.ScorePercent != nil {
		t.Errorf("score_percent = %v, want nil (possible<=0 guard)", *e.ScorePercent)
	}
}

func TestNew_ScorePercentNilWhenScoresAbsent(t *testing.T) {
	in := validAssessmentInput()
	in.ScoreEarned = nil
	in.ScorePossible = nil
	e, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.ScorePercent != nil {
		t.Errorf("score_percent = %v, want nil (certification entries carry no score)", *e.ScorePercent)
	}
	if e.ScoreEarned != nil || e.ScorePossible != nil {
		t.Error("scores must stay nil when not supplied")
	}
}

func TestNew_CertificationEntry(t *testing.T) {
	in := NewEntryInput{
		TenantID:       ttTenant,
		GCID:           ttGCID,
		Kind:           KindCertification,
		SourceRef:      "cert-1",
		Title:          "course-1",
		CourseID:       "course-1",
		OccurredAt:     time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC),
		IdempotencyKey: "cert:cert-1:issued",
	}
	e, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.Kind != KindCertification {
		t.Errorf("kind = %q, want certification", e.Kind)
	}
	if e.SourceRef != "cert-1" {
		t.Errorf("source_ref = %q, want cert-1", e.SourceRef)
	}
	if e.CourseID != "course-1" {
		t.Errorf("course_id = %q, want course-1", e.CourseID)
	}
	if e.ScoreEarned != nil || e.ScorePossible != nil || e.ScorePercent != nil || e.Passed != nil {
		t.Error("certification entries carry no score/passed")
	}
}

func TestNew_RejectsMissingTenant(t *testing.T) {
	in := validAssessmentInput()
	in.TenantID = ""
	if _, err := New(in); err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
}

func TestNew_RejectsMissingGCID(t *testing.T) {
	in := validAssessmentInput()
	in.GCID = ""
	if _, err := New(in); err == nil {
		t.Fatal("expected error for missing gcid")
	}
}

func TestNew_RejectsInvalidKind(t *testing.T) {
	in := validAssessmentInput()
	in.Kind = Kind("bogus")
	if _, err := New(in); err == nil {
		t.Fatal("expected error for invalid kind")
	}
}

func TestNew_RejectsMissingSourceRef(t *testing.T) {
	in := validAssessmentInput()
	in.SourceRef = ""
	if _, err := New(in); err == nil {
		t.Fatal("expected error for missing source_ref")
	}
}

func TestNew_RejectsMissingIdempotencyKey(t *testing.T) {
	in := validAssessmentInput()
	in.IdempotencyKey = ""
	if _, err := New(in); err == nil {
		t.Fatal("expected error for missing idempotency_key")
	}
}

func TestNew_RejectsZeroOccurredAt(t *testing.T) {
	in := validAssessmentInput()
	in.OccurredAt = time.Time{}
	if _, err := New(in); err == nil {
		t.Fatal("expected error for zero occurred_at")
	}
}

func TestKind_Valid(t *testing.T) {
	if !KindAssessment.Valid() {
		t.Error("assessment must be valid")
	}
	if !KindCertification.Valid() {
		t.Error("certification must be valid")
	}
	if Kind("bogus").Valid() {
		t.Error("unknown kind must be invalid")
	}
}

func TestNew_NowInjectable(t *testing.T) {
	in := validAssessmentInput()
	fixedNow := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	in.Now = fixedNow
	e, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !e.CreatedAt.Equal(fixedNow) || !e.UpdatedAt.Equal(fixedNow) {
		t.Errorf("created_at/updated_at must use injected Now; got %v / %v", e.CreatedAt, e.UpdatedAt)
	}
}
