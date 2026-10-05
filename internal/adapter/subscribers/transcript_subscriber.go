// transcript_subscriber.go — projects verified per-learner graded-outcome
// events into the StudentTranscript read-model (W6 Slice 1, Four-Mode plan
// "Outcome spine").
//
// chora-consumption never queries chora_delivery directly (ddd-enforcement
// #1); the transcript is fed ONLY by verified Pub/Sub events FANNED OUT from
// the SAME push endpoints the LearnerProfile projection already owns (see
// adapter/http/learner_profile_push_handler.go) — no new Pub/Sub
// subscriptions were provisioned for this slice.
//
// Idempotency: unlike LearnerProfileSubscriber (which layers an in-process
// idempotencyTracker in front of an append-only activity log keyed on
// event_id), this subscriber needs no such tracker — student_transcript.
// Repository.Upsert is ALREADY fully idempotent at the DB layer via
// ON CONFLICT (tenant_id, idempotency_key), and idempotency_key here is a
// DETERMINISTIC natural key derived from the source aggregate
// ("submission:<id>:graded" / "cert:<id>:issued"), NOT the volatile
// event_id. That means both a true Pub/Sub redelivery (same event_id) AND a
// legitimate re-grade of the same submission (fresh event_id, same
// submission_id) converge on the SAME row — exactly the semantics a
// per-outcome transcript ledger needs (a re-grade should UPDATE, not
// duplicate).
package subscribers

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

// TranscriptSubscriber projects verified per-learner graded-outcome events
// into the StudentTranscript read-model.
type TranscriptSubscriber struct {
	repo st.Repository
}

// NewTranscriptSubscriber constructs the subscriber over a Repository.
func NewTranscriptSubscriber(repo st.Repository) *TranscriptSubscriber {
	return &TranscriptSubscriber{repo: repo}
}

// TranscriptSubmissionGradedPayload carries the fields the TranscriptSubscriber
// needs from chora.delivery.submission.graded.v1 beyond what the
// LearnerProfile path uses: raw earned/possible (not just the computed
// percentage) plus the assessment identity + title. AssessmentTitle IS now
// carried on submission.graded.v1 (deliveryv1.SubmissionGraded field 13,
// assessment_title) and persisted into student_transcript_entries.title. It
// stays decode-gap tolerant: an absent/empty value simply yields an empty
// transcript Title rather than failing the projection.
type TranscriptSubmissionGradedPayload struct {
	SubmissionID    string
	AssessmentID    string
	AssessmentTitle string
	LearnerGCID     string
	// DeliveryType is the parent Offering's mode, snapshotted at grade time
	// (deliveryv1.SubmissionGraded field 14, CHO-2224). It is the entry's ONLY
	// mode-bearing signal, and the axis §10.6 criterion 1 turns on: without it a
	// graduate assessment and a short-course grade are both kind='assessment'.
	//
	// Raw off the wire and NOT trusted: chora_delivery owns this vocabulary, so
	// an unrecognised value is normalised to "" (and warned about) rather than
	// rejected. Empty/absent = genuinely unattributable.
	DeliveryType  string
	ScoreEarned   *float64
	ScorePossible *float64
	Passed        *bool
	OccurredAt    time.Time
}

// TranscriptCertIssuedPayload mirrors chora.delivery.certification.issued.v1.
type TranscriptCertIssuedPayload struct {
	CertID      string
	CourseID    string
	LearnerGCID string
	IssuedAt    time.Time
}

// HandleSubmissionGraded upserts an `assessment` transcript entry (ref =
// assessment_id, falling back to submission_id if the wire ever omits
// assessment_id — defensive, since the entry must still land rather than be
// silently dropped).
func (s *TranscriptSubscriber) HandleSubmissionGraded(env events.Envelope, p TranscriptSubmissionGradedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if p.SubmissionID == "" {
		return fmt.Errorf("transcript_subscriber: submission_id required")
	}
	sourceRef := p.AssessmentID
	if sourceRef == "" {
		sourceRef = p.SubmissionID
	}
	occurred := p.OccurredAt
	if occurred.IsZero() {
		occurred = env.OccurredAt
	}
	entry, err := st.New(st.NewEntryInput{
		TenantID:       env.TenantID,
		GCID:           p.LearnerGCID,
		Kind:           st.KindAssessment,
		SourceRef:      sourceRef,
		Title:          p.AssessmentTitle,
		DeliveryType:   deliveryTypeOrWarn(p.DeliveryType, p.SubmissionID),
		ScoreEarned:    p.ScoreEarned,
		ScorePossible:  p.ScorePossible,
		Passed:         p.Passed,
		OccurredAt:     occurred,
		IdempotencyKey: "submission:" + p.SubmissionID + ":graded",
	})
	if err != nil {
		return fmt.Errorf("transcript_subscriber submission_graded: %w", err)
	}
	// RLS: the pg Repository reads the tenant from the CONTEXT (SET LOCAL
	// chora.tenant_id), not an explicit arg — stamp the envelope tenant so the
	// projection lands under the right tenant (mirrors learner_profile_subscriber).
	ctx := tracing.WithTenantID(context.Background(), env.TenantID)
	if err := s.repo.Upsert(ctx, entry); err != nil {
		return fmt.Errorf("transcript_subscriber submission_graded upsert: %w", err)
	}
	return nil
}

// deliveryTypeOrWarn converts the wire's raw delivery_type into the domain type,
// normalising anything unrecognised to "" and LOGGING LOUDLY when it does
// (CHO-2224).
//
// The warn lives HERE, at the adapter, not in the domain: student_transcript is
// stdlib-only with no infra imports, and a projector that silently swallowed an
// unknown mode would hide exactly the drift worth seeing. The rule itself
// (DeliveryType.Valid) stays in the domain and has ONE copy, so this cannot
// drift from what New enforces.
//
// It deliberately does NOT return an error. chora_delivery owns this vocabulary
// and offerings.delivery_type has no DB CHECK, so a 4th mode or a typo is
// reachable; erroring would NACK the event, dead-letter it, and lose the
// learner's GRADE over a label. Keep the row, drop the label, make noise.
func deliveryTypeOrWarn(raw, submissionID string) st.DeliveryType {
	dt := st.DeliveryType(strings.TrimSpace(raw))
	if dt == "" {
		// Genuinely unattributable: a freestanding assessment (no Offering), or an
		// event published before field 14 existed. Not noteworthy.
		return ""
	}
	if !dt.Valid() {
		log.Printf("transcript_subscriber: UNRECOGNISED delivery_type %q on submission %s — projecting the grade with NO mode. chora_delivery may have added a mode this projector does not know (student_transcript.DeliveryType); the transcript entry is unattributed until it does.", raw, submissionID)
		return ""
	}
	return dt
}

// HandleCertIssued upserts a `certification` transcript entry (ref =
// cert_id). Title falls back to the course_id label — certifications carry
// no dedicated title field on the wire (mirrors learner_profile.
// HandleCertIssued's Detail.Label handling of the same event).
//
// ⚠ KNOWN GAP (CHO-2224): cert entries carry NO delivery_type.
// chora.delivery.certification.issued.v1 has no mode on the wire — its
// CertType {COMPLETION|COMPETENCY|ACCREDITED|MICRO_CREDENTIAL} is a CREDENTIAL
// type, orthogonal to a delivery mode — and the Offering aggregate publishes no
// events at all, so there is nothing to project a mode from. §10.6 criterion 1
// needs >=2 modes and the two assessment lanes supply them, so attributing the
// cert lane would cost a SECOND live Pub/Sub schema revision for no additional
// criterion. Deliberately deferred, not overlooked.
func (s *TranscriptSubscriber) HandleCertIssued(env events.Envelope, p TranscriptCertIssuedPayload) error {
	if err := validateInboundEnvelope(env); err != nil {
		return err
	}
	if p.CertID == "" {
		return fmt.Errorf("transcript_subscriber: cert_id required")
	}
	occurred := p.IssuedAt
	if occurred.IsZero() {
		occurred = env.OccurredAt
	}
	entry, err := st.New(st.NewEntryInput{
		TenantID:       env.TenantID,
		GCID:           p.LearnerGCID,
		Kind:           st.KindCertification,
		SourceRef:      p.CertID,
		Title:          p.CourseID,
		CourseID:       p.CourseID,
		OccurredAt:     occurred,
		IdempotencyKey: "cert:" + p.CertID + ":issued",
	})
	if err != nil {
		return fmt.Errorf("transcript_subscriber cert_issued: %w", err)
	}
	ctx := tracing.WithTenantID(context.Background(), env.TenantID)
	if err := s.repo.Upsert(ctx, entry); err != nil {
		return fmt.Errorf("transcript_subscriber cert_issued upsert: %w", err)
	}
	return nil
}
