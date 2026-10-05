// submission_graded_delivery_type_test — decode SubmissionGraded.delivery_type
// (field 14) onto the snake_case map the push handlers read (CHO-2224, §10.6
// capstone criterion 1).
//
// chora.delivery.submission.graded.v1 is a Schema-Registry BINARY protobuf
// topic, so the ONLY way the mode reaches the StudentTranscript projection is
// through this projector. A field that decodes to nothing here is a field that
// does not exist as far as the transcript is concerned.
package protodecode_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/common/v1"
	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events/protodecode"
)

func decodeGradedWithDeliveryType(t *testing.T, deliveryType string) map[string]any {
	t.Helper()
	bz, err := proto.Marshal(&deliveryv1.SubmissionGraded{
		Envelope:            &commonv1.EventEnvelope{TenantId: "tenant-acme", Gcid: "gcid-learner"},
		SubmissionId:        "sub-1",
		AssessmentId:        "assess-1",
		LearnerGcid:         "gcid-learner",
		TotalPointsEarned:   8.5,
		TotalPointsPossible: 10,
		Passed:              true,
		DeliveryType:        deliveryType,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.submission.graded.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	return got
}

func TestDecode_SubmissionGraded_DeliveryTypeGraduate(t *testing.T) {
	got := decodeGradedWithDeliveryType(t, "graduate")
	if got["delivery_type"] != "graduate" {
		t.Errorf("delivery_type: want graduate, got %v", got["delivery_type"])
	}
}

// The other half of the ">=2 modes" proof.
func TestDecode_SubmissionGraded_DeliveryTypeShort(t *testing.T) {
	got := decodeGradedWithDeliveryType(t, "short")
	if got["delivery_type"] != "short" {
		t.Errorf("delivery_type: want short, got %v", got["delivery_type"])
	}
}

// TestDecode_SubmissionGraded_OmitsEmptyDeliveryType — an event from a
// freestanding assessment, or any event published BEFORE field 14 existed,
// carries no mode. The key must be absent rather than present-and-empty, so the
// transcript stores NULL instead of "".
func TestDecode_SubmissionGraded_OmitsEmptyDeliveryType(t *testing.T) {
	got := decodeGradedWithDeliveryType(t, "")
	if _, present := got["delivery_type"]; present {
		t.Errorf("delivery_type must be absent when unset, got %v", got["delivery_type"])
	}
}

// TestDecode_SubmissionGraded_LegacyEventWithoutField14 — the decode-gap case
// stated explicitly: an event whose bytes never carried field 14 at all (every
// graded event published before this change) must still decode cleanly.
func TestDecode_SubmissionGraded_LegacyEventWithoutField14(t *testing.T) {
	bz, err := proto.Marshal(&deliveryv1.SubmissionGraded{
		Envelope:     &commonv1.EventEnvelope{TenantId: "tenant-acme", Gcid: "gcid-learner"},
		SubmissionId: "sub-legacy",
		AssessmentId: "assess-legacy",
		LearnerGcid:  "gcid-learner",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.submission.graded.v1", bz)
	if err != nil {
		t.Fatalf("a legacy graded event must still decode: %v", err)
	}
	if got["submission_id"] != "sub-legacy" {
		t.Errorf("submission_id: want sub-legacy, got %v", got["submission_id"])
	}
	if _, present := got["delivery_type"]; present {
		t.Errorf("delivery_type must be absent on a pre-field-14 event, got %v", got["delivery_type"])
	}
}

// TestDecode_SubmissionGraded_DeliveryTypeDoesNotDisplaceTitle — both string
// enrichments must survive together; field 13 and 14 are independent.
func TestDecode_SubmissionGraded_DeliveryTypeDoesNotDisplaceTitle(t *testing.T) {
	bz, err := proto.Marshal(&deliveryv1.SubmissionGraded{
		Envelope:        &commonv1.EventEnvelope{TenantId: "tenant-acme", Gcid: "gcid-learner"},
		SubmissionId:    "sub-1",
		AssessmentId:    "assess-1",
		LearnerGcid:     "gcid-learner",
		AssessmentTitle: "Algebra Midterm",
		DeliveryType:    "graduate",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := protodecode.DecodePayloadMap("chora.delivery.submission.graded.v1", bz)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got["assessment_title"] != "Algebra Midterm" {
		t.Errorf("assessment_title: want Algebra Midterm, got %v", got["assessment_title"])
	}
	if got["delivery_type"] != "graduate" {
		t.Errorf("delivery_type: want graduate, got %v", got["delivery_type"])
	}
}
