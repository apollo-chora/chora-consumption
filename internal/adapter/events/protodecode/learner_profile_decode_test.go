// learner_profile_decode_test verifies the LearnerProfile feed decoders
// (ADR-200, WS1.c3): canonical gen-struct bytes for certification.issued and
// learning_path.completed round-trip through DecodePayloadMap into the
// snake_case map the push handlers read.
package protodecode_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	consumptionv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/consumption/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protodecode"
)

func TestDecode_CertIssued_Binary(t *testing.T) {
	issuedAt := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	bz, err := proto.Marshal(&deliveryv1.CertIssued{
		Envelope: &commonv1.EventEnvelope{
			TenantId:    "tenant-acme",
			Gcid:        "gcid-learner",
			Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		},
		CertId:      "cert-1",
		LearnerGcid: "gcid-learner",
		CourseId:    "course-1",
		IssuedAt:    timestamppb.New(issuedAt),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.certification.issued.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got["cert_id"] != "cert-1" {
		t.Errorf("cert_id: want cert-1, got %v", got["cert_id"])
	}
	if got["learner_gcid"] != "gcid-learner" {
		t.Errorf("learner_gcid: want gcid-learner, got %v", got["learner_gcid"])
	}
	if got["course_id"] != "course-1" {
		t.Errorf("course_id: want course-1, got %v", got["course_id"])
	}
	if got["tenant_id"] != "tenant-acme" {
		t.Errorf("tenant_id (from envelope): want tenant-acme, got %v", got["tenant_id"])
	}
	if got["issued_at"] != issuedAt.Format(time.RFC3339Nano) {
		t.Errorf("issued_at: want %v, got %v", issuedAt.Format(time.RFC3339Nano), got["issued_at"])
	}
}

func TestDecode_PathCompleted_Binary(t *testing.T) {
	completedAt := time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC)
	bz, err := proto.Marshal(&consumptionv1.PathCompleted{
		Envelope: &commonv1.EventEnvelope{
			TenantId: "tenant-acme",
			Gcid:     "gcid-learner",
		},
		PathId:      "path-1",
		LearnerGcid: "gcid-learner",
		CompletedAt: timestamppb.New(completedAt),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.consumption.learning_path.completed.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got["path_id"] != "path-1" {
		t.Errorf("path_id: want path-1, got %v", got["path_id"])
	}
	if got["learner_gcid"] != "gcid-learner" {
		t.Errorf("learner_gcid: want gcid-learner, got %v", got["learner_gcid"])
	}
	if got["completed_at"] != completedAt.Format(time.RFC3339Nano) {
		t.Errorf("completed_at: want %v, got %v", completedAt.Format(time.RFC3339Nano), got["completed_at"])
	}
}

func TestDecode_SubmissionGraded_Binary(t *testing.T) {
	bz, err := proto.Marshal(&deliveryv1.SubmissionGraded{
		Envelope:            &commonv1.EventEnvelope{TenantId: "tenant-acme", Gcid: "gcid-learner"},
		SubmissionId:        "sub-1",
		AssessmentId:        "assess-1",
		LearnerGcid:         "gcid-learner",
		TotalPointsEarned:   8.5,
		TotalPointsPossible: 10,
		Passed:              true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.submission.graded.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got["submission_id"] != "sub-1" {
		t.Errorf("submission_id: want sub-1, got %v", got["submission_id"])
	}
	// W6 Slice 1 (StudentTranscript) — assessment_id is the ref the transcript
	// projects; it must decode even though the LearnerProfile path never reads it.
	if got["assessment_id"] != "assess-1" {
		t.Errorf("assessment_id: want assess-1, got %v", got["assessment_id"])
	}
	if got["total_points_earned"] != float64(8.5) {
		t.Errorf("total_points_earned: want 8.5, got %v", got["total_points_earned"])
	}
	if got["total_points_possible"] != 10 {
		t.Errorf("total_points_possible: want 10, got %v", got["total_points_possible"])
	}
	if got["passed"] != true {
		t.Errorf("passed: want true, got %v", got["passed"])
	}
}

func TestDecode_EnrollmentCompleted_Binary(t *testing.T) {
	completedAt := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
	bz, err := proto.Marshal(&deliveryv1.EnrollmentCompleted{
		Envelope:     &commonv1.EventEnvelope{TenantId: "tenant-acme", Gcid: "gcid-learner"},
		EnrollmentId: "enr-1",
		LearnerGcid:  "gcid-learner",
		CourseId:     "course-1",
		Passed:       true,
		CompletedAt:  timestamppb.New(completedAt),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.enrollment.completed.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got["enrollment_id"] != "enr-1" {
		t.Errorf("enrollment_id: want enr-1, got %v", got["enrollment_id"])
	}
	if got["learner_gcid"] != "gcid-learner" {
		t.Errorf("learner_gcid: want gcid-learner, got %v", got["learner_gcid"])
	}
	if got["course_id"] != "course-1" {
		t.Errorf("course_id: want course-1, got %v", got["course_id"])
	}
	if got["passed"] != true {
		t.Errorf("passed: want true, got %v", got["passed"])
	}
	if got["tenant_id"] != "tenant-acme" {
		t.Errorf("tenant_id (from envelope): want tenant-acme, got %v", got["tenant_id"])
	}
	if got["completed_at"] != completedAt.Format(time.RFC3339Nano) {
		t.Errorf("completed_at: want %v, got %v", completedAt.Format(time.RFC3339Nano), got["completed_at"])
	}
}
