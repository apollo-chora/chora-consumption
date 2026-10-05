// projectors_tail_cover_test.go — statement coverage for the projector +
// decoder tails not driven by the round-trip / cover tests:
//
//   - every registered binary topic's `proto.Unmarshal` error arm (feed bytes
//     that are neither valid proto nor valid JSON → the decode closure's
//     `return nil, err` fires).
//   - the `!ok || m == nil` type-assertion guard of the delivery / consumption
//     / collection projectors that the existing wrong-type test did not reach.
//   - the conditional traceparent / tracestate / idempotency_key / graded_at /
//     completed_at projection arms only covered when the fields are populated.
//   - warnUnknownAtomTypeOnce's already-warned early return.
//
// White-box (package protodecode) so the unexported projectors are reachable
// directly, mirroring protodecode_cover_test.go.
package protodecode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/creation/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"
)

// The eight registered binary topics whose in-decoder `proto.Unmarshal` error
// arm (return nil, err) is not exercised by the round-trip tests. Feeding bytes
// that are invalid proto for the type AND invalid JSON forces that arm.
func TestDecode_BinaryTopics_InvalidProtoFailsLoud(t *testing.T) {
	topics := []string{
		"chora.delivery.certification.issued.v1",
		"chora.consumption.learning_path.completed.v1",
		"chora.delivery.submission.graded.v1",
		"chora.delivery.enrollment.completed.v1",
		"chora.delivery.course.created.v1",
		"chora.delivery.course.released.v1",
		"chora.delivery.live_quiz_session.score_awarded.v1",
		"chora.creation.collection.converted_to_study_list.v1",
	}
	garbage := []byte{0xff, 0xfe, 0xfd} // invalid proto + invalid JSON
	for _, topic := range topics {
		t.Run(topic, func(t *testing.T) {
			_, err := DecodePayloadMap(topic, garbage)
			require.Error(t, err)
			// The error must still name the topic so a mis-wired decoder
			// (byte-valid for a different type) is diagnosable.
			assert.Contains(t, err.Error(), topic)
		})
	}
}

// TestProjectors_RemainingWrongTypeGuards feeds a wrong concrete type to each
// projector whose `!ok || m == nil` guard the existing test did not cover. The
// projector must write NOTHING and must not panic.
func TestProjectors_RemainingWrongTypeGuards(t *testing.T) {
	wrong := &creationv1.AtomUpdated{AtomId: "should-be-ignored"}
	type check struct {
		name string
		fn   projector
	}
	cases := []check{
		{"liveQuizScoreAwarded", projectLiveQuizScoreAwarded},
		{"collectionConvertedToStudyList", projectCollectionConvertedToStudyList},
		{"certIssued", projectCertIssued},
		{"pathCompleted", projectPathCompleted},
		{"submissionGraded", projectSubmissionGraded},
		{"enrollmentCompleted", projectEnrollmentCompleted},
		{"courseCreated", projectCourseCreated},
		{"courseReleased", projectCourseReleased},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := map[string]any{}
			tc.fn(wrong, out)
			assert.Empty(t, out)
		})
	}
}

// TestProjectCollectionConvertedToStudyList_Full populates every conditional arm
// of the study-list projector (incl. tracestate + idempotency_key, which stay
// absent when the envelope lacks them) and the []string atom_ids copy.
func TestProjectCollectionConvertedToStudyList_Full(t *testing.T) {
	m := &creationv1.CollectionConvertedToStudyList{
		Envelope: &commonv1.EventEnvelope{
			EventId:        "evt-1",
			TenantId:       "tenant-acme",
			Gcid:           "gcid-owner",
			Traceparent:    "00-trace-span-01",
			Tracestate:     "ts=1",
			IdempotencyKey: "idem-collect-1",
		},
		CollectionId:     "col-1",
		OwnerGcid:        "gcid-owner",
		StudyListEventId: "evt-1",
		AtomIds:          []string{"a1", "a2"},
	}
	out := map[string]any{}
	projectCollectionConvertedToStudyList(m, out)

	assert.Equal(t, "evt-1", out["event_id"])
	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-owner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "ts=1", out["tracestate"])
	assert.Equal(t, "idem-collect-1", out["idempotency_key"])
	assert.Equal(t, "col-1", out["collection_id"])
	assert.Equal(t, "gcid-owner", out["owner_gcid"])
	assert.Equal(t, "evt-1", out["study_list_event_id"])
	require.IsType(t, []string{}, out["atom_ids"])
	assert.Equal(t, []string{"a1", "a2"}, out["atom_ids"])
}

// TestProjectLiveQuizScoreAwarded_GucPopulated drives the full projection AND
// the conditional arms (envelope + session/live_quiz/question/atom ids + tags).
func TestProjectLiveQuizScoreAwarded_Full(t *testing.T) {
	m := &deliveryv1.LiveQuizSessionScoreAwarded{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-trace-span-01",
		},
		SessionId:       "session-1",
		LiveQuizId:      "quiz-1",
		QuestionId:      "q-1",
		AtomId:          "atom-1",
		TopicTags:       []string{"algebra"},
		Correct:         true,
		AwardedPoints:   12,
		CumulativeScore: 42,
	}
	out := map[string]any{}
	projectLiveQuizScoreAwarded(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "session-1", out["session_id"])
	assert.Equal(t, "quiz-1", out["live_quiz_id"])
	assert.Equal(t, "q-1", out["question_id"])
	assert.Equal(t, "atom-1", out["atom_id"])
	assert.Equal(t, []string{"algebra"}, out["topic_tags"])
	assert.Equal(t, true, out["correct"])
	assert.Equal(t, float64(12), out["awarded_points"])
	assert.Equal(t, float64(42), out["cumulative_score"])
}

// TestProjectCertIssued_Full drives all conditional arms of the cert issuer
// projector (envelope + cert/learner/course ids + issued_at timestamp).
func TestProjectCertIssued_Full(t *testing.T) {
	m := &deliveryv1.CertIssued{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-trace-span-01",
		},
		CertId:      "cert-1",
		LearnerGcid: "gcid-learner",
		CourseId:    "course-1",
		IssuedAt:    timestamppb.New(fixedTime),
	}
	out := map[string]any{}
	projectCertIssued(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "cert-1", out["cert_id"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, "course-1", out["course_id"])
	assert.Equal(t, "2026-05-16T09:30:00.123456789Z", out["issued_at"])
}

// TestProjectPathCompleted_TraceparentArm covers the envelope + path fields
// (the traceparent conditional arm was previously untested).
func TestProjectPathCompleted_Full(t *testing.T) {
	m := &consumptionv1.PathCompleted{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-trace-span-01",
		},
		PathId:      "path-1",
		LearnerGcid: "gcid-learner",
		CompletedAt: timestamppb.New(fixedTime),
	}
	out := map[string]any{}
	projectPathCompleted(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "path-1", out["path_id"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, "2026-05-16T09:30:00.123456789Z", out["completed_at"])
}

// TestProjectSubmissionGraded_Full covers the delivery_type + graded_at arms
// and the always-projected numeric/bool fields.
func TestProjectSubmissionGraded_Full(t *testing.T) {
	m := &deliveryv1.SubmissionGraded{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-trace-span-01",
		},
		SubmissionId:        "sub-1",
		AssessmentId:        "assess-1",
		LearnerGcid:         "gcid-learner",
		TotalPointsEarned:   8,
		TotalPointsPossible: 10,
		Passed:              true,
		AssessmentTitle:     "Fractions Quiz",
		DeliveryType:        "in_class",
		GradedAt:            timestamppb.New(fixedTime),
	}
	out := map[string]any{}
	projectSubmissionGraded(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "sub-1", out["submission_id"])
	assert.Equal(t, "assess-1", out["assessment_id"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, float64(8), out["total_points_earned"])
	assert.Equal(t, 10, out["total_points_possible"])
	assert.Equal(t, true, out["passed"])
	assert.Equal(t, "Fractions Quiz", out["assessment_title"])
	assert.Equal(t, "in_class", out["delivery_type"])
	assert.Equal(t, "2026-05-16T09:30:00.123456789Z", out["graded_at"])
}

// TestProjectEnrollmentCompleted_Full drives the envelope + course/learner arms
// (line traceparent conditional arm covered here) and always-projected passed.
func TestProjectEnrollmentCompleted_Full(t *testing.T) {
	m := &deliveryv1.EnrollmentCompleted{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-trace-span-01",
		},
		EnrollmentId: "enroll-1",
		LearnerGcid:  "gcid-learner",
		CourseId:     "course-1",
		Passed:       true,
		CompletedAt:  timestamppb.New(fixedTime),
	}
	out := map[string]any{}
	projectEnrollmentCompleted(m, out)

	assert.Equal(t, "tenant-acme", out["tenant_id"])
	assert.Equal(t, "gcid-learner", out["gcid"])
	assert.Equal(t, "00-trace-span-01", out["traceparent"])
	assert.Equal(t, "enroll-1", out["enrollment_id"])
	assert.Equal(t, "gcid-learner", out["learner_gcid"])
	assert.Equal(t, "course-1", out["course_id"])
	assert.Equal(t, true, out["passed"])
	assert.Equal(t, "2026-05-16T09:30:00.123456789Z", out["completed_at"])
}

// TestProjectCourseCreatedAndReleased_Full drives the course_id→title projects
// (covering the wrong-type guard via TestProjectors_RemainingWrongTypeGuards and
// their envelope + field arms here).
func TestProjectCourseCreatedAndReleased_Full(t *testing.T) {
	createdEnv := &commonv1.EventEnvelope{TenantId: "tenant-acme", Traceparent: "00-trace-span-01"}
	createdOut := map[string]any{}
	projectCourseCreated(&deliveryv1.CourseCreated{
		Envelope: createdEnv, CourseId: "course-1", Title: "Algebra I",
	}, createdOut)
	assert.Equal(t, "tenant-acme", createdOut["tenant_id"])
	assert.Equal(t, "00-trace-span-01", createdOut["traceparent"])
	assert.Equal(t, "course-1", createdOut["course_id"])
	assert.Equal(t, "Algebra I", createdOut["title"])

	releasedOut := map[string]any{}
	projectCourseReleased(&deliveryv1.CourseReleased{
		Envelope: createdEnv, CourseId: "course-2", Title: "Fractions",
	}, releasedOut)
	assert.Equal(t, "tenant-acme", releasedOut["tenant_id"])
	assert.Equal(t, "00-trace-span-01", releasedOut["traceparent"])
	assert.Equal(t, "course-2", releasedOut["course_id"])
	assert.Equal(t, "Fractions", releasedOut["title"])
}

// TestWarnUnknownAtomTypeOnce_AlreadyWarned covers the already-warned early
// return: a repeated unknown value must take the `if warnedAtomTypes[v]` branch
// (second call) and still return "" from atomTypeToDomain.
func TestWarnUnknownAtomTypeOnce_AlreadyWarned(t *testing.T) {
	const unknown = int32(99)
	warnedAtomTypesMu.Lock()
	delete(warnedAtomTypes, unknown)
	warnedAtomTypesMu.Unlock()

	// First call → records + logs.
	_ = atomTypeToDomain(creationv1.AtomType(unknown))
	// Second call → already-warned early return.
	if got := atomTypeToDomain(creationv1.AtomType(unknown)); got != "" {
		t.Errorf("atomTypeToDomain(99) = %q, want \"\" (unknown → absent)", got)
	}
}

// fixedTime is a fixed instant (123456789ns kept so RFC3339Nano formats to
// ".123456789Z", matching the existing cover test's expectation) used by the
// timestamppb assertions above.
var fixedTime = time.Date(2026, 5, 16, 9, 30, 0, 123456789, time.UTC)
