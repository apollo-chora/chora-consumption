// transcript_subscriber_test.go — TDD for the StudentTranscript projection
// subscriber (W6 Slice 1). Fanned out from the SAME push endpoints the
// LearnerProfile projection uses (chora.delivery.submission.graded.v1,
// chora.delivery.certification.issued.v1) — no new Pub/Sub subscriptions.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

const (
	ttTenant  = "01970000-0000-7000-8000-00000000cccc"
	ttLearner = "01970000-0000-7000-9000-00000000dddd"
)

// fakeTranscriptRepo is an in-memory student_transcript.Repository for tests.
// Upsert mirrors the pg adapter's ON CONFLICT (tenant_id, idempotency_key)
// semantics so redelivery/re-grade tests can assert row convergence.
type fakeTranscriptRepo struct {
	entries   []*st.TranscriptEntry
	upsertErr error
}

func (f *fakeTranscriptRepo) Upsert(_ context.Context, e *st.TranscriptEntry) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for i, existing := range f.entries {
		if existing.TenantID == e.TenantID && existing.IdempotencyKey == e.IdempotencyKey {
			f.entries[i] = e
			return nil
		}
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeTranscriptRepo) ListByGCID(context.Context, string, string, int) ([]*st.TranscriptEntry, error) {
	return f.entries, nil
}

func (f *fakeTranscriptRepo) CountUnseenByGCID(_ context.Context, _, gcid string) (int, error) {
	n := 0
	for _, e := range f.entries {
		if e.GCID == gcid && e.Unseen() {
			n++
		}
	}
	return n, nil
}

func (f *fakeTranscriptRepo) ListByAssessmentIDs(context.Context, string, []string) ([]*st.TranscriptEntry, error) {
	return f.entries, nil
}

func ttEnv(eventID string) events.Envelope {
	return newTestEnvelope(eventID, ttTenant, ttLearner)
}

func ptrF64(v float64) *float64 { return &v }
func ptrBool(v bool) *bool      { return &v }

// ---- submission.graded ----

func TestTranscript_SubmissionGraded_ProjectsAssessmentEntry(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000101")
	gradedAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	err := sub.HandleSubmissionGraded(env, TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-1", AssessmentID: "assess-1", AssessmentTitle: "Algebra Quiz",
		LearnerGCID: ttLearner, ScoreEarned: ptrF64(8.5), ScorePossible: ptrF64(10),
		Passed: ptrBool(true), OccurredAt: gradedAt,
	})
	if err != nil {
		t.Fatalf("HandleSubmissionGraded: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(repo.entries))
	}
	e := repo.entries[0]
	if e.Kind != st.KindAssessment || e.SourceRef != "assess-1" {
		t.Errorf("entry wrong: %+v", e)
	}
	if e.Title != "Algebra Quiz" {
		t.Errorf("title = %q, want Algebra Quiz", e.Title)
	}
	if e.ScorePercent == nil || *e.ScorePercent != 85 {
		t.Errorf("score_percent = %v, want 85", e.ScorePercent)
	}
	if e.GCID != ttLearner {
		t.Errorf("gcid = %q, want %q", e.GCID, ttLearner)
	}
	if !e.OccurredAt.Equal(gradedAt) {
		t.Errorf("occurred_at = %v, want %v", e.OccurredAt, gradedAt)
	}
	if e.IdempotencyKey != "submission:sub-1:graded" {
		t.Errorf("idempotency_key = %q, want submission:sub-1:graded", e.IdempotencyKey)
	}
}

func TestTranscript_SubmissionGraded_IdempotentOnRedelivery(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000102")
	p := TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-2", AssessmentID: "assess-2", LearnerGCID: ttLearner,
		ScoreEarned: ptrF64(5), ScorePossible: ptrF64(10), OccurredAt: time.Now().UTC(),
	}
	if err := sub.HandleSubmissionGraded(env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleSubmissionGraded(env, p); err != nil { // redelivery, same submission
		t.Fatalf("redelivery: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("dedup: want 1 entry after redelivery, got %d", len(repo.entries))
	}
}

func TestTranscript_SubmissionGraded_RegradeUpdatesSameRow(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env1 := ttEnv("01970000-0000-7000-e000-000000000103")
	if err := sub.HandleSubmissionGraded(env1, TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-3", AssessmentID: "assess-3", LearnerGCID: ttLearner,
		ScoreEarned: ptrF64(5), ScorePossible: ptrF64(10), OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("first grade: %v", err)
	}
	// Re-grade: SAME submission_id, a DIFFERENT event_id (fresh delivery,
	// fresh score) — must UPDATE the same row, not duplicate it.
	env2 := ttEnv("01970000-0000-7000-e000-000000000104")
	if err := sub.HandleSubmissionGraded(env2, TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-3", AssessmentID: "assess-3", LearnerGCID: ttLearner,
		ScoreEarned: ptrF64(9), ScorePossible: ptrF64(10), OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("re-grade: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("re-grade: want 1 entry (updated in place), got %d", len(repo.entries))
	}
	if repo.entries[0].ScoreEarned == nil || *repo.entries[0].ScoreEarned != 9 {
		t.Errorf("re-grade must update the score: got %v", repo.entries[0].ScoreEarned)
	}
}

func TestTranscript_SubmissionGraded_FallsBackToSubmissionIDWhenAssessmentIDMissing(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000105")
	if err := sub.HandleSubmissionGraded(env, TranscriptSubmissionGradedPayload{
		SubmissionID: "sub-4", LearnerGCID: ttLearner, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("HandleSubmissionGraded: %v", err)
	}
	if repo.entries[0].SourceRef != "sub-4" {
		t.Errorf("source_ref fallback = %q, want sub-4", repo.entries[0].SourceRef)
	}
}

func TestTranscript_SubmissionGraded_RejectsMissingSubmissionID(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000106")
	err := sub.HandleSubmissionGraded(env, TranscriptSubmissionGradedPayload{LearnerGCID: ttLearner, OccurredAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected error for missing submission_id")
	}
	if len(repo.entries) != 0 {
		t.Errorf("must not write, got %d entries", len(repo.entries))
	}
}

func TestTranscript_SubmissionGraded_RejectsIncompleteEnvelope(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	err := sub.HandleSubmissionGraded(events.Envelope{}, TranscriptSubmissionGradedPayload{SubmissionID: "s", LearnerGCID: ttLearner, OccurredAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected envelope-validation error")
	}
	if len(repo.entries) != 0 {
		t.Errorf("must not write on invalid envelope, got %d entries", len(repo.entries))
	}
}

func TestTranscript_SubmissionGraded_UpsertErrorPropagatesFailLoud(t *testing.T) {
	repo := &fakeTranscriptRepo{upsertErr: errors.New("boom")}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000107")
	err := sub.HandleSubmissionGraded(env, TranscriptSubmissionGradedPayload{SubmissionID: "s", LearnerGCID: ttLearner, OccurredAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected upsert error to propagate (fail-loud -> Pub/Sub retry -> DLQ)")
	}
}

// ---- certification.issued ----

func TestTranscript_CertIssued_ProjectsCertificationEntry(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000201")
	issuedAt := time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC)

	if err := sub.HandleCertIssued(env, TranscriptCertIssuedPayload{
		CertID: "cert-1", CourseID: "course-1", LearnerGCID: ttLearner, IssuedAt: issuedAt,
	}); err != nil {
		t.Fatalf("HandleCertIssued: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(repo.entries))
	}
	e := repo.entries[0]
	if e.Kind != st.KindCertification || e.SourceRef != "cert-1" {
		t.Errorf("entry wrong: %+v", e)
	}
	if e.CourseID != "course-1" || e.Title != "course-1" {
		t.Errorf("course_id/title = %q/%q, want course-1/course-1", e.CourseID, e.Title)
	}
	if !e.OccurredAt.Equal(issuedAt) {
		t.Errorf("occurred_at = %v, want %v", e.OccurredAt, issuedAt)
	}
	if e.IdempotencyKey != "cert:cert-1:issued" {
		t.Errorf("idempotency_key = %q, want cert:cert-1:issued", e.IdempotencyKey)
	}
}

func TestTranscript_CertIssued_IdempotentOnRedelivery(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000202")
	p := TranscriptCertIssuedPayload{CertID: "cert-2", CourseID: "course-2", LearnerGCID: ttLearner, IssuedAt: time.Now().UTC()}
	if err := sub.HandleCertIssued(env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleCertIssued(env, p); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(repo.entries) != 1 {
		t.Fatalf("dedup: want 1 entry, got %d", len(repo.entries))
	}
}

func TestTranscript_CertIssued_RejectsMissingCertID(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000203")
	err := sub.HandleCertIssued(env, TranscriptCertIssuedPayload{CourseID: "course-3", LearnerGCID: ttLearner, IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected error for missing cert_id")
	}
	if len(repo.entries) != 0 {
		t.Errorf("must not write, got %d entries", len(repo.entries))
	}
}

func TestTranscript_CertIssued_RejectsIncompleteEnvelope(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	err := sub.HandleCertIssued(events.Envelope{}, TranscriptCertIssuedPayload{CertID: "c", LearnerGCID: ttLearner, IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected envelope-validation error")
	}
	if len(repo.entries) != 0 {
		t.Errorf("must not write on invalid envelope, got %d entries", len(repo.entries))
	}
}

func TestTranscript_CertIssued_MissingLearnerGCIDRejectedByDomain(t *testing.T) {
	repo := &fakeTranscriptRepo{}
	sub := NewTranscriptSubscriber(repo)
	env := ttEnv("01970000-0000-7000-e000-000000000204")
	err := sub.HandleCertIssued(env, TranscriptCertIssuedPayload{CertID: "cert-4", CourseID: "course-4", LearnerGCID: "", IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected rejection when learner_gcid is empty")
	}
	if len(repo.entries) != 0 {
		t.Errorf("must not write a entry with no learner, got %d", len(repo.entries))
	}
}
