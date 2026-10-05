// learner_profile_subscriber.go — projects the per-learner verified events into
// the global LearnerProfile read-model (ADR-200, WS1.c3).
//
// chora-consumption never queries chora_delivery / chora_identity directly
// (ddd-enforcement #1); the profile is fed ONLY by verified Pub/Sub events. Each
// handler maps one event → one Fact (+ one activity-log line), both carrying the
// source event_id so the verified-only invariant (ADR-203 anti-gaming) holds by
// construction — there is no self-declaration path into the profile.
//
// Delivery semantics: each event is processed inside the idempotency Store's
// claim-run-persist (Process) so the dedup key is recorded ONLY after the write
// succeeds. A transient repo error therefore leaves the key unclaimed → the
// Pub/Sub redelivery re-runs (fail-loud, no silent loss), while a successful
// redelivery is a true no-op. The pg ProjectionRepo is additionally idempotent
// on (natural key, source_event_id), so cross-replica redelivery converges too.
package subscribers

import (
	"context"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/domain/learner_profile"
)

// LearnerProfileSubscriber projects verified per-learner events into the
// LearnerProfile read-model.
type LearnerProfileSubscriber struct {
	repo    learner_profile.ProjectionRepo
	tracker *idempotencyTracker
}

// NewLearnerProfileSubscriber constructs the subscriber over a ProjectionRepo.
func NewLearnerProfileSubscriber(repo learner_profile.ProjectionRepo) *LearnerProfileSubscriber {
	return &LearnerProfileSubscriber{repo: repo, tracker: newIdempotencyTracker()}
}

// --- per-event payloads (built by the push handlers from the decoded event) ---

// CertIssuedPayload mirrors chora.delivery.certification.issued.v1 (CertIssued).
type CertIssuedPayload struct {
	CertID      string
	CourseID    string
	LearnerGCID string
	IssuedAt    time.Time
}

// PathCompletedPayload mirrors chora.consumption.learning_path.completed.v1.
type PathCompletedPayload struct {
	PathID      string
	LearnerGCID string
	CompletedAt time.Time
}

// (enrollment.created reuses the existing EnrollmentCreatedPayload declared in
// subscribers.go — the same chora.delivery.enrollment.created.v1 event.)

// SubmissionGradedPayload mirrors chora.delivery.submission.graded.v1. Score is
// the percentage (earned/possible*100) computed at decode; HintCount=0 anchors
// the ADR-203 first-attempt-mastery bonus.
type SubmissionGradedPayload struct {
	SubmissionID string
	LearnerGCID  string
	Score        *float64
	Passed       *bool
	HintCount    *int
	OccurredAt   time.Time
}

// EnrollmentCompletedPayload mirrors chora.delivery.enrollment.completed.v1.
type EnrollmentCompletedPayload struct {
	CourseID    string
	LearnerGCID string
	Passed      bool
	CompletedAt time.Time
}

// HandleCertIssued projects a certification_issued fact (ref = cert_id; the
// course_id is carried as the human label).
func (s *LearnerProfileSubscriber) HandleCertIssued(env events.Envelope, p CertIssuedPayload) error {
	return s.project(env, projectSpec{
		learnerGCID:  p.LearnerGCID,
		factType:     learner_profile.FactCertificationIssued,
		refID:        p.CertID,
		detail:       learner_profile.Detail{Label: p.CourseID},
		occurredAt:   p.IssuedAt,
		activityKind: "earned_certification",
		summary:      "Earned a certification (course " + p.CourseID + ")",
	})
}

// HandlePathCompleted projects a path_completed fact (ref = path_id).
func (s *LearnerProfileSubscriber) HandlePathCompleted(env events.Envelope, p PathCompletedPayload) error {
	return s.project(env, projectSpec{
		learnerGCID:  p.LearnerGCID,
		factType:     learner_profile.FactPathCompleted,
		refID:        p.PathID,
		occurredAt:   p.CompletedAt,
		activityKind: "completed_path",
		summary:      "Completed learning path " + p.PathID,
	})
}

// HandleEnrollmentCreated projects an enrollment fact (ref = course_id). When the
// event carries no enrolled_at the verified instant falls back to the envelope's
// occurred_at (handled in project).
func (s *LearnerProfileSubscriber) HandleEnrollmentCreated(env events.Envelope, p EnrollmentCreatedPayload) error {
	return s.project(env, projectSpec{
		learnerGCID:  p.LearnerGCID,
		factType:     learner_profile.FactEnrollment,
		refID:        p.CourseID,
		occurredAt:   p.EnrolledAt,
		activityKind: "enrolled",
		summary:      "Enrolled in course " + p.CourseID,
	})
}

// HandleSubmissionGraded projects an assessment_graded fact (ref = submission_id)
// carrying the graded outcome (score / passed / hint_count).
func (s *LearnerProfileSubscriber) HandleSubmissionGraded(env events.Envelope, p SubmissionGradedPayload) error {
	return s.project(env, projectSpec{
		learnerGCID:  p.LearnerGCID,
		factType:     learner_profile.FactAssessmentGraded,
		refID:        p.SubmissionID,
		detail:       learner_profile.Detail{Score: p.Score, Passed: p.Passed, HintCount: p.HintCount},
		occurredAt:   p.OccurredAt,
		activityKind: "scored_assessment",
		summary:      "Was graded on assessment submission " + p.SubmissionID,
	})
}

// HandleEnrollmentCompleted projects a course_completed fact (ref = course_id).
func (s *LearnerProfileSubscriber) HandleEnrollmentCompleted(env events.Envelope, p EnrollmentCompletedPayload) error {
	passed := p.Passed
	return s.project(env, projectSpec{
		learnerGCID:  p.LearnerGCID,
		factType:     learner_profile.FactCourseCompleted,
		refID:        p.CourseID,
		detail:       learner_profile.Detail{Passed: &passed},
		occurredAt:   p.CompletedAt,
		activityKind: "completed_course",
		summary:      "Completed course " + p.CourseID,
	})
}

// projectSpec captures the per-event mapping inputs for project.
type projectSpec struct {
	learnerGCID  string
	factType     learner_profile.FactType
	refID        string
	detail       learner_profile.Detail
	occurredAt   time.Time
	activityKind string
	summary      string
}

// project is the shared verified-only projection: validate the envelope, then
// (claim-run-persist) build + upsert the Fact and append the activity line. The
// write runs inside the idempotency Store so the dedup key commits only on
// success.
func (s *LearnerProfileSubscriber) project(env events.Envelope, spec projectSpec) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	occurred := spec.occurredAt
	if occurred.IsZero() {
		occurred = env.OccurredAt
	}
	// RLS: the pg ProjectionRepo reads the tenant from the CONTEXT (SET LOCAL
	// chora.tenant_id), not an explicit arg — stamp the envelope tenant so the
	// projection lands under the right tenant (mirrors course_content_subscriber).
	ctx := tracing.WithTenantID(context.Background(), env.TenantID)

	return s.processOnce(env.EventID, func() error {
		fact, err := learner_profile.New(learner_profile.NewFactInput{
			TenantID:      env.TenantID,
			LearnerGCID:   spec.learnerGCID,
			Type:          spec.factType,
			RefID:         spec.refID,
			Detail:        spec.detail,
			SourceEventID: env.EventID,
			OccurredAt:    occurred,
		})
		if err != nil {
			return fmt.Errorf("learner_profile project %s: %w", spec.factType, err)
		}
		if err := s.repo.UpsertFact(ctx, fact); err != nil {
			return fmt.Errorf("learner_profile upsert %s: %w", spec.factType, err)
		}
		act, err := learner_profile.NewActivity(learner_profile.NewActivityInput{
			TenantID:      env.TenantID,
			LearnerGCID:   spec.learnerGCID,
			Kind:          spec.activityKind,
			Summary:       spec.summary,
			RefID:         spec.refID,
			SourceEventID: env.EventID,
			OccurredAt:    occurred,
		})
		if err != nil {
			return fmt.Errorf("learner_profile activity %s: %w", spec.factType, err)
		}
		return s.repo.AppendActivity(ctx, act)
	})
}

// processOnce runs fn under the idempotency Store's claim-run-persist so the
// dedup key is recorded only after fn succeeds (a failed write is retried by
// Pub/Sub, never silently dropped). An empty event_id has no dedup key and just
// runs fn (matches the tracker's markSeen short-circuit).
func (s *LearnerProfileSubscriber) processOnce(eventID string, fn func() error) error {
	if eventID == "" {
		return fn()
	}
	return s.tracker.store.Process(context.Background(), "envID:"+eventID, s.tracker.ttl, fn)
}
