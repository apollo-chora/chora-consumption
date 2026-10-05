// learner_profile_subscriber_test.go — TDD for the LearnerProfile projection
// subscriber (ADR-200, WS1.c3). One verified Pub/Sub event → one projected Fact
// (+ one activity-log line). The verified-only invariant (every Fact cites the
// source event_id) is asserted per event; redelivery is a no-op.
package subscribers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

const (
	lpTenant  = "01970000-0000-7000-8000-00000000aaaa"
	lpLearner = "01970000-0000-7000-9000-00000000bbbb"
)

// fakeProfileRepo is an in-memory learner_profile.ProjectionRepo for tests.
type fakeProfileRepo struct {
	facts      []*learner_profile.Fact
	activities []*learner_profile.ActivityEntry
	upsertErr  error
	appendErr  error
}

func (f *fakeProfileRepo) UpsertFact(_ context.Context, fact *learner_profile.Fact) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.facts = append(f.facts, fact)
	return nil
}

func (f *fakeProfileRepo) AppendActivity(_ context.Context, e *learner_profile.ActivityEntry) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.activities = append(f.activities, e)
	return nil
}

func (f *fakeProfileRepo) ListFacts(context.Context, string, string) ([]*learner_profile.Fact, error) {
	return f.facts, nil
}

func (f *fakeProfileRepo) RecentActivity(context.Context, string, string, int) ([]*learner_profile.ActivityEntry, error) {
	return f.activities, nil
}

func lpEnv(eventID string) events.Envelope {
	return newTestEnvelope(eventID, lpTenant, lpLearner)
}

func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }
func ptrI(v int) *int         { return &v }

// ---- certification.issued ----

func TestLearnerProfile_CertIssued_ProjectsVerifiedFact(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000001")
	issuedAt := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)

	if err := sub.HandleCertIssued(env, CertIssuedPayload{
		CertID: "cert-1", CourseID: "course-1", LearnerGCID: lpLearner, IssuedAt: issuedAt,
	}); err != nil {
		t.Fatalf("HandleCertIssued: %v", err)
	}
	if len(repo.facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(repo.facts))
	}
	f := repo.facts[0]
	if f.Type != learner_profile.FactCertificationIssued {
		t.Errorf("type: want certification_issued, got %q", f.Type)
	}
	if f.RefID != "cert-1" {
		t.Errorf("ref_id: want cert-1, got %q", f.RefID)
	}
	if f.Detail.Label != "course-1" {
		t.Errorf("detail.label: want course-1, got %q", f.Detail.Label)
	}
	if f.SourceEventID != env.EventID {
		t.Errorf("source_event_id: want %q (verified-only), got %q", env.EventID, f.SourceEventID)
	}
	if !f.OccurredAt.Equal(issuedAt) {
		t.Errorf("occurred_at: want %v, got %v", issuedAt, f.OccurredAt)
	}
	if f.LearnerGCID != lpLearner {
		t.Errorf("learner: want %q, got %q", lpLearner, f.LearnerGCID)
	}
	if len(repo.activities) != 1 {
		t.Fatalf("want 1 activity entry, got %d", len(repo.activities))
	}
}

// ---- learning_path.completed ----

func TestLearnerProfile_PathCompleted_ProjectsFact(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000002")
	completedAt := time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC)

	if err := sub.HandlePathCompleted(env, PathCompletedPayload{
		PathID: "path-1", LearnerGCID: lpLearner, CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("HandlePathCompleted: %v", err)
	}
	if len(repo.facts) != 1 || repo.facts[0].Type != learner_profile.FactPathCompleted || repo.facts[0].RefID != "path-1" {
		t.Fatalf("path fact wrong: %+v", repo.facts)
	}
	if !repo.facts[0].OccurredAt.Equal(completedAt) {
		t.Errorf("occurred_at: want %v, got %v", completedAt, repo.facts[0].OccurredAt)
	}
}

// ---- enrollment.created (JSON) ----

func TestLearnerProfile_EnrollmentCreated_ProjectsFact(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000003")

	if err := sub.HandleEnrollmentCreated(env, EnrollmentCreatedPayload{
		CourseID: "course-9", LearnerGCID: lpLearner,
	}); err != nil {
		t.Fatalf("HandleEnrollmentCreated: %v", err)
	}
	if len(repo.facts) != 1 || repo.facts[0].Type != learner_profile.FactEnrollment || repo.facts[0].RefID != "course-9" {
		t.Fatalf("enrollment fact wrong: %+v", repo.facts)
	}
	// enrollment.created carries no event-specific timestamp → fall back to the
	// envelope's occurred_at (still a verified instant).
	if !repo.facts[0].OccurredAt.Equal(env.OccurredAt) {
		t.Errorf("occurred_at: want envelope occurred_at %v, got %v", env.OccurredAt, repo.facts[0].OccurredAt)
	}
}

// ---- submission.graded ----

func TestLearnerProfile_SubmissionGraded_ProjectsScoredFact(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000004")
	gradedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)

	if err := sub.HandleSubmissionGraded(env, SubmissionGradedPayload{
		SubmissionID: "sub-1", LearnerGCID: lpLearner,
		Score: ptrF(87.5), Passed: ptrB(true), HintCount: ptrI(0), OccurredAt: gradedAt,
	}); err != nil {
		t.Fatalf("HandleSubmissionGraded: %v", err)
	}
	if len(repo.facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(repo.facts))
	}
	f := repo.facts[0]
	if f.Type != learner_profile.FactAssessmentGraded || f.RefID != "sub-1" {
		t.Fatalf("assessment fact wrong: %+v", f)
	}
	if f.Detail.Score == nil || *f.Detail.Score != 87.5 {
		t.Errorf("score: want 87.5, got %v", f.Detail.Score)
	}
	if f.Detail.Passed == nil || !*f.Detail.Passed {
		t.Errorf("passed: want true, got %v", f.Detail.Passed)
	}
	if f.Detail.HintCount == nil || *f.Detail.HintCount != 0 {
		t.Errorf("hint_count: want 0 (first-attempt-mastery anchor), got %v", f.Detail.HintCount)
	}
}

// ---- enrollment.completed ----

func TestLearnerProfile_EnrollmentCompleted_ProjectsCourseCompletedFact(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000005")
	completedAt := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)

	if err := sub.HandleEnrollmentCompleted(env, EnrollmentCompletedPayload{
		CourseID: "course-5", LearnerGCID: lpLearner, Passed: true, CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("HandleEnrollmentCompleted: %v", err)
	}
	if len(repo.facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(repo.facts))
	}
	f := repo.facts[0]
	if f.Type != learner_profile.FactCourseCompleted || f.RefID != "course-5" {
		t.Fatalf("course-completed fact wrong: %+v", f)
	}
	if f.Detail.Passed == nil || !*f.Detail.Passed {
		t.Errorf("passed: want true, got %v", f.Detail.Passed)
	}
}

// ---- cross-cutting: dedup, envelope validation, fail-loud ----

func TestLearnerProfile_Dedup_RedeliveryIsNoOp(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000006")
	p := CertIssuedPayload{CertID: "cert-1", CourseID: "course-1", LearnerGCID: lpLearner, IssuedAt: time.Now().UTC()}

	if err := sub.HandleCertIssued(env, p); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := sub.HandleCertIssued(env, p); err != nil { // same event_id — redelivery
		t.Fatalf("redelivery: %v", err)
	}
	if len(repo.facts) != 1 {
		t.Fatalf("dedup: want 1 fact after redelivery, got %d", len(repo.facts))
	}
}

func TestLearnerProfile_RejectsIncompleteEnvelope(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := events.Envelope{} // missing mandatory fields

	err := sub.HandleCertIssued(env, CertIssuedPayload{CertID: "c", CourseID: "co", LearnerGCID: lpLearner, IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected envelope-validation error")
	}
	if len(repo.facts) != 0 {
		t.Fatalf("must not write on invalid envelope, got %d facts", len(repo.facts))
	}
}

func TestLearnerProfile_UpsertError_PropagatesFailLoud(t *testing.T) {
	repo := &fakeProfileRepo{upsertErr: errors.New("boom")}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000007")

	err := sub.HandleCertIssued(env, CertIssuedPayload{CertID: "c", CourseID: "co", LearnerGCID: lpLearner, IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected upsert error to propagate (fail-loud → Pub/Sub retry → DLQ)")
	}
}

func TestLearnerProfile_MissingLearner_RejectedByVerifiedOnly(t *testing.T) {
	repo := &fakeProfileRepo{}
	sub := NewLearnerProfileSubscriber(repo)
	env := lpEnv("01970000-0000-7000-e000-000000000008")

	// A per-learner event with no learner_gcid cannot project a learner fact —
	// the domain constructor must reject it (verified-only / well-formed).
	err := sub.HandleCertIssued(env, CertIssuedPayload{CertID: "c", CourseID: "co", LearnerGCID: "", IssuedAt: time.Now().UTC()})
	if err == nil {
		t.Fatal("expected rejection when learner_gcid is empty")
	}
	if len(repo.facts) != 0 {
		t.Fatalf("must not write a fact with no learner, got %d", len(repo.facts))
	}
}
