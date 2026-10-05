// transcript_delivery_type_test.go — the transcript's wire shape must expose
// delivery_type (CHO-2224, §10.6 capstone criterion 1).
//
// This is the LAST MILE, and skipping it would make the whole chain inert: the
// event could carry the mode, the projector could store it, and the learner's
// transcript would still render two identical-looking 'assessment' rows because
// the DTO never surfaced the field. §10.6 is demonstrated over this response.
package http

import (
	"encoding/json"
	"testing"
	"time"

	st "github.com/apollo-chora/chora-consumption/internal/domain/student_transcript"
)

func TestTranscriptEntryResp_ExposesDeliveryType(t *testing.T) {
	got := toTranscriptEntryResp(&st.TranscriptEntry{
		EntryID:      "entry-1",
		GCID:         "gcid-1",
		Kind:         st.KindAssessment,
		SourceRef:    "ass-1",
		Title:        "Algebra Midterm",
		DeliveryType: st.DeliveryTypeGraduate,
		OccurredAt:   time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if got.DeliveryType == nil {
		t.Fatal("delivery_type must be exposed on the wire; a stored mode nobody can read is not attribution")
	}
	if *got.DeliveryType != "graduate" {
		t.Errorf("delivery_type = %q; want %q", *got.DeliveryType, "graduate")
	}
}

// TestTranscriptEntryResp_DeliveryTypeJSONKey pins the JSON key itself: the FE
// contract is the key, not the Go field name.
func TestTranscriptEntryResp_DeliveryTypeJSONKey(t *testing.T) {
	bz, err := json.Marshal(toTranscriptEntryResp(&st.TranscriptEntry{
		EntryID:      "entry-1",
		GCID:         "gcid-1",
		Kind:         st.KindAssessment,
		SourceRef:    "ass-1",
		DeliveryType: st.DeliveryTypeShort,
		OccurredAt:   time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(bz, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, present := m["delivery_type"]
	if !present {
		t.Fatalf("response has no delivery_type key: %s", bz)
	}
	if v != "short" {
		t.Errorf("delivery_type = %v; want short", v)
	}
}

// TestTranscriptEntryResp_UnattributedModeIsExplicitNull — an unattributed entry
// renders delivery_type as an explicit null rather than omitting the key,
// matching the file's stated contract for score/passed/course_id ("no omitempty
// ... so the FE contract always carries the key"). A missing key and a null mean
// different things to a typed FE client.
func TestTranscriptEntryResp_UnattributedModeIsExplicitNull(t *testing.T) {
	bz, err := json.Marshal(toTranscriptEntryResp(&st.TranscriptEntry{
		EntryID:      "entry-1",
		GCID:         "gcid-1",
		Kind:         st.KindCertification,
		SourceRef:    "cert-1",
		DeliveryType: "",
		OccurredAt:   time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(bz, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, present := m["delivery_type"]
	if !present {
		t.Fatalf("delivery_type key must always be present (explicit null), got: %s", bz)
	}
	if v != nil {
		t.Errorf("delivery_type = %v; want null for an unattributed entry", v)
	}
}

// TestTranscriptEntryResp_TwoModesRenderDistinctly — §10.6 criterion 1 as the
// learner's own transcript response actually sees it: two same-kind entries that
// a reader can tell apart.
func TestTranscriptEntryResp_TwoModesRenderDistinctly(t *testing.T) {
	grad := toTranscriptEntryResp(&st.TranscriptEntry{
		EntryID: "e1", GCID: "gcid-1", Kind: st.KindAssessment, SourceRef: "ass-1",
		DeliveryType: st.DeliveryTypeGraduate, OccurredAt: time.Now().UTC(),
	})
	short := toTranscriptEntryResp(&st.TranscriptEntry{
		EntryID: "e2", GCID: "gcid-1", Kind: st.KindAssessment, SourceRef: "ass-2",
		DeliveryType: st.DeliveryTypeShort, OccurredAt: time.Now().UTC(),
	})
	if grad.Kind != short.Kind {
		t.Fatalf("precondition: both must be kind=assessment; got %q and %q", grad.Kind, short.Kind)
	}
	if grad.DeliveryType == nil || short.DeliveryType == nil {
		t.Fatal("both modes must render")
	}
	if *grad.DeliveryType == *short.DeliveryType {
		t.Fatalf("§10.6 criterion 1 UNMET on the wire: two same-kind entries render the same mode %q", *grad.DeliveryType)
	}
}
